//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an advisory lock without waiting; errLockHeld means another
// holder has it.
func tryLockFile(f *os.File, shared bool) error {
	mode := syscall.LOCK_EX
	if shared {
		mode = syscall.LOCK_SH
	}
	if e := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); e != nil {
		if errors.Is(e, syscall.EWOULDBLOCK) {
			return errLockHeld
		}
		return e
	}
	return nil
}

func unlockFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
