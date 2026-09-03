package main

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
)

func TestDeploymentModeStorageContract(t *testing.T) {
	base := config{
		deploymentMode: deploymentModeSingleNode,
		replicaID:      "local-worker",
		artifactMode:   artifactModeLocal,
		authMode:       httpapi.AuthModeNone,
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid single-node configuration: %v", err)
	}

	postgresOnSingle := base
	postgresOnSingle.postgresDSNFile = "/run/secrets/postgres-dsn"
	if err := validateConfig(postgresOnSingle); err == nil || !strings.Contains(err.Error(), "multi-replica") {
		t.Fatalf("single-node PostgreSQL configuration error = %v", err)
	}

	multi := base
	multi.deploymentMode = deploymentModeMultiReplica
	multi.postgresDSNFile = "/run/secrets/postgres-dsn"
	multi.replicaID = "labd-0"
	multi.artifactMode = artifactModeShared
	multi.artifactDir = "/var/lib/labcentral-artifacts"
	multi.authMode = httpapi.AuthModeLocal
	multi.publicOrigin = "https://labs.example.test"
	multi.sessionKeyringFile = "/run/secrets/session-keys"
	multi.tlsCertificateFile = "/run/secrets/tls-cert"
	multi.tlsKeyFile = "/run/secrets/tls-key"
	if err := validateConfig(multi); err != nil {
		t.Fatalf("valid multi-replica configuration: %v", err)
	}

	for name, mutate := range map[string]func(*config){
		"missing PostgreSQL": func(candidate *config) { candidate.postgresDSNFile = "" },
		"default replica":    func(candidate *config) { candidate.replicaID = "local-worker" },
		"local artifacts":    func(candidate *config) { candidate.artifactMode = artifactModeLocal },
		"relative artifacts": func(candidate *config) { candidate.artifactDir = "artifacts" },
		"Proxmox journal":    func(candidate *config) { candidate.proxmoxPlugin = "/opt/labcentral/lab-provider-proxmox" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := multi
			mutate(&candidate)
			if err := validateConfig(candidate); err == nil {
				t.Fatal("unsafe multi-replica configuration was accepted")
			}
		})
	}
}

