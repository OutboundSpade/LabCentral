package proxmox

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testTokenID = "labcentral@pve!discovery"
const testTokenSecret = "11111111-2222-3333-4444-555555555555"

func TestClientDiscoversTypedSortedInventoryAndPartialIssues(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s", request.Method)
		}
		if actual := request.Header.Get("Authorization"); actual != "PVEAPIToken="+testTokenID+"="+testTokenSecret {
			t.Errorf("Authorization = %q", actual)
		}
		mu.Lock()
		requested = append(requested, request.URL.RequestURI())
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api2/json/version":
			writeAPIData(t, response, map[string]any{"release": "9.0", "version": "pve-manager/9.0.3/abcd1234", "repoid": "abcd1234", "console": "html5"})
		case "/api2/json/nodes":
			writeAPIData(t, response, []any{
				map[string]any{"node": "pve-b", "status": "online", "maxcpu": 16, "maxmem": 64 << 30},
				map[string]any{"node": "pve-offline", "status": "offline", "maxcpu": 8},
				map[string]any{"node": "pve-a", "status": "online", "maxcpu": 32, "maxmem": 128 << 30, "mem": 8 << 30},
			})
		case "/api2/json/nodes/pve-a/storage":
			assertQuery(t, request, "content", "images")
			assertQuery(t, request, "enabled", "1")
			assertQuery(t, request, "format", "1")
			writeAPIData(t, response, []any{
				map[string]any{"storage": "z-store", "type": "zfspool", "content": "images,rootdir", "active": 1, "enabled": true, "shared": 0, "total": 1000, "avail": 600, "formats": map[string]any{"default": "raw", "supported": []string{"raw"}}},
				map[string]any{"storage": "a-store", "type": "dir", "content": "iso,images", "active": true, "enabled": 1, "shared": false, "formats": map[string]any{"default": "qcow2", "supported": []string{"raw", "qcow2", "qcow2"}}},
			})
		case "/api2/json/nodes/pve-b/storage":
			http.Error(response, `{"errors":"`+testTokenSecret+`"}`, http.StatusForbidden)
		case "/api2/json/nodes/pve-a/network":
			assertQuery(t, request, "type", "bridge")
			writeAPIData(t, response, []any{
				map[string]any{"iface": "vmbr1", "type": "bridge", "active": 1, "bridge_vlan_aware": 1, "bridge_ports": "enp2s0 enp1s0"},
				map[string]any{"iface": "vmbr0", "type": "bridge", "active": true, "autostart": 1, "bridge_vlan_aware": 0},
			})
		case "/api2/json/nodes/pve-b/network":
			writeAPIData(t, response, []any{})
		case "/api2/json/nodes/pve-a/qemu":
			assertQuery(t, request, "full", "0")
			writeAPIData(t, response, []any{
				map[string]any{"vmid": 202, "name": "template-b", "status": "stopped", "template": 1},
				map[string]any{"vmid": 201, "name": "ordinary-vm", "status": "running", "template": 0},
				map[string]any{"vmid": 101, "name": "template-a", "status": "stopped", "template": true},
			})
		case "/api2/json/nodes/pve-b/qemu":
			http.Error(response, "temporarily unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	defer client.CloseIdleConnections()

	inventory, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := nodeNames(inventory.Nodes); got != "pve-a,pve-b,pve-offline" {
		t.Fatalf("nodes = %s", got)
	}
	if !inventory.Nodes[0].QEMUAPI || inventory.Nodes[1].QEMUAPI || inventory.Nodes[2].QEMUAPI {
		t.Fatalf("unexpected QEMU evidence: %#v", inventory.Nodes)
	}
	if len(inventory.Storages) != 2 || inventory.Storages[0].ID != "a-store" || strings.Join(inventory.Storages[0].Formats, ",") != "qcow2,raw" {
		t.Fatalf("storages not normalized: %#v", inventory.Storages)
	}
	if len(inventory.Bridges) != 2 || inventory.Bridges[0].Name != "vmbr0" || strings.Join(inventory.Bridges[1].Ports, ",") != "enp1s0,enp2s0" {
		t.Fatalf("bridges not normalized: %#v", inventory.Bridges)
	}
	if len(inventory.Templates) != 2 || inventory.Templates[0].VMID != 101 || inventory.Templates[1].VMID != 202 {
		t.Fatalf("templates not normalized: %#v", inventory.Templates)
	}
	wantIssues := []DiscoveryIssue{{Node: "pve-b", Scope: "qemu", Code: "api_unavailable"}, {Node: "pve-b", Scope: "storage", Code: "permission_denied"}}
	if fmt.Sprint(inventory.Issues) != fmt.Sprint(wantIssues) {
		t.Fatalf("issues = %#v, want %#v", inventory.Issues, wantIssues)
	}
	firstDigest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := inventory.Digest()
	if err != nil || firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("unstable digest %q %q: %v", firstDigest, secondDigest, err)
	}
	for _, uri := range requested {
		if strings.Contains(uri, "pve-offline") {
			t.Fatalf("offline node was queried: %s", uri)
		}
	}
}

