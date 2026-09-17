package labbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"testing"
)

func fixture(t *testing.T, name string, kind byte, content []byte, digest string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	manifest := []byte(fmt.Sprintf("apiVersion: labcentral.bundle/v1alpha1\nkind: LabBundle\nentrypoint: %s\nfiles:\n  - path: %s\n    size: %d\n    sha256: %s\n    mediaType: application/yaml\n", name, name, len(content), digest))
	if err := tw.WriteHeader(&tar.Header{Name: "bundle.yaml", Mode: 0600, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(manifest)
	header := &tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: kind}
	if kind == tar.TypeSymlink {
		header.Size = 0
		header.Linkname = "/etc/passwd"
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if header.Size > 0 {
		tw.Write(content)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestReadValidBundleAndRejectTampering(t *testing.T) {
	source := []byte("apiVersion: labs.labcentral.io/v1alpha1\nkind: Lab\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(source))
	valid := fixture(t, "lab.yaml", tar.TypeReg, source, digest)
	entry, count, err := Source(valid)
	if err != nil || count != 2 || !bytes.Equal(entry, source) {
		t.Fatalf("read: count=%d err=%v", count, err)
	}
	for _, name := range []string{"../lab.yaml", "/lab.yaml", "a/../lab.yaml", "a\\lab.yaml", "a:e"} {
		if _, err := Read(fixture(t, name, tar.TypeReg, source, digest)); err == nil {
			t.Fatalf("accepted path %q", name)
		}
	}
	if _, err := Read(fixture(t, "lab.yaml", tar.TypeSymlink, source, digest)); err == nil {
		t.Fatal("accepted symlink")
	}
	if _, err := Read(fixture(t, "lab.yaml", tar.TypeReg, []byte("substituted"), digest)); err == nil {
		t.Fatal("accepted changed bytes")
	}
	if _, err := Read(append(valid, valid...)); err == nil {
		t.Fatal("accepted concatenated compressed members")
	}
}
func TestLegacySourceIsBytePreserving(t *testing.T) {
	source := []byte("# original\nkind: Lab\n")
	got, count, err := Source(source)
	if err != nil || count != 1 || !bytes.Equal(source, got) {
		t.Fatal("legacy source changed")
	}
}
