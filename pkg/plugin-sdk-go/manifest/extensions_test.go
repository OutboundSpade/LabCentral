package manifest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBrowserSnapshotDigestPermissionsAndMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "plugin.json")
	content := []byte("<!doctype html><title>Fixture</title>")
	if err := os.WriteFile(filepath.Join(root, "index.html"), content, 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{APIVersion: ExtensionVersion, ID: "fixture.browser", Version: "1", Categories: []string{"browser"}, ProtocolMajor: 1, OperatingSystems: []string{runtime.GOOS}, Architectures: []string{runtime.GOARCH}, RequestedPermissions: []string{"context.read"}, Browser: &BrowserPackage{APIVersion: "labcentral.browser/v1", Assets: []BrowserAsset{{Path: "index.html", SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}}, Contributions: []BrowserContribution{{ID: "panel", Slot: "lab-panel", Title: "Fixture", Entrypoint: "index.html", Permissions: []string{"context.read"}}}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, executable, err := Load(root, file, []string{"context.read"})
	if err != nil || executable != "" {
		t.Fatalf("browser-only load: %v", err)
	}
	snapshot, err := SnapshotBrowser(root, file, loaded)
	if err != nil {
		t.Fatal(err)
	}
	m.Browser.Contributions[0].Permissions = []string{"access.create"}
	if _, err := SnapshotBrowser(root, file, m); err == nil {
		t.Fatal("browser capability escaped requested permissions")
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotBrowser(root, file, loaded); err == nil {
		t.Fatal("changed bytes accepted")
	}
	if string(snapshot["index.html"].Content) != string(content) {
		t.Fatal("installed snapshot was mutable")
	}
}
func TestConfigurationSchemasNeverResolveExternalData(t *testing.T) {
	for _, data := range []string{`{"$ref":"file:///etc/passwd"}`, `{"$ref":"https://example.invalid/schema"}`, `[]`, `{"type":"invented"}`} {
		if ValidateConfigurationSchema([]byte(data)) == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	if err := ValidateConfigurationSchema([]byte(`{"type":"object","properties":{"ref":{"type":"string"}},"additionalProperties":false}`)); err != nil {
		t.Fatal(err)
	}
}
