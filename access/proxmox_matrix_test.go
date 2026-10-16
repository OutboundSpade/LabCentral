package access

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestProxmoxDesktopTicketFailureMatrix(t *testing.T) {
	for _, fault := range []string{"response-loss", "authorization", "validation", "throttle", "server"} {
		t.Run(fault, func(t *testing.T) {
			var account atomic.Bool
			var tickets atomic.Int32
			c := lc.Command{Action: "access.open", Owner: "example-owner", Operation: "example-access", Access: lc.AccessOptions{Generation: strings.Repeat("a", 24)}, Machine: lc.PlannedMachine{Name: "desktop", HostPublicKey: ""}, Prior: lc.Resource{Kind: "machine", ID: "2000", Owner: "example-owner", Name: "desktop", Address: "192.0.2.20"}}
			machine := sshtest.New(t, nil, func(command string) string {
				if strings.Contains(command, "useradd") {
					account.Store(true)
				}
				if strings.Contains(command, "userdel") {
					account.Store(false)
				}
				return ""
			})
			gateway := sshtest.New(t, func(string) string { return machine.Address }, nil)
			key, _ := sshtest.Key(t)
			c.ClientPrivateKey = key
			c.Machine.HostPublicKey = machine.HostKey
			pin, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey})
			c.Gateway = lc.Resource{Kind: "gateway", ID: "2001", Owner: c.Owner, Address: gateway.Address, Complete: true, Data: pin}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api2/json/nodes/node/qemu/2000/config":
					fmt.Fprintf(w, `{"data":{"description":%q}}`, "labcentral:v2:"+c.Owner+":"+c.Machine.Name)
				case "/api2/json/nodes/node/qemu/2000/vncproxy":
					tickets.Add(1)
					if fault == "response-loss" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					w.WriteHeader(map[string]int{"authorization": 403, "validation": 400, "throttle": 429, "server": 500}[fault])
					fmt.Fprint(w, `{"message":"PRIVATE-UPSTREAM-PAYLOAD"}`)
				default:
					t.Error("unexpected native request", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			t.Setenv("LABCENTRAL_TEST_PROXMOX_TOKEN", "example-secret")
			c.Environment = lc.Environment{Platform: "proxmox", Proxmox: lc.ProxmoxConfig{URL: server.URL, Node: "node", TokenID: "example@pve!test", TokenEnv: "LABCENTRAL_TEST_PROXMOX_TOKEN", InsecureSkipTLSVerify: true}}
			manager := NewManager()
			defer manager.Close()
			_, err := manager.Execute(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "PRIVATE-UPSTREAM-PAYLOAD") || tickets.Load() != 1 || !account.Load() {
				t.Fatal("native failure hidden, replayed or exposed", err, tickets.Load(), account.Load())
			}
			// A new provider/manager over the retained command can revoke the guest effect
			// even though no local relay/session object was ever returned by the grant.
			manager = NewManager()
			c.Action = "access.close"
			result, err := manager.Execute(context.Background(), c)
			if err != nil || !result.Absent || account.Load() {
				t.Fatal("uncertain grant not revocable", result, err, account.Load())
			}
		})
	}
}

