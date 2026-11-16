//go:build !windows

// Package durablefile publishes already flushed temporary files on one filesystem.
package durablefile

import (
	"os"
	"path/filepath"
)

func Rename(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
