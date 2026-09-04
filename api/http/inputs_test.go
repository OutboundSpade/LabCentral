package httpapi

import (
	"testing"
)

func TestTypedInputBoundary(t *testing.T) {
	text := "hello"
	integer := int64(1)
	boolean := false
	cases := []struct {
		name   string
		values []InputValue
		valid  bool
	}{
		{"underscore parameter", []InputValue{{Name: "lab_name", Kind: "string", String: &text}}, true},
		{"explicit false", []InputValue{{Name: "enabled", Kind: "boolean", Boolean: &boolean}}, true},
		{"empty list", []InputValue{{Name: "names", Kind: "stringList", StringList: []string{}}}, true},
		{"ambiguous value", []InputValue{{Name: "name", Kind: "string", String: &text, Integer: &integer}}, false},
		{"mismatched kind", []InputValue{{Name: "name", Kind: "integer", String: &text}}, false},
		{"duplicate", []InputValue{{Name: "name", Kind: "string", String: &text}, {Name: "name", Kind: "string", String: &text}}, false},
		{"secret", []InputValue{{Name: "password", Kind: "secret", String: &text}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := decodeRenderOptions(c.values)
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
		})
	}
}
