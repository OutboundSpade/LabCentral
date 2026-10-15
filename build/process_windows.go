//go:build windows

package build

import "os/exec"

// Native image builds are qualified only on Linux operator hosts; on Windows the
// process is stopped directly.
func stopWithGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
