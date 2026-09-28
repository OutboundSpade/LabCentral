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
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/platform/proxmox"
	"github.com/coder/websocket"
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if c.Action != "access.open" {
		return lc.Result{}, fmt.Errorf("unsupported access action")
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
	info.Expires = time.Now().Add(30 * time.Minute)
	m.mu.Lock()
	m.sessions[c.Operation] = s
	m.mu.Unlock()
	go func() {
		timer := time.NewTimer(30 * time.Minute)
		defer timer.Stop()
		<-timer.C
		m.mu.Lock()
		active := m.sessions[c.Operation]
		delete(m.sessions, c.Operation)
		m.mu.Unlock()
		if active != nil {
			_ = active.close()
		}
	}()
	b, _ := json.Marshal(info)
	return lc.Result{Resource: lc.Resource{Kind: "access", Name: c.Machine.Name, ID: c.Operation, Owner: c.Owner, Address: info.URL, Complete: true, Data: b}}, nil
}
func dcvName(c lc.Command) string {
	h := sha256.Sum256([]byte(c.Operation))
	return "lc" + hex.EncodeToString(h[:6])
}
func openDCV(ctx context.Context, c lc.Command) (*desktop, Info, error) {
	var info Info
	sshClient, e := remote.Dial(ctx, c.Prior.Address, "lab", c.ClientPrivateKey, c.Machine.HostPublicKey)
	if e != nil {
		return nil, info, e
	}
	defer sshClient.Close()
	username := dcvName(c)
	password := random()
	script := "sudo useradd --create-home --shell /bin/bash " + username + " && printf '%s\\n' '" + username + ":" + password + "' | sudo chpasswd && sudo dcv create-session --type virtual --owner " + username + " " + username
	if _, e = remote.Run(ctx, sshClient, script); e != nil {
		return nil, info, fmt.Errorf("DCV session creation failed: %w", e)
	}
	// Read the instance certificate over an SSH connection with an exact host key.
	cert, e := remote.Run(ctx, sshClient, "sudo openssl x509 -in /etc/dcv/dcv.pem -outform PEM")
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
	relay, e := NewRelay(context.Background(), proxy)
	if e != nil {
		_ = closeDCV(context.Background(), c)
		return nil, info, e
	}
	var once sync.Once
	closer := func() error {
		var err error
		once.Do(func() {
			_ = relay.Close()
			transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err = closeDCV(ctx, c)
		})
		return err
	}
	info = Info{URL: relay.URL, Username: username, Password: password}
	return &desktop{relay, closer}, info, nil
}
func closeDCV(ctx context.Context, c lc.Command) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sshClient, e := remote.Dial(ctx, c.Prior.Address, "lab", c.ClientPrivateKey, c.Machine.HostPublicKey)
	if e != nil {
		return e
	}
	defer sshClient.Close()
	name := dcvName(c)
	_, e = remote.Run(ctx, sshClient, "sudo dcv close-session "+name+"; if id "+name+" >/dev/null 2>&1; then sudo userdel --force --remove "+name+"; fi")
	return e
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
	sshClient, err := remote.Dial(ctx, cmd.Prior.Address, "lab", cmd.ClientPrivateKey, cmd.Machine.HostPublicKey)
	if err != nil {
		return nil, info, err
	}
	_, err = remote.Run(ctx, sshClient, "sudo useradd --create-home --shell /bin/bash "+username+" && printf '%s\\n' '"+username+":"+password+"' | sudo chpasswd")
	sshClient.Close()
	if err != nil {
		return nil, info, err
	}
	var ticket struct {
		Ticket   string `json:"ticket"`
		Port     int    `json:"port"`
		Password string `json:"password"`
	}
	if e = c.Call(ctx, "POST", c.VM(id, "/vncproxy"), struct {
		Websocket int `json:"websocket"`
	}{1}, &ticket); e != nil {
		return nil, info, e
	}
	upstream := strings.Replace(c.URL, "https://", "wss://", 1) + c.VM(id, "/vncwebsocket") + "?port=" + strconv.Itoa(ticket.Port) + "&vncticket=" + url.QueryEscape(ticket.Ticket)
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
	return &desktop{relay, closer}, info, nil
}

func closeGuestUser(ctx context.Context, c lc.Command) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := remote.Dial(ctx, c.Prior.Address, "lab", c.ClientPrivateKey, c.Machine.HostPublicKey)
	if err != nil {
		return err
	}
	defer client.Close()
	name := dcvName(c)
	_, err = remote.Run(ctx, client, "if id "+name+" >/dev/null 2>&1; then sudo loginctl terminate-user "+name+" || true; sudo userdel --force --remove "+name+"; fi")
	return err
}
