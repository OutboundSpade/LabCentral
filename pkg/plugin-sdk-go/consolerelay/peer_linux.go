//go:build linux

package consolerelay

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func lockDirectory(directory string) (*os.File, error) {
	fd, err := syscall.Open(filepath.Join(directory, ".provider.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "console-relay-lock")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedBy(info, uint32(os.Getuid())) || info.Mode().Perm()&0o077 != 0 || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		_ = f.Close()
		return nil, fmt.Errorf("console relay lock rejected")
	}
	return f, nil
}

func ownedBy(info os.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}

func verifyPeer(conn *net.UnixConn, uid uint32) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("console relay peer unavailable")
	}
	valid := false
	if raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		valid = err == nil && cred.Uid == uid
	}) != nil || !valid {
		return fmt.Errorf("console relay peer rejected")
	}
	return nil
}
