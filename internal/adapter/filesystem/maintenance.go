package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/OutboundSpade/LabCentral/internal/domain"
	"golang.org/x/sys/unix"
)

// Maintenance owns the artifact root's exclusive maintenance lease. It can be
// opened only after every Store using the same root has closed.
type Maintenance struct {
	root *os.Root
	lock *os.File
}

// GarbageCollectionResult describes a complete reference-aware artifact scan.
// A dry run reports reclaimable objects without changing the store.
type GarbageCollectionResult struct {
	Applied             bool
	Objects             uint64
	ReferencedObjects   uint64
	UnreferencedObjects uint64
	StagingFiles        uint64
	BytesReclaimable    uint64
	ObjectsDeleted      uint64
	StagingFilesDeleted uint64
}

type artifactObject struct {
	name       string
	prefix     string
	digest     string
	size       uint64
	referenced bool
}

// OpenMaintenance takes an exclusive non-blocking lease on an existing
// artifact root. Supported labd processes hold a shared lease for their entire
// lifetime, so maintenance fails closed while any replica is using the root.
func OpenMaintenance(path string) (*Maintenance, error) {
	abs, err := filepath.Abs(path)
	if err != nil || abs == string(filepath.Separator) {
		return nil, fmt.Errorf("artifact maintenance root must be a non-root absolute path")
	}
	if err := rejectSymlink(abs); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("artifact root mode must be 0700")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open artifact maintenance root: %w", err)
	}
	lock, err := acquireMaintenanceLock(root, unix.LOCK_EX, "maintenance")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	maintenance := &Maintenance{root: root, lock: lock}
	for _, name := range []string{stagingDirectory, objectsDirectory} {
		entry, statErr := root.Lstat(name)
		if statErr != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 || entry.Mode().Perm() != 0o700 {
			_ = maintenance.Close()
			return nil, fmt.Errorf("artifact store directory %q is unavailable or unsafe", name)
		}
	}
	return maintenance, nil
}

func (m *Maintenance) Close() error {
	if m == nil {
		return nil
	}
	lockErr := releaseMaintenanceLock(m.lock)
	m.lock = nil
	var rootErr error
	if m.root != nil {
		rootErr = m.root.Close()
		m.root = nil
	}
	return errors.Join(lockErr, rootErr)
}

// Collect verifies the complete committed-object and staging inventories
// before deleting anything. Every supplied live reference must exist with its
// expected digest. Unknown filesystem entries, unsafe modes, links, or corrupt
// objects stop the run without deletion.
func (m *Maintenance) Collect(ctx context.Context, liveReferences []domain.ArtifactRef, apply bool) (GarbageCollectionResult, error) {
	if err := ctx.Err(); err != nil {
		return GarbageCollectionResult{}, err
	}
	if m == nil || m.root == nil || m.lock == nil {
		return GarbageCollectionResult{}, fmt.Errorf("artifact maintenance lease is unavailable")
	}
	live := make(map[string]struct{}, len(liveReferences))
	for _, reference := range liveReferences {
		digest, err := digestFromReference(reference)
		if err != nil {
			return GarbageCollectionResult{}, fmt.Errorf("invalid live artifact reference: %w", err)
		}
		live[digest.Hex()] = struct{}{}
	}
	objects, found, err := m.inventoryObjects(ctx, live)
	if err != nil {
		return GarbageCollectionResult{}, err
	}
	for digest := range live {
		if !found[digest] {
			return GarbageCollectionResult{}, fmt.Errorf("live artifact sha256:%s is missing", digest)
		}
	}
	staging, err := m.inventoryStaging(ctx)
	if err != nil {
		return GarbageCollectionResult{}, err
	}
	result := GarbageCollectionResult{Applied: apply, Objects: uint64(len(objects)), StagingFiles: uint64(len(staging))}
	for _, object := range objects {
		if object.referenced {
			result.ReferencedObjects++
			continue
		}
		result.UnreferencedObjects++
		result.BytesReclaimable += object.size
	}
	if !apply {
		return result, nil
	}
	changedPrefixes := make(map[string]bool)
	retainedPrefixes := make(map[string]bool)
	for _, object := range objects {
		if object.referenced {
			retainedPrefixes[object.prefix] = true
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := m.root.Remove(object.name); err != nil {
			return result, fmt.Errorf("delete unreferenced artifact: %w", err)
		}
		changedPrefixes[object.prefix] = true
		result.ObjectsDeleted++
	}
	for prefix := range changedPrefixes {
		if err := m.syncDirectory(objectsDirectory + "/" + prefix); err != nil {
			return result, err
		}
		if !retainedPrefixes[prefix] {
			if err := m.root.Remove(objectsDirectory + "/" + prefix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return result, fmt.Errorf("remove empty artifact prefix: %w", err)
			}
		}
	}
	if len(changedPrefixes) != 0 {
		if err := m.syncDirectory(objectsDirectory); err != nil {
			return result, err
		}
	}
	for _, name := range staging {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := m.root.Remove(name); err != nil {
			return result, fmt.Errorf("delete abandoned staged artifact: %w", err)
		}
		result.StagingFilesDeleted++
	}
	if len(staging) != 0 {
		if err := m.syncDirectory(stagingDirectory); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *Maintenance) inventoryObjects(ctx context.Context, live map[string]struct{}) ([]artifactObject, map[string]bool, error) {
	directory, err := m.root.Open(objectsDirectory)
	if err != nil {
		return nil, nil, fmt.Errorf("open artifact object inventory: %w", err)
	}
	prefixes, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, nil, fmt.Errorf("read artifact object inventory")
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].Name() < prefixes[j].Name() })
	objects := make([]artifactObject, 0)
	found := make(map[string]bool)
	store := &Store{root: m.root}
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		prefixName := prefix.Name()
		prefixPath := objectsDirectory + "/" + prefixName
		info, err := m.root.Lstat(prefixPath)
		if err != nil || !validLowerHex(prefixName, 2) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return nil, nil, fmt.Errorf("artifact object prefix %q is invalid", prefixName)
		}
		prefixDirectory, err := m.root.Open(prefixPath)
		if err != nil {
			return nil, nil, fmt.Errorf("open artifact prefix %q: %w", prefixName, err)
		}
		entries, readErr := prefixDirectory.ReadDir(-1)
		closeErr := prefixDirectory.Close()
		if readErr != nil || closeErr != nil {
			return nil, nil, fmt.Errorf("read artifact prefix %q", prefixName)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			digest := entry.Name()
			name := prefixPath + "/" + digest
			info, err := m.root.Lstat(name)
			if err != nil || !validLowerHex(digest, 64) || !strings.HasPrefix(digest, prefixName) || !safeMaintenanceFile(info, 0o400) {
				return nil, nil, fmt.Errorf("artifact object %q is invalid", name)
			}
			measured, err := store.measure(name)
			if err != nil || measured.Digest.Hex() != digest || measured.Size != uint64(info.Size()) {
				return nil, nil, fmt.Errorf("artifact object sha256:%s failed integrity verification", digest)
			}
			_, referenced := live[digest]
			objects = append(objects, artifactObject{name: name, prefix: prefixName, digest: digest, size: measured.Size, referenced: referenced})
			found[digest] = true
		}
	}
	return objects, found, nil
}

