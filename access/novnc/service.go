package novnc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	PluginID      = "io.labcentral.access.novnc"
	PluginVersion = "0.1.0"
	stateSchema   = "novnc.access-state/v1.0"
	maxSessions   = 4096
	maxFrameBytes = 4 << 20
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

type Config struct {
	PublicOrigin      string
	WebSocketBasePath string
	DialContext       func(context.Context, string, string) (net.Conn, error)
	Now               func() time.Time
	TailscaleProfile  *TailscaleProfile
}

type Service struct {
	pluginv1.UnimplementedAccessServiceServer
	pluginv1.UnimplementedPluginRuntimeServiceServer

	origin     *url.URL
	basePath   string
	dial       func(context.Context, string, string) (net.Conn, error)
	now        func() time.Time
	tailscale  *TailscaleProfile
	mu         sync.Mutex
	callbacks  *pluginruntime.AccessCallbacks
	sessions   map[string]*session
	createLock [64]sync.Mutex
}

type session struct {
	id       string
	handle   string
	token    string
	expires  time.Time
	upstream net.Conn
	mu       sync.Mutex
	lease    string
	consumed bool
	closed   bool
}

type accessState struct {
	SchemaVersion   string `json:"schemaVersion"`
	AccessSessionID string `json:"accessSessionId"`
	Handle          string `json:"handle"`
}

func New(config Config) (*Service, error) {
	origin, err := url.Parse(strings.TrimSpace(config.PublicOrigin))
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, fmt.Errorf("public origin must be an origin URL")
	}
	if origin.Scheme != "https" && !(origin.Scheme == "http" && isLoopbackHost(origin.Hostname())) {
		return nil, fmt.Errorf("public origin must use HTTPS outside loopback development")
	}
	basePath := strings.TrimSuffix(config.WebSocketBasePath, "/")
	if basePath == "" {
		basePath = "/access-proxy"
	}
	if !strings.HasPrefix(basePath, "/") || path.Clean(basePath) != basePath || strings.Contains(basePath, "..") {
		return nil, fmt.Errorf("WebSocket base path is invalid")
	}
	dial := config.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	var tailnet *TailscaleProfile
	if config.TailscaleProfile != nil {
		copy := *config.TailscaleProfile
		if err := copy.validate(); err != nil {
			return nil, err
		}
		tailnet = &copy
	}
	return &Service{origin: origin, basePath: basePath, dial: dial, now: now, tailscale: tailnet, sessions: make(map[string]*session)}, nil
}

func (service *Service) ConfigureCallbacks(callbacks pluginruntime.AccessCallbacks) error {
	if callbacks.Authorization == nil || callbacks.Credential == "" || callbacks.Audience != service.origin.String() || callbacks.Protocol.GetMajor() != 1 || callbacks.Protocol.GetMinor() < 2 {
		return fmt.Errorf("access callback binding is invalid")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.callbacks != nil {
		return fmt.Errorf("access callback binding is already configured")
	}
	copy := callbacks
	service.callbacks = &copy
	return nil
}

func (service *Service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	service.mu.Lock()
	configured := service.callbacks != nil
	service.mu.Unlock()
	if !configured {
		return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_UNHEALTHY, SafeMessage: "access authorization channel is unavailable"}, nil
	}
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_HEALTHY, SafeMessage: "noVNC access proxy is ready"}, nil
}

