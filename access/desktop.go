package access

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/dcv"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"github.com/OutboundSpade/LabCentral/v2/platform/proxmox"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Info struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Username string    `json:"username,omitempty"`
	Password string    `json:"password,omitempty"`
	Expires  time.Time `json:"expires"`
}
type desktop struct {
	relay *Relay
	close func() error
	ready func(context.Context) (bool, error)
	info  Info
}
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*desktop
}

func NewManager() *Manager { return &Manager{sessions: map[string]*desktop{}} }
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		_ = s.close()
		delete(m.sessions, k)
	}
}
func (m *Manager) Execute(ctx context.Context, c lc.Command) (lc.Result, error) {
	if c.Prior.Owner != c.Owner || c.Prior.Name != c.Machine.Name {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: access")
	}
	if c.Action == "access.close" {
		m.mu.Lock()
		s := m.sessions[c.Operation]
		delete(m.sessions, c.Operation)
		m.mu.Unlock()
		if s != nil {
			return lc.Result{Absent: true}, s.close()
		}
		if c.Environment.Platform == "aws" {
			return lc.Result{Absent: true}, closeDCV(ctx, c)
		}
		return lc.Result{Absent: true}, closeGuestUser(ctx, c)
	}
	ctx, cancel := context.WithTimeout(ctx, desktopOpenBudget(c.Machine.OS))
	defer cancel()
	if c.Action != "access.open" {
		return lc.Result{}, fmt.Errorf("unsupported access action")
	}
	m.mu.Lock()
	existing := m.sessions[c.Operation]
	m.mu.Unlock()
	if existing != nil && existing.info.Expires.After(time.Now()) {
		ready := true
		var err error
		if existing.ready != nil {
			ready, err = existing.ready(ctx)
		}
		if err != nil {
			return lc.Result{}, err
		}
		if ready {
			return accessResult(c, existing.info), nil
		}
		if err = existing.close(); err != nil {
			return lc.Result{}, err
		}
		m.mu.Lock()
		delete(m.sessions, c.Operation)
		m.mu.Unlock()
	}
	if c.Prior.Owner != c.Owner || c.Prior.Name != c.Machine.Name {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: access")
	}
	var s *desktop
	var info Info
	var err error
	if c.Environment.Platform == "aws" {
		s, info, err = openDCV(ctx, c)
	} else {
		s, info, err = openProxmox(ctx, c)
	}
	if err != nil {
		return lc.Result{}, err
	}
	info.ID = c.Operation
	info.Expires = accessExpiry(c)
	s.info = info
	m.mu.Lock()
	m.sessions[c.Operation] = s
	m.mu.Unlock()
	go func() {
		timer := time.NewTimer(time.Until(info.Expires))
		defer timer.Stop()
		<-timer.C
		m.mu.Lock()
		active := m.sessions[c.Operation]
		if active != s {
			m.mu.Unlock()
			return
		}
		delete(m.sessions, c.Operation)
		m.mu.Unlock()
		if active != nil {
			_ = active.close()
		}
	}()
	return accessResult(c, info), nil
}
func accessResult(c lc.Command, info Info) lc.Result {
	b, _ := json.Marshal(info)
	return lc.Result{Resource: lc.Resource{Kind: "access", Name: c.Machine.Name, ID: c.Operation, Owner: c.Owner, Address: info.URL, Complete: true, Data: b}}
}
func accessExpiry(c lc.Command) time.Time {
	if t, err := time.Parse(time.RFC3339, c.Access.Expires); err == nil {
		return t
	}
	return time.Now().Add(30 * time.Minute)
}
func dcvName(c lc.Command) string {
	h := sha256.Sum256([]byte(c.Operation))
	return "lc" + hex.EncodeToString(h[:6])
}
func dcvMarker(c lc.Command) string {
	return fmt.Sprintf("labcentral-%x", sha256.Sum256([]byte(c.Owner+":"+c.Operation)))
}