func TestClientRejectsUnsafeConfiguration(t *testing.T) {
	validCredentials := APITokenCredentials{TokenID: testTokenID, TokenSecret: testTokenSecret}
	tests := []TargetConfig{
		{Endpoint: "http://pve.example:8006"},
		{Endpoint: "https://user:p@pve.example:8006"},
		{Endpoint: "https://pve.example:8006/api2"},
		{Endpoint: "https://pve.example:8006?x=1"},
		{Endpoint: "https://pve.example:8006", RequestTimeoutSeconds: 31},
		{Endpoint: "https://pve.example:8006", MaxResponseBytes: 512},
		{Endpoint: "https://pve.example:8006", CACertificatePEM: "not a certificate"},
	}
	for _, config := range tests {
		if _, err := NewClient(config, validCredentials); err == nil {
			t.Errorf("NewClient(%#v) succeeded", config)
		}
	}
	if _, err := NewClient(TargetConfig{Endpoint: "https://pve.example:8006"}, APITokenCredentials{TokenID: "invalid", TokenSecret: testTokenSecret}); err == nil {
		t.Fatal("invalid token ID accepted")
	}
	if _, err := NewClient(TargetConfig{Endpoint: "https://pve.example:8006"}, APITokenCredentials{TokenID: testTokenID, TokenSecret: "bad\r\nheader"}); err == nil {
		t.Fatal("invalid token secret accepted")
	}
}

func TestClientEnvelopesAreStrictAndCredentialErrorsAreRedacted(t *testing.T) {
	config := []byte(`{"endpoint":"https://pve.example:8006","unknown":true}`)
	credentials := []byte(`{"tokenID":"` + testTokenID + `","tokenSecret":"` + testTokenSecret + `"}`)
	if _, err := NewClientFromEnvelopes(config, credentials); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown config field error = %v", err)
	}
	validConfig := []byte(`{"endpoint":"https://pve.example:8006"}`)
	badCredentials := []byte(`{"tokenID":"bad","tokenSecret":"` + testTokenSecret + `","extra":"` + testTokenSecret + `"}`)
	_, err := NewClientFromEnvelopes(validConfig, badCredentials)
	if err == nil || strings.Contains(err.Error(), testTokenSecret) {
		t.Fatalf("credential error was absent or leaked secret: %v", err)
	}
	duplicateConfig := []byte(`{"endpoint":"https://pve.example:8006","endpoint":"https://other.example:8006"}`)
	if _, err := NewClientFromEnvelopes(duplicateConfig, credentials); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("duplicate config field error = %v", err)
	}
	duplicateCredentials := []byte(`{"tokenID":"` + testTokenID + `","tokenSecret":"` + testTokenSecret + `","tokenSecret":"must-not-win"}`)
	if _, err := NewClientFromEnvelopes(validConfig, duplicateCredentials); err == nil || strings.Contains(err.Error(), testTokenSecret) {
		t.Fatalf("duplicate credential field error was absent or leaked secret: %v", err)
	}
}