func (service *Service) CreateAccess(ctx context.Context, request *pluginv1.CreateAccessRequest) (*pluginv1.CreateAccessResponse, error) {
	fail := func(code, message string) (*pluginv1.CreateAccessResponse, error) {
		slog.Warn("VNC access request rejected", "code", code)
		return &pluginv1.CreateAccessResponse{Error: &pluginv1.StructuredError{Code: code, ErrorClass: "permanent", SafeMessage: message}}, nil
	}
	if request == nil || request.GetContext() == nil || !identifierPattern.MatchString(request.GetAccessSessionId()) ||
		!identifierPattern.MatchString(request.GetSubjectId()) || request.GetMethod() != "vnc" || request.GetAudience() != service.origin.String() ||
		request.GetEndpointSchema().GetName() != "proxmox.vnc-endpoint" || request.GetEndpointSchema().GetMajor() != 1 || request.GetEndpointSchema().GetMinor() != 0 ||
		len(request.GetOpaqueConnectionData()) == 0 || len(request.GetOpaqueConnectionData()) > 64<<10 || !validSHA256(request.GetEndpointDigest()) ||
		!validSHA256(request.GetProviderStateDigest()) || request.GetExpiresAt() == nil || !request.GetExpiresAt().IsValid() {
		return fail("invalid_access_request", "VNC access request is invalid")
	}
	digest := sha256.Sum256(request.GetOpaqueConnectionData())
	if hex.EncodeToString(digest[:]) != request.GetEndpointDigest() {
		return fail("invalid_endpoint_digest", "VNC endpoint digest does not match")
	}
	var endpoint accessendpoint.ProxmoxVNC
	if err := decodeClosedJSON(request.GetOpaqueConnectionData(), &endpoint); err != nil || endpoint.SchemaVersion != accessendpoint.ProxmoxVNCSchema ||
		endpoint.TargetID != request.GetContext().GetProviderTargetId() || endpoint.InstanceID != request.GetContext().GetLabInstanceId() ||
		endpoint.LogicalResourceID != request.GetContext().GetLogicalResourceId() || endpoint.Method != request.GetMethod() ||
		endpoint.ProviderStateDigest != request.GetProviderStateDigest() || endpoint.Host == "" || endpoint.ServerName == "" || endpoint.Port < 5900 ||
		endpoint.CACertificatePEM == "" || endpoint.Password == "" || len(endpoint.Password) > 256 {
		return fail("invalid_access_endpoint", "VNC endpoint binding is invalid")
	}
	expires := request.GetExpiresAt().AsTime().UTC()
	if !expires.After(service.now().UTC()) || expires.After(service.now().UTC().Add(time.Hour)) {
		return fail("invalid_access_expiry", "VNC access expiry is invalid")
	}
	stripe := &service.createLock[sha256.Sum256([]byte(request.GetAccessSessionId()))[0]%byte(len(service.createLock))]
	stripe.Lock()
	defer stripe.Unlock()
	service.mu.Lock()
	existing := service.sessions[request.GetAccessSessionId()]
	service.mu.Unlock()
	if existing != nil {
		if response := service.replayCreate(existing); response != nil {
			endpoint.Password = ""
			return response, nil
		}
		endpoint.Password = ""
		return fail("access_session_unavailable", "VNC access session cannot issue new launch material")
	}
	upstream, err := service.connectUpstream(ctx, endpoint)
	endpoint.Password = ""
	if err != nil {
		slog.Warn("VNC upstream connection failed", "stage", upstreamFailureStage(err))
		return fail("upstream_unavailable", "VNC upstream is unavailable")
	}
	handle, err := randomSecret(24)
	if err != nil {
		_ = upstream.Close()
		return nil, err
	}
	token, err := randomSecret(32)
	if err != nil {
		_ = upstream.Close()
		return nil, err
	}
	created := &session{id: request.GetAccessSessionId(), handle: handle, token: token, expires: expires, upstream: upstream}
	service.mu.Lock()
	if len(service.sessions) >= maxSessions && service.sessions[created.id] == nil {
		service.mu.Unlock()
		_ = upstream.Close()
		return fail("capacity_exhausted", "VNC access proxy capacity is exhausted")
	}
	service.sessions[created.id] = created
	service.mu.Unlock()
	go service.expireSession(created)
	return service.replayCreate(created), nil
}

// Log a bounded stage, never the underlying address, ticket or credential.
func upstreamFailureStage(err error) string {
	message := err.Error()
	for _, stage := range []string{"tailnet identity", "tailnet peer", "upstream RFB"} {
		if strings.HasPrefix(message, stage) {
			return stage
		}
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS verification"
	}
	var socketErr *net.OpError
	if errors.As(err, &socketErr) {
		return "socket"
	}
	return "transport"
}

