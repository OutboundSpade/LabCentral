package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClosedInstallationDecode(t *testing.T) {
	for _, input := range []string{`{"id":"a","id":"b"}`, `{"unexpected":true}`, `{} {}`, strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		var m Manifest
		if Decode([]byte(input), &m) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
func TestManifestContainmentAndPermissionGrant(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{APIVersion: Version, ID: "example.provider", Version: "1.0.0", Executable: "provider", SHA256: strings.Repeat("a", 64), Categories: []string{"provider"}, ProviderKinds: []string{"example"}, ProtocolMajor: 1, OperatingSystems: []string{runtime.GOOS}, Architectures: []string{runtime.GOARCH}, RequestedPermissions: []string{"network.target"}}
	path := filepath.Join(root, "plugin.json")
	write := func() {
		b, _ := json.Marshal(m)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, _, err := Load(root, path, nil); err == nil {
		t.Fatal("ungranted permission accepted")
	}
	if _, got, err := Load(root, path, []string{"network.target"}); err != nil || got != binary {
		t.Fatal(got, err)
	}
	m.Executable = "../outside"
	write()
	if _, _, err := Load(root, path, []string{"network.target"}); err == nil {
		t.Fatal("escaped root")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	m.Executable = "link"
	write()
	if _, _, err := Load(root, path, []string{"network.target"}); err == nil {
		t.Fatal("symlink accepted")
	}
}
