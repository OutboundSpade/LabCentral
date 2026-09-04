package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

type keyPaths struct {
	Identity   string
	Recipient  string
	SigningKey string
	Trust      string
}

type signingKeyFile struct {
	SchemaVersion string `json:"schemaVersion"`
	KeyID         string `json:"keyId"`
	PrivateKey    string `json:"privateKey"`
}

type signingTrustFile struct {
	SchemaVersion string            `json:"schemaVersion"`
	Keys          []signingTrustKey `json:"keys"`
}

type signingTrustKey struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Revoked   bool   `json:"revoked"`
}

func generateRecoveryKeys(directory, keyID string) (keyPaths, error) {
	if !safeRecoveryToken(keyID) {
		return keyPaths{}, fmt.Errorf("key-id must be a safe 1-64 character token")
	}
	absolute, err := privateExistingDirectory(directory)
	if err != nil {
		return keyPaths{}, err
	}
	paths := keyPaths{
		Identity: filepath.Join(absolute, "recovery.age-identity"), Recipient: filepath.Join(absolute, "recovery.age-recipient"),
		SigningKey: filepath.Join(absolute, "recovery.signing-key.json"), Trust: filepath.Join(absolute, "recovery.signing-trust.json"),
	}
	for _, path := range []string{paths.Identity, paths.Recipient, paths.SigningKey, paths.Trust} {
		if _, err := os.Lstat(path); err == nil {
			return keyPaths{}, fmt.Errorf("refusing to overwrite %s", path)
		} else if !os.IsNotExist(err) {
			return keyPaths{}, fmt.Errorf("inspect key output %s: %w", path, err)
		}
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		return keyPaths{}, fmt.Errorf("generate age identity: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return keyPaths{}, fmt.Errorf("generate signing key: %w", err)
	}
	defer clear(privateKey)
	privateDocument, err := json.MarshalIndent(signingKeyFile{
		SchemaVersion: "labcentral.recovery-signing-key/v1", KeyID: keyID,
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
	}, "", "  ")
	if err != nil {
		return keyPaths{}, err
	}
	trustDocument, err := json.MarshalIndent(signingTrustFile{
		SchemaVersion: "labcentral.recovery-signing-trust/v1",
		Keys:          []signingTrustKey{{KeyID: keyID, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey)}},
	}, "", "  ")
	if err != nil {
		return keyPaths{}, err
	}
	writes := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{paths.Identity, []byte(identity.String() + "\n"), 0o600},
		{paths.Recipient, []byte(identity.Recipient().String() + "\n"), 0o644},
		{paths.SigningKey, append(privateDocument, '\n'), 0o600},
		{paths.Trust, append(trustDocument, '\n'), 0o644},
	}
	created := make([]string, 0, len(writes))
	for _, write := range writes {
		if err := writeExclusiveFile(write.path, write.data, write.mode); err != nil {
			for _, path := range created {
				_ = os.Remove(path)
			}
			return keyPaths{}, err
		}
		created = append(created, write.path)
	}
	return paths, nil
}

func loadSigningKey(path string) (string, ed25519.PrivateKey, error) {
	contents, err := readProtectedFile(path, 16<<10, true)
	if err != nil {
		return "", nil, fmt.Errorf("read recovery signing key: %w", err)
	}
	defer clear(contents)
	var document signingKeyFile
	if err := strictJSON(contents, &document); err != nil || document.SchemaVersion != "labcentral.recovery-signing-key/v1" || !safeRecoveryToken(document.KeyID) {
		return "", nil, fmt.Errorf("recovery signing key is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(document.PrivateKey)
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return "", nil, fmt.Errorf("recovery signing key material is invalid")
	}
	return document.KeyID, ed25519.PrivateKey(decoded), nil
}

func loadSigningTrust(path, keyID string) (ed25519.PublicKey, error) {
	contents, err := readProtectedFile(path, 64<<10, false)
	if err != nil {
		return nil, fmt.Errorf("read recovery signing trust: %w", err)
	}
	var document signingTrustFile
	if err := strictJSON(contents, &document); err != nil || document.SchemaVersion != "labcentral.recovery-signing-trust/v1" || len(document.Keys) == 0 || len(document.Keys) > 32 {
		return nil, fmt.Errorf("recovery signing trust is invalid")
	}
	seen := make(map[string]bool, len(document.Keys))
	for _, key := range document.Keys {
		if !safeRecoveryToken(key.KeyID) || seen[key.KeyID] {
			return nil, fmt.Errorf("recovery signing trust contains an invalid or duplicate key")
		}
		seen[key.KeyID] = true
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if decodeErr != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("recovery signing trust contains invalid key material")
		}
		if key.KeyID == keyID {
			if key.Revoked {
				return nil, fmt.Errorf("recovery signing key %q is revoked", keyID)
			}
			return ed25519.PublicKey(decoded), nil
		}
	}
	return nil, fmt.Errorf("recovery signing key %q is not trusted", keyID)
}

func safeRecoveryToken(value string) bool {
	if len(value) == 0 || len(value) > 64 || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
