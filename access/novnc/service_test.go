package novnc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAccessProxyConsumesProviderCredentialAndRequiresCoreExchange(t *testing.T) {
	upstreamAddress, caPEM, upstreamDone := startTestRFBServer(t, "vnc-secret-canary")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	service, err := New(Config{PublicOrigin: origin})
	if err != nil {
		t.Fatal(err)
	}
	authorization := &testAuthorizationClient{}
	if err := service.ConfigureCallbacks(pluginruntime.AccessCallbacks{
		Authorization: authorization, Credential: "callback-secret", Audience: origin,
		Protocol: &pluginv1.ProtocolVersion{Major: 1, Minor: 2},
	}); err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: service}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		service.Shutdown()
	})

	request := testCreateRequest(t, upstreamAddress, caPEM, "vnc-secret-canary", origin)
	created, err := service.CreateAccess(context.Background(), request)
	if err != nil || created.GetError() != nil {
		t.Fatalf("create access = %#v, %v", created, err)
	}
	if created.GetOneTimeToken() == "" || strings.Contains(created.String(), "vnc-secret-canary") || strings.Contains(created.GetLaunchUrl(), created.GetOneTimeToken()) {
		t.Fatalf("create response exposed provider or launch credential: %#v", created)
	}
	replayed, err := service.CreateAccess(context.Background(), request)
	if err != nil || replayed.GetError() != nil || replayed.GetOneTimeToken() != created.GetOneTimeToken() || replayed.GetAccessStateDigest() != created.GetAccessStateDigest() {
		t.Fatalf("idempotent create replay = %#v, %v", replayed, err)
	}

	websocketURL := "ws://" + listener.Addr().String() + "/access-proxy/v1/access/access.test/websocket"
	connection, _, err := websocket.Dial(context.Background(), websocketURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{origin}}, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	launch, _ := json.Marshal(map[string]string{"accessSessionId": "access.test", "oneTimeToken": created.GetOneTimeToken()})
	if err := connection.Write(context.Background(), websocket.MessageText, launch); err != nil {
		t.Fatal(err)
	}
	messageType, acknowledgement, err := connection.Read(context.Background())
	if err != nil || messageType != websocket.MessageText || string(acknowledgement) != `{"authorized":true}` {
		t.Fatalf("authorization acknowledgement = %q, %v", acknowledgement, err)
	}
	transport := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
	version := make([]byte, len(rfb38))
	if _, err := io.ReadFull(transport, version); err != nil || !bytes.Equal(version, rfb38) {
		t.Fatalf("downstream RFB version = %q, %v", version, err)
	}
	if _, err := transport.Write(rfb38); err != nil {
		t.Fatal(err)
	}
	securityTypes := make([]byte, 2)
	if _, err := io.ReadFull(transport, securityTypes); err != nil || !bytes.Equal(securityTypes, []byte{1, 1}) {
		t.Fatalf("downstream security types = %v, %v", securityTypes, err)
	}
	if _, err := transport.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	securityResult := make([]byte, 4)
	if _, err := io.ReadFull(transport, securityResult); err != nil || !bytes.Equal(securityResult, make([]byte, 4)) {
		t.Fatalf("downstream security result = %v, %v", securityResult, err)
	}
	if _, err := transport.Write([]byte{1}); err != nil { // ClientInit reaches the authenticated upstream.
		t.Fatal(err)
	}
	marker := make([]byte, 5)
	if _, err := io.ReadFull(transport, marker); err != nil || string(marker) != "READY" {
		t.Fatalf("bridged upstream marker = %q, %v", marker, err)
	}
	_ = transport.Close()
	select {
	case err := <-upstreamDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream RFB test did not complete")
	}
	if authorization.exchanges() != 1 {
		t.Fatalf("core token exchanges = %d, want 1", authorization.exchanges())
	}
}

