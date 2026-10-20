package hosted

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/instructor"
)

func TestGitSnapshotWithoutTemplatesRetiresAllNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	s, fixtures := importFixture(t)
	ctx := context.Background()
	src, bundle, err := instructor.LoadTemplate(filepath.Join(fixtures, "recovery"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.importRevision(ctx, "pinned", src, bundle, "retained handover", "original", time.Now(), "original"); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git := func(args ...string) *exec.Cmd {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		return cmd
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := git(args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init")
	if err = os.WriteFile(filepath.Join(repo, "README.md"), []byte("The last template was removed.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("-c", "user.name=Template test", "-c", "user.email=templates@example.invalid", "commit", "-m", "Remove last template")
	root := filepath.Join(t.TempDir(), "snapshot")
	if err = unpackTemplateRevision(git, "HEAD", root); err != nil {
		t.Fatal(err)
	}
	if err = s.importSnapshot(ctx, filepath.Join(root, "templates"), "empty", time.Now(), "empty"); err != nil {
		t.Fatal(err)
	}
	views, err := s.publicTemplates(ctx, "")
	if err != nil || len(views) != 0 {
		t.Fatal("deleted templates remain listed", views, err)
	}
	views, err = s.publicTemplates(ctx, "pinned")
	if err != nil || len(views) != 1 || views[0].Handover != "retained handover" {
		t.Fatal("existing revision was lost", views, err)
	}
	if err = unpackTemplateRevision(git, "unknown-revision", t.TempDir()); err == nil {
		t.Fatal("unknown revision accepted as an empty catalog")
	}
	if err = os.WriteFile(filepath.Join(repo, "templates"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "templates")
	run("-c", "user.name=Template test", "-c", "user.email=templates@example.invalid", "commit", "-m", "Invalid template tree")
	if err = unpackTemplateRevision(git, "HEAD", t.TempDir()); err == nil {
		t.Fatal("invalid template tree accepted as an empty catalog")
	}
	if err = os.Remove(filepath.Join(repo, "templates")); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repo, "templates", "restored"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "templates", "restored", "README.md"), []byte("Restored source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "templates")
	run("-c", "user.name=Template test", "-c", "user.email=templates@example.invalid", "commit", "-m", "Restore template directory")
	restored := t.TempDir()
	if err = unpackTemplateRevision(git, "HEAD", restored); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(restored, "templates", "restored", "README.md"))
	if err != nil || string(data) != "Restored source\n" {
		t.Fatal("ordinary template tree did not unpack", string(data), err)
	}
}

func TestGitArchiveMetadataAndUnsafeEntries(t *testing.T) {
	for _, entry := range []struct {
		name  string
		kind  byte
		valid bool
	}{
		{"templates/bank/lab.yaml", tar.TypeReg, true},
		{"templates/../../escape", tar.TypeReg, false},
		{"templates/bank/link", tar.TypeSymlink, false},
	} {
		t.Run(entry.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "commit"}}); err != nil {
				t.Fatal(err)
			}
			content := []byte("version: 2\n")
			size := int64(len(content))
			if entry.kind != tar.TypeReg {
				size = 0
			}
			if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0600, Size: size, Linkname: "/etc/passwd"}); err != nil {
				t.Fatal(err)
			}
			if size > 0 {
				if _, err := tw.Write(content); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			err := unpackSource(&buf, root)
			if !entry.valid {
				if err == nil {
					t.Fatal("unsafe entry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, entry.name))
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal(string(data), err)
			}
		})
	}
}

func TestRemovedTemplateNameLeavesCatalogButRetainsPinnedRevision(t *testing.T) {
	s, root := importFixture(t)
	ctx := context.Background()
	src, b, err := instructor.LoadTemplate(filepath.Join(root, "recovery"), true)
	if err != nil {
		t.Fatal(err)
	}
	oldName := src.Lab.Name
	if err = s.Store.importRevision(ctx, "old", src, b, "old handover", "old-commit", time.Now(), "old"); err != nil {
		t.Fatal(err)
	}
	// The renamed source is a new lab name, with the same image/source definitions.
	raw, err := os.ReadFile(filepath.Join(root, "recovery", "lab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "name: "+oldName, "name: renamed-lab", 1))
	if err = os.WriteFile(filepath.Join(root, "recovery", "lab.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.importSnapshot(ctx, root, "new-commit", time.Now(), "rename"); err != nil {
		t.Fatal(err)
	}
	views, err := s.publicTemplates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Name == oldName {
			t.Fatal("removed name still listed")
		}
	}
	pinned, err := s.publicTemplates(ctx, "old")
	if err != nil || len(pinned) != 1 || pinned[0].Name != oldName {
		t.Fatal("lost pinned revision", pinned, err)
	}
	if err = s.Store.Get(ctx, "template-head", oldName, new(string)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale head", err)
	}
	// Reintroducing the old name clears retirement and makes it discoverable again.
	if err = s.Store.importRevision(ctx, "restored", src, b, "restored", "restored-commit", time.Now(), "restore"); err != nil {
		t.Fatal(err)
	}
	views, err = s.publicTemplates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range views {
		found = found || v.Name == oldName
	}
	if !found {
		t.Fatal("restored name remains hidden")
	}
}
func TestIncompleteRefreshDoesNotRetireOtherNames(t *testing.T) {
	s, root := importFixture(t)
	ctx := context.Background()
	src, b, err := instructor.LoadTemplate(filepath.Join(root, "recovery"), true)
	if err != nil {
		t.Fatal(err)
	}
	src.Lab.Name = "retained-on-error"
	if err = s.Store.importRevision(ctx, "retained", src, b, "retained", "old", time.Now(), "old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "broken"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.importSnapshot(ctx, root, "partial", time.Now(), "partial"); err != nil {
		t.Fatal(err)
	}
	var head string
	if err = s.Store.Get(ctx, "template-head", "retained-on-error", &head); err != nil || head != "retained" {
		t.Fatal("incomplete snapshot retired name", head, err)
	}
}
