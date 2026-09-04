//go:build windows

package main

import (
	"fmt"
	"os"
)

func requireNativeRecoveryPlatform() error {
	return fmt.Errorf("native SQLite recovery is supported only on the systemd/POSIX deployment")
}

func lockNativeDatabaseFile(_ *os.File) error { return requireNativeRecoveryPlatform() }

func unlockNativeDatabaseFile(_ *os.File) error { return nil }
