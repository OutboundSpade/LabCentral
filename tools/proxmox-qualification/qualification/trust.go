package qualification

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sort"
	"time"
)

const TrustPolicySchema = "labcentral.proxmox-qualification-trust/v1"

type TrustPolicy struct {
	SchemaVersion         string     `json:"schemaVersion"`
	MaxEvidenceAgeSeconds uint32     `json:"maxEvidenceAgeSeconds"`
	Keys                  []TrustKey `json:"keys"`
}

type TrustKey struct {
	KeyID     string `json:"keyID"`
	PublicKey string `json:"publicKey"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	Revoked   bool   `json:"revoked"`
}

// DecodeTrustPolicy parses a closed, duplicate-key-rejecting production trust
// policy. Revoked keys remain recorded so an old artifact cannot become
// trusted again merely because its key disappeared from a replacement file.
func DecodeTrustPolicy(input []byte) (TrustPolicy, error) {
	var policy TrustPolicy
	if err := decodeClosed(input, &policy); err != nil {
		return TrustPolicy{}, err
	}
	if err := validateTrustPolicy(policy); err != nil {
		return TrustPolicy{}, err
	}
	return policy, nil
}

func (policy TrustPolicy) MaxEvidenceAge() time.Duration {
	return time.Duration(policy.MaxEvidenceAgeSeconds) * time.Second
}

// TrustedKeysAt returns only active keys at the supplied UTC instant. A
// policy with no currently active key fails closed.
func (policy TrustPolicy) TrustedKeysAt(now time.Time) (map[string]ed25519.PublicKey, error) {
	if err := validateTrustPolicy(policy); err != nil {
		return nil, err
	}
	now = now.UTC()
	trusted := make(map[string]ed25519.PublicKey)
	for _, key := range policy.Keys {
		notBefore, _ := parseTimestamp(key.NotBefore)
		notAfter, _ := parseTimestamp(key.NotAfter)
		if key.Revoked || now.Before(notBefore) || now.After(notAfter) {
			continue
		}
		decoded, _ := base64.RawURLEncoding.DecodeString(key.PublicKey)
		trusted[key.KeyID] = ed25519.PublicKey(append([]byte(nil), decoded...))
	}
	if len(trusted) == 0 {
		return nil, errors.New("qualification trust policy has no active key")
	}
	return trusted, nil
}

func validateTrustPolicy(policy TrustPolicy) error {
	if policy.SchemaVersion != TrustPolicySchema || policy.MaxEvidenceAgeSeconds < 300 || policy.MaxEvidenceAgeSeconds > uint32(MaxValidity/time.Second) || len(policy.Keys) == 0 || len(policy.Keys) > 32 {
		return errors.New("qualification trust policy is invalid")
	}
	seen := make(map[string]bool, len(policy.Keys))
	last := ""
	for _, key := range policy.Keys {
		if !stableIDPattern.MatchString(key.KeyID) || seen[key.KeyID] || last != "" && last >= key.KeyID {
			return errors.New("qualification trust keys must have unique sorted IDs")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(decoded) != key.PublicKey {
			return errors.New("qualification trust key is invalid")
		}
		notBefore, err := parseTimestamp(key.NotBefore)
		if err != nil {
			return errors.New("qualification trust key validity is invalid")
		}
		notAfter, err := parseTimestamp(key.NotAfter)
		if err != nil || !notAfter.After(notBefore) || notAfter.Sub(notBefore) > 5*365*24*time.Hour {
			return errors.New("qualification trust key validity is invalid")
		}
		seen[key.KeyID] = true
		last = key.KeyID
	}
	return nil
}

func NormalizeTrustPolicy(policy TrustPolicy) TrustPolicy {
	result := policy
	result.Keys = append([]TrustKey(nil), policy.Keys...)
	sort.Slice(result.Keys, func(i, j int) bool { return result.Keys[i].KeyID < result.Keys[j].KeyID })
	return result
}
