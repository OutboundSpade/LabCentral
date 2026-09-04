package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareJournalDirectoryCreatesPrivateProcessSafeStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxmox-journal")
	if err := prepareJournalDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory = %#v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(path, ".labcentral-mutation.lock")); err != nil {
		t.Fatalf("journal lock was not initialized: %v", err)
	}
	if err := prepareJournalDirectory(path); err != nil {
		t.Fatalf("journal initialization is not restart-safe: %v", err)
	}
	if err := prepareJournalDirectory("relative"); err == nil {
		t.Fatal("relative journal path was accepted")
	}
}

func TestPrepareJournalDirectoryDoesNotFollowOrReprotectForeignPaths(t *testing.T) {
	parent := t.TempDir()
	foreign := filepath.Join(parent, "foreign")
	if err := os.Mkdir(foreign, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := prepareJournalDirectory(foreign); err == nil {
		t.Fatal("non-private existing directory was accepted")
	}
	info, err := os.Lstat(foreign)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("foreign directory mode changed: %#v, %v", info, err)
	}
	link := filepath.Join(parent, "journal-link")
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareJournalDirectory(link); err == nil {
		t.Fatal("symlinked journal was accepted")
	}

	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := prepareJournalDirectory(filepath.Join(parent, "new-journal")); err == nil {
		t.Fatal("world-accessible journal parent was accepted")
	}
}
