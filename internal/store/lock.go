package store

import (
	"context"
	"errors"
	"os"
)

// errLockHeld reports a lock currently held by another process or handle.
var errLockHeld = errors.New("lock is held")

// LockDirectoryContext opens only a file lock in an explicitly supplied private
// directory. Provider fences use this without opening any SQLite database.
func LockDirectoryContext(ctx context.Context, root, key string, shared bool) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	s := &Store{Root: root}
	return s.lockKeyContext(ctx, key, shared)
}
