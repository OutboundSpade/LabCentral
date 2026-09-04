package qualification

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const CleanupSchema = "labcentral.proxmox-cleanup/v1"

type CleanupGrant struct {
	SchemaVersion        string `json:"schemaVersion"`
	ID                   string `json:"id"`
	IssuedAt             string `json:"issuedAt"`
	ValidUntil           string `json:"validUntil"`
	TargetID             string `json:"targetID"`
	InstallationID       string `json:"installationID"`
	ConfigurationDigest  string `json:"configurationDigest"`
	InventoryDigest      string `json:"inventoryDigest"`
	ProviderBinaryDigest string `json:"providerBinaryDigest"`
	InstanceID           string `json:"instanceID"`
	ResourceID           string `json:"resourceID"`
	OwnershipDigest      string `json:"ownershipDigest"`
}
type SignedCleanup struct {
	Grant     CleanupGrant `json:"grant"`
	KeyID     string       `json:"keyID"`
	Signature string       `json:"signature"`
}

func DecodeCleanupGrant(b []byte) (CleanupGrant, error) {
	var g CleanupGrant
	if err := decodeClosed(b, &g); err != nil {
		return g, err
	}
	return g, validateCleanupGrant(g)
}
func validateCleanupGrant(g CleanupGrant) error {
	if g.SchemaVersion != CleanupSchema || !stableIDPattern.MatchString(g.ID) || !stableIDPattern.MatchString(g.TargetID) || !stableIDPattern.MatchString(g.InstallationID) || !stableIDPattern.MatchString(g.InstanceID) || !stableIDPattern.MatchString(g.ResourceID) {
		return errors.New("cleanup grant identity is invalid")
	}
	for _, d := range []string{g.ConfigurationDigest, g.InventoryDigest, g.ProviderBinaryDigest, g.OwnershipDigest} {
		if !digestPattern.MatchString(d) {
			return errors.New("cleanup grant digest is invalid")
		}
	}
	issued, e := parseTimestamp(g.IssuedAt)
	until, e2 := parseTimestamp(g.ValidUntil)
	if e != nil || e2 != nil || !until.After(issued) || until.Sub(issued) > 24*time.Hour {
		return errors.New("cleanup grant validity must be at most 24 hours")
	}
	return nil
}
func cleanupBytes(g CleanupGrant, keyID string) ([]byte, error) {
	return canonicalJSON(struct {
		Grant CleanupGrant `json:"grant"`
		KeyID string       `json:"keyID"`
	}{g, keyID})
}
func SignCleanup(g CleanupGrant, keyID string, key ed25519.PrivateKey) ([]byte, error) {
	if err := validateCleanupGrant(g); err != nil {
		return nil, err
	}
	if !stableIDPattern.MatchString(keyID) || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("cleanup signer is invalid")
	}
	b, err := cleanupBytes(g, keyID)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(key, append([]byte(CleanupSchema+"\x00"), b...))
	return canonicalJSON(SignedCleanup{Grant: g, KeyID: keyID, Signature: base64.RawURLEncoding.EncodeToString(signature)})
}

// VerifyCleanup rechecks current signer authority independently of old qualification.
func VerifyCleanup(b []byte, trust TrustPolicy, now time.Time, expected CleanupGrant) (string, error) {
	var document SignedCleanup
	if err := decodeClosed(b, &document); err != nil {
		return "", err
	}
	g := document.Grant
	if err := validateCleanupGrant(g); err != nil {
		return "", err
	}
	keys, err := trust.TrustedKeysAt(now)
	if err != nil {
		return "", err
	}
	key := keys[document.KeyID]
	signature, err := base64.RawURLEncoding.DecodeString(document.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return "", errors.New("cleanup signature is invalid")
	}
	payload, err := cleanupBytes(g, document.KeyID)
	if err != nil || !ed25519.Verify(key, append([]byte(CleanupSchema+"\x00"), payload...), signature) {
		return "", errors.New("cleanup signature verification failed")
	}
	issued, _ := parseTimestamp(g.IssuedAt)
	until, _ := parseTimestamp(g.ValidUntil)
	if now.Before(issued) || !now.Before(until) {
		return "", errors.New("cleanup grant is not current")
	}
	if g.TargetID != expected.TargetID || g.InstallationID != expected.InstallationID || g.ConfigurationDigest != expected.ConfigurationDigest || g.InventoryDigest != expected.InventoryDigest || g.ProviderBinaryDigest != expected.ProviderBinaryDigest || g.InstanceID != expected.InstanceID || g.ResourceID != expected.ResourceID || g.OwnershipDigest != expected.OwnershipDigest {
		return "", errors.New("cleanup grant does not bind this owned resource")
	}
	digest := sha256.Sum256(payload)
	return "cleanup grant " + g.ID + " sha256:" + hex.EncodeToString(digest[:]), nil
}