func TestCreateAccessRejectsDuplicateEndpointFields(t *testing.T) {
	service, err := New(Config{PublicOrigin: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{"schemaVersion":"proxmox.vnc-endpoint/v1.0","schemaVersion":"proxmox.vnc-endpoint/v1.0"}`)
	digest := sha256.Sum256(contents)
	response, err := service.CreateAccess(context.Background(), &pluginv1.CreateAccessRequest{
		Context:         &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "vm.test", ProviderTargetId: "target.test"},
		AccessSessionId: "access.test", SubjectId: "user.test", Method: "vnc", Audience: "http://127.0.0.1:8080",
		EndpointSchema: &pluginv1.SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 1}, EndpointDigest: hex.EncodeToString(digest[:]),
		ProviderStateDigest: strings.Repeat("a", 64), OpaqueConnectionData: contents, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	})
	if err != nil || response.GetError().GetCode() != "invalid_access_endpoint" {
		t.Fatalf("duplicate endpoint response = %#v, %v", response, err)
	}
}

func TestAccessHTTPListenerRejectsNonLoopbackAddress(t *testing.T) {
	service, err := New(Config{PublicOrigin: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunHTTP(context.Background(), "0.0.0.0:8091"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback access listener error = %v", err)
	}
}

func TestAccessContract008AuthorizationOutageFailsClosed(t *testing.T) {
	base := time.Now().UTC()
	clockCalls := 0
	service, err := New(Config{
		PublicOrigin: "http://127.0.0.1:8080",
		Now: func() time.Time {
			clockCalls++
			if clockCalls == 1 {
				return base
			}
			return base.Add(31 * time.Second)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := &session{id: "access.outage", lease: "lease-secret", expires: base.Add(time.Minute)}
	callbacks := &pluginruntime.AccessCallbacks{
		Authorization: outageAuthorizationClient{}, Credential: "callback-secret", Audience: "http://127.0.0.1:8080",
	}
	initial := &pluginv1.ExchangeAccessTokenResponse{
		AuthorizationLease: "lease-secret", ValidUntil: timestamppb.New(base.Add(time.Minute)), RecheckAfter: durationpb.New(time.Millisecond),
	}
	lost := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go service.recheckLoop(ctx, current, callbacks, initial, lost)
	select {
	case <-lost:
	case <-ctx.Done():
		t.Fatal("authorization outage did not fail the active lease closed")
	}
}

func TestAccessContract010IdempotentPluginRevocation(t *testing.T) {
	service, err := New(Config{PublicOrigin: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	upstream, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	current := &session{id: "access.revoke", handle: "handle-revoke", token: strings.Repeat("t", 43), expires: time.Now().Add(time.Minute), upstream: upstream}
	service.sessions[current.id] = current
	state, err := json.Marshal(accessState{SchemaVersion: stateSchema, AccessSessionID: current.id, Handle: current.handle})
	if err != nil {
		t.Fatal(err)
	}
	request := &pluginv1.RevokeAccessRequest{AccessSessionId: current.id, OpaqueAccessState: state}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := service.RevokeAccess(context.Background(), request)
		if err != nil || response.GetError() != nil || !response.GetActiveConnectionsClosed() {
			t.Fatalf("revoke %d = %#v, %v", attempt+1, response, err)
		}
	}
	current.mu.Lock()
	closed := current.closed
	current.mu.Unlock()
	if !closed || service.sessions[current.id] != nil {
		t.Fatal("revocation did not remove and close the active session")
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatal("revocation left the upstream stream open")
	}
}

func testCreateRequest(t *testing.T, upstreamAddress, caPEM, password, origin string) *pluginv1.CreateAccessRequest {
	t.Helper()
	host, portText, err := net.SplitHostPort(upstreamAddress)
	if err != nil {
		t.Fatal(err)
	}
	var port uint16
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	providerDigest := strings.Repeat("a", 64)
	endpoint := accessendpoint.ProxmoxVNC{
		SchemaVersion: accessendpoint.ProxmoxVNCSchema, TargetID: "target.test", InstanceID: "instance.test",
		LogicalResourceID: "vm.test", Method: "vnc", ProviderStateDigest: providerDigest,
		Host: host, ServerName: "localhost", Port: port, CACertificatePEM: caPEM, Password: password,
	}
	contents, _ := json.Marshal(endpoint)
	digest := sha256.Sum256(contents)
	return &pluginv1.CreateAccessRequest{
		Context:         &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "vm.test", ProviderTargetId: "target.test"},
		AccessSessionId: "access.test", SubjectId: "user.test", Method: "vnc", Audience: origin,
		EndpointSchema: &pluginv1.SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 1}, EndpointDigest: hex.EncodeToString(digest[:]),
		ProviderStateDigest: providerDigest, OpaqueConnectionData: contents, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	}
}

func startTestRFBServer(t *testing.T, password string) (string, string, <-chan error) {
	t.Helper()
	certificate, caPEM := testTLSCertificate(t)
	listener, err := tlsListen("127.0.0.1:0", certificate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		if _, err = connection.Write(rfb38); err != nil {
			done <- err
			return
		}
		version := make([]byte, len(rfb38))
		if _, err = io.ReadFull(connection, version); err != nil || !bytes.Equal(version, rfb38) {
			done <- fmt.Errorf("client version: %q, %v", version, err)
			return
		}
		if _, err = connection.Write([]byte{1, 2}); err != nil {
			done <- err
			return
		}
		selected := make([]byte, 1)
		if _, err = io.ReadFull(connection, selected); err != nil || selected[0] != 2 {
			done <- fmt.Errorf("security selection: %v, %v", selected, err)
			return
		}
		challenge := []byte("0123456789abcdef")
		if _, err = connection.Write(challenge); err != nil {
			done <- err
			return
		}
		response := make([]byte, 16)
		expected, _ := vncChallengeResponse(password, challenge)
		if _, err = io.ReadFull(connection, response); err != nil || !bytes.Equal(response, expected) {
			done <- fmt.Errorf("challenge response mismatch: %v", err)
			return
		}
		if _, err = connection.Write(make([]byte, 4)); err != nil {
			done <- err
			return
		}
		clientInit := make([]byte, 1)
		if _, err = io.ReadFull(connection, clientInit); err != nil || clientInit[0] != 1 {
			done <- fmt.Errorf("client init: %v, %v", clientInit, err)
			return
		}
		_, err = connection.Write([]byte("READY"))
		done <- err
	}()
	return listener.Addr().String(), caPEM, done
}

func tlsListen(address string, certificate tls.Certificate) (net.Listener, error) {
	return tls.Listen("tcp", address, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
}

func testTLSCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}
	return certificate, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

type testAuthorizationClient struct {
	mu        sync.Mutex
	exchanged int
}

type outageAuthorizationClient struct {
	pluginv1.AccessAuthorizationServiceClient
}

func (outageAuthorizationClient) RecheckAccessLease(context.Context, *pluginv1.RecheckAccessLeaseRequest, ...grpc.CallOption) (*pluginv1.RecheckAccessLeaseResponse, error) {
	return nil, fmt.Errorf("core unavailable")
}

func (client *testAuthorizationClient) ExchangeAccessToken(_ context.Context, request *pluginv1.ExchangeAccessTokenRequest, _ ...grpc.CallOption) (*pluginv1.ExchangeAccessTokenResponse, error) {
	if request.GetCallbackCredential() != "callback-secret" || request.GetAudience() == "" || request.GetOneTimeToken() == "" {
		return nil, fmt.Errorf("invalid exchange")
	}
	client.mu.Lock()
	client.exchanged++
	client.mu.Unlock()
	return &pluginv1.ExchangeAccessTokenResponse{AuthorizationLease: "lease-secret", ValidUntil: timestamppb.New(time.Now().Add(time.Minute)), RecheckAfter: durationpb.New(15 * time.Second)}, nil
}

func (client *testAuthorizationClient) RecheckAccessLease(context.Context, *pluginv1.RecheckAccessLeaseRequest, ...grpc.CallOption) (*pluginv1.RecheckAccessLeaseResponse, error) {
	return &pluginv1.RecheckAccessLeaseResponse{AuthorizationLease: "next-lease", ValidUntil: timestamppb.New(time.Now().Add(time.Minute)), RecheckAfter: durationpb.New(15 * time.Second)}, nil
}

func (client *testAuthorizationClient) EndAccessLease(context.Context, *pluginv1.EndAccessLeaseRequest, ...grpc.CallOption) (*pluginv1.EndAccessLeaseResponse, error) {
	return &pluginv1.EndAccessLeaseResponse{Ended: true}, nil
}

func (client *testAuthorizationClient) exchanges() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.exchanged
}

var _ pluginv1.AccessAuthorizationServiceClient = (*testAuthorizationClient)(nil)
