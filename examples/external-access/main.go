// Fixture traffic enforcement lives here: every socket has a core authorization
// deadline. Killing this process closes traffic; no remote cleanup is assumed.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type session struct {
	closeConnections                       []func()
	id, method, token, networkToken, lease string
	expires, until                         time.Time
	closed, consumed                       bool
	cancel                                 context.CancelFunc
	listener                               net.Listener
}
type adapter struct {
	wire.UnimplementedAccessServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
	mu        sync.Mutex
	root      string
	callbacks runtime.AccessCallbacks
	sessions  map[string]*session
}

func secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(b []byte) string                                 { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func (s *adapter) Configure(c runtime.AccessCallbacks) error { s.callbacks = c; return nil }
func (s *adapter) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (s *adapter) CreateAccess(_ context.Context, r *wire.CreateAccessRequest) (*wire.CreateAccessResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.GetAccessSessionId()
	if !validID.MatchString(id) || (r.GetMethod() != "org.example.echo" && r.GetMethod() != "org.example.network") || r.GetEndpointSchema().GetName() != "fixture.endpoint" || digest(r.GetOpaqueConnectionData()) != r.GetEndpointDigest() || r.GetAudience() != s.callbacks.Audience || r.GetExpiresAt() == nil || !r.GetExpiresAt().AsTime().After(time.Now()) {
		return nil, fmt.Errorf("invalid access request")
	}
	current := s.sessions[id]
	if current == nil {
		path := filepath.Join(s.root, id)
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, fmt.Errorf("issuance already closed or uncertain")
		}
		_, e = f.Write([]byte("issued\n"))
		if e == nil {
			e = f.Sync()
		}
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		d, e := os.Open(s.root)
		if e != nil {
			return nil, e
		}
		e = d.Sync()
		_ = d.Close()
		if e != nil {
			return nil, e
		}
		current = &session{id: id, method: r.GetMethod(), token: secret(), networkToken: secret(), expires: r.GetExpiresAt().AsTime()}
		s.sessions[id] = current
	}
	if current.closed || current.consumed {
		return nil, fmt.Errorf("session unavailable")
	}
	state := []byte(id)
	return &wire.CreateAccessResponse{OpaqueAccessState: state, LaunchUrl: "/access/" + id, OneTimeToken: current.token, ValidUntil: timestamppb.New(current.expires), AccessStateSchema: &wire.SchemaVersion{Name: "fixture.access-state", Major: 1}, AccessStateDigest: digest(state)}, nil
}
func (s *adapter) closeLocked(v *session) {
	if v == nil || v.closed {
		return
	}
	v.closed = true
	for _, close := range v.closeConnections {
		close()
	}
	v.closeConnections = nil
	v.token = ""
	v.networkToken = ""
	v.lease = ""
	if v.cancel != nil {
		v.cancel()
	}
	if v.listener != nil {
		_ = v.listener.Close()
	}
}
func (s *adapter) RevokeAccess(_ context.Context, r *wire.RevokeAccessRequest) (*wire.RevokeAccessResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(s.sessions[r.GetAccessSessionId()])
	return &wire.RevokeAccessResponse{ActiveConnectionsClosed: true}, nil
}
func (s *adapter) InspectAccess(_ context.Context, r *wire.InspectAccessRequest) (*wire.InspectAccessResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.sessions[r.GetAccessSessionId()]
	if v == nil || v.closed || v.consumed && !v.until.After(time.Now()) {
		s.closeLocked(v)
		return &wire.InspectAccessResponse{State: "closed"}, nil
	}
	return &wire.InspectAccessResponse{State: "active", EnforcedUntil: timestamppb.New(v.until)}, nil
}
func (s *adapter) authorize(ctx context.Context, id, token string) (*session, context.Context, error) {
	s.mu.Lock()
	v := s.sessions[id]
	if v == nil || v.closed || v.consumed || !v.expires.After(time.Now()) || subtle.ConstantTimeCompare([]byte(token), []byte(v.token)) != 1 {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("denied")
	}
	v.consumed = true
	v.token = ""
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	decision, e := s.callbacks.Authorization.ExchangeAccessToken(call, &wire.ExchangeAccessTokenRequest{CallbackCredential: s.callbacks.Credential, AccessSessionId: id, Audience: s.callbacks.Audience, OneTimeToken: token})
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e != nil || decision.GetError() != nil || decision.GetAuthorizationLease() == "" || decision.GetValidUntil() == nil || !decision.GetValidUntil().AsTime().After(time.Now()) || v.closed {
		s.closeLocked(v)
		return nil, nil, fmt.Errorf("denied")
	}
	v.lease = decision.GetAuthorizationLease()
	v.until = minTime(decision.GetValidUntil().AsTime(), v.expires)
	live, stop := context.WithCancel(context.Background())
	v.cancel = stop
	go s.renew(live, v)
	go s.enforce(live, v)
	return v, live, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *adapter) enforce(ctx context.Context, v *session) {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			if !v.until.After(time.Now()) {
				s.closeLocked(v)
			}
			s.mu.Unlock()
		}
	}
}
func (s *adapter) renew(ctx context.Context, v *session) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			lease := v.lease
			closed := v.closed
			s.mu.Unlock()
			if closed {
				return
			}
			call, cancel := context.WithTimeout(ctx, 2*time.Second)
			d, e := s.callbacks.Authorization.RecheckAccessLease(call, &wire.RecheckAccessLeaseRequest{CallbackCredential: s.callbacks.Credential, AccessSessionId: v.id, Audience: s.callbacks.Audience, AuthorizationLease: lease})
			cancel()
			s.mu.Lock()
			if e == nil && d.GetError() == nil && d.GetAuthorizationLease() != "" && d.GetValidUntil() != nil && !v.closed && v.until.After(time.Now()) {
				v.lease = d.GetAuthorizationLease()
				v.until = minTime(d.GetValidUntil().AsTime(), v.expires)
			}
			s.mu.Unlock()
		}
	}
}
func (s *adapter) DownloadAccess(ctx context.Context, r *wire.DownloadAccessRequest) (*wire.DownloadAccessResponse, error) {
	v, live, e := s.authorize(ctx, r.GetAccessSessionId(), r.GetOneTimeToken())
	if e != nil {
		return nil, e
	}
	if v.method != "org.example.network" {
		return nil, fmt.Errorf("wrong presentation")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	if v.closed {
		s.mu.Unlock()
		listener.Close()
		return nil, fmt.Errorf("closed")
	}
	v.listener = listener
	token := v.networkToken
	s.mu.Unlock()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go s.network(live, v, conn)
		}
	}()
	data, _ := json.Marshal(struct{ Address, Token string }{listener.Addr().String(), token})
	return &wire.DownloadAccessResponse{Content: data, MediaType: "text/plain", Filename: "fixture-network.json"}, nil
}
func (s *adapter) network(ctx context.Context, v *session, c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	if v.closed {
		s.mu.Unlock()
		return
	}
	v.closeConnections = append(v.closeConnections, func() { _ = c.Close() })
	s.mu.Unlock()
	go func() { <-ctx.Done(); _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReaderSize(c, 1024)
	token, e := reader.ReadString('\n')
	if e != nil || len(token) > 256 {
		return
	}
	s.mu.Lock()
	allowed := !v.closed && v.until.After(time.Now()) && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(v.networkToken)) == 1
	s.mu.Unlock()
	if !allowed {
		return
	}
	buf := make([]byte, 4096)
	for {
		s.mu.Lock()
		until := v.until
		closed := v.closed
		s.mu.Unlock()
		if closed || !until.After(time.Now()) {
			return
		}
		_ = c.SetDeadline(until)
		n, e := reader.Read(buf)
		if e != nil {
			return
		}
		s.mu.Lock()
		allowed = !v.closed && v.until.After(time.Now())
		s.mu.Unlock()
		if !allowed {
			return
		}
		if _, e = c.Write(buf[:n]); e != nil {
			return
		}
	}
}
func (s *adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 5 || parts[1] != "v1" || parts[2] != "access" || parts[4] != "websocket" {
		http.NotFound(w, r)
		return
	}
	conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}, InsecureSkipVerify: true})
	if e != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(65536)
	read, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_, data, e := conn.Read(read)
	cancel()
	if e != nil {
		return
	}
	var launch struct {
		AccessSessionID string `json:"accessSessionId"`
		OneTimeToken    string `json:"oneTimeToken"`
	}
	if json.Unmarshal(data, &launch) != nil || launch.AccessSessionID != parts[3] {
		return
	}
	v, live, e := s.authorize(r.Context(), launch.AccessSessionID, launch.OneTimeToken)
	clear(data)
	launch.OneTimeToken = ""
	if e != nil || v.method != "org.example.echo" {
		return
	}
	defer func() { s.mu.Lock(); s.closeLocked(v); s.mu.Unlock() }()
	go func() { <-live.Done(); conn.CloseNow() }()
	if e = conn.Write(live, websocket.MessageText, []byte(`{"authorized":true}`)); e != nil {
		return
	}
	for {
		kind, b, e := conn.Read(live)
		if e != nil {
			return
		}
		if kind != websocket.MessageBinary {
			return
		}
		s.mu.Lock()
		allowed := !v.closed && v.until.After(time.Now())
		s.mu.Unlock()
		if !allowed {
			return
		}
		if e = conn.Write(live, kind, b); e != nil {
			return
		}
	}
}
func main() {
	root := flag.String("state", "", "owned journal")
	listen := flag.String("listen", "127.0.0.1:0", "fixture stream listener")
	flag.Parse()
	if *root == "" {
		panic("state required")
	}
	if e := os.MkdirAll(*root, 0700); e != nil {
		panic(e)
	}
	s := &adapter{root: *root, sessions: map[string]*session{}}
	l, e := net.Listen("tcp", *listen)
	if e != nil {
		panic(e)
	}
	go http.Serve(l, s)
	path, e := os.Executable()
	if e != nil {
		panic(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	schema := &wire.SchemaVersion{Name: "fixture.endpoint", Major: 1}
	e = runtime.Serve(context.Background(), runtime.Services{Info: &wire.PluginInfo{PluginId: "org.example.access", PluginVersion: "0.1.0", BuildDigest: "sha256:" + digest(b), Categories: []string{"access"}, AccessKinds: []string{"fixture"}, SupportedAccessProtocols: []*wire.ProtocolVersionRange{{Major: 1, MinMinor: 2, MaxMinor: 2}}, SupportedAccessMethods: []string{"org.example.echo", "org.example.network"}, AcceptedAccessEndpointSchemas: []*wire.SchemaVersion{schema}, Features: []string{"access-authorization-recheck-v1", "access-credential-delivery-v1", "access-enforcement-inspection-v1", "access-issuance-recovery-v1"}, AccessDescriptors: []*wire.AccessDescriptor{{Method: "org.example.echo", Title: "Fixture terminal", TargetKinds: []string{"VM", "Container"}, Presentation: "terminal", EndpointSchemas: []*wire.SchemaVersion{schema}, MaximumOfflineSeconds: 30, Renderer: "fixture.browser/echo"}, {Method: "org.example.network", Title: "Fixture network", TargetKinds: []string{"Network"}, Presentation: "credential-download", EndpointSchemas: []*wire.SchemaVersion{schema}, MaximumOfflineSeconds: 30}}}, Runtime: s, Access: s, ConfigureAccessCallbacks: s.Configure})
	if e != nil {
		panic(e)
	}
}
