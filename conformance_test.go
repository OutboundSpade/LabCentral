package labcentral

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func TestLabSchemaSynchronized(t *testing.T) {
	generated, err := LabSchema()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile("schemas/lab-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, generated) {
		t.Fatal("stale lab schema: run go run ./tools/schema")
	}
}
func conformanceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	b, err := LabSchema()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err = c.AddResource("lab-v3.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("lab-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// YAML-only constraints are checked before conversion: JSON Schema cannot
// represent duplicate keys, aliases, non-string keys or multiple YAML documents.
func schemaYAML(s *jsonschema.Schema, b []byte) error {
	var n yaml.Node
	if e := DecodeStrictYAML(b, &n, YAMLLimits{}); e != nil {
		return e
	}
	if len(n.Content) != 1 {
		return errors.New("expected one document")
	}
	v, e := jsonNode(n.Content[0])
	if e != nil {
		return e
	}
	encoded, e := json.Marshal(v)
	if e != nil {
		return e
	}
	doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if e != nil {
		return e
	}
	return s.Validate(doc)
}
func conformanceEnvironment() Environment {
	e := testEnvironment()
	e.Bases["ubuntu-24.04"] = Base{ID: "ami-test", Identity: "immutable", User: "ubuntu", DiskGiB: 1}
	e.AWS.Types = append(e.AWS.Types, InstanceType{Name: "large", CPU: 64, MemoryMiB: 262144})
	e.AWS.Types = append(e.AWS.Types, InstanceType{Name: "t3.large", CPU: 2, MemoryMiB: 8192})
	e.AWS.DesktopProfile = "license-only"
	e.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "pinned", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	return e
}
func sourceYAML(t *testing.T, b []byte) (Source, error) {
	t.Helper()
	s := Source{Files: []File{{Path: "build.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")}, {Path: "data.txt", Content: []byte("data")}, {Path: "lab.yaml", Content: b}}}
	if strings.Contains(string(b), ".ps1") {
		s.Files = append(s.Files, File{Path: "build.ps1", Content: []byte("exit 0\n")})
	}
	s.Digest = digest(s.Files)
	if e := DecodeStrictYAML(b, &s.Lab, YAMLLimits{}); e != nil {
		return s, declarationError(e)
	}
	return s, Validate(s)
}
func TestLabConformance(t *testing.T) {
	schema := conformanceSchema(t)
	for _, kind := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob("testdata/conformance/lab/" + kind + "/*.yaml")
		if err != nil || len(paths) == 0 {
			t.Fatal("missing corpus", err)
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				schemaErr := schemaYAML(schema, b)
				metadata := map[string]string{}
				for _, line := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(line, "# ") {
						parts := strings.SplitN(line[2:], ": ", 2)
						if len(parts) == 2 {
							metadata[parts[0]] = parts[1]
						}
					}
				}
				env := conformanceEnvironment()
				if metadata["platform"] == "proxmox" {
					env = platformOptionsProxmoxEnvironment()
					env.Bases["ubuntu-24.04"] = Base{ID: "2000", Identity: "immutable", User: "ubuntu", DiskGiB: 1}
				}
				if metadata["allowEgress"] == "true" {
					env.AWS.AllowEgress, env.Proxmox.AllowEgress = true, true
					if env.Platform == "proxmox" {
						env.Proxmox.Gateway = "10.90.0.1"
					}
				}
				s, goErr := sourceYAML(t, b)
				if goErr == nil {
					_, goErr = PlanLab(s, windowsConformanceEnvironment(s, env), nil)
				}
				if kind == "valid" {
					if schemaErr != nil || goErr != nil {
						t.Fatalf("schema=%v; Go=%v", schemaErr, goErr)
					}
					return
				}
				if metadata["schema"] != "reject" && metadata["schema"] != "accept" {
					t.Fatal("case must declare schema: reject (structural) or accept (semantic)")
				}
				if (schemaErr != nil) != (metadata["schema"] == "reject") {
					t.Fatalf("schema expectation %s: %v", metadata["schema"], schemaErr)
				}
				var problem *Error
				if !errors.As(goErr, &problem) {
					t.Fatalf("expected field error, got %v", goErr)
				}
				if problem.Field != metadata["field"] || string(problem.Code) != metadata["code"] {
					t.Fatalf("expected %s %s; got %v", metadata["code"], metadata["field"], goErr)
				}
			})
		}
	}
	for _, glob := range []string{"examples/*/lab.yaml", "templates/*/lab.yaml"} {
		paths, e := filepath.Glob(glob)
		if e != nil {
			t.Fatal(e)
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = schemaYAML(schema, b); e != nil {
					t.Fatal(e)
				}
				s, e := Load(filepath.Dir(path))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = PlanLab(s, windowsConformanceEnvironment(s, conformanceEnvironment()), nil); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

func windowsConformanceEnvironment(s Source, e Environment) Environment {
	for _, i := range s.Lab.Images {
		if i.OS == "windows" {
			e.Bases["windows-2022"] = Base{OS: "windows", ID: "ami-0123456789abcdef0", Identity: "ami-0123456789abcdef0", User: "Administrator", DiskGiB: 30}
			e.Build.InstanceType = "t3.medium"
			e.Build.WindowsDCVURL = "https://example.invalid/windows.msi"
			e.Build.WindowsDCVSHA256 = strings.Repeat("b", 64)
		}
	}
	return e
}
