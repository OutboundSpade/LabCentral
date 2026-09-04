package pluginhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
)

const helperAccessPluginID = "io.labcentral.access.helper"
const inheritedEnvironmentCanary = "LABCENTRAL_PLUGIN_ENV_SECRET_CANARY"

func TestMain(m *testing.M) {
	if os.Getenv(launch.Token) != "" {
		if os.Getenv(inheritedEnvironmentCanary) != "" {
			os.Exit(9)
		}
		if os.Getenv(launch.ConfiguredPluginID) == helperAccessPluginID {
			service := &helperAccessService{}
			err := pluginruntime.Serve(context.Background(), pluginruntime.Services{
				Info: &pluginv1.PluginInfo{
					PluginId: helperAccessPluginID, PluginVersion: "0.1.0", BuildDigest: "test",
					Categories: []string{"access"}, AccessKinds: []string{"browser-vnc"},
					SupportedAccessProtocols: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 2, MaxMinor: 2}},
					Features:                 []string{"access-authorization-recheck-v1"}, SupportedAccessMethods: []string{"vnc"},
					AcceptedAccessEndpointSchemas: []*pluginv1.SchemaVersion{{Name: "proxmox.vnc-endpoint", Major: 1}},
				},
				Runtime: service, Access: service, ConfigureAccessCallbacks: service.configure,
			})
			if err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
		service, _ := dockerprovider.NewService(helperEngine{})
		err := pluginruntime.Serve(context.Background(), pluginruntime.Services{
			Info: &pluginv1.PluginInfo{
				PluginId: dockerprovider.PluginID, PluginVersion: dockerprovider.PluginVersion, BuildDigest: "test",
				SupportedProtocols: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 0}},
				Categories:         []string{"provider"}, ProviderKinds: []string{"docker"},
			},
			Runtime: service, Provider: service,
		})
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLaunchAccessPluginBindsPrivateAuthorizationCallback(t *testing.T) {
	executable := protectedTestExecutable(t)
	runtimeParent, err := os.MkdirTemp("", "lca-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeParent) })
	authorization := &helperAuthorization{identities: make(chan application.AccessCallbackIdentity, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: executable, ExecutableSHA256: fileDigest(t, executable), RuntimeParent: runtimeParent,
		ConfiguredPluginID: helperAccessPluginID, ConfiguredAccessKind: "browser-vnc", PluginInstanceID: "access_instance_test",
		AccessAudience: "https://console.example.test", AccessAuthorization: authorization,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if client.SelectedProtocol.GetMajor() != 1 || client.SelectedProtocol.GetMinor() != 2 || client.Info.GetCategories()[0] != "access" {
		t.Fatalf("access registration = protocol %v info %#v", client.SelectedProtocol, client.Info)
	}
	response, err := client.Access.CreateAccess(ctx, &pluginv1.CreateAccessRequest{
		AccessSessionId: "access.test", SubjectId: "launch-token-test",
	})
	if err != nil || response.GetOneTimeToken() != strings.Repeat("l", 43) {
		t.Fatalf("access callback response = %#v, %v", response, err)
	}
	select {
	case identity := <-authorization.identities:
		if identity.PluginID != helperAccessPluginID || identity.PluginInstanceID != "access_instance_test" ||
			identity.Audience != "https://console.example.test" || identity.ProtocolMajor != 1 || identity.ProtocolMinor != 2 {
			t.Fatalf("callback identity = %#v", identity)
		}
	case <-ctx.Done():
		t.Fatal("access authorization callback was not received")
	}
}

func TestLaunchUsesPrivateOneTimeHandshake(t *testing.T) {
	t.Setenv(inheritedEnvironmentCanary, "must-not-reach-plugin")
	executable := protectedTestExecutable(t)
	digest := fileDigest(t, executable)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: executable, ExecutableSHA256: digest, RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin_instance_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.Info.GetPluginId() != dockerprovider.PluginID {
		t.Fatalf("registered plugin = %q", client.Info.GetPluginId())
	}
	if client.SelectedProtocol.GetMajor() != 1 || client.SelectedProtocol.GetMinor() != 0 {
		t.Fatalf("selected protocol = %v, want 1.0", client.SelectedProtocol)
	}
	discovery, err := client.Provider.DiscoverTarget(ctx, &pluginv1.DiscoverTargetRequest{})
	if err != nil || len(discovery.GetCapabilities()) == 0 {
		t.Fatalf("discovery = %#v, %v", discovery, err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close plugin: %v", err)
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("plugin process exit was not observable")
	}
}

func TestLaunchRejectsExecutableDigestMismatch(t *testing.T) {
	executable := protectedTestExecutable(t)
	var err error
	_, err = pluginhost.Launch(context.Background(), pluginhost.Config{
		Executable: executable, ExecutableSHA256: string(make([]byte, 64)), RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin_instance_test",
	})
	if err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}

func TestLaunchRejectsMismatchedConfiguredProviderKind(t *testing.T) {
	executable := protectedTestExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: executable, ExecutableSHA256: fileDigest(t, executable), RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "proxmox", PluginInstanceID: "plugin_instance_test",
	})
	if err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("provider-kind mismatch error = %v", err)
	}
}

func TestLaunchRejectsUnboundedPluginArguments(t *testing.T) {
	executable := protectedTestExecutable(t)
	base := pluginhost.Config{
		Executable: executable, ExecutableSHA256: fileDigest(t, executable), RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin_instance_test",
	}
	tooMany := base
	tooMany.Args = make([]string, 17)
	for index := range tooMany.Args {
		tooMany.Args[index] = "argument"
	}
	if _, err := pluginhost.Launch(context.Background(), tooMany); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("excess argument count error = %v", err)
	}
	tooLong := base
	tooLong.Args = []string{strings.Repeat("x", 4097)}
	if _, err := pluginhost.Launch(context.Background(), tooLong); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("oversized argument error = %v", err)
	}
}

