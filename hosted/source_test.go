package hosted

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
