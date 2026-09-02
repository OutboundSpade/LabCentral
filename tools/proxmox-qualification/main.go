// Command proxmox-qualification signs and verifies bounded Proxmox T3 evidence.
// It never connects to Proxmox and cannot enable provider mutations.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/qualification"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: proxmox-qualification <sign|verify> [options]")
	}
	switch arguments[0] {
	case "sign":
		return runSign(arguments[1:], output)
	case "verify":
		return runVerify(arguments[1:], output)
	default:
		return fmt.Errorf("unknown qualification command %q", arguments[0])
	}
}

func runSign(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	evidencePath := flags.String("evidence", "", "closed unsigned evidence JSON")
	privateKeyPath := flags.String("private-key", "", "mode-protected base64url Ed25519 private key file")
	keyID := flags.String("key-id", "", "trusted signing key ID")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *evidencePath == "" || *privateKeyPath == "" || *keyID == "" {
		return errors.New("usage: proxmox-qualification sign --evidence FILE --private-key FILE --key-id ID")
	}
	rawEvidence, err := readBoundedRegularFile(*evidencePath, qualification.MaxDocumentBytes, false)
	if err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	evidence, err := qualification.DecodeEvidence(rawEvidence)
	if err != nil {
		return err
	}
	keyBytes, err := readEncodedKey(*privateKeyPath, ed25519.PrivateKeySize, true)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	defer clear(keyBytes)
	document, err := qualification.Sign(evidence, *keyID, ed25519.PrivateKey(keyBytes))
	if err != nil {
		return err
	}
	if _, err := output.Write(append(document, '\n')); err != nil {
		return fmt.Errorf("write signed evidence: %w", err)
	}
	return nil
}

func runVerify(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	documentPath := flags.String("document", "", "signed evidence JSON")
	publicKeyPath := flags.String("public-key", "", "base64url Ed25519 public key file")
	keyID := flags.String("key-id", "", "trusted signing key ID")
	targetID := flags.String("target-id", "", "expected provider target ID")
	installationID := flags.String("installation-id", "", "expected Proxmox installation ID")
	configurationDigest := flags.String("configuration-digest", "", "expected target configuration digest")
	capabilityDigest := flags.String("capability-digest", "", "expected current capability digest")
	inventoryDigest := flags.String("inventory-digest", "", "expected current inventory digest")
	policyDigest := flags.String("policy-digest", "", "expected current policy digest")
	pveRelease := flags.String("pve-release", "", "expected exact PVE release")
	pveVersion := flags.String("pve-version", "", "expected exact pve-manager version/build")
	providerDigest := flags.String("provider-digest", "", "expected provider executable digest")
	sourceTreeDigest := flags.String("source-tree-digest", "", "expected provider source-tree digest")
	at := flags.String("at", "", "verification time as canonical UTC RFC3339 (defaults to now)")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *documentPath == "" || *publicKeyPath == "" || *keyID == "" || *targetID == "" || *installationID == "" || *configurationDigest == "" || *capabilityDigest == "" || *inventoryDigest == "" || *policyDigest == "" || *pveRelease == "" || *pveVersion == "" || *providerDigest == "" || *sourceTreeDigest == "" {
		return errors.New("usage: proxmox-qualification verify --document FILE --public-key FILE --key-id ID --target-id ID --installation-id ID --configuration-digest sha256:... --capability-digest sha256:... --inventory-digest sha256:... --policy-digest sha256:... --pve-release 9.x.y --pve-version pve-manager/9.x.y/build --provider-digest sha256:... --source-tree-digest sha256:... [--at RFC3339]")
	}
	now := time.Now().UTC()
	if *at != "" {
		parsed, err := time.Parse(time.RFC3339, *at)
		if err != nil || parsed.Format(time.RFC3339) != *at || parsed.Location() != time.UTC {
			return errors.New("verification time must be canonical UTC RFC3339")
		}
		now = parsed
	}
	document, err := readBoundedRegularFile(*documentPath, qualification.MaxDocumentBytes, false)
	if err != nil {
		return fmt.Errorf("read signed evidence: %w", err)
	}
	keyBytes, err := readEncodedKey(*publicKeyPath, ed25519.PublicKeySize, false)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	result, err := qualification.Verify(document, qualification.VerificationPolicy{
		Now:                          now,
		TrustedKeys:                  map[string]ed25519.PublicKey{*keyID: ed25519.PublicKey(keyBytes)},
		ExpectedTargetID:             *targetID,
		ExpectedInstallationID:       *installationID,
		ExpectedConfigurationDigest:  *configurationDigest,
		ExpectedCapabilityDigest:     *capabilityDigest,
		ExpectedInventoryDigest:      *inventoryDigest,
		ExpectedPolicyDigest:         *policyDigest,
		ExpectedPVERelease:           *pveRelease,
		ExpectedPVEVersion:           *pveVersion,
		ExpectedProviderBinaryDigest: *providerDigest,
		ExpectedSourceTreeDigest:     *sourceTreeDigest,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write verification result: %w", err)
	}
	if !result.Qualifies {
		return errors.New("signed evidence is authentic but does not qualify")
	}
	return nil
}

func readEncodedKey(path string, expectedBytes int, private bool) ([]byte, error) {
	raw, err := readBoundedRegularFile(path, 4096, private)
	if err != nil {
		return nil, err
	}
	encoded := bytes.TrimSpace(raw)
	decoded := make([]byte, base64.RawURLEncoding.DecodedLen(len(encoded)))
	written, err := base64.RawURLEncoding.Decode(decoded, encoded)
	clear(raw)
	if err != nil || written != expectedBytes {
		clear(decoded)
		return nil, errors.New("key must be unpadded base64url with the expected Ed25519 length")
	}
	return decoded[:written], nil
}

func readBoundedRegularFile(path string, maximum int, private bool) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > int64(maximum) {
		return nil, errors.New("path must name a bounded non-symlink regular file")
	}
	if private && before.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private key file must not grant group or other permissions")
	}
	if !private && before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("public input file must not be group or other writable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || private && after.Mode().Perm()&0o077 != 0 || !private && after.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("input file identity or permissions changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 || len(contents) > maximum {
		clear(contents)
		return nil, errors.New("input file is empty or exceeds its size limit")
	}
	return contents, nil
}
