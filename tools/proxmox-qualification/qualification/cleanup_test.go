package qualification

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestCleanupGrantIsExactCurrentAndIndependentlyTrusted(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	stamp := func(t time.Time) string { return t.Format(time.RFC3339) }
	digest := "sha256:" + strings.Repeat("a", 64)
	grant := CleanupGrant{SchemaVersion: CleanupSchema, ID: "cleanup.one", IssuedAt: stamp(now), ValidUntil: stamp(now.Add(time.Hour)), TargetID: "target.one", InstallationID: "installation.one", ConfigurationDigest: digest, InventoryDigest: digest, ProviderBinaryDigest: digest, InstanceID: "instance.one", ResourceID: "vm.one", OwnershipDigest: digest}
	trust := TrustPolicy{SchemaVersion: TrustPolicySchema, MaxEvidenceAgeSeconds: 3600, Keys: []TrustKey{{KeyID: "reviewer.one", PublicKey: base64.RawURLEncoding.EncodeToString(pub), NotBefore: stamp(now.Add(-time.Hour)), NotAfter: stamp(now.Add(48 * time.Hour))}}}
	document, err := SignCleanup(grant, "reviewer.one", key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCleanup(document, trust, now, grant); err != nil {
		t.Fatal(err)
	}
	for _, when := range []time.Time{now.Add(-time.Second), now.Add(time.Hour)} {
		if _, err := VerifyCleanup(document, trust, when, grant); err == nil {
			t.Fatal("noncurrent grant accepted")
		}
	}
	for _, field := range []string{"target", "instance", "resource", "ownership", "binary", "inventory", "config", "installation"} {
		t.Run(field, func(t *testing.T) {
			other := grant
			switch field {
			case "target":
				other.TargetID = "target.other"
			case "instance":
				other.InstanceID = "instance.other"
			case "resource":
				other.ResourceID = "vm.other"
			case "ownership":
				other.OwnershipDigest = "sha256:" + strings.Repeat("b", 64)
			case "binary":
				other.ProviderBinaryDigest = "sha256:" + strings.Repeat("b", 64)
			case "inventory":
				other.InventoryDigest = "sha256:" + strings.Repeat("b", 64)
			case "config":
				other.ConfigurationDigest = "sha256:" + strings.Repeat("b", 64)
			case "installation":
				other.InstallationID = "installation.other"
			}
			if _, err := VerifyCleanup(document, trust, now, other); err == nil {
				t.Fatal("foreign binding accepted")
			}
		})
	}
	trust.Keys[0].Revoked = true
	if _, err := VerifyCleanup(document, trust, now, grant); err == nil {
		t.Fatal("revoked signer accepted")
	}
	trust.Keys[0].Revoked = false
	altered := []byte(strings.Replace(string(document), "vm.one", "vm.two", 1))
	if _, err := VerifyCleanup(altered, trust, now, grant); err == nil {
		t.Fatal("modified document accepted")
	}
	grant.ValidUntil = stamp(now.Add(25 * time.Hour))
	if _, err := SignCleanup(grant, "reviewer.one", key); err == nil {
		t.Fatal("long lived cleanup signed")
	}
}
