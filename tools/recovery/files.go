package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maximumRecoveryManifestBytes = 64 << 10
	maximumRecoveryFiles         = 1_000_000
	maximumRecoveryPathBytes     = 4096
	maximumDatabaseDumpBytes     = int64(256 << 30)
	maximumStateArchiveBytes     = int64(2 << 40)
)

func privateExistingDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("private directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) {
		return "", fmt.Errorf("private directory must resolve to a non-root absolute path")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect private directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("private directory must be a non-symlink directory with no group/other permissions")
	}
	return absolute, nil
}

func checkedStateRoot(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("state root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) {
		return "", fmt.Errorf("state root must resolve to a non-root absolute path")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect state root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("state root must be a non-symlink directory")
	}
	return absolute, nil
}

func checkedNewPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("output path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) {
		return "", fmt.Errorf("output must resolve to a non-root absolute path")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("refusing to overwrite %s", absolute)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect output path: %w", err)
	}
	parentInfo, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("output parent must be an existing non-symlink directory with no group/other write permissions")
	}
	return absolute, nil
}

func readProtectedFile(path string, maximum int64, sensitive bool) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("file path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("file must be a bounded non-symlink regular file")
	}
	if sensitive && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("sensitive file must have no group/other permissions")
	}
	if !sensitive && info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("public input file must not be group/other writable")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while it was being opened")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) != info.Size() {
		return nil, fmt.Errorf("read bounded protected file")
	}
	return contents, nil
}

func writeExclusiveFile(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := writeAll(file, contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func strictJSON(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func writeAll(writer io.Writer, contents []byte) error {
	for len(contents) > 0 {
		written, err := writer.Write(contents)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(contents) {
			return io.ErrShortWrite
		}
		contents = contents[written:]
	}
	return nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveStateRoot(root, destination string) error {
	root, err := checkedStateRoot(root)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	writer := tar.NewWriter(output)
	count := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > maximumRecoveryFiles {
			return fmt.Errorf("state root exceeds the file-count limit")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == "" {
			name = "."
		}
		if len(name) > maximumRecoveryPathBytes {
			return fmt.Errorf("state path exceeds the length limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("state path %q is not a regular file or directory", name)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name, header.Uname, header.Gname = name, "", ""
		header.AccessTime, header.ChangeTime, header.PAXRecords, header.Xattrs = time.Time{}, time.Time{}, nil, nil
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return fmt.Errorf("state path %q changed while it was being opened", name)
		}
		_, copyErr := io.CopyN(writer, file, info.Size())
		var extra [1]byte
		if copyErr == nil {
			if extraCount, extraErr := file.Read(extra[:]); extraCount != 0 || !errors.Is(extraErr, io.EOF) {
				copyErr = fmt.Errorf("state path changed while it was being read")
			}
		}
		finalInfo, finalStatErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if finalStatErr != nil || finalInfo.Size() != info.Size() || !finalInfo.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("state path %q changed while it was being read", name)
		}
		return closeErr
	})
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("archive state root: %w", err)
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

type restoredDirectory struct {
	path    string
	mode    os.FileMode
	uid     int
	gid     int
	modTime time.Time
}

func stageStateArchive(archivePath, destination string, preserveOwnership bool) (string, error) {
	destination, err := checkedNewPath(destination)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".labcentral-restore-")
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(stage)
		}
	}()
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := tar.NewReader(io.LimitReader(file, maximumStateArchiveBytes+1))
	count := 0
	rootSeen := false
	var directories []restoredDirectory
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read state archive: %w", err)
		}
		count++
		if count > maximumRecoveryFiles || len(header.Name) == 0 || len(header.Name) > maximumRecoveryPathBytes {
			return "", fmt.Errorf("state archive path/count limit exceeded")
		}
		if header.Uid < 0 || header.Gid < 0 || header.Uid > 1<<31-1 || header.Gid > 1<<31-1 || header.Mode < 0 || header.Mode&^0o777 != 0 {
			return "", fmt.Errorf("state archive contains invalid ownership or mode metadata")
		}
		clean := filepath.ToSlash(filepath.Clean(header.Name))
		if clean == "." {
			if count != 1 || header.Typeflag != tar.TypeDir {
				return "", fmt.Errorf("state archive root entry is invalid")
			}
			rootSeen = true
			directories = append(directories, restoredDirectory{stage, os.FileMode(header.Mode).Perm(), header.Uid, header.Gid, header.ModTime})
			continue
		}
		if strings.HasPrefix(clean, "../") || filepath.IsAbs(header.Name) || clean != header.Name {
			return "", fmt.Errorf("state archive contains unsafe path %q", header.Name)
		}
		target := filepath.Join(stage, filepath.FromSlash(clean))
		if !strings.HasPrefix(target, stage+string(filepath.Separator)) {
			return "", fmt.Errorf("state archive path escapes the destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(target, 0o700); err != nil {
				return "", err
			}
			directories = append(directories, restoredDirectory{target, os.FileMode(header.Mode).Perm(), header.Uid, header.Gid, header.ModTime})
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maximumDatabaseDumpBytes {
				return "", fmt.Errorf("state file %q exceeds the size limit", header.Name)
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return "", err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			if copyErr == nil {
				copyErr = output.Sync()
			}
			closeErr := output.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			if preserveOwnership {
				if err := os.Chown(target, header.Uid, header.Gid); err != nil {
					return "", err
				}
			}
			if err := os.Chmod(target, os.FileMode(header.Mode).Perm()); err != nil {
				return "", err
			}
			if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("state archive contains unsupported entry type")
		}
	}
	if !rootSeen {
		return "", fmt.Errorf("state archive has no root entry")
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index].path); err != nil {
			return "", err
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if preserveOwnership {
			if err := os.Chown(directory.path, directory.uid, directory.gid); err != nil {
				return "", err
			}
		}
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return "", err
		}
		if err := os.Chtimes(directory.path, directory.modTime, directory.modTime); err != nil {
			return "", err
		}
	}
	remove = false
	return stage, nil
}

func commitStateArchive(stage, destination string) (bool, error) {
	destination, err := checkedNewPath(destination)
	if err != nil {
		return false, err
	}
	if stage == "" || filepath.Dir(stage) != filepath.Dir(destination) {
		return false, fmt.Errorf("staged state must be on the destination filesystem")
	}
	if err := os.Rename(stage, destination); err != nil {
		return false, err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return true, err
	}
	return true, nil
}

func rollbackStateArchive(destination, stage string) error {
	if destination == "" || stage == "" || filepath.Dir(stage) != filepath.Dir(destination) {
		return fmt.Errorf("state rollback paths are invalid")
	}
	if err := os.Rename(destination, stage); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func restoreStateArchive(archivePath, destination string, preserveOwnership bool) error {
	stage, err := stageStateArchive(archivePath, destination, preserveOwnership)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	committed, err := commitStateArchive(stage, destination)
	if err != nil && committed {
		if rollbackErr := rollbackStateArchive(destination, stage); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("state rollback failed: %w", rollbackErr))
		}
	}
	return err
}
