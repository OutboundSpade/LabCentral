package domain

import "testing"

func TestOpaqueIDValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewInstanceID("01KLABCENTRAL_7"); err != nil {
		t.Fatalf("valid ID rejected: %v", err)
	}
	if _, err := NewInstanceBatchID("batch_01KLABCENTRAL"); err != nil {
		t.Fatalf("valid batch ID rejected: %v", err)
	}
	if _, err := NewLogicalResourceID("network.red"); err != nil {
		t.Fatalf("qualified logical resource ID rejected: %v", err)
	}
	if _, err := NewLogicalResourceID("container.loadgen[one]"); err != nil {
		t.Fatalf("stable expanded logical resource ID rejected: %v", err)
	}
	for _, invalid := range []string{"", "contains space", "/absolute"} {
		if _, err := NewInstanceID(invalid); err == nil {
			t.Fatalf("invalid ID accepted: %q", invalid)
		}
	}
}

func TestSHA256DigestNormalization(t *testing.T) {
	t.Parallel()
	hexDigest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, input := range []string{hexDigest, "sha256:" + hexDigest} {
		digest, err := ParseSHA256Digest(input)
		if err != nil {
			t.Fatalf("valid digest rejected: %v", err)
		}
		if digest.Hex() != hexDigest || digest.String() != "sha256:"+hexDigest {
			t.Fatalf("digest was not normalized: %#v", digest)
		}
	}
	for _, invalid := range []string{"", "sha256:abcd", "SHA256:" + hexDigest, "sha256:" + "G123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if _, err := ParseSHA256Digest(invalid); err == nil {
			t.Fatalf("invalid digest accepted: %q", invalid)
		}
	}
}

func TestNameValidationMatchesAuthorContract(t *testing.T) {
	t.Parallel()
	if _, err := NewName("target-router-1"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	for _, invalid := range []string{"Target", "1target", "target_router", "target."} {
		if _, err := NewName(invalid); err == nil {
			t.Fatalf("invalid name accepted: %q", invalid)
		}
	}
}
