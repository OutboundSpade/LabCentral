package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoAuthListenerClassification(t *testing.T) {
	for _, testCase := range []struct {
		name string
		ip   net.IP
		want bool
	}{
		{"IPv4 loopback", net.ParseIP("127.0.0.1"), true},
		{"IPv6 loopback", net.ParseIP("::1"), true},
		{"IPv4 unspecified", net.ParseIP("0.0.0.0"), false},
		{"private interface", net.ParseIP("192.168.10.2"), false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			listener := stubListener{address: &net.TCPAddr{IP: testCase.ip, Port: 8080}}
			if got := listenerIsLoopback(listener); got != testCase.want {
				t.Fatalf("listenerIsLoopback(%s) = %t, want %t", testCase.ip, got, testCase.want)
			}
		})
	}
}

func TestHealthcheckRequiresHealthyV1ServerInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/server-info" {
			t.Errorf("healthcheck path = %q", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"apiVersion":"v1"}`))
	}))
	defer server.Close()
	t.Setenv("LABCENTRAL_HEALTHCHECK_URL", server.URL+"/api/v1/server-info")
	if err := healthcheck(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheckRejectsWrongVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"apiVersion":"v2"}`))
	}))
	defer server.Close()
	t.Setenv("LABCENTRAL_HEALTHCHECK_URL", server.URL)
	if err := healthcheck(); err == nil {
		t.Fatal("healthcheck accepted an unexpected API version")
	}
}

func TestConfiguredPublicInputRejectsWritableOrIndirectFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "target.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if contents, err := readRegularFile(path, 1024); err != nil || len(contents) == 0 {
		t.Fatalf("protected input was rejected: %q, %v", contents, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(path, 1024); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("writable input error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "target-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(link, 1024); err == nil {
		t.Fatal("symlink input was accepted")
	}
}

func TestPrivateDataDirectoryPermissions(t *testing.T) {
	private := filepath.Join(t.TempDir(), "private")
	if err := ensurePrivateDirectory(private); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(open); err == nil {
		t.Fatal("group-readable data directory was accepted")
	}
}

func TestProxmoxAuthoringExampleUsesOnlyApprovedAliasesAndDigest(t *testing.T) {
	example, err := proxmoxAuthoringExample([]byte(`{
		"templates":[{"alias":"debian","contentDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
		"storages":[{"alias":"primary"}],
		"networks":[{"alias":"isolated"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(example.Source, "labcentral/proxmox/debian@sha256:"+strings.Repeat("a", 64)) {
		t.Fatalf("example source = %q", example.Source)
	}
	if string(example.ProviderOverride) != `{"networkAlias":"isolated","storageAlias":"primary","templateAlias":"debian"}` {
		t.Fatalf("provider override = %s", example.ProviderOverride)
	}
}

type stubListener struct{ address net.Addr }

func (stubListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (stubListener) Close() error              { return nil }
func (s stubListener) Addr() net.Addr          { return s.address }
