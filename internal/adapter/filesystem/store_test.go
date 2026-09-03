package filesystem

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestStoreStagesVerifiesCommitsAndOpensArtifact(t *testing.T) {
	store, err := New(artifactTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte("hello world")
	manifest := manifestFor(t, content)
	uploadID, err := store.Stage(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := store.Write(context.Background(), uploadID, strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}
	if measured.Digest != manifest.Digest || measured.Size != manifest.ByteCount {
		t.Fatalf("measured artifact = %#v", measured)
	}
	reference, err := store.Commit(context.Background(), uploadID, manifest)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), application.AuthorizedArtifactRef{Reference: reference, SubjectID: "user-local"})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || string(opened) != string(content) {
		t.Fatalf("opened artifact = %q, read=%v close=%v", opened, err, closeErr)
	}
	mode, err := os.Stat(filepath.Join(store.root.Name(), "objects", manifest.Digest.Hex()[:2], manifest.Digest.Hex()))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o400 {
		t.Fatalf("committed mode = %o", mode.Mode().Perm())
	}
}

func TestStoreRejectsOversizeAndMismatchedContent(t *testing.T) {
	store, err := New(artifactTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manifest := manifestFor(t, []byte("short"))
	uploadID, err := store.Stage(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), uploadID, strings.NewReader("too long")); err == nil {
		t.Fatal("oversized artifact was accepted")
	}
	uploadID, err = store.Stage(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), uploadID, strings.NewReader("other")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), uploadID, manifest); err == nil {
		t.Fatal("artifact with mismatched digest was committed")
	}
}

func TestStoreRefusesSymlinkArtifact(t *testing.T) {
	root := artifactTempDir(t)
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte("content")
	manifest := manifestFor(t, content)
	prefix := filepath.Join(root, "objects", manifest.Digest.Hex()[:2])
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(prefix, manifest.Digest.Hex())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), application.AuthorizedArtifactRef{Reference: domain.ArtifactRef(manifest.Digest.String())}); err == nil {
		t.Fatal("symlink artifact was followed")
	}
}

func TestStoreRejectsPublicArtifactRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("public artifact root error = %v", err)
	}
}

func TestSharedStoreSerializesCommitAndSharesCommittedObjects(t *testing.T) {
	root := artifactTempDir(t)
	first, err := NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	content := []byte("shared artifact")
	manifest := manifestFor(t, content)
	uploadID, err := first.Stage(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write(context.Background(), uploadID, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			<-start
			_, commitErr := store.Commit(context.Background(), uploadID, manifest)
			results <- commitErr
		}(store)
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for commitErr := range results {
		if commitErr == nil {
			succeeded++
		} else {
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("shared commit succeeded=%d rejected=%d", succeeded, rejected)
	}
	reader, err := second.Open(context.Background(), application.AuthorizedArtifactRef{Reference: domain.ArtifactRef(manifest.Digest.String())})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || string(opened) != string(content) {
		t.Fatalf("shared committed artifact = %q, read=%v close=%v", opened, err, closeErr)
	}
}

func manifestFor(t *testing.T, content []byte) application.VerifiedManifest {
	t.Helper()
	sum := sha256.Sum256(content)
	digest, err := domain.ParseSHA256Digest("sha256:" + fmtHex(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return application.VerifiedManifest{Digest: digest, FileCount: 1, ByteCount: uint64(len(content))}
}

func artifactTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func fmtHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = alphabet[current>>4]
		result[index*2+1] = alphabet[current&15]
	}
	return string(result)
}
