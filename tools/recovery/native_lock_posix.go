//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func requireNativeRecoveryPlatform() error { return nil }

func lockNativeDatabaseFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockNativeDatabaseFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
