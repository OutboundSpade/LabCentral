package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeAgentConfigurationRequiresExactMTLSIdentity(t *testing.T) {
	valid := []string{
		"--listen", "10.20.0.10:9443",
		"--server-cert", "/qualification/server.crt",
		"--server-key", "/qualification/server.key",
		"--client-ca", "/qualification/client-ca.crt",
		"--agent-image-digest", testAgentDigest("a"),
	}
	config, err := parseConfiguration(valid)
	if err != nil || config.listen != "10.20.0.10:9443" {
		t.Fatalf("valid config = %#v, %v", config, err)
	}
	for name, arguments := range map[string][]string{
		"missing":         nil,
		"wildcard":        replaceArgument(valid, "10.20.0.10:9443", "0.0.0.0:9443"),
		"loopback":        replaceArgument(valid, "10.20.0.10:9443", "127.0.0.1:9443"),
		"privileged port": replaceArgument(valid, "10.20.0.10:9443", "10.20.0.10:443"),
		"relative key":    replaceArgument(valid, "/qualification/server.key", "server.key"),
		"reused key":      replaceArgument(valid, "/qualification/server.key", "/qualification/server.crt"),
		"bad digest":      replaceArgument(valid, testAgentDigest("a"), "latest"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfiguration(arguments); err == nil {
				t.Fatal("unsafe probe agent configuration was accepted")
			}
		})
	}
}

func TestProbeAgentRejectsPermissiveOrSymlinkPrivateKey(t *testing.T) {
	directory := t.TempDir()
	protected := filepath.Join(directory, "server.key")
	if err := os.WriteFile(protected, []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := readAgentFile(protected, 1024, true)
	if err != nil || string(contents) != "private-key" {
		t.Fatalf("protected input = %q, %v", contents, err)
	}
	clear(contents)
	permissive := filepath.Join(directory, "permissive.key")
	if err := os.WriteFile(permissive, []byte("private-key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentFile(permissive, 1024, true); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("permissive key error = %v", err)
	}
	link := filepath.Join(directory, "server.link")
	if err := os.Symlink(protected, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentFile(link, 1024, true); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink key error = %v", err)
	}
}

func replaceArgument(source []string, old, replacement string) []string {
	result := append([]string(nil), source...)
	for index := range result {
		if result[index] == old {
			result[index] = replacement
			return result
		}
	}
	return result
}

func testAgentDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