func (service *Service) replayCreate(current *session) *pluginv1.CreateAccessResponse {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.closed || current.consumed || current.token == "" || !current.expires.After(service.now().UTC()) {
		return nil
	}
	stateBytes, _ := json.Marshal(accessState{SchemaVersion: stateSchema, AccessSessionID: current.id, Handle: current.handle})
	stateDigest := sha256.Sum256(stateBytes)
	launch := *service.origin
	launch.Path = "/access/" + url.PathEscape(current.id)
	return &pluginv1.CreateAccessResponse{
		OpaqueAccessState: stateBytes, LaunchUrl: launch.String(), OneTimeToken: current.token, ValidUntil: timestamppb.New(current.expires),
		AccessStateSchema: &pluginv1.SchemaVersion{Name: "novnc.access-state", Major: 1, Minor: 0}, AccessStateDigest: hex.EncodeToString(stateDigest[:]),
	}
}

func (service *Service) RevokeAccess(_ context.Context, request *pluginv1.RevokeAccessRequest) (*pluginv1.RevokeAccessResponse, error) {
	if request == nil || !identifierPattern.MatchString(request.GetAccessSessionId()) {
		return &pluginv1.RevokeAccessResponse{Error: &pluginv1.StructuredError{Code: "invalid_revoke_request", ErrorClass: "permanent", SafeMessage: "VNC access revocation is invalid"}}, nil
	}
	var state accessState
	if err := decodeClosedJSON(request.GetOpaqueAccessState(), &state); err != nil || state.SchemaVersion != stateSchema || state.AccessSessionID != request.GetAccessSessionId() || state.Handle == "" {
		return &pluginv1.RevokeAccessResponse{Error: &pluginv1.StructuredError{Code: "invalid_access_state", ErrorClass: "permanent", SafeMessage: "VNC access state is invalid"}}, nil
	}
	service.mu.Lock()
	current := service.sessions[state.AccessSessionID]
	if current != nil && current.handle == state.Handle {
		delete(service.sessions, state.AccessSessionID)
	} else {
		current = nil
	}
	service.mu.Unlock()
	if current != nil {
		current.close()
	}
	return &pluginv1.RevokeAccessResponse{ActiveConnectionsClosed: true}, nil
}

func (service *Service) connectUpstream(ctx context.Context, endpoint accessendpoint.ProxmoxVNC) (net.Conn, error) {
	if service.tailscale != nil {
		connectContext, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		connection, err := service.tailscale.connect(connectContext, endpoint)
		if err != nil {
			return nil, err
		}
		_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
		if err := authenticateUpstream(connection, endpoint.Password); err != nil {
			_ = connection.Close()
			return nil, err
		}
		_ = connection.SetDeadline(time.Time{})
		return connection, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM([]byte(endpoint.CACertificatePEM)) {
		return nil, fmt.Errorf("VNC endpoint trust is invalid")
	}
	connectContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := service.dial(connectContext, "tcp", net.JoinHostPort(endpoint.Host, fmt.Sprint(endpoint.Port)))
	if err != nil {
		return nil, err
	}
	connection := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: endpoint.ServerName})
	_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
	if err := connection.HandshakeContext(connectContext); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := authenticateUpstream(connection, endpoint.Password); err != nil {
		_ = connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}

