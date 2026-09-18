package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitSourcePackRejectsSymlinksAndOverwrite(t *testing.T) {
	root := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("lab.yaml", "kind: Lab\n")
	write("bundle.yaml", `apiVersion: labcentral.bundle/v1beta1
kind: LabBundle
entrypoint: lab.yaml
package: {apiVersion: labcentral.source-package/v1}
files: [{path: lab.yaml}]
`)
	// Unlisted files are never packaged.
	write("private.env", "secret-token")
	if err := run([]string{"lock", "--root", root}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "export.lab.tar.gz")
	if err := run([]string{"pack", "--root", root, "--output", output}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pack", "--root", root, "--output", output}); err == nil {
		t.Fatal("overwrote existing archive")
	}
	if err := os.Remove(filepath.Join(root, "lab.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private.env"), filepath.Join(root, "lab.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pack", "--root", root, "--output", output + ".other"}); err == nil {
		t.Fatal("read a source symlink")
	}
}