func openDCV(ctx context.Context, c lc.Command) (*desktop, Info, error) {
	var info Info
	dialer, e := lc.MachineDialer(c.Gateway, c.ClientPrivateKey)
	if e != nil {
		return nil, info, e
	}
	retained := false
	defer func() {
		if !retained {
			dialer.Close()
		}
	}()
	sshClient, e := dialer.Dial(ctx, c.Prior.Address, c.Machine.SSHUser(), c.ClientPrivateKey, c.Machine.HostPublicKey)
	if e != nil {
		return nil, info, e
	}
	defer sshClient.Close()
	username := dcvName(c)
	if c.Machine.OS == "windows" {
		username = "lab"
	}
	password := desktopPassword(c.Machine.OS)
	script := desktopAccountScript(username, password, c) + "; sudo dcv close-session " + username + " >/dev/null 2>&1 || true"
	if c.Machine.OS == "windows" {
		script = windowsDesktopProgram(c, password)
	}
	if _, e = remote.Run(ctx, sshClient, script); e != nil {
		return nil, info, fmt.Errorf("DCV session creation failed: %w", e)
	}
	run := func(ctx context.Context, command string) ([]byte, error) { return remote.Run(ctx, sshClient, command) }
	if c.Machine.OS == "windows" {
		e = dcv.StartWindows(ctx, run)
	} else {
		e = dcv.Start(ctx, run, username, username)
	}
	if e != nil {
		return nil, info, fmt.Errorf("DCV session readiness failed: %w", e)
	}
	// Read the instance certificate over an SSH connection with an exact host key.
	certCommand := "sudo openssl x509 -in /var/lib/dcv/.config/NICE/dcv/private/dcv.pem -outform PEM"
	if c.Machine.OS == "windows" {
		certCommand = windows.Command("[IO.File]::ReadAllText('" + dcv.WindowsCertificate + "')")
	}
	cert, e := remote.Run(ctx, sshClient, certCommand)
	if e != nil {
		_ = closeDCV(context.Background(), c)
		return nil, info, fmt.Errorf("read DCV certificate: %w", e)
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return nil, info, fmt.Errorf("invalid DCV certificate")
	}
	parsed, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, info, e
	}
	pin := sha256.Sum256(parsed.Raw)
	target, _ := url.Parse("https://" + net.JoinHostPort(c.Prior.Address, "8443"))
	proxy, transport := dcvProxy(target, pin)
	if c.Gateway.Kind != "" {
		transport.DialContext = dialer.DialTCP
	}
	relay, e := NewRelayUntil(context.Background(), proxy, accessExpiry(c))
	if e != nil {
		_ = closeDCV(context.Background(), c)
		return nil, info, e
	}
	var closeMu sync.Mutex
	closer := func() error {
		closeMu.Lock()
		defer closeMu.Unlock()
		_ = relay.Close()
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), desktopCloseBudget(c.Machine.OS))
		defer cancel()
		defer dialer.Close()
		client, err := dialer.Dial(ctx, c.Prior.Address, c.Machine.SSHUser(), c.ClientPrivateKey, c.Machine.HostPublicKey)
		if err != nil {
			return err
		}
		defer client.Close()
		return closeDCVClient(ctx, c, client)
	}
	info = Info{URL: relay.URL, Username: username, Password: password}
	ready := func(ctx context.Context) (bool, error) {
		client, err := dialer.Dial(ctx, c.Prior.Address, c.Machine.SSHUser(), c.ClientPrivateKey, c.Machine.HostPublicKey)
		if err != nil {
			return false, err
		}
		defer client.Close()
		if c.Machine.OS == "windows" {
			return dcv.WindowsReady(ctx, func(ctx context.Context, command string) ([]byte, error) { return remote.Run(ctx, client, command) })
		}
		return dcv.Ready(ctx, func(ctx context.Context, command string) ([]byte, error) { return remote.Run(ctx, client, command) }, username)
	}
	retained = true
	return &desktop{relay: relay, close: closer, ready: ready}, info, nil
}
func dcvProxy(target *url.URL, pin [sha256.Size]byte) (*httputil.ReverseProxy, *http.Transport) {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("missing DCV certificate")
		}
		got := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if got != pin {
			return fmt.Errorf("DCV certificate pin mismatch")
		}
		return nil
	}}, ResponseHeaderTimeout: 15 * time.Second}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = target.Host
		cookies := r.Cookies()
		r.Header.Del("Cookie")
		for _, cookie := range cookies {
			if cookie.Name != "labcentral_access" {
				r.AddCookie(cookie)
			}
		}
		r.Header.Set("Origin", target.String())
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		http.Error(w, "desktop upstream unavailable", 502)
	}
	return proxy, transport
}