func (service *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'self'")
	prefix := service.basePath + "/v1/access/"
	if request.Method != http.MethodGet || !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, "/websocket") || request.URL.RawQuery != "" {
		http.NotFound(writer, request)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), "/websocket")
	if !identifierPattern.MatchString(id) || request.Header.Get("Origin") != service.origin.String() {
		http.Error(writer, "access denied", http.StatusForbidden)
		return
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{OriginPatterns: []string{service.origin.Host}, Subprotocols: []string{"binary"}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	connection.SetReadLimit(4096)
	defer connection.CloseNow()
	readContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	messageType, message, err := connection.Read(readContext)
	cancel()
	if err != nil || (messageType != websocket.MessageText && messageType != websocket.MessageBinary) {
		_ = connection.Close(websocket.StatusPolicyViolation, "authorization required")
		return
	}
	var launch struct {
		AccessSessionID string `json:"accessSessionId"`
		OneTimeToken    string `json:"oneTimeToken"`
	}
	if decodeClosedJSON(message, &launch) != nil || launch.AccessSessionID != id || len(launch.OneTimeToken) < 43 || len(launch.OneTimeToken) > 256 {
		_ = connection.Close(websocket.StatusPolicyViolation, "authorization denied")
		return
	}
	current, callbacks := service.claimSession(id, launch.OneTimeToken)
	if current == nil || callbacks == nil || !current.expires.After(service.now().UTC()) {
		_ = connection.Close(websocket.StatusPolicyViolation, "authorization denied")
		return
	}
	current.mu.Lock()
	current.token = ""
	current.mu.Unlock()
	exchangeContext, exchangeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	decision, err := callbacks.Authorization.ExchangeAccessToken(exchangeContext, &pluginv1.ExchangeAccessTokenRequest{
		CallbackCredential: callbacks.Credential, AccessSessionId: id, Audience: callbacks.Audience, OneTimeToken: launch.OneTimeToken,
	})
	exchangeCancel()
	launch.OneTimeToken = ""
	clear(message)
	if err != nil || decision.GetError() != nil || decision.GetAuthorizationLease() == "" || decision.GetValidUntil() == nil || !decision.GetValidUntil().IsValid() {
		current.close()
		_ = connection.Close(websocket.StatusPolicyViolation, "authorization denied")
		return
	}
	current.mu.Lock()
	current.lease = decision.GetAuthorizationLease()
	current.mu.Unlock()
	if err := connection.Write(context.Background(), websocket.MessageText, []byte(`{"authorized":true}`)); err != nil {
		current.close()
		return
	}
	connection.SetReadLimit(maxFrameBytes)
	service.bridge(connection, current, callbacks, decision)
}

func (service *Service) claimSession(id, token string) (*session, *pluginruntime.AccessCallbacks) {
	service.mu.Lock()
	defer service.mu.Unlock()
	current := service.sessions[id]
	if current == nil {
		return nil, service.callbacks
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.consumed || current.closed || subtle.ConstantTimeCompare([]byte(current.token), []byte(token)) != 1 {
		return nil, service.callbacks
	}
	current.consumed = true
	return current, service.callbacks
}

func (service *Service) expireSession(current *session) {
	delay := time.Until(current.expires)
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
	service.mu.Lock()
	if service.sessions[current.id] == current {
		delete(service.sessions, current.id)
	}
	service.mu.Unlock()
	current.close()
}

func (service *Service) bridge(connection *websocket.Conn, current *session, callbacks *pluginruntime.AccessCallbacks, initial *pluginv1.ExchangeAccessTokenResponse) {
	ctx, cancel := context.WithDeadline(context.Background(), current.expires)
	defer cancel()
	// Session lifetime is fixed; authorization deadlines move only after a
	// successful core recheck. A stalled callback cannot extend this timer.
	leaseTimer := time.AfterFunc(time.Until(initial.GetValidUntil().AsTime()), cancel)
	defer leaseTimer.Stop()
	downstream := websocket.NetConn(ctx, connection, websocket.MessageBinary)
	if err := presentAuthorizedDownstream(downstream); err != nil {
		_ = downstream.Close()
		current.close()
		return
	}
	streamDone := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(current.upstream, downstream); streamDone <- struct{}{} }()
	go func() { _, _ = io.Copy(downstream, current.upstream); streamDone <- struct{}{} }()
	authorizationLost := make(chan struct{}, 1)
	go service.recheckLoop(ctx, current, callbacks, initial, authorizationLost, func(until time.Time) {
		leaseTimer.Reset(time.Until(until))
	})
	select {
	case <-streamDone:
	case <-authorizationLost:
	case <-ctx.Done():
	}
	current.close()
	_ = downstream.Close()
	current.mu.Lock()
	lease := current.lease
	current.mu.Unlock()
	if lease != "" {
		endContext, endCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = callbacks.Authorization.EndAccessLease(endContext, &pluginv1.EndAccessLeaseRequest{
			CallbackCredential: callbacks.Credential, AccessSessionId: current.id, Audience: callbacks.Audience,
			AuthorizationLease: lease, Reason: "client_closed",
		})
		endCancel()
	}
	service.mu.Lock()
	if service.sessions[current.id] == current {
		delete(service.sessions, current.id)
	}
	service.mu.Unlock()
}

func (service *Service) recheckLoop(ctx context.Context, current *session, callbacks *pluginruntime.AccessCallbacks, initial *pluginv1.ExchangeAccessTokenResponse, lost chan<- struct{}, renewed func(time.Time)) {
	delay := initial.GetRecheckAfter().AsDuration()
	if delay <= 0 || delay > 15*time.Second {
		lost <- struct{}{}
		return
	}
	lastSuccess := service.now().UTC()
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		current.mu.Lock()
		lease := current.lease
		closed := current.closed
		current.mu.Unlock()
		if closed {
			return
		}
		callContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		decision, err := callbacks.Authorization.RecheckAccessLease(callContext, &pluginv1.RecheckAccessLeaseRequest{
			CallbackCredential: callbacks.Credential, AccessSessionId: current.id, Audience: callbacks.Audience, AuthorizationLease: lease,
		})
		cancel()
		if err != nil {
			if !service.now().UTC().Before(lastSuccess.Add(30 * time.Second)) {
				lost <- struct{}{}
				return
			}
			delay = 2 * time.Second
			continue
		}
		if decision.GetError() != nil || decision.GetAuthorizationLease() == "" || decision.GetValidUntil() == nil || !decision.GetValidUntil().IsValid() {
			lost <- struct{}{}
			return
		}
		delay = decision.GetRecheckAfter().AsDuration()
		if delay <= 0 || delay > 15*time.Second || !decision.GetValidUntil().AsTime().After(service.now().UTC()) {
			lost <- struct{}{}
			return
		}
		current.mu.Lock()
		current.lease = decision.GetAuthorizationLease()
		current.mu.Unlock()
		lastSuccess = service.now().UTC()
		renewed(decision.GetValidUntil().AsTime())
	}
}

