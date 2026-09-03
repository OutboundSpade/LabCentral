package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualificationToolRequiresModeProtectedNonSymlinkKeys(t *testing.T) {
	directory := t.TempDir()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, ed25519.SeedSize))
	encoded := base64.RawURLEncoding.EncodeToString(privateKey)

	protected := filepath.Join(directory, "protected.key")
	if err := os.WriteFile(protected, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readEncodedKey(protected, ed25519.PrivateKeySize, true)
	if err != nil || !bytes.Equal(decoded, privateKey) {
		t.Fatalf("protected key read = %x, %v", decoded, err)
	}
	clear(decoded)

	permissive := filepath.Join(directory, "permissive.key")
	if err := os.WriteFile(permissive, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readEncodedKey(permissive, ed25519.PrivateKeySize, true); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("permissive private key error = %v", err)
	}

	link := filepath.Join(directory, "key.link")
	if err := os.Symlink(protected, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readEncodedKey(link, ed25519.PrivateKeySize, true); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink key error = %v", err)
	}
}

func TestQualificationToolHasNoImplicitAction(t *testing.T) {
	if err := run(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "<keygen|sign|verify>") {
		t.Fatalf("empty command error = %v", err)
	}
	if err := run([]string{"execute"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown qualification command") {
		t.Fatalf("unexpected command error = %v", err)
	}
}

func TestQualificationToolGeneratesProtectedReviewerKeypair(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"keygen", "--directory", directory, "--key-id", "reviewer.test-1"}, &output); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(directory, "reviewer.test-1.private")
	publicPath := filepath.Join(directory, "reviewer.test-1.public")
	privateKey, err := readEncodedKey(privatePath, ed25519.PrivateKeySize, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(privateKey)
	publicKey, err := readEncodedKey(publicPath, ed25519.PublicKeySize, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(privateKey[ed25519.SeedSize:], publicKey) {
		t.Fatal("generated public key does not match private key")
	}
	privateInfo, _ := os.Stat(privatePath)
	publicInfo, _ := os.Stat(publicPath)
	if privateInfo.Mode().Perm() != 0o600 || publicInfo.Mode().Perm() != 0o644 || !strings.Contains(output.String(), "reviewer.test-1") {
		t.Fatalf("generated key modes/output = %o %o %q", privateInfo.Mode().Perm(), publicInfo.Mode().Perm(), output.String())
	}
	if err := run([]string{"keygen", "--directory", directory, "--key-id", "reviewer.test-1"}, &bytes.Buffer{}); err == nil {
		t.Fatal("existing keypair was overwritten")
	}
}

func TestQualificationToolKeygenRejectsNonPrivateDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"keygen", "--directory", directory, "--key-id", "reviewer.test"}, &bytes.Buffer{}); err == nil {
		t.Fatal("non-private key directory was accepted")
	}
}
