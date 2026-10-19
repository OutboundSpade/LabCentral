package hosted

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
