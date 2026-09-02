package render

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLabSchemaFixtures(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	schemaBytes, err := os.ReadFile(filepath.Join(root, "schemas", "lab-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewSchemaValidator(schemaBytes)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	valid, err := filepath.Glob(filepath.Join(root, "examples", "*", "lab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range valid {
		input, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		document, err := ParseYAML(input, DefaultParseLimits())
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		if err := validator.Validate(document); err != nil {
			t.Errorf("valid example rejected %s: %v", file, err)
		}
	}
	invalid, err := filepath.Glob(filepath.Join(root, "schemas", "fixtures", "lab", "invalid", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range invalid {
		if filepath.Base(file) == "expected-errors.yaml" {
			continue
		}
		input, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		document, err := ParseYAML(input, DefaultParseLimits())
		if err != nil {
			t.Fatalf("parse invalid fixture %s: %v", file, err)
		}
		if err := validator.Validate(document); err == nil {
			t.Errorf("invalid fixture accepted: %s", file)
		}
	}
}
