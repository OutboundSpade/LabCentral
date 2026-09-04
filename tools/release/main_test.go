package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSafeToken(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"v0.1.0", "linux", "x86_64", "release-1"} {
		if !safeToken(value) {
			t.Errorf("safeToken(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-bad", "bad/path", "bad space", ".bad"} {
		if safeToken(value) {
			t.Errorf("safeToken(%q) = true", value)
		}
	}
}

func TestSanitizeRemote(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"https://token@example.com/org/repo.git": "https://example.com/org/repo.git",
		"git@example.com:org/repo.git":           "example.com:org/repo.git",
		"https://example.com/org/repo.git":       "https://example.com/org/repo.git",
	} {
		if actual := sanitizeRemote(input); actual != expected {
			t.Errorf("sanitizeRemote(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestReleaseReadmeSelectsAuthenticationModeExplicitly(t *testing.T) {
	t.Parallel()
	readme := releaseReadme("v1.0.0", "linux", "amd64", "labd", "lab-provider-docker", "lab-provider-proxmox", "lab-access-novnc")
	if !strings.Contains(readme, "--auth-mode=none") {
		t.Fatal("release launch instructions omit the required authentication mode")
	}
	if !strings.Contains(readme, "lab-access-novnc") {
		t.Fatal("release instructions omit the access plugin")
	}
}

func TestWriteArchiveIsDeterministicAndNormalized(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "labd"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugins", "docker.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Unix(1_700_000_000, 0).UTC()
	var first, second bytes.Buffer
	if err := writeArchive(&first, root, "labcentral-test-linux-amd64", timestamp); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(&second, root, "labcentral-test-linux-amd64", timestamp); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("archives differ")
	}

	gzipReader, err := gzip.NewReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gzipReader)
	var names []string
	var modes []int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		modes = append(modes, header.Mode)
		if header.Uid != 0 || header.Gid != 0 || !header.ModTime.Equal(timestamp) {
			t.Errorf("entry %s has non-normalized metadata", header.Name)
		}
	}
	wantNames := []string{
		"labcentral-test-linux-amd64/",
		"labcentral-test-linux-amd64/labd",
		"labcentral-test-linux-amd64/plugins/",
		"labcentral-test-linux-amd64/plugins/docker.json",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("names = %#v, want %#v", names, wantNames)
	}
	wantModes := []int64{0o755, 0o755, 0o755, 0o644}
	if !reflect.DeepEqual(modes, wantModes) {
		t.Fatalf("modes = %#v, want %#v", modes, wantModes)
	}
}
