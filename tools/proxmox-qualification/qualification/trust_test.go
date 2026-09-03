package qualification

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestTrustPolicySelectsActiveAndRejectsRevokedKeys(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	encoded := base64.RawURLEncoding.EncodeToString(public)
	input := `{"schemaVersion":"` + TrustPolicySchema + `","maxEvidenceAgeSeconds":604800,"keys":[{"keyID":"reviewer.1","publicKey":"` + encoded + `","notBefore":"2026-09-01T00:00:00Z","notAfter":"2027-09-01T00:00:00Z","revoked":false}]}`
	policy, err := DecodeTrustPolicy([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := policy.TrustedKeysAt(now)
	if err != nil || len(keys) != 1 || !strings.EqualFold(base64.RawURLEncoding.EncodeToString(keys["reviewer.1"]), encoded) {
		t.Fatalf("unexpected active keys: %#v, %v", keys, err)
	}
	policy.Keys[0].Revoked = true
	if _, err := policy.TrustedKeysAt(now); err == nil {
		t.Fatal("revoked key remained active")
	}
}

func TestTrustPolicyRejectsDuplicateKeysAndFields(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(public)
	key := `{"keyID":"reviewer.1","publicKey":"` + encoded + `","notBefore":"2026-09-01T00:00:00Z","notAfter":"2027-09-01T00:00:00Z","revoked":false}`
	duplicate := `{"schemaVersion":"` + TrustPolicySchema + `","maxEvidenceAgeSeconds":604800,"keys":[` + key + `,` + key + `]}`
	if _, err := DecodeTrustPolicy([]byte(duplicate)); err == nil {
		t.Fatal("duplicate trust key was accepted")
	}
	duplicateField := `{"schemaVersion":"` + TrustPolicySchema + `","maxEvidenceAgeSeconds":604800,"maxEvidenceAgeSeconds":300,"keys":[` + key + `]}`
	if _, err := DecodeTrustPolicy([]byte(duplicateField)); err == nil {
		t.Fatal("duplicate JSON field was accepted")
	}
}
