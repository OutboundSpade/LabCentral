package main

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"filippo.io/age"
)

const recoverySchemaVersion = "labcentral.recovery/v1"

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type recoveryManifest struct {
	SchemaVersion string                   `json:"schemaVersion"`
	CreatedAt     string                   `json:"createdAt"`
	Release       string                   `json:"release"`
	SignerKeyID   string                   `json:"signerKeyId"`
	Database      recoveryDatabaseManifest `json:"database"`
	State         recoveryComponent        `json:"state"`
}

type recoveryDatabaseManifest struct {
	Engine        string            `json:"engine"`
	Format        string            `json:"format"`
	ToolVersion   string            `json:"toolVersion"`
	AuditPosition uint64            `json:"auditPosition"`
	EventPosition uint64            `json:"eventPosition"`
	Jobs          uint64            `json:"jobs"`
	Component     recoveryComponent `json:"component"`
}

type recoveryComponent struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type stagedComponent struct {
	Manifest recoveryComponent
	Path     string
}

type verifiedRecoverySet struct {
	Manifest          recoveryManifest
	DatabaseComponent string
	StateComponent    string
}

type createResult struct {
	Path          string
	SHA256        string
	AuditPosition uint64
	EventPosition uint64
}

func writeRecoverySet(outputPath, recipientPath, signingKeyPath string, manifest recoveryManifest, databaseDump, stateArchive string) (createResult, error) {
	outputPath, err := checkedNewPath(outputPath)
	if err != nil {
		return createResult{}, err
	}
	checksumPath, err := checkedNewPath(outputPath + ".sha256")
	if err != nil {
		return createResult{}, err
	}
	recipientBytes, err := readProtectedFile(recipientPath, 64<<10, false)
	if err != nil {
		return createResult{}, fmt.Errorf("read age recipients: %w", err)
	}
	recipients, err := age.ParseRecipients(bytesReader(recipientBytes))
	if err != nil || len(recipients) == 0 || len(recipients) > 8 {
		return createResult{}, fmt.Errorf("age recipient file must contain one to eight native recipients")
	}
	keyID, privateKey, err := loadSigningKey(signingKeyPath)
	if err != nil {
		return createResult{}, err
	}
	defer clear(privateKey)
	manifest.SchemaVersion, manifest.SignerKeyID = recoverySchemaVersion, keyID
	if err := validateRecoveryManifest(manifest); err != nil {
		return createResult{}, fmt.Errorf("recovery manifest is invalid: %w", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return createResult{}, err
	}
	signature := ed25519.Sign(privateKey, recoverySignatureMessage(manifestBytes))

	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".labcentral-recovery-*.age")
	if err != nil {
		return createResult{}, err
	}
	temporaryPath := temporary.Name()
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return createResult{}, err
	}
	encrypted, err := age.Encrypt(temporary, recipients...)
	if err != nil {
		return createResult{}, fmt.Errorf("initialize recovery encryption: %w", err)
	}
	archive := tar.NewWriter(encrypted)
	created, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil {
		return createResult{}, fmt.Errorf("manifest creation time is invalid")
	}
	if err := writeBundleBytes(archive, "manifest.json", manifestBytes, created); err != nil {
		return createResult{}, err
	}
	if err := writeBundleBytes(archive, "manifest.sig", []byte(base64.RawURLEncoding.EncodeToString(signature)), created); err != nil {
		return createResult{}, err
	}
	for _, component := range []stagedComponent{
		{manifest.Database.Component, databaseDump}, {manifest.State, stateArchive},
	} {
		if err := writeBundleFile(archive, component, created); err != nil {
			return createResult{}, err
		}
	}
	if err := archive.Close(); err != nil {
		return createResult{}, err
	}
	if err := encrypted.Close(); err != nil {
		return createResult{}, fmt.Errorf("finalize recovery encryption: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return createResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return createResult{}, err
	}
	if err := os.Link(temporaryPath, outputPath); err != nil {
		return createResult{}, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return createResult{}, err
	}
	remove = false
	if err := syncDirectory(filepath.Dir(outputPath)); err != nil {
		return createResult{}, err
	}
	digest, _, err := fileDigest(outputPath)
	if err != nil {
		return createResult{}, err
	}
	if err := writeExclusiveFile(checksumPath, []byte(digest+"  "+filepath.Base(outputPath)+"\n"), 0o644); err != nil {
		return createResult{}, err
	}
	return createResult{Path: outputPath, SHA256: digest, AuditPosition: manifest.Database.AuditPosition, EventPosition: manifest.Database.EventPosition}, nil
}

