package proxmox

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
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
	env := lc.Environment{Platform: "proxmox", Proxmox: lc.ProxmoxConfig{URL: server.URL, TokenID: "test@pve!test", TokenEnv: "LC_TEST_TOKEN", Node: "node", Storage: "local", InsecureSkipTLSVerify: true}}
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
func TestBridgeWithUplinkRejectedBeforeMutation(t *testing.T) {
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
		case "/api2/json/nodes/node/network":
			response(w, []bridge{{Interface: "vmbr-labs", Type: "bridge", VLANAware: 1, Ports: "eno1"}})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	env.Proxmox.LabBridge = "vmbr-labs"
	_, err := (&Provider{}).check(context.Background(), c, lc.Command{Environment: env})
	if err == nil || !strings.Contains(err.Error(), "no bridge_ports") {
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
		if strings.HasSuffix(r.URL.Path, "/access/permissions") {
			response(w, map[string]any{"/vms/2000": map[string]int{"VM.Audit": 1}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/config") {
			response(w, VMConfig{Description: "other-owner"})
		} else {
			response(w, []map[string]any{{"vmid": 2000, "node": "node"}})
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

func TestDestroyDoesNotDeleteUnreferencedVMIDNamedDisks(t *testing.T) {
	deleted := false
	c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/storage/local/content"):
			response(w, []any{})
		case strings.HasSuffix(r.URL.Path, "/access/permissions"):
			response(w, map[string]any{"/": map[string]int{"VM.Audit": 1, "Datastore.Audit": 1}})
		case strings.HasSuffix(r.URL.Path, "/cluster/resources"):
			if deleted {
				response(w, []any{})
			} else {
				response(w, []map[string]any{{"vmid": 2000, "node": "node"}})
			}
		case strings.HasSuffix(r.URL.Path, "/config"):
			response(w, VMConfig{Template: 1, Description: marker("owner", "image")})
		case strings.Contains(r.URL.Path, "/tasks/"):
			response(w, map[string]string{"status": "stopped", "exitstatus": "OK"})
		case r.Method == "DELETE":
			if r.URL.Query().Get("destroy-unreferenced-disks") != "" {
				t.Fatal("deletion included unrelated VMID-named disks")
			}
			deleted = true
			response(w, "UPID:node:test")
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	result, err := (&Provider{Journal: journal.Journal{Root: t.TempDir()}}).destroy(context.Background(), c, lc.Command{Owner: "owner", Environment: env, Prior: lc.Resource{Kind: "image", Name: "image", ID: "2000", Owner: "owner"}})
	if err != nil || !result.Absent || !deleted {
		t.Fatal(result, err)
	}
}

func TestFilteredInventoryCannotConfirmAbsenceWithoutAudit(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/access/permissions" || r.Method != "GET" {
			t.Fatal("unverified visibility reached lifecycle API", r.Method, r.URL.Path)
		}
		response(w, map[string]any{})
	})
	result, err := (&Provider{}).destroy(context.Background(), c, lc.Command{Owner: "owner", Prior: lc.Resource{Kind: "machine", Name: "server", ID: "2000", Owner: "owner"}})
	if err == nil || result.Absent || !strings.Contains(err.Error(), "VM.Audit") {
		t.Fatal("filtered absence accepted", result, err)
	}
}

func TestDestroyRefusesVMOnAnotherNode(t *testing.T) {
	c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("unexpected mutation: %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/access/permissions") {
			response(w, map[string]any{"/vms": map[string]int{"VM.Audit": 1}})
		} else if r.URL.Path == "/api2/json/cluster/resources" {
			response(w, []map[string]any{{"vmid": 2000, "node": "another-node"}})
		} else {
			t.Fatalf("unexpected endpoint: %s", r.URL.Path)
		}
	})
	_, err := (&Provider{}).destroy(context.Background(), c, lc.Command{Owner: "owner", Environment: env, Prior: lc.Resource{Kind: "gateway", Name: "gateway", ID: "2000", Owner: "owner"}})
	if err == nil || !strings.Contains(err.Error(), "outside the configured node") {
		t.Fatalf("unexpected: %v", err)
	}
}
