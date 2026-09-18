package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type PinnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Profile struct {
	MemoryOverheadBytes uint64     `json:"memoryOverheadBytes"`
	MaximumMemoryBytes  uint64     `json:"maximumMemoryBytes"`
	MaximumStorageBytes uint64     `json:"maximumStorageBytes"`
	Version             uint32     `json:"version"`
	QEMU                PinnedFile `json:"qemu"`
	QEMUImage           PinnedFile `json:"qemuImage"`
	GuestImage          PinnedFile `json:"guestImage"`
	GuestDiskBytes      uint64     `json:"guestDiskBytes"`
	MaximumParallel     uint32     `json:"maximumParallel"`
	AllowedEgress       []string   `json:"allowedEgress"`
	EgressBytes         uint64     `json:"egressBytes"`
}

func LoadProfile(path string) (Profile, string, error) {
	var p Profile
	data, err := os.ReadFile(path)
	if err != nil {
		return p, "", err
	}
	if len(data) > 64<<10 {
		return p, "", fmt.Errorf("profile too large")
	}
	if err = strictJSON(data, &p); err != nil {
		return p, "", err
	}
	if p.MemoryOverheadBytes < 256<<20 || p.MemoryOverheadBytes > 4<<30 || p.MaximumMemoryBytes < p.MemoryOverheadBytes+(512<<20) || p.MaximumMemoryBytes > 256<<30 || p.MaximumStorageBytes < p.GuestDiskBytes || p.MaximumStorageBytes > 1<<40 || p.Version != 1 || p.GuestDiskBytes < 8<<30 || p.GuestDiskBytes > 128<<30 || p.MaximumParallel == 0 || p.MaximumParallel > 4 || p.EgressBytes == 0 || p.EgressBytes > 128<<30 {
		return p, "", fmt.Errorf("invalid worker profile bounds")
	}
	for _, file := range []PinnedFile{p.QEMU, p.QEMUImage, p.GuestImage} {
		if err = verifyPinned(file); err != nil {
			return p, "", err
		}
	}
	proxy, err := NewEgressProxy(p.AllowedEgress, int64(p.EgressBytes))
	if err != nil {
		return p, "", err
	}
	proxy.Close()
	canonical, _ := json.Marshal(p)
	sum := sha256.Sum256(canonical)
	return p, hex.EncodeToString(sum[:]), nil
}
func verifyPinned(pin PinnedFile) error {
	if !filepath.IsAbs(pin.Path) || filepath.Clean(pin.Path) != pin.Path || strings.ContainsAny(pin.Path, "\n\r,\x00") || !digestValid(pin.SHA256) {
		return fmt.Errorf("invalid pinned file")
	}
	info, err := os.Lstat(pin.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("pinned input must be a protected regular file")
	}
	f, err := os.Open(pin.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return fmt.Errorf("toolchain input digest mismatch")
	}
	return nil
}
func durableJSON(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".journal-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp, filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
