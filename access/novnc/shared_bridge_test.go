package novnc

import (
	"context"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedBridgeAuthorization struct {
	testAuthorizationClient
	mu     sync.Mutex
	driver string
	fence  uint64
	outage bool
}

func (a *sharedBridgeAuthorization) RecheckAccessLease(_ context.Context, r *wire.RecheckAccessLeaseRequest, _ ...grpc.CallOption) (*wire.RecheckAccessLeaseResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.outage {
		return nil, fmt.Errorf("core unavailable")
	}
	out := &wire.RecheckAccessLeaseResponse{AuthorizationLease: "test-renewed", ValidUntil: timestamppb.New(time.Now().Add(time.Second)), RecheckAfter: durationpb.New(30 * time.Millisecond), SharedConsole: true}
	if r.AccessSessionId == a.driver {
		out.InputLease = &wire.ConsoleInputLease{Fence: a.fence, ValidFor: durationpb.New(200 * time.Millisecond)}
	}
	return out, nil
}
func TestSharedBridgeEnforcesLeaseOnAuthenticatedStreams(t *testing.T) {
	service, err := New(Config{PublicOrigin: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	auth := &sharedBridgeAuthorization{driver: "access.0", fence: 1}
	current := map[string]*session{}
	messages := map[string]chan byte{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprint("access.", i)
		up, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		current[id] = &session{id: id, shared: true, lease: "test", expires: time.Now().Add(10 * time.Second), upstream: up}
		messages[id] = make(chan byte, 32)
		go func(id string, peer net.Conn) {
			defer peer.Close()
			var init [1]byte
			if _, err := io.ReadFull(peer, init[:]); err != nil {
				return
			}
			if init[0] != 1 {
				messages[id] <- 0
				return
			}
			if _, err := peer.Write([]byte("same desktop")); err != nil {
				return
			}
			for {
				frame, _, err := readClientMessage(peer)
				if err != nil {
					return
				}
				messages[id] <- frame[0]
			}
		}(id, peer)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		s := current[strings.TrimPrefix(r.URL.Path, "/")]
		service.bridge(ws, s, &runtime.AccessCallbacks{Authorization: auth}, &wire.ExchangeAccessTokenResponse{AuthorizationLease: "test", ValidUntil: timestamppb.New(time.Now().Add(time.Second)), RecheckAfter: durationpb.New(30 * time.Millisecond), SharedConsole: true})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	streams := []net.Conn{}
	for i := 0; i < 3; i++ {
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+fmt.Sprint("/access.", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		c := websocket.NetConn(ctx, ws, websocket.MessageBinary)
		streams = append(streams, c)
		for _, step := range []struct {
			read  int
			write []byte
		}{{12, rfb38}, {2, []byte{1}}, {4, []byte{0}}} {
			if _, err = io.ReadFull(c, make([]byte, step.read)); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Write(step.write); err != nil {
				t.Fatal(err)
			}
		}
		desktop := make([]byte, 12)
		if _, err = io.ReadFull(c, desktop); err != nil || string(desktop) != "same desktop" {
			t.Fatal("shared desktop", err)
		}
	}
	waitGrant := func(id string) {
		t.Helper()
		for end := time.Now().Add(time.Second); time.Now().Before(end); {
			s := current[id]
			s.mu.Lock()
			granted := time.Now().Before(s.inputUntil)
			s.mu.Unlock()
			if granted {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("input grant was not delivered")
	}
	send := func(i int, want bool) {
		t.Helper()
		id := fmt.Sprint("access.", i)
		if _, err := streams[i].Write([]byte{4, 1, 0, 0, 0, 0, 0, 65, 3, 1, 0, 0, 0, 0, 0, 1, 0, 1}); err != nil {
			t.Fatal(err)
		}
		input := false
		for {
			select {
			case kind := <-messages[id]:
				if kind == 4 {
					input = true
				}
				if kind == 3 {
					if input != want {
						t.Fatalf("session %d input=%v want %v", i, input, want)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal("input filter stalled")
			}
		}
	}
	waitGrant("access.0")
	send(0, true)
	send(1, false)
	send(2, false)
	// Model the core's handoff: withdraw renewal, wait out the old grant,
	// then activate a higher fence for the new session.
	auth.mu.Lock()
	auth.driver = ""
	auth.mu.Unlock()
	time.Sleep(250 * time.Millisecond)
	auth.mu.Lock()
	auth.driver = "access.1"
	auth.fence = 2
	auth.mu.Unlock()
	waitGrant("access.1")
	send(0, false)
	send(1, true)
	send(2, false)
	// A failed callback cannot extend the locally enforced input deadline.
	auth.mu.Lock()
	auth.outage = true
	auth.mu.Unlock()
	time.Sleep(250 * time.Millisecond)
	send(0, false)
	send(1, false)
	send(2, false)
	for _, s := range current {
		s.close()
	}
}
