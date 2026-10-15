package instructor

import (
	"bytes"
	"encoding/json"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (lc.Source, *Bundle) {
	t.Helper()
	src, b, e := LoadTemplate("../templates/ccdc-service-recovery", true)
	if e != nil {
		t.Fatal(e)
	}
	return src, b
}
func TestSchemaSynchronized(t *testing.T) {
	generated, e := Schema()
	if e != nil {
		t.Fatal(e)
	}
	stored, e := os.ReadFile("../schemas/instructor-v3.json")
	if e != nil || !bytes.Equal(stored, generated) {
		t.Fatal("stale instructor schema: run go run ./tools/schema", e)
	}
}
func TestConformance(t *testing.T) {
	raw, e := Schema()
	if e != nil {
		t.Fatal(e)
	}
	doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	compiler := jsonschema.NewCompiler()
	if e = compiler.AddResource("instructor-v3.json", doc); e != nil {
		t.Fatal(e)
	}
	schema, e := compiler.Compile("instructor-v3.json")
	if e != nil {
		t.Fatal(e)
	}
	src, base := fixture(t)
	for _, kind := range []string{"valid", "invalid"} {
		paths, _ := filepath.Glob("../testdata/conformance/instructor/" + kind + "/*.yaml")
		if len(paths) == 0 {
			t.Fatal("missing corpus")
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				var node yaml.Node
				schemaErr := lc.DecodeStrictYAML(raw, &node, lc.YAMLLimits{})
				if schemaErr == nil {
					var value any
					schemaErr = node.Decode(&value)
					if schemaErr == nil {
						data, e := json.Marshal(value)
						schemaErr = e
						if e == nil {
							doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(data))
							schemaErr = e
							if e == nil {
								schemaErr = schema.Validate(doc)
							}
						}
					}
				}
				b, goErr := Parse(raw)
				if goErr == nil {
					caseSource, caseBase := src, base
					if strings.Contains(string(raw), "# windows: true") {
						caseSource, caseBase, e = LoadTemplate("../examples/windows-iis", true)
						if e != nil {
							t.Fatal(e)
						}
					}
					b.Files = caseBase.Files
					goErr = b.Validate(caseSource)
				}
				if kind == "valid" {
					if schemaErr != nil || goErr != nil {
						t.Fatalf("schema=%v; Go=%v", schemaErr, goErr)
					}
					return
				}
				metadata := map[string]string{}
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "# ") {
						pair := strings.SplitN(line[2:], ": ", 2)
						if len(pair) == 2 {
							metadata[pair[0]] = pair[1]
						}
					}
				}
				if (schemaErr != nil) != (metadata["schema"] == "reject") {
					t.Fatalf("schema expected %s, got %v", metadata["schema"], schemaErr)
				}
				var problem *lc.Error
				if !errors.As(goErr, &problem) || problem.Field != metadata["field"] || string(problem.Code) != metadata["code"] {
					t.Fatalf("expected %s %s, got %v", metadata["code"], metadata["field"], goErr)
				}
				if !strings.Contains(problem.Message, "line ") {
					t.Fatal("missing YAML line", problem)
				}
			})
		}
	}
}
