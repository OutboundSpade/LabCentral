package hosted

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/coder/websocket"
)

type coachTransportEngine struct {
	Engine
	entered  chan string
	finished chan error
	calls    atomic.Int32
	wait     bool
}

func (e *coachTransportEngine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.calls.Add(1)
	if e.entered != nil {
		e.entered <- machine
	}
	if e.wait {
		<-ctx.Done()
		if e.finished != nil {
			e.finished <- context.Cause(ctx)
		}
		return ctx.Err()
	}
	return fmt.Errorf("Fixture SSH unavailable")
}

func TestCoachTerminalKeepaliveReleasesSilentPeer(t *testing.T) {
	if os.Getenv("LABCENTRAL_COACH_SSH_TEST") != "1" {
		t.Skip("enable isolated loopback transport checks with LABCENTRAL_COACH_SSH_TEST=1")
	}
	s, v, teams, _ := privateFixture(t)
	v.User.Admin = true
	ctx, cancel := context.WithTimeout(context.Background(), 42*time.Second)
	defer cancel()
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	engine := &coachTransportEngine{Engine: s.Engine, wait: true, finished: make(chan error, 1)}
	s.Engine = engine
	server := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + server.Listener.Addr().String()
	server.Config.Handler = s.Handler()
	server.StartTLS()
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL+"/api/admin/teams/"+teams[1].ID+"/machines/support/terminal", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err = conn.Write(ctx, websocket.MessageText, []byte(`{"csrf":"csrf"}`)); err != nil {
		t.Fatal(err)
	}
	// Deliberately don't read: the peer never processes Ping or sends Pong.
	select {
	case <-engine.finished:
	case <-ctx.Done():
		t.Fatal("quiet unresponsive browser retained its SSH context")
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) == coachSessionRenewal || websocket.CloseStatus(err) == coachAccessRevoked {
		t.Fatal("transport timeout was misclassified as renewal or access revocation", err)
	}
}
func TestCoachWebSocketRequiresCSRFAndRevokesOpenTransport(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	ctx := context.Background()
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	engine := &coachTransportEngine{Engine: s.Engine, entered: make(chan string, 1), wait: true}
	s.Engine = engine
	server := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + server.Listener.Addr().String()
	server.Config.Handler = s.Handler()
	server.StartTLS()
	defer server.Close()
	address := server.URL + "/api/admin/teams/" + teams[1].ID + "/machines/support/terminal"
	dial := func(csrf string) *websocket.Conn {
		t.Helper()
		connection, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = connection.Write(ctx, websocket.MessageText, []byte(`{"csrf":"`+csrf+`"}`)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.CloseNow() })
		return connection
	}
	bad := dial("wrong")
	deadline, stop := context.WithTimeout(ctx, 3*time.Second)
	_, _, err := bad.Read(deadline)
	stop()
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation || engine.calls.Load() != 0 {
		t.Fatal("unverified console reached SSH", err, engine.calls.Load())
	}
	good := dial(v.CSRF)
	select {
	case name := <-engine.entered:
		if name != "support" {
			t.Fatal(name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coach console did not select hidden machine")
	}
	v.User.Admin = false
	if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	deadline, stop = context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	if _, _, err = good.Read(deadline); websocket.CloseStatus(err) != coachAccessRevoked || deadline.Err() != nil {
		t.Fatal("revoked console remained open", err)
	}
}

func TestCoachTerminalRejectsUnboundHelloBeforeSSH(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	v.User.Admin = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	engine := &coachTransportEngine{Engine: s.Engine}
	s.Engine = engine
	server := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + server.Listener.Addr().String()
	server.Config.Handler = s.Handler()
	server.StartTLS()
	defer server.Close()
	for _, test := range []struct {
		name string
		kind websocket.MessageType
		body string
	}{
		{"binary hello", websocket.MessageBinary, `{"csrf":"csrf"}`},
		{"changed meeting", websocket.MessageText, `{"csrf":"csrf","meetingID":"other"}`},
		{"changed revision", websocket.MessageText, `{"csrf":"csrf","template":"other"}`},
		{"changed allocation", websocket.MessageText, `{"csrf":"csrf","instanceID":"other"}`},
		{"partial size", websocket.MessageText, `{"csrf":"csrf","cols":100}`},
		{"huge size", websocket.MessageText, `{"csrf":"csrf","cols":501,"rows":30}`},
		{"negative size", websocket.MessageText, `{"csrf":"csrf","cols":100,"rows":-1}`},
		{"noninteger size", websocket.MessageText, `{"csrf":"csrf","cols":100.5,"rows":30}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, _, err := websocket.Dial(ctx, server.URL+"/api/admin/teams/"+teams[1].ID+"/machines/support/terminal", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, test.kind, []byte(test.body)); err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation || engine.calls.Load() != 0 {
				t.Fatal("unbound terminal reached SSH", err, engine.calls.Load())
			}
		})
	}
}

// A real SSH protocol fixture exercises relay bytes and host-key pinning. It
// starts only a loopback listener, executes no guest commands, and is not native
// VM acceptance.
func TestCoachSSHRelayBytesAndPinnedIdentity(t *testing.T) {
	if os.Getenv("LABCENTRAL_COACH_SSH_TEST") != "1" {
		t.Skip("set LABCENTRAL_COACH_SSH_TEST=1 in an isolated test namespace permitting loopback SSH")
	}
	testCoachSSHRelay(t, "linux")
}
func TestCoachWindowsSSHRelayBytesAndPinnedIdentity(t *testing.T) { testCoachSSHRelay(t, "windows") }
func testCoachSSHRelay(t *testing.T, osFamily string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("loopback SSH fixture port unavailable: ", err)
	}
	defer listener.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialSigner, err := ssh.NewSignerFromKey(credential)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(credential)
	if err != nil {
		t.Fatal(err)
	}
	material := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		user := "lab"
		if osFamily == "windows" {
			user = "Administrator"
		}
		if meta.User() != user || !bytes.Equal(key.Marshal(), credentialSigner.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unauthorized fixture key")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	ptys := make(chan []byte, 16)
	windows := make(chan []byte, 16)
	go func() {
		for {
			socket, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer socket.Close()
				server, channels, requests, err := ssh.NewServerConn(socket, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for pending := range channels {
					if pending.ChannelType() != "session" {
						pending.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := pending.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							switch request.Type {
							case "pty-req":
								ptys <- request.Payload
								request.Reply(true, nil)
							case "window-change":
								windows <- request.Payload
							case "shell":
								request.Reply(true, nil)
								go func() {
									defer channel.Close()
									channel.Write([]byte("\x1b[32mCoach SSH fixture ready\x1b[0m\r\n"))
									buffer := make([]byte, 100)
									for {
										n, err := channel.Read(buffer)
										if err != nil {
											return
										}
										if bytes.Equal(buffer[:n], []byte("drop\r")) {
											server.Close()
											return
										}
										if bytes.Equal(buffer[:n], []byte("exit\r")) {
											channel.Write([]byte("Final shell output\r\n"))
											channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
											return
										}
										channel.Write(append([]byte("Received: "), buffer[:n]...))
									}
								}()
							default:
								request.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	s, v, teams, src := privateFixture(t)
	v.User.Admin = true
	if err = s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if osFamily == "windows" {
		for name, image := range src.Lab.Images {
			image.OS = "windows"
			src.Lab.Images[name] = image
		}
		if err = s.Store.Put(context.Background(), "source", "pinned", src); err != nil {
			t.Fatal(err)
		}
	}
	s.Engine = &pinnedCoachEngine{Engine: s.Engine, src: src, key: material, host: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())), os: osFamily, address: listener.Addr().String()}
	app := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, machine := range []string{"workstation", "support", "support"} {
		connection, _, err := websocket.Dial(ctx, app.URL+"/api/admin/teams/"+teams[1].ID+"/machines/"+machine+"/terminal", &websocket.DialOptions{HTTPClient: app.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = connection.Write(ctx, websocket.MessageText, []byte(`{"csrf":"csrf","meetingID":"incident","template":"pinned","cols":132,"rows":42}`)); err != nil {
			t.Fatal(err)
		}
		kind, output, err := connection.Read(ctx)
		var ready struct {
			Type    string
			Expires time.Time
		}
		if err != nil || kind != websocket.MessageText || json.Unmarshal(output, &ready) != nil || ready.Type != "ready" || ready.Expires.IsZero() {
			t.Fatal("terminal ready handshake", string(output), err)
		}
		var pty struct {
			Term                      string
			Cols, Rows, Width, Height uint32
			Modes                     string
		}
		select {
		case payload := <-ptys:
			if ssh.Unmarshal(payload, &pty) != nil || pty.Term != "xterm-256color" || pty.Cols != 132 || pty.Rows != 42 {
				t.Fatal("PTY settings", pty)
			}
		case <-ctx.Done():
			t.Fatal("PTY request missing")
		}
		kind, output, err = connection.Read(ctx)
		if err != nil || kind != websocket.MessageBinary || !bytes.Contains(output, []byte("Coach SSH fixture ready")) {
			t.Fatal("SSH output unavailable", string(output), err)
		}
		if err = connection.Write(ctx, websocket.MessageBinary, []byte("hello\r")); err != nil {
			t.Fatal(err)
		}
		_, output, err = connection.Read(ctx)
		if err != nil || !bytes.Contains(output, []byte("Received: hello")) {
			t.Fatal("SSH input unavailable", string(output), err)
		}
		if err = connection.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":25}`)); err != nil {
			t.Fatal(err)
		}
		var window struct{ Cols, Rows, Width, Height uint32 }
		if osFamily == "windows" {
			// A following input echo proves the prior control frame was consumed.
			if err = connection.Write(ctx, websocket.MessageBinary, []byte("after-resize\r")); err != nil {
				t.Fatal(err)
			}
			_, output, err = connection.Read(ctx)
			if err != nil || !bytes.Contains(output, []byte("after-resize")) {
				t.Fatal("Windows input after resize", string(output), err)
			}
			select {
			case <-windows:
				t.Fatal("Windows console received resize signal")
			default:
			}
		} else {
			select {
			case payload := <-windows:
				if ssh.Unmarshal(payload, &window) != nil || window.Cols != 80 || window.Rows != 25 {
					t.Fatal("window change", window)
				}
			case <-ctx.Done():
				t.Fatal("resize missing")
			}
		}
		if machine == "workstation" {
			if err = connection.Write(ctx, websocket.MessageBinary, []byte("exit\r")); err != nil {
				t.Fatal(err)
			}
			_, output, err = connection.Read(ctx)
			if err != nil || !bytes.Contains(output, []byte("Final shell output")) {
				t.Fatal("final output was lost", string(output), err)
			}
			_, _, err = connection.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatal("shell exit should stop reconnect", err)
			}
		} else if i == 1 {
			if err = connection.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":900,"rows":25}`)); err != nil {
				t.Fatal(err)
			}
			_, _, err = connection.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatal("invalid resize admitted", err)
			}
		}
		if i == 2 {
			if err = connection.Write(ctx, websocket.MessageBinary, []byte("drop\r")); err != nil {
				t.Fatal(err)
			}
			_, _, err = connection.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusInternalError {
				t.Fatal("lost SSH transport should be retryable", err)
			}
		}
		connection.CloseNow()
	}
	// A changed host key must fail before the ready handshake or any output.
	_, wrongPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongHost, err := ssh.NewSignerFromKey(wrongPrivate)
	if err != nil {
		t.Fatal(err)
	}
	s.Engine.(*pinnedCoachEngine).host = string(ssh.MarshalAuthorizedKey(wrongHost.PublicKey()))
	conn, _, err := websocket.Dial(ctx, app.URL+"/api/admin/teams/"+teams[1].ID+"/machines/support/terminal", &websocket.DialOptions{HTTPClient: app.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err = conn.Write(ctx, websocket.MessageText, []byte(`{"csrf":"csrf"}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatal("wrong SSH host pin admitted", err)
	}
}

type pinnedCoachEngine struct {
	Engine
	src         lc.Source
	key         []byte
	host        string
	os, address string
}

func (e *pinnedCoachEngine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	return fn(lc.Instance{ID: instance, Status: "ready", Plan: lc.Plan{Source: e.src}}, lc.PlannedMachine{Name: machine, Address: "10.77.0.10", HostPublicKey: e.host, OS: e.os}, lc.Resource{Kind: "machine", Name: machine, Owner: instance, Address: e.address, PrivateAddress: "10.77.0.10"}, e.key)
}

func TestCoachGuestBindsPrivateAddressAndOwnership(t *testing.T) {
	machine := lc.PlannedMachine{Name: "support", Address: "10.77.0.10"}
	resource := lc.Resource{Kind: "machine", Name: "support", Owner: "instance", Address: "203.0.113.10", PrivateAddress: machine.Address}
	if !coachGuestMatches("instance", "support", machine, resource) {
		t.Fatal("Public management endpoint must preserve the private lab identity")
	}
	for _, change := range []func(*lc.Resource){
		func(r *lc.Resource) { r.PrivateAddress = "10.77.0.11" },
		func(r *lc.Resource) { r.PrivateAddress = "" },
		func(r *lc.Resource) { r.Owner = "other-instance" },
		func(r *lc.Resource) { r.Name = "workstation" },
		func(r *lc.Resource) { r.Address = "" },
	} {
		bad := resource
		change(&bad)
		if coachGuestMatches("instance", "support", machine, bad) {
			t.Fatal("Unbound guest resource admitted", bad)
		}
	}
}
