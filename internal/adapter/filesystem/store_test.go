package filesystem

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestStoreStagesVerifiesCommitsAndOpensArtifact(t *testing.T) {
	store, err := New(t.TempDir())
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
	store, err := New(t.TempDir())
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
	root := t.TempDir()
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

func manifestFor(t *testing.T, content []byte) application.VerifiedManifest {
	t.Helper()
	sum := sha256.Sum256(content)
	digest, err := domain.ParseSHA256Digest("sha256:" + fmtHex(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return application.VerifiedManifest{Digest: digest, FileCount: 1, ByteCount: uint64(len(content))}
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
