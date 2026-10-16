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
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"github.com/OutboundSpade/LabCentral/v2/platform/proxmox"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"io"
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
	generation string
	retired    bool
	relay      *Relay
	close      func() error
	ready      func(context.Context) (bool, error)
	info       Info
}
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*desktop
	locks    map[string]*sessionLock
}

type sessionLock struct {
	mu   sync.Mutex
	refs int
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*desktop{}, locks: map[string]*sessionLock{}}
}

// Expiry participates in the same per-operation fence as explicit reads, grants
// and revokes. Keep the cache entry until its cleanup has actually completed.
func (m *Manager) lock(operation string) func() {
	m.mu.Lock()
	l := m.locks[operation]
	if l == nil {
		l = &sessionLock{}
		m.locks[operation] = l
	}
	l.refs++
	m.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(m.locks, operation)
		}
		m.mu.Unlock()
	}
}
func (m *Manager) retire(operation string, s *desktop) error {
	s.retired = true
	if err := s.close(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.sessions[operation] == s {
		delete(m.sessions, operation)
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) expire(operation string, s *desktop) {
	release := m.lock(operation)
	defer release()
	m.mu.Lock()
	active := m.sessions[operation]
	m.mu.Unlock()
	if active == s {
		_ = m.retire(operation, s)
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	sessions := make(map[string]*desktop, len(m.sessions))
	for k, s := range m.sessions {
		sessions[k] = s
	}
	m.mu.Unlock()
	for k, s := range sessions {
		m.expire(k, s)
	}
}
func (m *Manager) Execute(ctx context.Context, c lc.Command) (lc.Result, error) {
	if c.Prior.Owner != c.Owner || c.Prior.Name != c.Machine.Name {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: access")
	}
	release := m.lock(c.Operation)
	defer release()
	m.mu.Lock()
	existing := m.sessions[c.Operation]
	m.mu.Unlock()
	if existing != nil && existing.generation != c.Access.Generation {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: cached desktop generation")
	}
	if c.Action == "access.close" {
		if existing != nil {
			err := m.retire(c.Operation, existing)
			return lc.Result{Absent: err == nil}, err
		}
		if c.Environment.Platform == "aws" {
			err := closeDCV(ctx, c)
			return lc.Result{Absent: err == nil}, err
		}
		err := closeGuestUser(ctx, c)
		return lc.Result{Absent: err == nil}, err
	}
	ctx, cancel := context.WithTimeout(ctx, desktopOpenBudget(c.Machine.OS))
	defer cancel()
	if c.Action != "access.open" && c.Action != "access.read" {
		return lc.Result{}, fmt.Errorf("unsupported access action")
	}
	if existing != nil {
		ready := !existing.retired && existing.info.Expires.After(time.Now())
		var err error
		if ready && existing.ready != nil {
			ready, err = existing.ready(ctx)
		}
		if err != nil {
			return lc.Result{}, err
		}
		if ready {
			return accessResult(c, existing.info), nil
		}
		if err = m.retire(c.Operation, existing); err != nil {
			return lc.Result{}, err
		}
		if c.Action == "access.read" {
			return lc.Result{Absent: true}, nil
		}
		return lc.Result{}, fmt.Errorf("recovery_required: previous desktop generation retired; recover access before reopening")
	}
	if c.Action == "access.read" {
		return lc.Result{Absent: true}, nil
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
	s.generation = c.Access.Generation
	m.mu.Lock()
	m.sessions[c.Operation] = s
	m.mu.Unlock()
	go func() {
		timer := time.NewTimer(time.Until(info.Expires))
		defer timer.Stop()
		<-timer.C
		m.expire(c.Operation, s)
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
	script := desktopAccountScript(username, password, c)
	if c.Machine.OS == "windows" {
		script = windowsDesktopProgram(c, password)
	}
	if _, e = guestRun(ctx, sshClient, leaseCommand(c, "begin", script)); e != nil {
		return nil, info, fmt.Errorf("DCV session creation failed: %w", e)
	}
	run := func(ctx context.Context, command string) ([]byte, error) {
		return guestRun(ctx, sshClient, leaseCommand(c, "active", command))
	}
	if c.Machine.OS == "windows" {
		e = dcv.StartWindows(ctx, run)
	} else {
		e = dcv.Close(ctx, run, username, username)
		if e == nil {
			e = dcv.Start(ctx, run, username, username)
		}
	}
	if e != nil {
		return nil, info, fmt.Errorf("DCV session readiness failed: %w", e)
	}
	// Read the instance certificate over an SSH connection with an exact host key.
	certCommand := "sudo openssl x509 -in /var/lib/dcv/.config/NICE/dcv/private/dcv.pem -outform PEM"
	if c.Machine.OS == "windows" {
		certCommand = windows.Command("[IO.File]::ReadAllText('" + dcv.WindowsCertificate + "')")
	}
	cert, e := guestRun(ctx, sshClient, certCommand)
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
	cleanupStarted := false
	closer := func() error {
		closeMu.Lock()
		defer closeMu.Unlock()
		_ = relay.Close()
		transport.CloseIdleConnections()
		// The first attempt consumes the relay's dialer. A retained failed expiry
		// must be able to reconcile again with fresh pinned SSH material.
		if cleanupStarted {
			return closeDCV(context.Background(), c)
		}
		cleanupStarted = true
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
			return dcv.WindowsReady(ctx, func(ctx context.Context, command string) ([]byte, error) { return guestRun(ctx, client, command) })
		}
		return dcv.Ready(ctx, func(ctx context.Context, command string) ([]byte, error) { return guestRun(ctx, client, command) }, username)
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
	return "set -eu; if id " + username + " >/dev/null 2>&1; then g=\"$(getent passwd " + username + " | cut -d: -f5)\"; if test \"$g\" = '" + marker + "'; then sudo usermod --comment '" + comment + "' " + username + "; else test \"$g\" = '" + comment + "' || { echo 'ownership_mismatch: desktop account marker differs' >&2; exit 1; }; fi; else sudo useradd --create-home --shell /bin/bash --comment '" + comment + "' " + username + "; fi; sudo chmod 700 /home/" + username + "; printf '%s\\n' '" + username + ":" + password + "' | sudo chpasswd"
}

// desktopOwned succeeds only for an account this owner created, in either format.
func desktopOwned(username string, c lc.Command) string {
	marker, comment := desktopOwner(c)
	return "{ g=\"$(getent passwd " + username + " | cut -d: -f5)\"; test \"$g\" = '" + comment + "' || test \"$g\" = '" + marker + "' || { echo 'ownership_mismatch: desktop account marker differs' >&2; exit 1; }; }"
}
func closeDCVClient(ctx context.Context, c lc.Command, sshClient *ssh.Client) error {
	ctx = faultinject.WithOperation(ctx, c.Environment.Platform, "access.close")
	if c.Machine.OS == "windows" {
		return closeWindowsDCV(ctx, c, sshClient)
	}
	var e error
	name := dcvName(c)
	remove := "sudo userdel --remove " + name
	if c.Access.RetainWorkspace {
		remove = "sudo usermod --lock " + name
	}
	// Verify the account before touching its native session, including when a
	// previous cleanup removed the account but lost its response.
	if _, e = guestRun(ctx, sshClient, leaseCommand(c, "revoke", "set -eu; if id "+name+" >/dev/null 2>&1; then "+desktopOwned(name, c)+"; fi")); e != nil {
		return e
	}
	if e = dcv.Close(ctx, func(ctx context.Context, command string) ([]byte, error) {
		return guestRun(ctx, sshClient, leaseCommand(c, "revoke", command))
	}, name, name); e != nil {
		return e
	}
	cleanup := "set -eu; if id " + name + " >/dev/null 2>&1; then " + desktopOwned(name, c) + " || exit 1; fi; " +
		"if id " + name + " >/dev/null 2>&1; then " +
		"uid=$(id -u " + name + "); " +
		"sudo loginctl terminate-user \"$uid\" >/dev/null 2>&1 || true; " +
		"sudo pkill -KILL -u \"$uid\" >/dev/null 2>&1 || true; " +
		"n=0; while pgrep -u \"$uid\" >/dev/null; do n=$((n+1)); test \"$n\" -lt 100 || exit 1; sleep 0.1; done; " +
		remove + "; fi"
	if _, e = guestRun(ctx, sshClient, leaseCommand(c, "revoke", cleanup)); e != nil {
		return e
	}
	output, e := guestRun(ctx, sshClient, leaseCommand(c, "revoke", "sudo dcv list-sessions -j"))
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
	_, err = guestRun(ctx, sshClient, leaseCommand(cmd, "begin", desktopAccountScript(username, password, cmd)))
	closeClient()
	if err != nil {
		return nil, info, err
	}
	// Preflight native console permission before publishing a launch URL.
	type consoleTicket struct {
		Ticket   string      `json:"ticket"`
		Port     json.Number `json:"port"`
		Password string      `json:"password"`
	}
	freshTicket := func(ctx context.Context) (consoleTicket, error) {
		var ticket consoleTicket
		err := c.Call(ctx, "POST", c.VM(id, "/vncproxy"), struct {
			Websocket int `json:"websocket"`
		}{1}, &ticket)
		return ticket, err
	}
	if _, err = freshTicket(ctx); err != nil {
		return nil, info, err
	}
	header := http.Header{}
	header.Set("Authorization", "PVEAPIToken="+c.Token)
	used := false
	var mu sync.Mutex
	var sockets []*websocket.Conn
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler())
	mux.HandleFunc("/credentials", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if used {
			mu.Unlock()
			http.Error(w, "console already connected", 409)
			return
		}
		used = true
		mu.Unlock()
		defer func() { mu.Lock(); used = false; mu.Unlock() }()
		down, e := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if e != nil {
			return
		}
		defer down.CloseNow()
		// A fresh native ticket on every connection also supports close/reopen
		// while the same guest grant and authenticated relay remain valid.
		ticket, e := freshTicket(r.Context())
		if e != nil {
			return
		}
		port, e := strconv.Atoi(string(ticket.Port))
		if e != nil || port < 1 || port > 65535 || ticket.Ticket == "" {
			return
		}
		upstream := strings.Replace(c.URL, "https://", "wss://", 1) + c.VM(id, "/vncwebsocket") + "?port=" + strconv.Itoa(port) + "&vncticket=" + url.QueryEscape(ticket.Ticket)
		up, _, e := websocket.Dial(r.Context(), upstream, &websocket.DialOptions{HTTPClient: c.HTTP, HTTPHeader: header})
		if e != nil {
			_ = down.Close(websocket.StatusInternalError, "upstream unavailable")
			return
		}
		defer up.CloseNow()
		mu.Lock()
		sockets = append(sockets, down, up)
		mu.Unlock()
		defer func() { mu.Lock(); sockets = nil; mu.Unlock() }()
		down.SetReadLimit(16 << 20)
		up.SetReadLimit(16 << 20)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		u := websocket.NetConn(ctx, up, websocket.MessageBinary)
		d := websocket.NetConn(ctx, down, websocket.MessageBinary)
		secret := ticket.Password
		if secret == "" {
			secret = ticket.Ticket
		}
		if consoleHandshake(u, d, secret) != nil {
			return
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(u, d); done <- struct{}{} }()
		go func() { _, _ = io.Copy(d, u); done <- struct{}{} }()
		<-done
		cancel()
		<-done
	})
	relay, e := NewRelayUntil(context.Background(), mux, accessExpiry(cmd))
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
	ctx = faultinject.WithOperation(ctx, c.Environment.Platform, "access.close")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, closeClient, err := dialMachine(ctx, c)
	if err != nil {
		return err
	}
	defer closeClient()
	name := dcvName(c)
	remove := "sudo userdel --force --remove " + name
	if c.Access.RetainWorkspace {
		remove = "sudo usermod --lock " + name
	}
	_, err = guestRun(ctx, client, leaseCommand(c, "revoke", "set -eu; if id "+name+" >/dev/null 2>&1; then "+desktopOwned(name, c)+"; sudo loginctl terminate-user "+name+" >/dev/null 2>&1 || true; sudo pkill -KILL -u "+name+" >/dev/null 2>&1 || true; n=0; while pgrep -u "+name+" >/dev/null; do n=$((n+1)); test \"$n\" -lt 100 || exit 1; sleep 0.1; done; "+remove+"; fi"))
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
