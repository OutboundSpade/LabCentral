//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a byte-range lock on the whole file without waiting;
// errLockHeld means another holder has it.
func tryLockFile(f *os.File, shared bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var o windows.Overlapped
	if e := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &o); e != nil {
		if errors.Is(e, windows.ERROR_LOCK_VIOLATION) {
			return errLockHeld
		}
		return e
	}
	return nil
}

func unlockFile(f *os.File) {
	var o windows.Overlapped
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
}
