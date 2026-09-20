package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
	"github.com/OutboundSpade/LabCentral/sources/github"
)

func TestGitHubReleaseManifestAcceptsConfiguredLibrary(t *testing.T) {
	t.Parallel()
	m, err := githubSourceManifest("../../schemas/github-source-binding-v1.schema.json", config{goos: "linux", goarch: "amd64"}, subject{Name: "lab-source-github", SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var installed manifest.Manifest
	if err := manifest.Decode(data, &installed); err != nil {
		t.Fatal(err)
	}
	// Exercise the same configuration gate as installation, then the real source
	// parser. A manifest without its schema used to reject every real binding.
	for _, index := range []string{"index.yaml", "examples/template-library/index.yaml"} {
		value := []byte(`{"owner":"example-organization","repository":"lab-templates","reference":"meeting/lab-1","index":"` + index + `"}`)
		if err := manifest.ValidateConfiguration(installed.ConfigurationSchema, value); err != nil {
			t.Fatal(err)
		}
		if _, err := github.ParseConfiguration(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{
		`{}`,
		`{"owner":"../org","repository":"labs","reference":"main","index":"index.yaml"}`,
		`{"owner":"org","repository":"labs","reference":"","index":"index.yaml"}`,
		`{"owner":"org","repository":"labs","reference":"main\n","index":"index.yaml"}`,
		`{"owner":"org","repository":"labs","reference":"main","index":"../index.yaml"}`,
		`{"owner":"org","repository":"labs","reference":"main","index":"/index.yaml"}`,
		`{"owner":"org","repository":"labs","reference":"main","index":"index.yaml","token":"must-not-be-configuration"}`,
	} {
		if err := manifest.ValidateConfiguration(installed.ConfigurationSchema, []byte(value)); err == nil {
			t.Fatalf("invalid repository configuration accepted: %s", value)
		}
	}
}

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
	readme := releaseReadme("v1.0.0", "linux", "amd64", "labd", "lab-provider-docker", "lab-provider-proxmox", "lab-access-novnc", "labcentral-recovery")
	if !strings.Contains(readme, "--auth-mode=none") {
		t.Fatal("release launch instructions omit the required authentication mode")
	}
	if !strings.Contains(readme, "lab-access-novnc") {
		t.Fatal("release instructions omit the access plugin")
	}
	if !strings.Contains(readme, "kubernetes-proxmox.md") {
		t.Fatal("release instructions omit the Kubernetes deployment runbook")
	}
	if !strings.Contains(readme, "three OS identities") {
		t.Fatal("release instructions omit the native identity boundary")
	}
	if !strings.Contains(readme, "labcentral-recovery") || !strings.Contains(readme, "reference-aware filesystem artifact maintenance") || !strings.Contains(readme, "docs/operations/recovery.md") {
		t.Fatal("release instructions omit the encrypted recovery utility")
	}
	if !strings.Contains(readme, "docs/operations/release-verification.md") || !strings.Contains(readme, "external Sigstore bundle") {
		t.Fatal("release instructions omit external signature verification")
	}
	if !strings.Contains(readme, "docs/compatibility.md") || !strings.Contains(readme, "provenance attestation") {
		t.Fatal("release instructions omit compatibility or provenance verification")
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

func TestReleaseAssetPreservesCanonicalDirectoryLayout(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.md")
	if err := os.WriteFile(source, []byte("guide"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "archive", "docs", "operations", "guide.md")
	if err := copyReleaseFile(source, destination); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(destination)
	if err != nil || string(b) != "guide" {
		t.Fatal("release guide was not copied", err)
	}
}

func TestBuildWebReplacesIgnoredOutputAndStopsOnFailure(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("web", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("web/output", []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	bun := filepath.Join(root, "bun-test")
	script := `#!/bin/sh
set -eu
printf '%s:%s:%s\n' "$*" "$LABCENTRAL_WEB_VERSION" "$SOURCE_DATE_EPOCH" >> commands
if [ "$*" = 'run build' ]; then
 test "$LABCENTRAL_TEST_BUILD_FAIL" != 'yes'
 printf '%s' 'rebuilt' > output
fi
`
	if err := os.WriteFile(bun, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCENTRAL_TEST_BUILD_FAIL", "no")
	cfg := config{bunBinary: bun, version: "test-release", epoch: 123}
	if err := buildWeb(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	output, _ := os.ReadFile("web/output")
	if string(output) != "rebuilt" {
		t.Fatalf("stale assets retained: %s", output)
	}
	commands, _ := os.ReadFile("web/commands")
	if string(commands) != "install --frozen-lockfile:test-release:123\nrun check:test-release:123\nrun build:test-release:123\n" {
		t.Fatalf("build sequence: %s", commands)
	}
	t.Setenv("LABCENTRAL_TEST_BUILD_FAIL", "yes")
	if err := buildWeb(context.Background(), cfg); err == nil {
		t.Fatal("failed web build accepted")
	}
}
