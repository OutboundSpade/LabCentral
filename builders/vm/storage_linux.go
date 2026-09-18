package vm

import (
	"fmt"
	"golang.org/x/sys/unix"
)

// ValidatePersistentStorage rejects memory-backed worker disks: their dirty
// pages consume the QEMU cgroup's memory budget and cannot survive a reboot.
// Operators must additionally provide persistent, capacity-managed storage.
func ValidatePersistentStorage(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return fmt.Errorf("inspect worker storage: %w", err)
	}
	if stat.Type == unix.TMPFS_MAGIC || stat.Type == unix.RAMFS_MAGIC {
		return fmt.Errorf("worker state requires persistent disk storage; memory-backed filesystems are unsupported")
	}
	return nil
}
