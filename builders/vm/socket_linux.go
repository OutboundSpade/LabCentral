package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func guestSocketPath(spec RunSpec) string {
	digest := sha256.Sum256([]byte(spec.Directory))
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "labcentral-builder", hex.EncodeToString(digest[:16])+".sock")
}

func prepareGuestSocket(spec RunSpec) error {
	dir := filepath.Dir(guestSocketPath(spec))
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("private user runtime directory required")
		}
		var st unix.Stat_t
		if unix.Lstat(path, &st) != nil || int(st.Uid) != os.Getuid() {
			return fmt.Errorf("worker runtime directory has wrong owner")
		}
	}
	return nil
}