func TestClientRefusesRedirectsAndDoesNotExposeResponseBody(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			http.Redirect(response, request, "/api2/json/nodes", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		client := newTestClient(t, server, 0)
		defer client.CloseIdleConnections()
		_, err := client.Discover(context.Background())
		if err == nil || !strings.Contains(err.Error(), "redirects are disabled") {
			t.Fatalf("redirect error = %v", err)
		}
	})
	t.Run("error body", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			http.Error(response, testTokenSecret, http.StatusInternalServerError)
		}))
		defer server.Close()
		client := newTestClient(t, server, 0)
		defer client.CloseIdleConnections()
		_, err := client.Discover(context.Background())
		if err == nil || strings.Contains(err.Error(), testTokenSecret) {
			t.Fatalf("API error was absent or leaked body: %v", err)
		}
	})
}

func TestClientRejectsProviderNodeNameBeforeNodeRequests(t *testing.T) {
	var nodeRequest bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api2/json/nodes/") {
			nodeRequest = true
		}
		switch request.URL.Path {
		case "/api2/json/version":
			writeAPIData(t, response, map[string]any{"release": "9.0", "version": "pve-manager/9.0", "repoid": "abcd1234"})
		case "/api2/json/nodes":
			writeAPIData(t, response, []any{map[string]any{"node": "../malicious", "status": "online"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	defer client.CloseIdleConnections()
	if _, err := client.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid node") {
		t.Fatalf("malicious node name error = %v", err)
	}
	if nodeRequest {
		t.Fatal("provider-returned node name was used in a request path before validation")
	}
}

func TestClientBoundsResponsesAndVerifiesTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"data":{"release":"9.0","version":"` + strings.Repeat("x", 2000) + `"}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, 1024)
	defer client.CloseIdleConnections()
	if _, err := client.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("response limit error = %v", err)
	}
	untrusted, err := NewClient(TargetConfig{Endpoint: server.URL}, APITokenCredentials{TokenID: testTokenID, TokenSecret: testTokenSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.CloseIdleConnections()
	if _, err := untrusted.Discover(context.Background()); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
}

func TestClientRejectsMissingAPIData(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{}`,
		"null":    `{"data":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(body))
			}))
			defer server.Close()
			client := newTestClient(t, server, 0)
			defer client.CloseIdleConnections()
			if _, err := client.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "data is required") {
				t.Fatalf("missing data error = %v", err)
			}
		})
	}
}

func TestAPIBool(t *testing.T) {
	for input, expected := range map[string]bool{"true": true, "1": true, `"1"`: true, "false": false, "0": false, "null": false} {
		var value apiBool
		if err := json.Unmarshal([]byte(input), &value); err != nil || bool(value) != expected {
			t.Errorf("unmarshal %s = %t, %v", input, value, err)
		}
	}
	var value apiBool
	if err := json.Unmarshal([]byte(`"yes"`), &value); err == nil {
		t.Fatal("invalid Proxmox boolean accepted")
	}
}

func newTestClient(t *testing.T, server *httptest.Server, responseLimit int64) *Client {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS server has no certificate")
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if _, err := x509.ParseCertificate(certificate.Raw); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(TargetConfig{Endpoint: server.URL, CACertificatePEM: string(ca), MaxResponseBytes: responseLimit}, APITokenCredentials{TokenID: testTokenID, TokenSecret: testTokenSecret})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeAPIData(t *testing.T, response http.ResponseWriter, data any) {
	t.Helper()
	if err := json.NewEncoder(response).Encode(map[string]any{"data": data}); err != nil {
		t.Error(err)
	}
}

func assertQuery(t *testing.T, request *http.Request, name, expected string) {
	t.Helper()
	if actual := request.URL.Query().Get(name); actual != expected {
		t.Errorf("query %s = %q, want %q", name, actual, expected)
	}
}

func nodeNames(nodes []Node) string {
	values := make([]string, 0, len(nodes))
	for _, node := range nodes {
		values = append(values, node.Name)
	}
	return strings.Join(values, ",")
}
