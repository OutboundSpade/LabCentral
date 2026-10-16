package access

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