func closeDCV(ctx context.Context, c lc.Command) error {
	ctx, cancel := context.WithTimeout(ctx, desktopCloseBudget(c.Machine.OS))
	defer cancel()
	sshClient, closeClient, e := dialMachine(ctx, c)
	if e != nil {
		return e
	}
	defer closeClient()
	return closeDCVClient(ctx, c, sshClient)
}

// desktopOwner returns the ownership marker of a temporary desktop account and the
// account comment that carries it. The comment's first field is the full name that
// desktops show (XFCE's panel shows it instead of the long account name); the marker
// is its last field, so ownership is still compared exactly.
func desktopOwner(c lc.Command) (marker, comment string) {
	marker = dcvMarker(c)
	return marker, "Student,,,," + marker
}

// desktopAccountScript creates the temporary account, or reuses one this owner
// created. Accounts made before the display name keep their bare marker until
// ownership is verified, then gain the current comment.
func desktopAccountScript(username, password string, c lc.Command) string {
	marker, comment := desktopOwner(c)
	return "set -eu; if id " + username + " >/dev/null 2>&1; then g=\"$(getent passwd " + username + " | cut -d: -f5)\"; if test \"$g\" = '" + marker + "'; then sudo usermod --comment '" + comment + "' " + username + "; else test \"$g\" = '" + comment + "'; fi; else sudo useradd --create-home --shell /bin/bash --comment '" + comment + "' " + username + "; fi; sudo chmod 700 /home/" + username + "; printf '%s\\n' '" + username + ":" + password + "' | sudo chpasswd"
}

