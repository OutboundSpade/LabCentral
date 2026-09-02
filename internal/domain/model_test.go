package domain

import (
	"errors"
	"testing"
)

func TestProviderStateOwnershipAndSize(t *testing.T) {
	t.Parallel()
	valid := ProviderStateEnvelope{
		PluginID:          "provider.fake",
		PluginVersion:     "1.0.0",
		SchemaVersion:     1,
		InstanceID:        "instance-1",
		LogicalResourceID: "resource-1",
		Opaque:            []byte("state"),
	}
	if err := valid.ValidateFor("instance-1", "resource-1"); err != nil {
		t.Fatalf("valid provider state rejected: %v", err)
	}
	if err := valid.ValidateFor("other-instance", "resource-1"); err == nil {
		t.Fatal("provider state ownership mismatch accepted")
	} else {
		var domainError *DomainError
		if !errors.As(err, &domainError) || domainError.Code != "provider_state_ownership_mismatch" {
			t.Fatalf("unexpected ownership error: %v", err)
		}
	}
	oversized := valid
	oversized.Opaque = make([]byte, MaxProviderStateBytes+1)
	if err := oversized.ValidateFor("instance-1", "resource-1"); err == nil {
		t.Fatal("oversized provider state accepted")
	}
}
