// Package privatefile writes credentials without exposing partially written keys.
package privatefile

import (
	"github.com/OutboundSpade/LabCentral/v2/internal/durablefile"
	"os"
	"path/filepath"
)

func Write(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".key-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err == nil {
		err = closed
	}
	if err != nil {
		return err
	}
	return durablefile.Rename(f.Name(), path)
}