func openRecoverySet(inputPath, identityPath, trustPath string) (verifiedRecoverySet, func(), error) {
	cleanup := func() {}
	inputInfo, err := os.Lstat(inputPath)
	if err != nil || !inputInfo.Mode().IsRegular() || inputInfo.Mode()&os.ModeSymlink != 0 || inputInfo.Size() <= 0 {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("recovery input must be a non-empty non-symlink regular file")
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil || !os.SameFile(inputInfo, openedInfo) {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("recovery input changed while it was being opened")
	}
	identityBytes, err := readProtectedFile(identityPath, 64<<10, true)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("read age identities: %w", err)
	}
	defer clear(identityBytes)
	identities, err := age.ParseIdentities(bytesReader(identityBytes))
	if err != nil || len(identities) == 0 || len(identities) > 8 {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("age identity file must contain one to eight native identities")
	}
	decrypted, err := age.Decrypt(input, identities...)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("decrypt recovery set: %w", err)
	}
	archive := tar.NewReader(decrypted)
	manifestBytes, err := readBundleBytes(archive, "manifest.json", maximumRecoveryManifestBytes)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, err
	}
	var manifest recoveryManifest
	if err := strictJSON(manifestBytes, &manifest); err != nil || validateRecoveryManifest(manifest) != nil {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("recovery manifest is invalid")
	}
	signatureText, err := readBundleBytes(archive, "manifest.sig", 1024)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(string(signatureText))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("recovery manifest signature is invalid")
	}
	publicKey, err := loadSigningTrust(trustPath, manifest.SignerKeyID)
	if err != nil {
		return verifiedRecoverySet{}, cleanup, err
	}
	if !ed25519.Verify(publicKey, recoverySignatureMessage(manifestBytes), signature) {
		return verifiedRecoverySet{}, cleanup, fmt.Errorf("recovery manifest signature verification failed")
	}
	temporary, err := os.MkdirTemp("", "labcentral-recovery-verify-")
	if err != nil {
		return verifiedRecoverySet{}, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(temporary) }
	result := verifiedRecoverySet{Manifest: manifest}
	primaryMaximum := maximumDatabaseDumpBytes
	if manifest.Database.Engine == "sqlite" {
		primaryMaximum = maximumStateArchiveBytes
	}
	result.DatabaseComponent, err = readBundleComponent(archive, manifest.Database.Component, primaryMaximum, temporary)
	if err != nil {
		cleanup()
		return verifiedRecoverySet{}, func() {}, err
	}
	result.StateComponent, err = readBundleComponent(archive, manifest.State, maximumStateArchiveBytes, temporary)
	if err != nil {
		cleanup()
		return verifiedRecoverySet{}, func() {}, err
	}
	if _, err := archive.Next(); !errors.Is(err, io.EOF) {
		cleanup()
		return verifiedRecoverySet{}, func() {}, fmt.Errorf("recovery set contains unexpected trailing entries")
	}
	return result, cleanup, nil
}

func recoverySignatureMessage(manifest []byte) []byte {
	message := make([]byte, 0, len("labcentral-recovery-manifest-v1\x00")+len(manifest))
	message = append(message, "labcentral-recovery-manifest-v1\x00"...)
	return append(message, manifest...)
}