func (current *session) close() {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		return
	}
	current.closed = true
	current.token = ""
	connection := current.upstream
	current.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
}

func decodeClosedJSON(contents []byte, destination any) error {
	if len(contents) == 0 || len(contents) > 64<<10 {
		return fmt.Errorf("JSON envelope is invalid")
	}
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON envelope contains trailing data")
	}
	return nil
}

func rejectDuplicateJSONKeys(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	return scanJSONObject(decoder, true)
}

func scanJSONObject(decoder *json.Decoder, root bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, stringKey := key.(string)
			if err != nil || !stringKey || seen[name] {
				return fmt.Errorf("JSON object contains a duplicate or invalid key")
			}
			seen[name] = true
			if err := scanJSONObject(decoder, false); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONObject(decoder, false); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		if root {
			return fmt.Errorf("JSON envelope must be an object")
		}
		return fmt.Errorf("JSON delimiter is invalid")
	}
}

func randomSecret(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (service *Service) Shutdown() {
	service.mu.Lock()
	sessions := service.sessions
	service.sessions = make(map[string]*session)
	service.mu.Unlock()
	for _, current := range sessions {
		current.close()
	}
}

func (service *Service) RunHTTP(ctx context.Context, address string) error {
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || port == "" || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("access HTTP listener must be an explicit loopback IP and port")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: service, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		service.Shutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
