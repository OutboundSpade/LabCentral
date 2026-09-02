package render

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalGoldenVectors(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repositoryRoot(t), "schemas", "fixtures", "canonical")
	vectors := []struct {
		inputs   []string
		expected string
		digest   string
	}{
		{[]string{"key-order-a.json", "key-order-b.json"}, "key-order.expected.json", "39bc89007f94030900395e2b97f1f0ca01d507e44505925e0df44ab08ca3564e"},
		{[]string{"aliases-a.yaml", "aliases-b.yaml"}, "aliases.expected.json", "1ced113a2c758924b1d73b34c2a329f1f26766db306fb982528d215cb19e6e04"},
		{[]string{"numbers-a.json", "numbers-b.json"}, "numbers.expected.json", "658efcf2c3d7cc80ae611d6e7e06f10a7564beb50f4da058e34088e956837e8b"},
		{[]string{"unicode-a.json", "unicode-b.json"}, "unicode.expected.json", "ca3483419c0a55af583b675877dafcc3b4fe85f988e41c4024dc38dc1fc9ec53"},
	}
	for _, vector := range vectors {
		expectedInput, err := os.ReadFile(filepath.Join(root, vector.expected))
		if err != nil {
			t.Fatal(err)
		}
		expectedValue, err := DecodeJSON(expectedInput)
		if err != nil {
			t.Fatalf("decode expected %s: %v", vector.expected, err)
		}
		expected, err := Canonicalize(expectedValue)
		if err != nil {
			t.Fatalf("canonicalize expected %s: %v", vector.expected, err)
		}
		for _, inputName := range vector.inputs {
			input, err := os.ReadFile(filepath.Join(root, inputName))
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if filepath.Ext(inputName) == ".json" {
				value, err = DecodeJSON(input)
			} else {
				value, err = ParseYAML(input, DefaultParseLimits())
			}
			if err != nil {
				t.Fatalf("decode %s: %v", inputName, err)
			}
			actual, err := Canonicalize(value)
			if err != nil {
				t.Fatalf("canonicalize %s: %v", inputName, err)
			}
			if string(actual) != string(expected) {
				t.Errorf("canonical mismatch for %s\n got: %s\nwant: %s", inputName, actual, expected)
			}
			digest, err := CanonicalDigest("canonical.labcentral.io/v1alpha1", actual)
			if err != nil {
				t.Fatalf("digest %s: %v", inputName, err)
			}
			if digest.Hex() != vector.digest {
				t.Errorf("digest mismatch for %s: got %s, want %s", inputName, digest.Hex(), vector.digest)
			}
		}
	}
}

func TestCanonicalRFC8785NumberSamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, testCase := range tests {
		actual, err := Canonicalize(math.Float64frombits(testCase.bits))
		if err != nil {
			t.Fatalf("bits %016x: %v", testCase.bits, err)
		}
		if string(actual) != testCase.want {
			t.Errorf("bits %016x: got %s, want %s", testCase.bits, actual, testCase.want)
		}
	}
}

func TestCanonicalRejectsNonFiniteAndLoneSurrogate(t *testing.T) {
	t.Parallel()
	if _, err := Canonicalize(map[string]any{"bad": math.Inf(1)}); err == nil {
		t.Fatal("non-finite number accepted")
	}
	for _, input := range []string{`{"bad":"\ud800"}`, `{"bad":"\udc00"}`, `{"bad":"\ud800x"}`} {
		_, err := DecodeJSON([]byte(input))
		var renderError *Error
		if !errors.As(err, &renderError) || renderError.Code != "lone_unicode_surrogate" {
			t.Errorf("lone surrogate returned unexpected error for %s: %v", input, err)
		}
	}
}

func TestCanonicalRejectsDuplicateJSONKeysAndUnsafeTypedIntegers(t *testing.T) {
	t.Parallel()
	_, err := DecodeJSON([]byte(`{"outer":{"value":1,"value":2}}`))
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "duplicate_json_key" || renderError.Path != "outer.value" {
		t.Fatalf("duplicate JSON key returned unexpected error: %v", err)
	}
	if _, err := Canonicalize(uint64(maxSafeInteger + 1)); err == nil {
		t.Fatal("unsafe typed integer accepted")
	}
}

func TestCanonicalECMAScriptThresholds(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"fixedLow":       0.000001,
		"scientificLow":  0.0000001,
		"fixedHigh":      1e20,
		"scientificHigh": 1e21,
	}
	actual, err := Canonicalize(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"fixedHigh":100000000000000000000,"fixedLow":0.000001,"scientificHigh":1e+21,"scientificLow":1e-7}`
	if string(actual) != want {
		t.Fatalf("threshold serialization mismatch\n got: %s\nwant: %s", actual, want)
	}
}