func TestProxmoxConsoleReconnectAndNativeSecrets(t *testing.T) {
	var tickets atomic.Int32
	var retained atomic.Bool
	machine := sshtest.New(t, nil, func(command string) string {
		if strings.Contains(command, "usermod --lock") {
			retained.Store(true)
		}
		return ""
	})
	gateway := sshtest.New(t, func(string) string { return machine.Address }, nil)
	key, _ := sshtest.Key(t)
	pin, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey})
	c := lc.Command{Action: "access.open", Owner: "example-owner", Operation: "example-access", Access: lc.AccessOptions{Generation: strings.Repeat("a", 24), Principal: "student", RetainWorkspace: true}, ClientPrivateKey: key, Machine: lc.PlannedMachine{Name: "desktop", HostPublicKey: machine.HostKey}, Prior: lc.Resource{Kind: "machine", ID: "2000", Owner: "example-owner", Name: "desktop", Address: "192.0.2.20"}, Gateway: lc.Resource{Kind: "gateway", ID: "2001", Owner: "example-owner", Address: gateway.Address, Complete: true, Data: pin}}
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			fmt.Fprintf(w, `{"data":{"description":%q}}`, "labcentral:v2:"+c.Owner+":"+c.Machine.Name)
		case strings.HasSuffix(r.URL.Path, "/vncproxy"):
			tickets.Add(1)
			fmt.Fprint(w, `{"data":{"ticket":"PRIVATE-NATIVE-TICKET","port":"5900","password":"PRIVATE-VNC-PASSWORD"}}`)
		case strings.HasSuffix(r.URL.Path, "/vncwebsocket"):
			if r.URL.Query().Get("vncticket") != "PRIVATE-NATIVE-TICKET" || !strings.Contains(r.Header.Get("Authorization"), "PRIVATE-API-TOKEN") {
				t.Error("native credentials missing")
				w.WriteHeader(403)
				return
			}
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			conn := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
			conn.Write([]byte("RFB 003.008\n"))
			version := make([]byte, 12)
			if _, err = io.ReadFull(conn, version); err != nil {
				return
			}
			conn.Write([]byte{1, 2})
			choice := make([]byte, 1)
			if _, err = io.ReadFull(conn, choice); err != nil {
				return
			}
			conn.Write([]byte("random-challenge"))
			response := make([]byte, 16)
			if _, err = io.ReadFull(conn, response); err != nil {
				return
			}
			// The native secret itself must never be sent as a client response.
			if strings.Contains(string(response), "PRIVATE") {
				t.Error("unencoded native secret")
			}
			conn.Write([]byte{0, 0, 0, 0})
			_, _ = io.Copy(conn, conn)
		default:
			http.NotFound(w, r)
		}
	}))
	defer native.Close()
	t.Setenv("LABCENTRAL_TEST_PROXMOX_TOKEN", "PRIVATE-API-TOKEN")
	c.Environment = lc.Environment{Platform: "proxmox", Proxmox: lc.ProxmoxConfig{URL: native.URL, Node: "node", TokenID: "example@pve!test", TokenEnv: "LABCENTRAL_TEST_PROXMOX_TOKEN", InsecureSkipTLSVerify: true}}
	manager := NewManager()
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.Execute(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	if err = json.Unmarshal(result.Resource.Data, &info); err != nil {
		t.Fatal(err)
	}
	launch, _ := url.Parse(info.URL)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(info.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	cookie := res.Cookies()[0]
	for _, path := range []string{"/", "/credentials", "/vendor/core/rfb.js"} {
		req, _ := http.NewRequest("GET", "http://"+launch.Host+path, nil)
		req.AddCookie(cookie)
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		for _, secret := range []string{"PRIVATE-NATIVE-TICKET", "PRIVATE-VNC-PASSWORD", "PRIVATE-API-TOKEN"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("native secret exposed", path)
			}
		}
		if path == "/credentials" && res.StatusCode != 404 {
			t.Fatal("native credential endpoint still present")
		}
	}
	for range 2 {
		headers := http.Header{}
		headers.Set("Cookie", cookie.String())
		headers.Set("Origin", "http://"+launch.Host)
		var ws *websocket.Conn
		deadline := time.Now().Add(time.Second)
		for {
			var response *http.Response
			ws, response, err = websocket.Dial(ctx, "ws://"+launch.Host+"/websocket", &websocket.DialOptions{HTTPHeader: headers})
			if err == nil {
				break
			}
			if response == nil || response.StatusCode != 409 || time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
		conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
		version := make([]byte, 12)
		if _, err = io.ReadFull(conn, version); err != nil {
			t.Fatal(err)
		}
		conn.Write(version)
		types := make([]byte, 2)
		io.ReadFull(conn, types)
		if string(types) != string([]byte{1, 1}) {
			t.Fatal(types)
		}
		conn.Write([]byte{1})
		success := make([]byte, 4)
		io.ReadFull(conn, success)
		conn.Write([]byte("frame"))
		echo := make([]byte, 5)
		if _, err = io.ReadFull(conn, echo); err != nil || string(echo) != "frame" {
			t.Fatal("tunnel failed", err)
		}
		ws.Close(websocket.StatusNormalClosure, "")

	}
	if tickets.Load() != 3 {
		t.Fatal("connection did not receive fresh ticket", tickets.Load())
	}
	c.Action = "access.close"
	if _, err = manager.Execute(ctx, c); err != nil || !retained.Load() {
		t.Fatal("retained guest account was not locked", err)
	}
}
