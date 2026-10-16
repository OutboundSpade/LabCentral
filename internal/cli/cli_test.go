package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("LABCENTRAL_CONFIG", "")
	source, err := filepath.Abs("../../examples/single")
	if err != nil {
		t.Fatal(err)
	}
	return root, source
}

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	err := Run(context.Background(), args, os.Stdin, &out, &diagnostic)
	return out.String(), diagnostic.String(), err
}

func TestHelpAndInvalidCommandsNeverStartProviderOrCreateState(t *testing.T) {
	root, _ := cliFixture(t)
	provider := filepath.Join(root, "must-not-execute")
	marker := filepath.Join(root, "executed")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"deploy", "--help"}, {"help", "image", "remove"}, {"environment", "-h"}, {"--help"},
	} {
		out, _, err := runCLI(t, append(args, "--provider", provider)...)
		if err != nil || !strings.Contains(out, "Usage:") {
			t.Fatal(args, out, err)
		}
	}
	for _, args := range [][]string{
		{"deply", "missing"}, {"destroy"}, {"list", "unexpected"}, {"image", "remove"},
		{"environment", "unknown"}, {"access", "id", "server", "ftp"}, {"plan", "missing"},
		{"deploy", "missing", "--machine-concurrency", "-1"}, {"list", "--typo"},
	} {
		_, _, err := runCLI(t, append(args, "--provider", provider)...)
		if err == nil || strings.Contains(err.Error(), "provider") {
			t.Fatal(args, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("provider executed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("invalid invocation created state", err)
	}
	_, _, err := runCLI(t, "deply")
	if !strings.Contains(err.Error(), `Did you mean "deploy"`) {
		t.Fatal(err)
	}
}

func TestOptionsAnywhereAndLiteralArgumentsPreserveJSON(t *testing.T) {
	_, source := cliFixture(t)
	for _, args := range [][]string{
		{"validate", source, "--json"}, {"--json", "validate", source}, {"validate", "--json", "--", source},
	} {
		out, diagnostic, err := runCLI(t, args...)
		var result struct {
			Version int
			Result  struct{ Name string }
		}
		if err != nil || diagnostic != "" || json.Unmarshal([]byte(out), &result) != nil || result.Version != 2 || result.Result.Name == "" {
			t.Fatal(args, out, diagnostic, err)
		}
	}
	out, _, err := runCLI(t, "validate", source, "--provider", "--help")
	if err != nil || !strings.Contains(out, "Valid source:") {
		t.Fatal("flag value interpreted as help", out, err)
	}
	for _, args := range [][]string{{"validate", "--", "--json"}, {"list", "--state", "--json"}, {"list", "--json", "--json=false"}} {
		if JSONOutput(args) {
			t.Fatal("literal value switched JSON output", args)
		}
	}
}

func TestSavedDefaultsReloadOverrideAndDoNotAuthorizeMutations(t *testing.T) {
	root, source := cliFixture(t)
	out, _, err := runCLI(t, "config", "init")
	if err != nil || !strings.Contains(out, "Created CLI defaults") {
		t.Fatal(out, err)
	}
	path := defaultConfigPath()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config not private", info, err)
	}
	if _, _, err = runCLI(t, "config", "init"); err == nil {
		t.Fatal("overwrote config")
	}
	values := cliConfig{State: filepath.Join(root, "saved-state"), Environment: filepath.Join(root, "environment.yaml"), Provider: filepath.Join(root, "provider")}
	values.Engine.MaxMachineConcurrency = 1
	data, _ := json.Marshal(values)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, _, err = runCLI(t, "config", "show", "--json")
		if err != nil || !strings.Contains(out, values.State) || !strings.Contains(out, `"maxMachineConcurrency": 1`) {
			t.Fatal(out, err)
		}
	}
	out, _, err = runCLI(t, "config", "show", "--state", filepath.Join(root, "override"), "--machine-concurrency", "2", "--json")
	if err != nil || strings.Contains(out, values.State) || !strings.Contains(out, `"maxMachineConcurrency": 2`) {
		t.Fatal("flags did not override defaults", out, err)
	}
	out, _, err = runCLI(t, "list")
	if err != nil || out != "No instances.\n" {
		t.Fatal("offline query used provider", out, err)
	}
	if _, err = os.Stat(filepath.Join(values.State, "state.db")); err != nil {
		t.Fatal("saved state path ignored", err)
	}
	// Planning reads the saved environment instead of reporting a missing flag.
	_, _, err = runCLI(t, "plan", source)
	if err == nil || !strings.Contains(err.Error(), "environment.yaml") {
		t.Fatal(err)
	}
	data = []byte(`{"yes":true}`)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runCLI(t, "list")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal("saved config authorized mutations", err)
	}
}

func TestInvalidConfigAndExplicitMissingFileFailBeforeState(t *testing.T) {
	root, _ := cliFixture(t)
	path := filepath.Join(root, "defaults.json")
	for _, value := range []string{`null`, `{"state":"relative"}`, `{} {}`, `{"engine":{"maxCleanupConcurrency":33}}`, `{"unexpected":1}`, "{}" + strings.Repeat(" ", 65536)} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCLI(t, "list", "--config", path); err == nil {
			t.Fatal("invalid defaults accepted", value[:min(len(value), 100)])
		}
	}
	if _, _, err := runCLI(t, "list", "--config", filepath.Join(root, "missing")); err == nil {
		t.Fatal("explicit missing config ignored")
	}
	// Help remains usable when the default config is broken.
	t.Setenv("LABCENTRAL_CONFIG", path)
	if _, _, err := runCLI(t, "deploy", "--help"); err != nil {
		t.Fatal(err)
	}
}

func TestVersionNeedsNoProviderStateOrConfig(t *testing.T) {
	root, _ := cliFixture(t)
	old := Version
	Version = "v2.0.0-test"
	defer func() { Version = old }()
	broken := filepath.Join(root, "broken-config.json")
	if err := os.WriteFile(broken, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "version", "--config", broken, "--provider", filepath.Join(root, "missing"))
	if err != nil || out != "labcentral v2.0.0-test\n" {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := runCLI(t, "version", "extra"); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("version created state", err)
	}
	Version = ""
	if out, _, err := runCLI(t, "version"); err != nil || !strings.HasPrefix(out, "labcentral ") {
		t.Fatalf("%q %v", out, err)
	}
}
