// Package filesystem implements the single-node, content-addressed artifact
// store used by LabCentral's laptop deployment.
package filesystem

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const (
	stagingDirectory = "staging"
	objectsDirectory = "objects"
)

// Store keeps staging and committed objects beneath one filesystem root so a
// successful commit is an atomic rename rather than a copy.
type Store struct {
	root *os.Root
}

func New(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	if err := rejectSymlink(abs); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	store := &Store{root: root}
	for _, directory := range []string{stagingDirectory, objectsDirectory} {
		if err := store.ensureDirectory(directory); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Close() error { return s.root.Close() }

func (s *Store) Stage(ctx context.Context, expected application.VerifiedManifest) (domain.UploadID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateManifest(expected); err != nil {
		return "", err
	}
	for attempts := 0; attempts < 8; attempts++ {
		uploadID, err := newUploadID()
		if err != nil {
			return "", err
		}
		dataName, manifestName := stagingNames(uploadID)
		data, err := s.root.OpenFile(dataName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create staged artifact: %w", err)
		}
		if err := data.Sync(); err != nil {
			_ = data.Close()
			_ = s.root.Remove(dataName)
			return "", fmt.Errorf("sync staged artifact: %w", err)
		}
		if err := data.Close(); err != nil {
			_ = s.root.Remove(dataName)
			return "", fmt.Errorf("close staged artifact: %w", err)
		}
		if err := s.writeManifest(manifestName, expected); err != nil {
			_ = s.root.Remove(dataName)
			return "", err
		}
		if err := s.syncDirectory(stagingDirectory); err != nil {
			_ = s.root.Remove(dataName)
			_ = s.root.Remove(manifestName)
			return "", err
		}
		return uploadID, nil
	}
	return "", fmt.Errorf("allocate unique upload ID")
}

func (s *Store) Write(ctx context.Context, uploadID domain.UploadID, source io.Reader) (application.MeasuredArtifact, error) {
	if err := validateUploadID(uploadID); err != nil {
		return application.MeasuredArtifact{}, err
	}
	expected, err := s.readStagedManifest(uploadID)
	if err != nil {
		return application.MeasuredArtifact{}, err
	}
	dataName, _ := stagingNames(uploadID)
	if err := s.requireRegular(dataName); err != nil {
		return application.MeasuredArtifact{}, err
	}
	file, err := s.root.OpenFile(dataName, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("open staged artifact: %w", err)
	}
	hash := sha256.New()
	limited := io.LimitReader(&contextReader{ctx: ctx, reader: source}, int64(expected.ByteCount)+1)
	written, copyErr := io.Copy(io.MultiWriter(file, hash), limited)
	if copyErr == nil && uint64(written) > expected.ByteCount {
		copyErr = fmt.Errorf("artifact exceeds declared byte count %d", expected.ByteCount)
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("write staged artifact: %w", copyErr)
	}
	if closeErr != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("close staged artifact: %w", closeErr)
	}
	digest, err := domain.ParseSHA256Digest(hex.EncodeToString(hash.Sum(nil)))
	if err != nil {
		return application.MeasuredArtifact{}, err
	}
	return application.MeasuredArtifact{Size: uint64(written), Digest: digest}, nil
}

func (s *Store) Commit(ctx context.Context, uploadID domain.UploadID, supplied application.VerifiedManifest) (domain.ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateUploadID(uploadID); err != nil {
		return "", err
	}
	if err := validateManifest(supplied); err != nil {
		return "", err
	}
	expected, err := s.readStagedManifest(uploadID)
	if err != nil {
		return "", err
	}
	if expected != supplied {
		return "", fmt.Errorf("commit manifest differs from staged manifest")
	}
	dataName, manifestName := stagingNames(uploadID)
	measured, err := s.measure(dataName)
	if err != nil {
		return "", err
	}
	if measured.Size != supplied.ByteCount || measured.Digest != supplied.Digest {
		return "", fmt.Errorf("artifact content does not match verified manifest")
	}
	prefix := supplied.Digest.Hex()[:2]
	objectDirectory := objectsDirectory + "/" + prefix
	if err := s.ensureDirectory(objectDirectory); err != nil {
		return "", err
	}
	objectName := objectDirectory + "/" + supplied.Digest.Hex()
	if _, err := s.root.Lstat(objectName); err == nil {
		existing, measureErr := s.measure(objectName)
		if measureErr != nil || existing != measured {
			return "", fmt.Errorf("existing content-addressed artifact failed verification")
		}
		if err := s.root.Remove(dataName); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("remove redundant staged artifact: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect artifact destination: %w", err)
	} else {
		if err := s.root.Chmod(dataName, 0o400); err != nil {
			return "", fmt.Errorf("make staged artifact immutable: %w", err)
		}
		if err := s.root.Rename(dataName, objectName); err != nil {
			return "", fmt.Errorf("commit artifact atomically: %w", err)
		}
		if err := s.syncDirectory(objectDirectory); err != nil {
			return "", err
		}
	}
	if err := s.root.Remove(manifestName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("remove staged manifest: %w", err)
	}
	if err := s.syncDirectory(stagingDirectory); err != nil {
		return "", err
	}
	return domain.ArtifactRef(supplied.Digest.String()), nil
}

func (s *Store) Open(ctx context.Context, authorized application.AuthorizedArtifactRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest, err := digestFromReference(authorized.Reference)
	if err != nil {
		return nil, err
	}
	name := objectsDirectory + "/" + digest.Hex()[:2] + "/" + digest.Hex()
	measured, err := s.measure(name)
	if err != nil {
		return nil, err
	}
	if measured.Digest != digest {
		return nil, fmt.Errorf("committed artifact digest verification failed")
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open committed artifact: %w", err)
	}
	return file, nil
}

func (s *Store) measure(name string) (application.MeasuredArtifact, error) {
	if err := s.requireRegular(name); err != nil {
		return application.MeasuredArtifact{}, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("open artifact for verification: %w", err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("verify artifact: %w", copyErr)
	}
	if closeErr != nil {
		return application.MeasuredArtifact{}, fmt.Errorf("close verified artifact: %w", closeErr)
	}
	digest, err := domain.ParseSHA256Digest(hex.EncodeToString(hash.Sum(nil)))
	if err != nil {
		return application.MeasuredArtifact{}, err
	}
	return application.MeasuredArtifact{Size: uint64(size), Digest: digest}, nil
}

func (s *Store) writeManifest(name string, manifest application.VerifiedManifest) error {
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged manifest: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(manifest)
	if encodeErr == nil {
		encodeErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil {
		_ = s.root.Remove(name)
		return fmt.Errorf("write staged manifest: %w", encodeErr)
	}
	if closeErr != nil {
		_ = s.root.Remove(name)
		return fmt.Errorf("close staged manifest: %w", closeErr)
	}
	return nil
}

func (s *Store) readStagedManifest(uploadID domain.UploadID) (application.VerifiedManifest, error) {
	_, name := stagingNames(uploadID)
	if err := s.requireRegular(name); err != nil {
		return application.VerifiedManifest{}, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return application.VerifiedManifest{}, fmt.Errorf("open staged manifest: %w", err)
	}
	defer file.Close()
	var manifest application.VerifiedManifest
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return application.VerifiedManifest{}, fmt.Errorf("decode staged manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return application.VerifiedManifest{}, err
	}
	return manifest, nil
}

func (s *Store) ensureDirectory(name string) error {
	if err := s.root.MkdirAll(name, 0o700); err != nil {
		return fmt.Errorf("create artifact directory %q: %w", name, err)
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect artifact directory %q: %w", name, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact path %q is not a real directory", name)
	}
	return nil
}

func (s *Store) requireRegular(name string) error {
	info, err := s.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect artifact %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact path %q is not a regular file", name)
	}
	return nil
}

func (s *Store) syncDirectory(name string) error {
	directory, err := s.root.Open(name)
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync artifact directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact directory: %w", closeErr)
	}
	return nil
}

