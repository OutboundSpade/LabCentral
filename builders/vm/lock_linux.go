package vm

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func (s *Service) lockState() error {
	f, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("builder state already has an owner")
	}
	s.lock = f
	return nil
}