func validateRecoveryManifest(manifest recoveryManifest) error {
	created, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil || created.IsZero() || created.After(time.Now().UTC().Add(5*time.Minute)) || manifest.SchemaVersion != recoverySchemaVersion ||
		!safeRecoveryToken(manifest.Release) || !safeRecoveryToken(manifest.SignerKeyID) || manifest.Database.ToolVersion == "" {
		return fmt.Errorf("invalid recovery manifest metadata")
	}
	switch manifest.Database.Engine {
	case "postgresql":
		if manifest.Database.Format != "custom" {
			return fmt.Errorf("invalid PostgreSQL recovery format")
		}
		if err := validateRecoveryComponent(manifest.Database.Component, "database.dump", maximumDatabaseDumpBytes); err != nil {
			return err
		}
		return validateRecoveryComponent(manifest.State, "state.tar", maximumStateArchiveBytes)
	case "sqlite":
		if manifest.Database.Format != "filesystem" {
			return fmt.Errorf("invalid SQLite recovery format")
		}
		if err := validateRecoveryComponent(manifest.Database.Component, "core-state.tar", maximumStateArchiveBytes); err != nil {
			return err
		}
		return validateRecoveryComponent(manifest.State, "provider-state.tar", maximumStateArchiveBytes)
	default:
		return fmt.Errorf("invalid recovery database engine")
	}
}

func validateRecoveryComponent(component recoveryComponent, expected string, maximum int64) error {
	if component.Name != expected || !sha256Pattern.MatchString(component.SHA256) || component.SizeBytes <= 0 || component.SizeBytes > maximum {
		return fmt.Errorf("invalid recovery component")
	}
	return nil
}

func writeBundleBytes(writer *tar.Writer, name string, contents []byte, created time.Time) error {
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents)), ModTime: created, Format: tar.FormatPAX}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	return writeAll(writer, contents)
}

func writeBundleFile(writer *tar.Writer, component stagedComponent, created time.Time) error {
	if err := validateRecoveryComponent(component.Manifest, component.Manifest.Name, maximumStateArchiveBytes); err != nil {
		return err
	}
	file, err := os.Open(component.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != component.Manifest.SizeBytes {
		return fmt.Errorf("recovery component %s changed before it was read", component.Manifest.Name)
	}
	header := &tar.Header{Name: component.Manifest.Name, Mode: 0o600, Size: component.Manifest.SizeBytes, ModTime: created, Format: tar.FormatPAX}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(writer, hash), file, component.Manifest.SizeBytes)
	if err != nil || written != component.Manifest.SizeBytes {
		return fmt.Errorf("write recovery component %s: %w", component.Manifest.Name, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != component.Manifest.SHA256 {
		return fmt.Errorf("recovery component %s changed while it was read", component.Manifest.Name)
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || readErr != io.EOF {
		return fmt.Errorf("recovery component %s changed while being read", component.Manifest.Name)
	}
	return nil
}

func readBundleBytes(reader *tar.Reader, expected string, maximum int64) ([]byte, error) {
	header, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read recovery entry %s: %w", expected, err)
	}
	if header.Name != expected || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size <= 0 || header.Size > maximum {
		return nil, fmt.Errorf("recovery entry %s is invalid", expected)
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(contents)) != header.Size {
		return nil, fmt.Errorf("read recovery entry %s", expected)
	}
	return contents, nil
}

func readBundleComponent(reader *tar.Reader, component recoveryComponent, maximum int64, directory string) (string, error) {
	header, err := reader.Next()
	if err != nil {
		return "", err
	}
	if header.Name != component.Name || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size != component.SizeBytes || header.Size <= 0 || header.Size > maximum {
		return "", fmt.Errorf("recovery component %s header is invalid", component.Name)
	}
	path := filepath.Join(directory, component.Name)
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(output, hash), reader, header.Size)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil || written != header.Size {
		return "", fmt.Errorf("read recovery component %s: %w", component.Name, copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != component.SHA256 {
		return "", fmt.Errorf("recovery component %s checksum mismatch", component.Name)
	}
	return path, nil
}

type byteSliceReader struct {
	contents []byte
	offset   int
}

func bytesReader(contents []byte) io.Reader { return &byteSliceReader{contents: contents} }

func (reader *byteSliceReader) Read(destination []byte) (int, error) {
	if reader.offset >= len(reader.contents) {
		return 0, io.EOF
	}
	count := copy(destination, reader.contents[reader.offset:])
	reader.offset += count
	return count, nil
}
