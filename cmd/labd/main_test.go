package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	proxmoxprovider "github.com/OutboundSpade/LabCentral/providers/proxmox"
	"google.golang.org/grpc"
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

func TestMultiReplicaProxmoxJournalMustUseSharedArtifactFilesystem(t *testing.T) {
	shared := t.TempDir()
	artifactDirectory := filepath.Join(shared, "artifacts")
	journalDirectory := filepath.Join(shared, "proxmox-journal")
	if err := os.Mkdir(journalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config{deploymentMode: deploymentModeMultiReplica, artifactDir: artifactDirectory}
	target := proxmoxprovider.TargetConfig{JournalDirectory: journalDirectory}
	envelope, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProxmoxJournalTopology(configuration, envelope); err != nil {
		t.Fatalf("valid shared journal rejected: %v", err)
	}

	target.JournalDirectory = filepath.Join(t.TempDir(), "proxmox-journal")
	envelope, err = json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProxmoxJournalTopology(configuration, envelope); err == nil || !strings.Contains(err.Error(), "shared artifact directory") {
		t.Fatalf("journal outside shared artifacts accepted: %v", err)
	}
	if err := validateProxmoxJournalTopology(configuration, []byte(`{"journalDirectory":`)); err == nil || strings.Contains(err.Error(), "journalDirectory") {
		t.Fatalf("invalid envelope was accepted or reflected: %v", err)
	}
}

func TestExternalPluginModeIsExplicitAndExclusive(t *testing.T) {
	base := config{
		deploymentMode: deploymentModeSingleNode, replicaID: "local-worker", artifactMode: artifactModeLocal,
		authMode: httpapi.AuthModeNone, dockerPluginRuntimeDir: "/run/labcentral/docker", dockerPluginTokenFile: "/run/secrets/docker-token", dockerPluginInstanceID: "plugin.test-docker",
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("valid external plugin configuration: %v", err)
	}
	for name, mutate := range map[string]func(*config){
		"missing token":      func(candidate *config) { candidate.dockerPluginTokenFile = "" },
		"relative runtime":   func(candidate *config) { candidate.dockerPluginRuntimeDir = "run/docker" },
		"local and external": func(candidate *config) { candidate.dockerPlugin = "/opt/lab-provider-docker" },
		"hash without local": func(candidate *config) { candidate.dockerPluginHash = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := validateConfig(candidate); err == nil {
				t.Fatal("unsafe plugin execution mode was accepted")
			}
		})
	}
	accessWithoutProvider := base
	accessWithoutProvider.accessPluginRuntimeDir = "/run/labcentral/access"
	accessWithoutProvider.accessPluginTokenFile = "/run/secrets/access-token"
	accessWithoutProvider.accessPluginInstanceID = "plugin.test-access"
	if err := validateConfig(accessWithoutProvider); err == nil || !strings.Contains(err.Error(), "browser console access") {
		t.Fatalf("external access plugin without Proxmox/authentication error = %v", err)
	}
}

func TestAttachedPluginMonitorRequiresConsecutiveFailures(t *testing.T) {
	runtimeClient := &sequencedRuntimeClient{healthy: []bool{false, true, false, false}}
	client := &pluginhost.Client{Runtime: runtimeClient}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForAttachedPluginFailure(ctx, client, time.Millisecond, 2); err == nil || !strings.Contains(err.Error(), "2 consecutive") {
		t.Fatalf("attached plugin monitor error = %v", err)
	}
	if calls := runtimeClient.calls.Load(); calls != 4 {
		t.Fatalf("health calls = %d, want 4", calls)
	}
}

type sequencedRuntimeClient struct {
	healthy []bool
	calls   atomic.Int32
}

func (client *sequencedRuntimeClient) Health(context.Context, *pluginv1.HealthRequest, ...grpc.CallOption) (*pluginv1.HealthResponse, error) {
	call := int(client.calls.Add(1)) - 1
	healthy := call < len(client.healthy) && client.healthy[call]
	status := pluginv1.HealthResponse_STATUS_UNHEALTHY
	if healthy {
		status = pluginv1.HealthResponse_STATUS_HEALTHY
	}
	return &pluginv1.HealthResponse{Status: status}, nil
}

