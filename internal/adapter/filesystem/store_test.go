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
	staging, err := os.ReadDir(filepath.Join(store.root.Name(), stagingDirectory))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging entries after commit = %d, err=%v", len(staging), err)
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

func TestMaintenanceLeaseExcludesRunningStores(t *testing.T) {
	root := artifactTempDir(t)
	first, err := NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMaintenance(root); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("maintenance opened over live replicas: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMaintenance(root); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("maintenance opened over remaining live replica: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := OpenMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	if _, err := NewShared(root); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("runtime store opened during maintenance: %v", err)
	}
}

func TestStoreRejectsLinkedMaintenanceLock(t *testing.T) {
	for _, test := range []struct {
		name string
		link func(string, string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := artifactTempDir(t)
			target := filepath.Join(t.TempDir(), "lock-target")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.link(target, filepath.Join(root, maintenanceLockName)); err != nil {
				t.Fatal(err)
			}
			if _, err := New(root); err == nil || !strings.Contains(err.Error(), "maintenance lock") {
				t.Fatalf("linked maintenance lock was accepted: %v", err)
			}
		})
	}
}

func TestStoresConcurrentlyCreateMaintenanceLease(t *testing.T) {
	root := artifactTempDir(t)
	start := make(chan struct{})
	errors := make(chan error, 16)
	var group sync.WaitGroup
	for index := 0; index < cap(errors); index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			store, err := NewShared(root)
			if err == nil {
				err = store.Close()
			}
			errors <- err
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMaintenanceCollectsOnlyUnreferencedArtifactsAndAbandonedStaging(t *testing.T) {
	root := artifactTempDir(t)
	store, err := NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	referenced := commitTestArtifact(t, store, []byte("referenced"))
	unreferenced := commitTestArtifact(t, store, []byte("unreferenced"))
	abandonedManifest := manifestFor(t, []byte("abandoned"))
	if _, err := store.Stage(context.Background(), abandonedManifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	maintenance, err := OpenMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	dryRun, err := maintenance.Collect(context.Background(), []domain.ArtifactRef{referenced}, false)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Applied || dryRun.Objects != 2 || dryRun.ReferencedObjects != 1 || dryRun.UnreferencedObjects != 1 || dryRun.StagingFiles != 3 || dryRun.BytesReclaimable != uint64(len("unreferenced")) || dryRun.ObjectsDeleted != 0 {
		t.Fatalf("dry-run result = %#v", dryRun)
	}
	applied, err := maintenance.Collect(context.Background(), []domain.ArtifactRef{referenced}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.ObjectsDeleted != 1 || applied.StagingFilesDeleted != 3 {
		t.Fatalf("applied result = %#v", applied)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewShared(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reader, err := store.Open(context.Background(), application.AuthorizedArtifactRef{Reference: referenced})
	if err != nil {
		t.Fatal(err)
	}
	if contents, err := io.ReadAll(reader); err != nil || string(contents) != "referenced" {
		t.Fatalf("retained artifact = %q, err=%v", contents, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), application.AuthorizedArtifactRef{Reference: unreferenced}); err == nil {
		t.Fatal("unreferenced artifact survived collection")
	}
	staging, err := os.ReadDir(filepath.Join(root, stagingDirectory))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging entries after collection = %d, err=%v", len(staging), err)
	}
}

func TestMaintenanceRefusesMissingLiveArtifactBeforeDeletion(t *testing.T) {
	root := artifactTempDir(t)
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	unreferenced := commitTestArtifact(t, store, []byte("do-not-delete"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := OpenMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	missing := domain.ArtifactRef("sha256:" + strings.Repeat("a", 64))
	if _, err := maintenance.Collect(context.Background(), []domain.ArtifactRef{missing}, true); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing live artifact result = %v", err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reader, err := store.Open(context.Background(), application.AuthorizedArtifactRef{Reference: unreferenced})
	if err != nil {
		t.Fatalf("collection mutated before validating live references: %v", err)
	}
	_ = reader.Close()
}

func TestMaintenanceRejectsLinkedObjectWithoutDeletion(t *testing.T) {
	root := artifactTempDir(t)
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reference := commitTestArtifact(t, store, []byte("linked-object"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimPrefix(string(reference), "sha256:")
	objectPath := filepath.Join(root, objectsDirectory, digest[:2], digest)
	linkPath := filepath.Join(t.TempDir(), "unexpected-hardlink")
	if err := os.Link(objectPath, linkPath); err != nil {
		t.Fatal(err)
	}
	maintenance, err := OpenMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Collect(context.Background(), nil, true); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("linked artifact collection result = %v", err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("unsafe inventory was partially deleted: %v", err)
	}
}

func commitTestArtifact(t *testing.T, store *Store, content []byte) domain.ArtifactRef {
	t.Helper()
	manifest := manifestFor(t, content)
	uploadID, err := store.Stage(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), uploadID, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}
	reference, err := store.Commit(context.Background(), uploadID, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return reference
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
