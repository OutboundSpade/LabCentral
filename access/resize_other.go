//go:build !unix

package access

import "os"

// Windows consoles have no resize signal; the initial size is kept.
func notifyResize(chan<- os.Signal) {}
