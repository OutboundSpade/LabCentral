package render

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseYAMLRejectsDuplicateKeyWithStablePath(t *testing.T) {
	t.Parallel()
	input, err := os.ReadFile(filepath.Join(repositoryRoot(t), "schemas", "fixtures", "parser", "duplicate-key.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseYAML(input, DefaultParseLimits())
	var renderError *Error
	if !errors.As(err, &renderError) {
		t.Fatalf("duplicate key returned %T: %v", err, err)
	}
	if renderError.Code != "duplicate_yaml_key" || renderError.Path != "metadata.name" {
		t.Fatalf("unexpected duplicate error: %#v", renderError)
	}
}

func TestParseYAMLRejectsAmbiguousOrUnsafeDocuments(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"multiple documents": "first: true\n---\nsecond: true\n",
		"non-string key":     "1: value\n",
		"unsafe tag":         "value: !execute command\n",
		"invalid UTF-8":      "value: \xff\n",
	} {
		if _, err := ParseYAML([]byte(input), DefaultParseLimits()); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestParseYAMLEnforcesLimits(t *testing.T) {
	t.Parallel()
	limits := DefaultParseLimits()
	limits.MaxDepth = 2
	if _, err := ParseYAML([]byte("a:\n  b:\n    c: value\n"), limits); err == nil {
		t.Fatal("deep YAML accepted")
	}
	limits = DefaultParseLimits()
	limits.MaxAliases = 0
	if _, err := ParseYAML([]byte("a: &shared value\nb: *shared\n"), limits); err == nil {
		t.Fatal("YAML alias accepted after zero alias limit")
	}
	limits = DefaultParseLimits()
	limits.MaxNodes = 2
	if _, err := ParseYAML([]byte("a: value\n"), limits); err == nil {
		t.Fatal("YAML node limit was not enforced")
	}
}
