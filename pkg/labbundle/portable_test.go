package labbundle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func portableFixture(t *testing.T) (Manifest, map[string][]byte) {
	t.Helper()
	m := Manifest{APIVersion: PortableVersion, Kind: "LabBundle", Entrypoint: "lab.yaml", Package: &SourcePackage{APIVersion: SourcePackageVersion, Dependencies: []SourceDependency{{ID: "base", Root: "deps/base"}}, Builders: []BuilderRequirement{{ID: "linux", Kind: "ansible", Schema: "ansible-v1"}}, Recipes: []SourceImport{{Name: "workstation", Path: "deps/base/recipe.json"}}}}
	recipe, _ := json.Marshal(BuildRecipe{ID: "original", Builder: "linux", Schema: "ansible-v1", Architecture: "amd64", ConfigurationJSON: `{}`, Context: []string{"playbook.yaml"}, BaseImageDigests: []string{strings.Repeat("a", 64)}})
	files := map[string][]byte{"lab.yaml": []byte("kind: Lab\n"), "deps/base/recipe.json": recipe, "deps/base/playbook.yaml": []byte("- hosts: all\n  tasks: []\n")}
	locked, err := LockSources(m, files)
	if err != nil {
		t.Fatal(err)
	}
	if m.Package.Dependencies[0].SHA256 != "" {
		t.Fatal("locking mutated caller")
	}
	return locked, files
}
func TestPortableRoundTripPinsClosureAndPreservesManifest(t *testing.T) {
	m, files := portableFixture(t)
	a, err := Pack(m, files)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pack(m, files)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("nondeterministic pack: %v", err)
	}
	bundle, err := Read(a)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Builds != nil || len(bundle.Builds().Recipes) != 1 || bundle.Builds().Recipes[0].Context[0] != "deps/base/playbook.yaml" {
		t.Fatal("recipe closure not resolved independently")
	}
	exported, err := ExportSource(a)
	if err != nil || !bytes.Equal(exported, a) {
		t.Fatalf("export changed source identity: %v", err)
	}
	files["deps/base/playbook.yaml"] = []byte("changed content\n")
	if _, err = Pack(m, files); err == nil {
		t.Fatal("accepted changed pinned dependency")
	}
}
func TestPortableRejectsIncompleteGraphsAndBinaryArtifacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Manifest, map[string][]byte)
	}{
		{"missing dependency", func(m *Manifest, f map[string][]byte) { m.Package.Dependencies[0].Requires = []string{"missing"} }},
		{"cycle", func(m *Manifest, f map[string][]byte) { m.Package.Dependencies[0].Requires = []string{"base"} }},
		{"image", func(m *Manifest, f map[string][]byte) { f["disk.qcow2"] = []byte("text disguising a disk") }},
		{"binary", func(m *Manifest, f map[string][]byte) { f["data"] = []byte{0, 1, 2} }},
		{"OCI layout", func(m *Manifest, f map[string][]byte) { f["output/oci-layout"] = []byte(`{}`) }},
		{"missing builder", func(m *Manifest, f map[string][]byte) { m.Package.Builders = nil }},
		{"path escape", func(m *Manifest, f map[string][]byte) { f["../secrets"] = []byte("secret") }},
		{"unknown recipe field", func(m *Manifest, f map[string][]byte) { f["deps/base/recipe.json"] = []byte(`{"targetId":"secret"}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, f := portableFixture(t)
			test.change(&m, f)
			if _, err := Pack(m, f); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
}
func TestLegacyYAMLExportContainsSourceOnly(t *testing.T) {
	source := []byte("# preserve exact source\nkind: Lab\n")
	data, err := ExportSource(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Files[b.Manifest.Entrypoint], source) || b.Manifest.Package == nil || len(b.Files) != 2 {
		t.Fatal("unexpected portable content")
	}
}
