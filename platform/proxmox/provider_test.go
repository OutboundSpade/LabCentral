package proxmox

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, lc.Environment) {
	t.Helper()
	server := httptest.NewTLSServer(h)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(server.Close)
	t.Setenv("LC_TEST_TOKEN", "private-test-secret")
	env := lc.Environment{Platform: "proxmox", Proxmox: lc.ProxmoxConfig{URL: server.URL, TokenID: "test@pve!test", TokenEnv: "LC_TEST_TOKEN", Node: "node", InsecureSkipTLSVerify: true}}
	c, err := NewClient(env)
	if err != nil {
		t.Fatal(err)
	}
	return c, env
}
func response(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(struct {
		Data any `json:"data"`
	}{v})
}
func TestDisabledFirewallRejectedBeforeMutation(t *testing.T) {
	mutations := 0
	c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "PVEAPIToken=") {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/api2/json/version":
			response(w, map[string]string{"version": "9.2.11"})
		case "/api2/json/nodes":
			response(w, []map[string]string{{"node": "node", "status": "online"}})
		case "/api2/json/cluster/firewall/options":
			response(w, map[string]int{"enable": 0})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	_, err := (&Provider{}).check(context.Background(), c, lc.Command{Environment: env})
	if err == nil || !strings.Contains(err.Error(), "firewall is disabled") {
		t.Fatalf("unexpected: %v", err)
	}
	if mutations != 0 {
		t.Fatal("mutated before validation")
	}
}
func TestForeignVMNeverStoppedOrDeleted(t *testing.T) {
	c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("foreign VM mutation: %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/config") {
			response(w, VMConfig{Description: "other-owner"})
		} else {
			response(w, []map[string]int{{"vmid": 2000}})
		}
	})
	_, err := (&Provider{}).destroy(context.Background(), c, lc.Command{Owner: "expected-owner", Environment: env, Prior: lc.Resource{Kind: "machine", Name: "test", ID: "2000", Owner: "expected-owner"}})
	if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
		t.Fatalf("unexpected: %v", err)
	}
}
func TestTLSVerificationIsDefault(t *testing.T) {
	_, env := testClient(t, func(w http.ResponseWriter, r *http.Request) { response(w, "ok") })
	env.Proxmox.InsecureSkipTLSVerify = false
	c, err := NewClient(env)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Call(context.Background(), "GET", "/version", nil, nil); err == nil {
		t.Fatal("untrusted certificate accepted without explicit opt-out")
	}
}
func TestAPIErrorsDoNotExposeSecrets(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("private-test-secret"))
	})
	err := c.Call(context.Background(), "GET", "/version", nil, nil)
	if err == nil || strings.Contains(err.Error(), "private-test-secret") {
		t.Fatalf("secret leaked: %v", err)
	}
}