func TestLaunchRejectsMutableOrIndirectPluginExecutable(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "provider")
	if err := os.WriteFile(executable, []byte("not executed"), 0o777); err != nil {
		t.Fatal(err)
	}
	configuration := pluginhost.Config{
		Executable: executable, ExecutableSHA256: fileDigest(t, executable), RuntimeParent: directory,
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin_instance_test",
	}
	if _, err := pluginhost.Launch(context.Background(), configuration); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("writable executable error = %v", err)
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "provider-link")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	configuration.Executable = link
	if _, err := pluginhost.Launch(context.Background(), configuration); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("symlink executable error = %v", err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func protectedTestExecutable(t *testing.T) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "pluginhost-test")
	if err := os.WriteFile(destination, contents, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0o500); err != nil {
		t.Fatal(err)
	}
	return destination
}

type helperEngine struct{}

func (helperEngine) Discover(context.Context) (dockerprovider.TargetInfo, error) {
	return dockerprovider.TargetInfo{APIVersion: "1.55", EngineVersion: "test", OSType: "linux", Architecture: "amd64", OperatingSystem: "test"}, nil
}
func (helperEngine) EnsureImage(context.Context, string) error { return nil }
func (helperEngine) EnsureNetwork(context.Context, dockerprovider.NetworkCreate) (string, error) {
	return "network", nil
}
func (helperEngine) RemoveNetwork(context.Context, string, map[string]string) error { return nil }
func (helperEngine) EnsureVolume(context.Context, dockerprovider.VolumeCreate) (string, error) {
	return "volume", nil
}
func (helperEngine) RemoveVolume(context.Context, string, map[string]string) error { return nil }
func (helperEngine) EnsureContainer(context.Context, dockerprovider.ContainerCreate) (dockerprovider.ContainerObservation, error) {
	return dockerprovider.ContainerObservation{ID: "container", Running: true}, nil
}
func (helperEngine) InspectContainer(context.Context, string) (dockerprovider.ContainerObservation, error) {
	return dockerprovider.ContainerObservation{ID: "container", Running: true}, nil
}
func (helperEngine) RemoveContainer(context.Context, string, map[string]string) error { return nil }

type helperAccessService struct {
	pluginv1.UnimplementedPluginRuntimeServiceServer
	pluginv1.UnimplementedAccessServiceServer
	mu        sync.Mutex
	callbacks *pluginruntime.AccessCallbacks
}

func (service *helperAccessService) configure(callbacks pluginruntime.AccessCallbacks) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.callbacks = &callbacks
	return nil
}

func (service *helperAccessService) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	service.mu.Lock()
	configured := service.callbacks != nil
	service.mu.Unlock()
	if !configured {
		return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_UNHEALTHY, SafeMessage: "callback unavailable"}, nil
	}
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_HEALTHY}, nil
}

func (service *helperAccessService) CreateAccess(ctx context.Context, request *pluginv1.CreateAccessRequest) (*pluginv1.CreateAccessResponse, error) {
	service.mu.Lock()
	callbacks := *service.callbacks
	service.mu.Unlock()
	decision, err := callbacks.Authorization.ExchangeAccessToken(ctx, &pluginv1.ExchangeAccessTokenRequest{
		CallbackCredential: callbacks.Credential, AccessSessionId: request.GetAccessSessionId(),
		Audience: callbacks.Audience, OneTimeToken: request.GetSubjectId(),
	})
	if err != nil {
		return nil, err
	}
	return &pluginv1.CreateAccessResponse{OneTimeToken: decision.GetAuthorizationLease()}, nil
}

func (*helperAccessService) RevokeAccess(context.Context, *pluginv1.RevokeAccessRequest) (*pluginv1.RevokeAccessResponse, error) {
	return &pluginv1.RevokeAccessResponse{ActiveConnectionsClosed: true}, nil
}

type helperAuthorization struct {
	identities chan application.AccessCallbackIdentity
}

func (authorization *helperAuthorization) ExchangeAccessToken(_ context.Context, identity application.AccessCallbackIdentity, sessionID, token string) (application.AccessAuthorizationDecision, error) {
	if sessionID != "access.test" || token != "launch-token-test" {
		return application.AccessAuthorizationDecision{}, fmt.Errorf("unexpected access callback values")
	}
	authorization.identities <- identity
	return application.AccessAuthorizationDecision{Granted: true, Lease: strings.Repeat("l", 43), ValidUntil: time.Now().Add(time.Minute), RecheckAfter: 15 * time.Second}, nil
}

func (*helperAuthorization) RecheckAccessLease(context.Context, application.AccessCallbackIdentity, string, string) (application.AccessAuthorizationDecision, error) {
	return application.AccessAuthorizationDecision{Granted: false}, nil
}

func (*helperAuthorization) EndAccessLease(context.Context, application.AccessCallbackIdentity, string, string, string) (bool, error) {
	return true, nil
}
