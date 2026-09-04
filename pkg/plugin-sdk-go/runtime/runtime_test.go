package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
)

func TestSelectedProtocolMustBeInsideAdvertisedRange(t *testing.T) {
	ranges := []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 1}}
	if !supportsProtocol(ranges, &pluginv1.ProtocolVersion{Major: 1, Minor: 1}) {
		t.Fatal("advertised protocol 1.1 was rejected")
	}
	for _, selected := range []*pluginv1.ProtocolVersion{
		nil,
		{Major: 1, Minor: 2},
		{Major: 2, Minor: 0},
	} {
		if supportsProtocol(ranges, selected) {
			t.Fatalf("unadvertised protocol was accepted: %v", selected)
		}
	}
}

func TestConfiguredLaunchTokenFileIsPrivateBoundedAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(launch.Token, "")
	t.Setenv(launch.TokenFile, path)
	if _, err := configuredLaunchToken(); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("public token file accepted or reflected: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := configuredLaunchToken(); err != nil || got != token {
		t.Fatalf("protected token = %q, %v", got, err)
	}
	if err := os.Chmod(path, 0o440); err != nil {
		t.Fatal(err)
	}
	if got, err := configuredLaunchToken(); err != nil || got != token {
		t.Fatalf("group-projected token = %q, %v", got, err)
	}
	t.Setenv(launch.Token, token)
	if _, err := configuredLaunchToken(); err == nil {
		t.Fatal("simultaneous token sources were accepted")
	}
}

func TestSharedRuntimeDirectoryRejectsUnprotectedOrSymlinkedParent(t *testing.T) {
	worldParent := t.TempDir()
	if err := os.Chmod(worldParent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeDirectory(filepath.Join(worldParent, "ipc"), 0o660); err == nil {
		t.Fatal("world-accessible runtime parent was accepted")
	}

	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if err := prepareRuntimeDirectory(filepath.Join(linkedParent, "ipc"), 0o660); err == nil {
		t.Fatal("symlinked runtime parent was accepted")
	}
}
