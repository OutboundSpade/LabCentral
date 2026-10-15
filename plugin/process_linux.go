package plugin

import (
	"os/exec"
	"syscall"
)

// stopWithParent stops the provider plugin if this process dies.
func stopWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