func validateManifest(manifest application.VerifiedManifest) error {
	if _, err := domain.ParseSHA256Digest(manifest.Digest.Hex()); err != nil {
		return fmt.Errorf("invalid artifact manifest digest: %w", err)
	}
	if manifest.FileCount == 0 {
		return fmt.Errorf("artifact manifest file count must be positive")
	}
	if manifest.ByteCount >= 1<<63-1 {
		return fmt.Errorf("artifact manifest byte count exceeds the filesystem adapter limit")
	}
	return nil
}

func validateUploadID(uploadID domain.UploadID) error {
	if _, err := domain.NewUploadID(string(uploadID)); err != nil {
		return fmt.Errorf("invalid upload ID: %w", err)
	}
	if !strings.HasPrefix(string(uploadID), "upload-") {
		return fmt.Errorf("invalid filesystem upload ID")
	}
	return nil
}

func digestFromReference(reference domain.ArtifactRef) (domain.SHA256Digest, error) {
	value := string(reference)
	if !strings.HasPrefix(value, "sha256:") {
		return "", fmt.Errorf("unsupported artifact reference")
	}
	digest, err := domain.ParseSHA256Digest(value)
	if err != nil {
		return "", fmt.Errorf("invalid artifact reference: %w", err)
	}
	return digest, nil
}

func stagingNames(uploadID domain.UploadID) (string, string) {
	base := stagingDirectory + "/" + string(uploadID)
	return base + ".data", base + ".json"
}

func newUploadID() (domain.UploadID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate upload ID: %w", err)
	}
	return domain.NewUploadID("upload-" + hex.EncodeToString(random))
}

func rejectSymlink(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect artifact root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("artifact root must be a real directory")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

var _ application.ArtifactStore = (*Store)(nil)
