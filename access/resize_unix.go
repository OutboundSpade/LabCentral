//go:build unix

package access

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize delivers terminal size changes so the remote PTY can follow them.
func notifyResize(c chan<- os.Signal) { signal.Notify(c, syscall.SIGWINCH) }
