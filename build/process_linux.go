package build

import "syscall"

func parentDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }
