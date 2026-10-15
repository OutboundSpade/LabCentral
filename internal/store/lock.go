package store

import "errors"

// errLockHeld reports a lock currently held by another process or handle.
var errLockHeld = errors.New("lock is held")
