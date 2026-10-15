//go:build !linux

package plugin

import "os/exec"

func stopWithParent(*exec.Cmd) {}
