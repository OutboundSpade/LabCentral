package novnc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type renewingAuthorization struct {
	testAuthorizationClient
	mode   string
	checks atomic.Int32
}

func (c *renewingAuthorization) RecheckAccessLease(ctx context.Context, _ *pluginv1.RecheckAccessLeaseRequest, _ ...grpc.CallOption) (*pluginv1.RecheckAccessLeaseResponse, error) {
	c.checks.Add(1)
	switch c.mode {
	case "denied":
		return &pluginv1.RecheckAccessLeaseResponse{Error: &pluginv1.StructuredError{Code: "access_denied"}}, nil
	case "stalled":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pluginv1.RecheckAccessLeaseResponse{AuthorizationLease: "renewed-fixture", ValidUntil: timestamppb.New(time.Now().Add(time.Second)), RecheckAfter: durationpb.New(100 * time.Millisecond)}, nil
}

// Exercise the actual WebSocket/RFB bridge across multiple authorization
// deadlines. Renewal must preserve traffic, while denial, a stalled core and
// the fixed session expiry must close the stream.
func TestBridgeRenewsAuthorizationWithoutExtendingSession(t *testing.T) {
	for _, mode := range []string{"renewed", "denied", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			service, err := New(Config{PublicOrigin: "http://127.0.0.1:8080"})
			if err != nil {
				t.Fatal(err)
			}
			upstream, peer := net.Pipe()
			defer peer.Close()
			auth := &renewingAuthorization{mode: mode}
			started := time.Now()
			current := &session{id: "access.renewal", lease: "fixture", expires: started.Add(2200 * time.Millisecond), upstream: upstream}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				service.bridge(ws, current, &pluginruntime.AccessCallbacks{Authorization: auth}, &pluginv1.ExchangeAccessTokenResponse{AuthorizationLease: "fixture", ValidUntil: timestamppb.New(started.Add(time.Second)), RecheckAfter: durationpb.New(100 * time.Millisecond)})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
			read := func(n int) {
				t.Helper()
				if _, err := io.ReadFull(stream, make([]byte, n)); err != nil {
					t.Fatal(err)
				}
			}
			read(len(rfb38))
			if _, err := stream.Write(rfb38); err != nil {
				t.Fatal(err)
			}
			read(2)
			if _, err := stream.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			read(4)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for range ticker.C {
					if _, err := peer.Write([]byte{42}); err != nil {
						return
					}
				}
			}()
			receivedAfterInitialLease := false
			for {
				var b [1]byte
				_, err := io.ReadFull(stream, b[:])
				if err != nil {
					break
				}
				if b[0] != 42 {
					t.Fatal(fmt.Sprintf("unexpected RFB payload: %d", b[0]))
				}
				if time.Since(started) > 1200*time.Millisecond {
					receivedAfterInitialLease = true
				}
			}
			elapsed := time.Since(started)
			if auth.checks.Load() == 0 {
				t.Fatal("authorization was never rechecked")
			}
			if mode == "renewed" {
				if !receivedAfterInitialLease || elapsed < 2*time.Second || elapsed > 3*time.Second {
					t.Fatalf("renewed stream lifetime %s, traffic past initial lease: %v", elapsed, receivedAfterInitialLease)
				}
			} else if receivedAfterInitialLease || elapsed > 1500*time.Millisecond {
				t.Fatalf("%s authorization did not fail closed: %s", mode, elapsed)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream remained open")
			}
		})
	}
}
