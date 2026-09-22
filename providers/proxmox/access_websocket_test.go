package proxmox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestVNCWebSocketAuthenticatesExactEndpointAndKeepsStreamAfterDiscovery(t *testing.T) {
	const ticket = "ticket-canary +/:?&"
	var upgraded atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken="+testTokenID+"="+testTokenSecret {
			t.Error("missing provider authorization")
			http.Error(w, "denied", 403)
			return
		}
		switch r.URL.Path {
		case "/api2/json/nodes/pve-a/qemu/910001/vncproxy":
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("websocket") != "1" || r.Form.Get("generate-password") != "1" {
				t.Error("incorrect console request")
			}
			writeAPIData(t, w, map[string]any{"port": "5901", "ticket": ticket, "password": "vnc-only"})
		case "/api2/json/nodes/pve-a/qemu/910001/vncwebsocket":
			if r.Method != "GET" || r.URL.Query().Get("vncticket") != ticket || r.URL.Query().Get("port") != "5901" {
				t.Error("incorrect WebSocket binding")
				return
			}
			ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
			if err != nil {
				return
			}
			defer ws.CloseNow()
			upgraded.Store(true)
			conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
			_, _ = io.Copy(conn, conn)
		default:
			t.Error("unexpected upstream endpoint")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	conn, password, err := client.VNCWebSocket(ctx, "pve-a", 910001)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel() // discovery's request context must not own the active console
	if password != "vnc-only" || !upgraded.Load() {
		t.Fatal("console setup failed")
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "RFB bytes"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 9)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "RFB bytes" {
		t.Fatalf("stream: %q %v", buf, err)
	}
}

func TestVNCWebSocketRejectsRedirectUntrustedCertificateAndMalformedTicket(t *testing.T) {
	for _, scenario := range []string{"redirect", "untrusted", "bad-port", "missing-ticket", "missing-password", "wrong-subprotocol"} {
		t.Run(scenario, func(t *testing.T) {
			var redirected atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/vncproxy") {
					port := 5901
					ticket := "never-log-ticket"
					password := "never-log-password"
					if scenario == "bad-port" {
						port = 8006
					}
					if scenario == "missing-ticket" {
						ticket = ""
					}
					if scenario == "missing-password" {
						password = ""
					}
					writeAPIData(t, w, map[string]any{"port": port, "ticket": ticket, "password": password})
					return
				}
				if r.URL.Path == "/leak" {
					redirected.Store(true)
					return
				}
				if scenario == "wrong-subprotocol" {
					ws, err := websocket.Accept(w, r, nil)
					if err == nil {
						defer ws.CloseNow()
						_, _, _ = ws.Read(context.Background())
					}
					return
				}
				http.Redirect(w, r, "/leak", http.StatusFound)
			}))
			defer server.Close()
			client := newTestClient(t, server, 0)
			defer client.CloseIdleConnections()
			if scenario == "untrusted" {
				transport := client.httpClient.Transport.(*http.Transport).Clone()
				transport.TLSClientConfig = transport.TLSClientConfig.Clone()
				transport.TLSClientConfig.RootCAs = nil
				client.httpClient.Transport = transport
			}
			conn, password, err := client.VNCWebSocket(context.Background(), "pve-a", 910001)
			if err == nil || conn != nil || password != "" {
				if conn != nil {
					conn.Close()
				}
				t.Fatal("invalid WebSocket accepted")
			}
			if redirected.Load() || strings.Contains(err.Error(), "never-log") || strings.Contains(err.Error(), testTokenSecret) {
				t.Fatal("redirect or secret escaped provider boundary")
			}
		})
	}
}
