// Command lab-template packs and inspects portable source; it never executes a
// recipe or accesses a provider, registry or repository.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"go.yaml.in/yaml/v4"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lab-template pack|lock|inspect [options]")
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	root := flags.String("root", ".", "source root; only explicitly listed files are read")
	manifestPath := flags.String("manifest", "bundle.yaml", "manifest path relative to root")
	output := flags.String("output", "", "archive path (pack)")
	archive := flags.String("archive", "", "archive path (inspect)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if command == "inspect" {
		data, err := readBounded(*archive)
		if err != nil {
			return err
		}
		bundle, err := labbundle.Read(data)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(bundle.Manifest)
	}
	if command != "lock" && command != "pack" {
		return fmt.Errorf("unknown command")
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if !labbundle.ValidPath(*manifestPath) {
		return fmt.Errorf("invalid manifest path")
	}
	manifestBytes, err := readSource(absolute, *manifestPath)
	if err != nil {
		return err
	}
	var m labbundle.Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.KnownFields(true)
	if err = decoder.Decode(&m); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing manifest document")
	}
	if len(m.Files) == 0 || len(m.Files) >= labbundle.MaxFiles {
		return fmt.Errorf("manifest must explicitly list source files")
	}
	files := map[string][]byte{}
	total := 0
	for _, file := range m.Files {
		if _, ok := files[file.Path]; ok {
			return fmt.Errorf("duplicate source path")
		}
		body, err := readSource(absolute, file.Path)
		if err != nil {
			return err
		}
		total += len(body)
		if total > labbundle.MaxBytes {
			return fmt.Errorf("source exceeds package limit")
		}
		files[file.Path] = body
	}
	if command == "lock" {
		m, err = labbundle.LockSources(m, files)
		if err != nil {
			return err
		}
		data, err := yaml.Marshal(m)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(absolute, filepath.FromSlash(*manifestPath)), data, 0600)
	}
	if *output == "" {
		return fmt.Errorf("--output is required")
	}
	data, err := labbundle.Pack(m, files)
	if err != nil {
		return err
	}
	// Never overwrite an existing file, including a source or symlink.
	out, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := out.Write(data)
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
func readSource(root, name string) ([]byte, error) {
	if !labbundle.ValidPath(name) {
		return nil, fmt.Errorf("unsafe source path")
	}
	current := root
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source symlinks are forbidden")
		}
	}
	return readBounded(current)
}
func readBounded(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > labbundle.MaxBytes {
		return nil, fmt.Errorf("expected a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, labbundle.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > labbundle.MaxBytes {
		return nil, fmt.Errorf("file exceeds limit")
	}
	return data, nil
}