func (m *Maintenance) inventoryStaging(ctx context.Context) ([]string, error) {
	directory, err := m.root.Open(stagingDirectory)
	if err != nil {
		return nil, fmt.Errorf("open artifact staging inventory: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read artifact staging inventory")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := entry.Name()
		valid := false
		for _, suffix := range []string{".data", ".json", ".lock"} {
			if strings.HasSuffix(base, suffix) {
				valid = validUploadName(strings.TrimSuffix(base, suffix))
				break
			}
		}
		if strings.HasPrefix(base, ".shared-lock-selftest-") {
			valid = validUploadName(strings.TrimPrefix(base, ".shared-lock-selftest-"))
		}
		name := stagingDirectory + "/" + base
		info, err := m.root.Lstat(name)
		if err != nil || !valid || !safeMaintenanceFileEitherMode(info, 0o600, 0o400) {
			return nil, fmt.Errorf("artifact staging entry %q is invalid", base)
		}
		result = append(result, name)
	}
	return result, nil
}

func (m *Maintenance) syncDirectory(name string) error {
	directory, err := m.root.Open(name)
	if err != nil {
		return fmt.Errorf("open artifact maintenance directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("sync artifact maintenance directory")
	}
	return nil
}

func acquireMaintenanceLock(root *os.Root, mode int, owner string) (*os.File, error) {
	var lock *os.File
	for attempts := 0; attempts < 8 && lock == nil; attempts++ {
		expected, err := root.Lstat(maintenanceLockName)
		if errors.Is(err, fs.ErrNotExist) {
			created, createErr := root.OpenFile(maintenanceLockName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(createErr, fs.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, fmt.Errorf("create artifact maintenance lock: %w", createErr)
			}
			lock = created
			break
		}
		if err != nil || !safeMaintenanceFile(expected, 0o600) {
			return nil, fmt.Errorf("artifact maintenance lock is unsafe")
		}
		opened, openErr := root.OpenFile(maintenanceLockName, os.O_RDWR, 0)
		if openErr != nil {
			return nil, fmt.Errorf("open artifact maintenance lock: %w", openErr)
		}
		actual, statErr := opened.Stat()
		if statErr != nil || !safeMaintenanceFile(actual, 0o600) || !os.SameFile(expected, actual) {
			_ = opened.Close()
			return nil, fmt.Errorf("artifact maintenance lock changed while it was being opened")
		}
		lock = opened
	}
	if lock == nil {
		return nil, fmt.Errorf("artifact maintenance lock could not be opened safely")
	}
	info, err := lock.Stat()
	if err != nil || !safeMaintenanceFile(info, 0o600) {
		_ = lock.Close()
		return nil, fmt.Errorf("artifact maintenance lock is unsafe")
	}
	if err := unix.Flock(int(lock.Fd()), mode|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("artifact store is in use; %s lease refused", owner)
		}
		return nil, fmt.Errorf("acquire artifact %s lease: %w", owner, err)
	}
	current, err := root.Lstat(maintenanceLockName)
	if err != nil || !safeMaintenanceFile(current, 0o600) || !os.SameFile(info, current) {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
		return nil, fmt.Errorf("artifact maintenance lock changed during lease acquisition")
	}
	return lock, nil
}

func releaseMaintenanceLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	closeErr := lock.Close()
	return errors.Join(unlockErr, closeErr)
}

func validUploadName(value string) bool {
	return strings.HasPrefix(value, "upload-") && validLowerHex(strings.TrimPrefix(value, "upload-"), 32)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func safeMaintenanceFile(info os.FileInfo, mode os.FileMode) bool {
	return safeMaintenanceFileEitherMode(info, mode)
}

func safeMaintenanceFileEitherMode(info os.FileInfo, modes ...os.FileMode) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 {
		return false
	}
	modeAllowed := false
	for _, mode := range modes {
		modeAllowed = modeAllowed || info.Mode().Perm() == mode
	}
	if !modeAllowed {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
