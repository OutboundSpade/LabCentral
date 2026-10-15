//go:build unix && !linux

package build

import "syscall"

func parentDeathSignal(*syscall.SysProcAttr) {}
