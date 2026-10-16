package access

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

func TestDCVThroughGatewayReusesSSHAndPinsTLS(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			defer upstream.Close()
			u, _ := url.Parse(upstream.URL)
			cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
			if !valid {
				altered := append([]byte(nil), upstream.Certificate().Raw...)
				altered[len(altered)-1] ^= 1
				cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: altered})
			}
			var closed atomic.Bool
			closed.Store(true)
			c := lc.Command{Action: "access.open", Operation: "dcv-operation", Owner: "owner", Environment: lc.Environment{Platform: "aws"}, Prior: lc.Resource{Kind: "machine", Owner: "owner", Name: "workstation", Address: "10.77.0.11"}, Machine: lc.PlannedMachine{Name: "workstation"}}
			name := dcvName(c)
			machine := sshtest.New(t, nil, func(command string) string {
				if strings.Contains(command, "openssl x509") {
					return string(cert)
				}
				if strings.Contains(command, "dcv create-session") {
					closed.Store(false)
				}
				if strings.Contains(command, "dcv close-session") && !strings.Contains(command, "useradd") {
					closed.Store(true)
				}
				if command == "sudo dcv list-sessions -j" {
					if closed.Load() {
						return "[]"
					}
					return fmt.Sprintf(`[{"id":%q,"owner":%q,"status":"running","x11-display":":1"}]`, name, name)
				}
				return ""
			})
			gateway := sshtest.New(t, func(dest string) string {
				switch dest {
				case "10.77.0.11:22":
					return machine.Address
				case "10.77.0.11:8443":
					return u.Host
				}
				return ""
			}, nil)
			key, _ := sshtest.Key(t)
			c.ClientPrivateKey = key
			c.Machine.HostPublicKey = machine.HostKey
			data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey, ENI: "eni-owned"})
			c.Gateway = lc.Resource{Kind: "gateway", Owner: "owner", ID: "i-gateway", Address: gateway.Address, Complete: true, Data: data}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			desktop, info, err := openDCV(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			defer desktop.close()
			if machine.Connections.Load() != 1 || gateway.Connections.Load() != 1 {
				t.Fatal("initial launch did not reuse pinned connections")
			}
			if ready, err := desktop.ready(ctx); err != nil || !ready {
				t.Fatal("cached readiness", err)
			}
			launch, _ := url.Parse(info.URL)
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+launch.Host+"/", nil)
			req.AddCookie(&http.Cookie{Name: "labcentral_access", Value: desktop.relay.cookie})
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			want := 204
			if !valid {
				want = 502
			}
			if res.StatusCode != want {
				t.Fatal("wrong tunneled TLS result", res.StatusCode, want)
			}
			if gateway.Connections.Load() != 1 || machine.Connections.Load() != 2 || gateway.Channels.Load() != 3 {
				t.Fatal("desktop redialed", gateway.Connections.Load(), machine.Connections.Load(), gateway.Channels.Load())
			}
			if err = desktop.close(); err != nil {
				t.Fatal(err)
			}
			if err = desktop.close(); err != nil {
				t.Fatal("retained cleanup could not retry after closing its original dialer", err)
			}
		})
	}
}