func TestPluginRegistrationTokenFileAllowsOnlyOwnerOrReadOnlyGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-token")
	token := strings.Repeat("b", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	contents, err := readPluginRegistrationTokenFile(path)
	if err != nil || string(trimOneLineEnding(contents)) != token {
		t.Fatalf("read group-projected token: %v", err)
	}
	clear(contents)
	for _, mode := range []os.FileMode{0o444, 0o460, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readPluginRegistrationTokenFile(path); err == nil || strings.Contains(err.Error(), token) {
			t.Fatalf("unsafe mode %o accepted or reflected: %v", mode, err)
		}
	}
}

func TestOrchestratorProjectedSecretRequiresExplicitReadOnlyGroupOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projected-secret")
	canary := []byte("projected-secret-canary")
	if err := os.WriteFile(path, canary, 0o440); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguredSecretFile(config{}, path, 1024); err == nil {
		t.Fatal("group-readable secret was accepted without orchestrator opt-in")
	}
	contents, err := readConfiguredSecretFile(config{allowGroupReadableSecrets: true}, path, 1024)
	if err != nil || string(contents) != string(canary) {
		t.Fatalf("read group-projected secret = %q, %v", contents, err)
	}
	clear(contents)
	for _, mode := range []os.FileMode{0o444, 0o460, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfiguredSecretFile(config{allowGroupReadableSecrets: true}, path, 1024); err == nil || strings.Contains(err.Error(), string(canary)) {
			t.Fatalf("unsafe projected-secret mode %o accepted or reflected: %v", mode, err)
		}
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

func TestAuthenticationModeMustBeExplicit(t *testing.T) {
	configuration := config{
		deploymentMode: deploymentModeSingleNode,
		replicaID:      "local-worker",
		artifactMode:   artifactModeLocal,
	}
	if err := validateConfig(configuration); err == nil || !strings.Contains(err.Error(), "--auth-mode must be explicitly set") {
		t.Fatalf("missing authentication mode error = %v", err)
	}
	configuration.authMode = httpapi.AuthModeNone
	if err := validateConfig(configuration); err != nil {
		t.Fatalf("explicit no-auth mode was rejected: %v", err)
	}
	if err := validateListenerSecurity(configuration, true); err != nil {
		t.Fatalf("explicit loopback no-auth mode was rejected: %v", err)
	}
	if err := validateListenerSecurity(configuration, false); err == nil {
		t.Fatal("explicit no-auth mode accepted a non-loopback listener")
	}
}

func TestObservabilityConfigurationFailsClosed(t *testing.T) {
	base := config{observabilityListen: "127.0.0.1:9090", traceSampleRatio: 0.05}
	if err := validateObservabilityConfig(base); err != nil {
		t.Fatalf("valid private observability configuration: %v", err)
	}
	for name, mutate := range map[string]func(*config){
		"hostname listener": func(candidate *config) { candidate.observabilityListen = "localhost:9090" },
		"missing port":      func(candidate *config) { candidate.observabilityListen = "127.0.0.1" },
		"excessive sample":  func(candidate *config) { candidate.traceSampleRatio = 1.01 },
		"plaintext remote":  func(candidate *config) { candidate.otlpTracesEndpoint = "http://collector.example.test:4318/v1/traces" },
		"endpoint secret": func(candidate *config) {
			candidate.otlpTracesEndpoint = "https://user:password@collector.example.test/v1/traces"
		},
		"partial mTLS": func(candidate *config) {
			candidate.otlpTracesEndpoint = "https://collector.example.test/v1/traces"
			candidate.otlpTracesClientCertificate = "/run/secrets/client.crt"
		},
		"orphan CA": func(candidate *config) { candidate.otlpTracesCAFile = "/run/secrets/ca.crt" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := validateObservabilityConfig(candidate); err == nil {
				t.Fatal("unsafe observability configuration was accepted")
			}
		})
	}
	loopback := base
	loopback.otlpTracesEndpoint = "http://127.0.0.1:4318/v1/traces"
	if err := validateObservabilityConfig(loopback); err != nil {
		t.Fatalf("loopback OTLP collector was rejected: %v", err)
	}
	https := base
	https.otlpTracesEndpoint = "https://collector.example.test/v1/traces"
	https.otlpTracesCAFile = "/run/secrets/ca.crt"
	https.otlpTracesClientCertificate = "/run/secrets/client.crt"
	https.otlpTracesClientKey = "/run/secrets/client.key"
	if err := validateObservabilityConfig(https); err != nil {
		t.Fatalf("mTLS OTLP collector was rejected: %v", err)
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
	configuration.authMode = httpapi.AuthModeNone
	if _, err := configureHTTPAuthentication(context.Background(), configuration, store); err == nil {
		t.Fatal("authenticated installation was downgraded to no-auth")
	}
}

func TestFailedFirstBootstrapDoesNotPersistAuthenticationMode(t *testing.T) {
	directory := t.TempDir()
	keyringPath := writeTestSessionKeyring(t, directory)
	store, err := localdb.Open(context.Background(), filepath.Join(directory, "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	configuration := config{
		authMode: httpapi.AuthModeLocal, publicOrigin: "https://labs.example.test", sessionKeyringFile: keyringPath,
		bootstrapUserID: "user.bootstrap-admin", bootstrapUsername: "bootstrap-admin", bootstrapDisplayName: "Bootstrap administrator",
	}
	if _, err := configureHTTPAuthentication(context.Background(), configuration, store); err == nil || !strings.Contains(err.Error(), "--bootstrap-password-file") {
		t.Fatalf("failed bootstrap error = %v", err)
	}
	if err := store.ValidateAuthenticationStartup(context.Background(), httpapi.AuthModeNone); err != nil {
		t.Fatalf("failed bootstrap persisted authentication mode: %v", err)
	}
}

func TestLegacyAuthenticationPreflightHasNoExternalActivity(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "labcentral.db")
	seedLegacyInstance(t, databasePath)
	keyringPath := writeTestSessionKeyring(t, directory)
	passwordPath := filepath.Join(directory, "bootstrap-password")
	if err := os.WriteFile(passwordPath, []byte("a-production-length-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(directory, "oidc-secret")
	if err := os.WriteFile(secretPath, []byte("client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(directory, "provider-started")
	pluginPath := filepath.Join(directory, "provider-marker")
	plugin := []byte("#!/bin/sh\n: > '" + markerPath + "'\nexit 1\n")
	if err := os.WriteFile(pluginPath, plugin, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(plugin)
	if err := os.WriteFile(pluginPath+".sha256", []byte(fmt.Sprintf("%x\n", digest)), 0o644); err != nil {
		t.Fatal(err)
	}

	base := config{
		listen: "127.0.0.1:0", dataDir: directory, deploymentMode: deploymentModeSingleNode,
		replicaID: "local-worker", artifactMode: artifactModeLocal, dockerPlugin: pluginPath,
		publicOrigin: "https://labs.example.test", trustedProxyCIDRs: "127.0.0.1/32", sessionKeyringFile: keyringPath,
		bootstrapUserID: "user.bootstrap-admin", bootstrapUsername: "bootstrap-admin", bootstrapDisplayName: "Bootstrap administrator",
	}
	local := base
	local.authMode = httpapi.AuthModeLocal
	local.bootstrapPasswordFile = passwordPath
	if err := runConfigured(slogDiscard(), local); err == nil || !strings.Contains(err.Error(), "legacy no-auth database") {
		t.Fatalf("legacy local-auth startup error = %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("provider was started before legacy preflight: %v", err)
	}

	var oidcRequests atomic.Int32
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		oidcRequests.Add(1)
	}))
	defer issuer.Close()
	oidc := base
	oidc.authMode = httpapi.AuthModeOIDC
	oidc.oidcIssuer = issuer.URL
	oidc.oidcClientID = "labcentral"
	oidc.oidcClientSecretFile = secretPath
	oidc.bootstrapOIDCSubject = "bootstrap-subject"
	if err := runConfigured(slogDiscard(), oidc); err == nil || !strings.Contains(err.Error(), "legacy no-auth database") {
		t.Fatalf("legacy OIDC startup error = %v", err)
	}
	if got := oidcRequests.Load(); got != 0 {
		t.Fatalf("OIDC issuer received %d request(s) before legacy preflight", got)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("provider was started before OIDC legacy preflight: %v", err)
	}

	store, err := localdb.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAuthenticationMode(context.Background(), httpapi.AuthModeLocal); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runConfigured(slogDiscard(), oidc); err == nil || !strings.Contains(err.Error(), "conflicts with installation mode \"local\"") {
		t.Fatalf("conflicting OIDC startup error = %v", err)
	}
	if got := oidcRequests.Load(); got != 0 {
		t.Fatalf("OIDC issuer received %d request(s) before mode-conflict preflight", got)
	}

	none := config{
		listen: "127.0.0.1:0", dataDir: directory, deploymentMode: deploymentModeSingleNode,
		replicaID: "local-worker", artifactMode: artifactModeLocal, dockerPlugin: pluginPath, authMode: httpapi.AuthModeNone,
	}
	if err := runConfigured(slogDiscard(), none); err == nil || !strings.Contains(err.Error(), "cannot be downgraded to no-auth") {
		t.Fatalf("no-auth downgrade error = %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("provider was started before authentication-mode conflict checks: %v", err)
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

func TestPackagedProxmoxServicesUseExternalIdentityBoundary(t *testing.T) {
	service, err := os.ReadFile(filepath.Join("..", "..", "deploy", "proxmox", "labcentral.service"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(service)
	for _, required := range []string{
		"--proxmox-plugin-runtime-dir=/run/labcentral-proxmox/ipc",
		"--proxmox-plugin-token-file=${CREDENTIALS_DIRECTORY}/proxmox-registration.token",
		"--proxmox-credential-file=${CREDENTIALS_DIRECTORY}/proxmox-credential.json",
		"--access-plugin-runtime-dir=/run/labcentral-access/ipc",
		"LoadCredential=proxmox-credential.json:/etc/labcentral/proxmox-credential.json",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("packaged core service is missing external boundary %q", required)
		}
	}
	if strings.Contains(contents, "--proxmox-plugin=/opt") || strings.Contains(contents, "--access-plugin=/opt") {
		t.Fatal("packaged core service launches a production plugin under the core identity")
	}
	for _, required := range []string{"--auth-mode=${LABCENTRAL_AUTH_MODE}", "--public-origin=${LABCENTRAL_PUBLIC_ORIGIN}", "--session-keyring-file=${LABCENTRAL_SESSION_KEYRING_FILE}", "--observability-listen=${LABCENTRAL_OBSERVABILITY_LISTEN}"} {
		if !strings.Contains(contents, required) {
			t.Fatalf("packaged service is missing authenticated option %q", required)
		}
	}
	provider, err := os.ReadFile(filepath.Join("..", "..", "deploy", "proxmox", "labcentral-proxmox-provider.service"))
	if err != nil {
		t.Fatal(err)
	}
	providerContents := string(provider)
	for _, required := range []string{
		"User=labcentral-proxmox",
		"Group=labcentral-proxmox-ipc",
		"ExecStart=/opt/labcentral/lab-provider-proxmox",
		"LoadCredential=proxmox-registration.token:/etc/labcentral/proxmox-registration.token",
		"IPAddressDeny=any",
	} {
		if !strings.Contains(providerContents, required) {
			t.Fatalf("packaged provider service is missing identity boundary %q", required)
		}
	}
}

func TestConfiguredPluginDigestReadsAdjacentChecksum(t *testing.T) {
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

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTestSessionKeyring(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "session-keys.json")
	material := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"current","keys":[{"id":"current","material":"`+material+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func seedLegacyInstance(t *testing.T, databasePath string) {
	t.Helper()
	store, err := localdb.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO labcentral_local_instances
		(instance_id, canonical_digest, canonical_lab_json, source_yaml, phase, desired_lifecycle, generation,
		resource_version, isolation_warning_acknowledged, outputs_json, error_code, error_message, created_at_us,
		updated_at_us, provider_target_id, provider_override_json, project_id, owner_kind, owner_id, created_by)
		VALUES (?, ?, ?, ?, 'pending', 'running', 1, 1, 1, ?, '', '', 1, 1, 'target.local-docker', ?,
		'project.local', 'user', 'local-admin', 'local-admin')`,
		"instance.legacy", strings.Repeat("a", 64), []byte(`{}`), []byte("apiVersion: labs.labcentral.io/v1alpha1\n"), []byte(`{}`), []byte{})
	if err != nil {
		t.Fatal(err)
	}
}