// desktopOwned succeeds only for an account this owner created, in either format.
func desktopOwned(username string, c lc.Command) string {
	marker, comment := desktopOwner(c)
	return "{ g=\"$(getent passwd " + username + " | cut -d: -f5)\"; test \"$g\" = '" + comment + "' || test \"$g\" = '" + marker + "'; }"
}
func closeDCVClient(ctx context.Context, c lc.Command, sshClient *ssh.Client) error {
	if c.Machine.OS == "windows" {
		return closeWindowsDCV(ctx, c, sshClient)
	}
	var e error
	name := dcvName(c)
	remove := "sudo userdel --remove " + name
	if c.Access.RetainWorkspace {
		remove = "sudo usermod --lock " + name
	}
	cleanup := "set -eu; if id " + name + " >/dev/null 2>&1; then " + desktopOwned(name, c) + " || exit 1; fi; sudo dcv close-session " + name + " >/dev/null 2>&1 || true; " +
		"if id " + name + " >/dev/null 2>&1; then " +
		"uid=$(id -u " + name + "); " +
		"sudo loginctl terminate-user \"$uid\" >/dev/null 2>&1 || true; " +
		"sudo pkill -KILL -u \"$uid\" >/dev/null 2>&1 || true; " +
		"n=0; while pgrep -u \"$uid\" >/dev/null; do n=$((n+1)); test \"$n\" -lt 100 || exit 1; sleep 0.1; done; " +
		remove + "; fi"
	if _, e = remote.Run(ctx, sshClient, cleanup); e != nil {
		return e
	}
	output, e := remote.Run(ctx, sshClient, "sudo dcv list-sessions -j")
	if e != nil {
		return e
	}
	var sessions []struct {
		ID string `json:"id"`
	}
	if e = json.Unmarshal(output, &sessions); e != nil {
		return fmt.Errorf("DCV session removal unconfirmed: %w", e)
	}
	for _, session := range sessions {
		if session.ID == name {
			return fmt.Errorf("DCV session still exists")
		}
	}
	return nil
}
func openProxmox(ctx context.Context, cmd lc.Command) (*desktop, Info, error) {
	var info Info
	c, e := proxmox.NewClient(cmd.Environment)
	if e != nil {
		return nil, info, e
	}
	id, e := strconv.Atoi(cmd.Prior.ID)
	if e != nil {
		return nil, info, e
	}
	v, e := c.Config(ctx, id)
	if e != nil {
		return nil, info, e
	}
	if v.Description != "labcentral:v2:"+cmd.Owner+":"+cmd.Machine.Name {
		return nil, info, fmt.Errorf("ownership_mismatch: console")
	}
	username, password := dcvName(cmd), random()
	sshClient, closeClient, err := dialMachine(ctx, cmd)
	if err != nil {
		return nil, info, err
	}
	_, err = remote.Run(ctx, sshClient, desktopAccountScript(username, password, cmd))
	closeClient()
	if err != nil {
		return nil, info, err
	}
	var ticket struct {
		Ticket   string      `json:"ticket"`
		Port     json.Number `json:"port"`
		Password string      `json:"password"`
	}
	if e = c.Call(ctx, "POST", c.VM(id, "/vncproxy"), struct {
		Websocket int `json:"websocket"`
	}{1}, &ticket); e != nil {
		return nil, info, e
	}
	port, err := strconv.Atoi(string(ticket.Port))
	if err != nil || port < 1 || port > 65535 {
		return nil, info, fmt.Errorf("invalid Proxmox console port")
	}
	upstream := strings.Replace(c.URL, "https://", "wss://", 1) + c.VM(id, "/vncwebsocket") + "?port=" + strconv.Itoa(port) + "&vncticket=" + url.QueryEscape(ticket.Ticket)
	header := http.Header{}
	header.Set("Authorization", "PVEAPIToken="+c.Token)
	used := false
	var mu sync.Mutex
	var sockets []*websocket.Conn
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler())
	mux.HandleFunc("/credentials", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		password := ticket.Password
		if password == "" {
			password = ticket.Ticket
		}
		_ = json.NewEncoder(w).Encode(struct {
			Password string `json:"password"`
		}{password})
	})
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if used {
			mu.Unlock()
			http.Error(w, "console already connected", 409)
			return
		}
		used = true
		mu.Unlock()
		down, e := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if e != nil {
			return
		}
		defer down.CloseNow()
		up, _, e := websocket.Dial(r.Context(), upstream, &websocket.DialOptions{HTTPClient: c.HTTP, HTTPHeader: header})
		if e != nil {
			_ = down.Close(websocket.StatusInternalError, "upstream unavailable")
			return
		}
		defer up.CloseNow()
		mu.Lock()
		sockets = append(sockets, down, up)
		mu.Unlock()
		down.SetReadLimit(16 << 20)
		up.SetReadLimit(16 << 20)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		done := make(chan struct{}, 2)
		copyMessages := func(dst, src *websocket.Conn) {
			defer func() { done <- struct{}{} }()
			for {
				t, b, e := src.Read(ctx)
				if e != nil {
					return
				}
				if e = dst.Write(ctx, t, b); e != nil {
					return
				}
			}
		}
		go copyMessages(up, down)
		go copyMessages(down, up)
		<-done
		cancel()
		<-done
	})
	relay, e := NewRelay(context.Background(), mux)
	if e != nil {
		return nil, info, e
	}
	info.URL = relay.URL
	info.Username = username
	info.Password = password
	closer := func() error {
		mu.Lock()
		for _, s := range sockets {
			_ = s.CloseNow()
		}
		mu.Unlock()
		_ = relay.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return closeGuestUser(ctx, cmd)
	}
	return &desktop{relay: relay, close: closer}, info, nil
}

func closeGuestUser(ctx context.Context, c lc.Command) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, closeClient, err := dialMachine(ctx, c)
	if err != nil {
		return err
	}
	defer closeClient()
	name := dcvName(c)
	_, err = remote.Run(ctx, client, "set -eu; if id "+name+" >/dev/null 2>&1; then "+desktopOwned(name, c)+"; sudo loginctl terminate-user "+name+" || true; sudo userdel --force --remove "+name+"; fi")
	return err
}

func dialMachine(ctx context.Context, c lc.Command) (*ssh.Client, func(), error) {
	d, err := lc.MachineDialer(c.Gateway, c.ClientPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	client, err := d.Dial(ctx, c.Prior.Address, c.Machine.SSHUser(), c.ClientPrivateKey, c.Machine.HostPublicKey)
	if err != nil {
		d.Close()
		return nil, nil, err
	}
	return client, func() { client.Close(); d.Close() }, nil
}

func desktopOpenBudget(os string) time.Duration {
	if os == "windows" {
		return 10 * time.Minute
	}
	return 2 * time.Minute
}
func desktopCloseBudget(os string) time.Duration {
	if os == "windows" {
		return 2 * time.Minute
	}
	return 20 * time.Second
}
