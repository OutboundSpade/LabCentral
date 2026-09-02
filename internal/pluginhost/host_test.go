package pluginhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
)

func TestMain(m *testing.M) {
	if os.Getenv(launch.Token) != "" {
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

func TestLaunchUsesPrivateOneTimeHandshake(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
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
}

func TestLaunchRejectsExecutableDigestMismatch(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = pluginhost.Launch(context.Background(), pluginhost.Config{
		Executable: executable, ExecutableSHA256: string(make([]byte, 64)), RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin_instance_test",
	})
	if err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}

func TestLaunchRejectsMismatchedConfiguredProviderKind(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = pluginhost.Launch(ctx, pluginhost.Config{
		Executable: executable, ExecutableSHA256: fileDigest(t, executable), RuntimeParent: t.TempDir(),
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "proxmox", PluginInstanceID: "plugin_instance_test",
	})
	if err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("provider-kind mismatch error = %v", err)
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
