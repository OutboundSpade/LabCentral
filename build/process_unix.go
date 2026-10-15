//go:build unix

package build

import (
	"os/exec"
	"syscall"
)

// stopWithGroup runs Packer in its own process group so cancellation also stops
// its plugins, and (on Linux) stops it if this process dies.
func stopWithGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	parentDeathSignal(cmd.SysProcAttr)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