func TestPostgreSQLDSNFileIsPrivateAndErrorsAreRedacted(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "postgres-dsn")
	canary := "postgres://labcentral:do-not-log-this@127.0.0.1:1/labcentral?sslmode=verify-full"
	if err := os.WriteFile(path, []byte(canary+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configuration := config{deploymentMode: deploymentModeMultiReplica, postgresDSNFile: path}
	if _, err := openControlPlaneStore(context.Background(), configuration); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("open with public secret error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openControlPlaneStore(ctx, configuration)
	if err == nil {
		t.Fatal("cancelled PostgreSQL connection unexpectedly opened")
	}
	if strings.Contains(err.Error(), "do-not-log-this") || strings.Contains(err.Error(), canary) {
		t.Fatalf("PostgreSQL error leaked DSN secret: %v", err)
	}
}

func TestProductionPostgreSQLRequiresVerifiedTLS(t *testing.T) {
	for value, valid := range map[string]bool{
		"postgres://labcentral@example.test/labcentral?sslmode=verify-full": true,
		"postgres://labcentral@example.test/labcentral?sslmode=require":     false,
		"postgres://labcentral@example.test/labcentral?sslmode=disable":     false,
		"postgres:///labcentral?sslmode=verify-full":                        false,
	} {
		err := validateProductionPostgreSQLDSN(value)
		if (err == nil) != valid {
			t.Fatalf("validateProductionPostgreSQLDSN(%q) = %v, valid=%t", value, err, valid)
		}
	}
}

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

func TestAuthenticationStartupContract(t *testing.T) {
	base := config{
		deploymentMode: deploymentModeSingleNode, replicaID: "local-worker", artifactMode: artifactModeLocal,
		authMode: httpapi.AuthModeLocal, publicOrigin: "https://labs.example.test", sessionKeyringFile: "/run/secrets/session-keys",
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid local authentication config: %v", err)
	}
	for name, mutate := range map[string]func(*config){
		"missing origin":  func(candidate *config) { candidate.publicOrigin = "" },
		"HTTP origin":     func(candidate *config) { candidate.publicOrigin = "http://labs.example.test" },
		"origin path":     func(candidate *config) { candidate.publicOrigin = "https://labs.example.test/path" },
		"missing keyring": func(candidate *config) { candidate.sessionKeyringFile = "" },
		"OIDC confusion":  func(candidate *config) { candidate.oidcIssuer = "https://issuer.example.test" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := validateConfig(candidate); err == nil {
				t.Fatal("unsafe authentication configuration was accepted")
			}
		})
	}
	oidc := base
	oidc.authMode = httpapi.AuthModeOIDC
	oidc.oidcIssuer = "https://issuer.example.test"
	oidc.oidcClientID = "labcentral"
	oidc.oidcClientSecretFile = "/run/secrets/oidc"
	if err := validateConfig(oidc); err != nil {
		t.Fatalf("valid OIDC configuration: %v", err)
	}

	if err := validateListenerSecurity(config{authMode: httpapi.AuthModeNone}, false); err == nil {
		t.Fatal("non-loopback no-auth listener was accepted")
	}
	if err := validateListenerSecurity(base, false); err == nil {
		t.Fatal("non-loopback authenticated listener without TLS was accepted")
	}
	if err := validateListenerSecurity(base, true); err == nil {
		t.Fatal("authenticated cleartext listener without a trusted proxy was accepted")
	}
	proxied := base
	proxied.trustedProxyCIDRs = "127.0.0.1/32"
	if err := validateListenerSecurity(proxied, true); err != nil {
		t.Fatalf("trusted loopback proxy configuration: %v", err)
	}
	directTLS := base
	directTLS.tlsCertificateFile, directTLS.tlsKeyFile = "/cert.pem", "/key.pem"
	if err := validateListenerSecurity(directTLS, false); err != nil {
		t.Fatalf("direct TLS listener configuration: %v", err)
	}
	if _, err := parseTrustedProxyCIDRs("10.0.0.0/8,127.0.0.1/32"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"0.0.0.0/0", "10.1.0.0/8", "10.0.0.0/8, 127.0.0.1/32", "10.0.0.0/8,10.0.0.0/8"} {
		if _, err := parseTrustedProxyCIDRs(invalid); err == nil {
			t.Fatalf("unsafe proxy CIDRs %q were accepted", invalid)
		}
	}
}

func TestFirstLocalStartupBootstrapsExactlyOnceFromPrivateFiles(t *testing.T) {
	directory := t.TempDir()
	keyringPath := filepath.Join(directory, "session-keys.json")
	passwordPath := filepath.Join(directory, "bootstrap-password")
	material := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if err := os.WriteFile(keyringPath, []byte(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"current","keys":[{"id":"current","material":"`+material+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordPath, []byte("a-production-length-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := localdb.Open(context.Background(), filepath.Join(directory, "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	configuration := config{
		authMode: httpapi.AuthModeLocal, publicOrigin: "https://labs.example.test", sessionKeyringFile: keyringPath,
		bootstrapUserID: "user.bootstrap-admin", bootstrapUsername: "bootstrap-admin", bootstrapDisplayName: "Bootstrap administrator",
		bootstrapPasswordFile: passwordPath,
	}
	options, err := configureHTTPAuthentication(context.Background(), configuration, store)
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != httpapi.AuthModeLocal || options.Manager == nil || options.Authorizer == nil {
		t.Fatalf("authentication options = %#v", options)
	}
	user, err := store.UserByUsername(context.Background(), "bootstrap-admin")
	if err != nil || user.ID != "user.bootstrap-admin" {
		t.Fatalf("bootstrap user = %#v, %v", user, err)
	}
	configuration.bootstrapPasswordFile = ""
	if _, err := configureHTTPAuthentication(context.Background(), configuration, store); err != nil {
		t.Fatalf("restart required bootstrap secret again: %v", err)
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

func TestPackagedProxmoxServiceUsesAdjacentChecksumFile(t *testing.T) {
	service, err := os.ReadFile(filepath.Join("..", "..", "deploy", "proxmox", "labcentral.service"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(service)
	if !strings.Contains(contents, "--proxmox-plugin /opt/labcentral/lab-provider-proxmox") {
		t.Fatal("packaged service does not select the Proxmox provider")
	}
	if strings.Contains(contents, "--proxmox-plugin-sha256") {
		t.Fatal("packaged service passes the checksum filename as a literal digest")
	}
	directory := t.TempDir()
	plugin := filepath.Join(directory, "lab-provider-proxmox")
	if err := os.WriteFile(plugin+".sha256", []byte(strings.Repeat("a", 64)+"  lab-provider-proxmox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if digest, err := configuredPluginDigest(plugin, "", "Proxmox"); err != nil || digest != strings.Repeat("a", 64) {
		t.Fatalf("adjacent packaged checksum = %q, %v", digest, err)
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
