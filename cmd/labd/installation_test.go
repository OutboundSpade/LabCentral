package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
)

func TestExternalModuleInstallsTwoTargetsWithoutCoreRegistration(t *testing.T) {
	root, err := os.MkdirTemp("", "lc-ext-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "external-provider")
	build := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-o", binary, ".")
	build.Dir = "../../examples/external-provider"
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("external module build: %v\n%s", err, b)
	}
	if err := os.Chmod(binary, 0500); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(b)
	m := manifest.Manifest{APIVersion: manifest.Version, ID: "org.example.provider", Version: "0.1.0", Executable: "external-provider", SHA256: hex.EncodeToString(digest[:]), Categories: []string{"provider"}, ProviderKinds: []string{"example"}, ProtocolMajor: 1, MaximumProtocolMinor: 1, OperatingSystems: []string{runtime.GOOS}, Architectures: []string{runtime.GOARCH}, RequestedPermissions: []string{}}
	write := func(path string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "manifest.json"), m)
	install := installation{APIVersion: "labcentral.io/installation/v1", PluginRoot: root, Providers: []installedProvider{{Manifest: "manifest.json", InstanceID: "plugin.external", Mode: "local", Targets: []installedTarget{{ID: "target.external-one", Kind: "example"}, {ID: "target.external-two", Kind: "example"}}}}}
	path := filepath.Join(root, "installation.json")
	write(path, install)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bindings, clients, err := loadInstalledProviders(ctx, config{installationFile: path, authMode: "none"}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	registry, err := controlplane.NewTargetRegistry(bindings)
	if err != nil || len(registry.IDs()) != 2 {
		t.Fatal("two independent targets were not installed", err)
	}
	for _, id := range registry.IDs() {
		session, err := registry.Acquire(ctx, id, application.InvocationScope{}, false)
		if err != nil {
			t.Fatal(err)
		}
		discovery, err := session.Provider.Discover(ctx, session.Context)
		session.Close()
		if err != nil || len(discovery.Capabilities) != 1 {
			t.Fatal("external target discovery failed", err)
		}
	}
}
