package sourceidentity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDigestChangesOnlyForSelectedInputs(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("provider.go", "first")
	write("README.md", "first")
	files := map[string]bool{"provider.go": true}
	first, e := DigestFiles(root, files)
	if e != nil {
		t.Fatal(e)
	}
	write("README.md", "new docs")
	same, e := DigestFiles(root, files)
	if e != nil || same != first {
		t.Fatal("unrelated documentation changed identity", e)
	}
	write("provider.go", "changed behavior")
	changed, e := DigestFiles(root, files)
	if e != nil || changed == first {
		t.Fatal("provider change did not change identity", e)
	}
}

func TestDependencyEnumerationIncludesTransitiveSourceAndEmbed(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"go.mod": "module example.test/provider\n\ngo 1.27.0\n", "go.sum": "", "main.go": "package main\nimport _ \"example.test/provider/dep\"\nfunc main(){}\n", "dep/dep.go": "package dep\nimport _ \"embed\"\n//go:embed fixture.txt\nvar Fixture string\n", "dep/fixture.txt": "one", "README.md": "notes"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Digest(context.Background(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("new notes"), 0600); err != nil {
		t.Fatal(err)
	}
	same, err := Digest(context.Background(), root, ".")
	if err != nil || same != first {
		t.Fatal("unrelated source entered dependency closure", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dep/fixture.txt"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := Digest(context.Background(), root, ".")
	if err != nil || changed == first {
		t.Fatal("transitive embedded source was omitted", err)
	}
}
