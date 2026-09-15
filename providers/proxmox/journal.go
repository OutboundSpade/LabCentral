package proxmox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maximumJournalBytes = 1 << 20
	journalLockName     = ".labcentral-mutation.lock"
	journalLockRetry    = 10 * time.Millisecond
)

var invocationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

type InvocationJournal struct {
	directory string
	mu        sync.Mutex
}

// InvocationLease retains kernel-owned compatibility and exclusive claim locks
// for the entire remote mutation. Legacy journal-wide leases remain exclusive.
type InvocationLease struct {
	journal *InvocationJournal
	file    *os.File
	locks   []*os.File
}

func OpenInvocationJournal(directory string) (*InvocationJournal, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return nil, fmt.Errorf("journal path is invalid")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect invocation journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("invocation journal is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("invocation journal directory mode must be 0700")
	}
	journal := &InvocationJournal{directory: directory}
	lock, err := journal.openLock()
	if err != nil {
		return nil, err
	}
	if err := lock.Close(); err != nil {
		return nil, fmt.Errorf("close invocation journal lock: %w", err)
	}
	if err := journal.selfTest(); err != nil {
		return nil, err
	}
	return journal, nil
}

func (journal *InvocationJournal) selfTest() error {
	file, err := os.CreateTemp(journal.directory, ".labcentral-journal-selftest-*")
	if err != nil {
		return fmt.Errorf("create invocation journal self-test: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("protect invocation journal self-test: %w", err)
	}
	if _, err := file.Write([]byte("ok")); err != nil {
		file.Close()
		return fmt.Errorf("write invocation journal self-test: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync invocation journal self-test: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close invocation journal self-test: %w", err)
	}
	first, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open invocation journal lock self-test: %w", err)
	}
	defer first.Close()
	second, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("reopen invocation journal lock self-test: %w", err)
	}
	defer second.Close()
	if err := unix.Flock(int(first.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("acquire invocation journal lock self-test: %w", err)
	}
	defer unix.Flock(int(first.Fd()), unix.LOCK_UN) //nolint:errcheck
	if err := unix.Flock(int(second.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		if err == nil {
			_ = unix.Flock(int(second.Fd()), unix.LOCK_UN)
		}
		return fmt.Errorf("invocation journal filesystem does not enforce advisory locks")
	}
	return nil
}

func (journal *InvocationJournal) Write(record JournalRecord) error {
	lease, err := journal.Acquire(context.Background())
	if err != nil {
		return err
	}
	writeErr := lease.Write(record)
	return errors.Join(writeErr, lease.Close())
}

// Acquire waits for the process-safe mutation lease or returns when ctx is
// canceled. Callers must hold the returned lease for the complete remote
// mutation, not only while reading or writing a checkpoint.
func (journal *InvocationJournal) Acquire(ctx context.Context) (*InvocationLease, error) {
	if ctx == nil {
		return nil, fmt.Errorf("invocation journal lock context is required")
	}
	file, err := journal.openLock()
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &InvocationLease{journal: journal, file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock invocation journal: %w", err)
		}
		timer := time.NewTimer(journalLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (journal *InvocationJournal) openLock() (*os.File, error) {
	return journal.openNamedLock(journalLockName)
}
func (journal *InvocationJournal) openNamedLock(name string) (*os.File, error) {
	path := filepath.Join(journal.directory, name)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open invocation journal lock: %w", err)
	}
	opened, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, pathInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("invocation journal lock type or mode is invalid")
	}
	return file, nil
}

func (lease *InvocationLease) Write(record JournalRecord) error {
	if lease == nil || lease.journal == nil || lease.file == nil {
		return fmt.Errorf("invocation journal lease is unavailable")
	}
	journal := lease.journal
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := validateJournalRecord(record); err != nil {
		return err
	}
	recordBytes, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode invocation journal record: %w", err)
	}
	envelopeBytes, err := json.Marshal(journalEnvelope{Record: record, Checksum: digestDocument(recordBytes)})
	if err != nil {
		return fmt.Errorf("encode invocation journal envelope: %w", err)
	}
	if len(envelopeBytes) > maximumJournalBytes {
		return fmt.Errorf("invocation journal record exceeds %d bytes", maximumJournalBytes)
	}
	temporary, err := os.CreateTemp(journal.directory, ".labcentral-journal-write-*")
	if err != nil {
		return fmt.Errorf("create invocation journal record: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect invocation journal record: %w", err)
	}
	if _, err := temporary.Write(envelopeBytes); err != nil {
		temporary.Close()
		return fmt.Errorf("write invocation journal record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync invocation journal record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close invocation journal record: %w", err)
	}
	path := journal.recordPath(record.InvocationID)
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("commit invocation journal record: %w", err)
	}
	removeTemporary = false
	directory, err := os.Open(journal.directory)
	if err != nil {
		return fmt.Errorf("open invocation journal directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync invocation journal directory: %w", err)
	}
	return nil
}

func (lease *InvocationLease) Read(invocationID string) (JournalRecord, error) {
	if lease == nil || lease.journal == nil || lease.file == nil {
		return JournalRecord{}, fmt.Errorf("invocation journal lease is unavailable")
	}
	return lease.journal.Read(invocationID)
}

func (lease *InvocationLease) recordPath(invocationID string) string {
	return lease.journal.recordPath(invocationID)
}

func (lease *InvocationLease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	for i := len(lease.locks) - 1; i >= 0; i-- {
		_ = unix.Flock(int(lease.locks[i].Fd()), unix.LOCK_UN)
		_ = lease.locks[i].Close()
	}
	lease.locks = nil
	file := lease.file
	lease.file = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock invocation journal: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close invocation journal lock: %w", closeErr)
	}
	return nil
}

func (journal *InvocationJournal) Read(invocationID string) (JournalRecord, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var zero JournalRecord
	if !invocationIDPattern.MatchString(invocationID) {
		return zero, fmt.Errorf("invocation ID is invalid")
	}
	path := journal.recordPath(invocationID)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return zero, os.ErrNotExist
		}
		return zero, fmt.Errorf("inspect invocation journal record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return zero, fmt.Errorf("invocation journal record type or mode is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, fmt.Errorf("open invocation journal record: %w", err)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumJournalBytes+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return zero, fmt.Errorf("read invocation journal record")
	}
	if len(contents) > maximumJournalBytes {
		return zero, fmt.Errorf("invocation journal record exceeds %d bytes", maximumJournalBytes)
	}
	var envelope journalEnvelope
	if err := decodeClosedJSON(contents, &envelope); err != nil {
		return zero, fmt.Errorf("decode invocation journal record: %w", err)
	}
	recordBytes, err := json.Marshal(envelope.Record)
	if err != nil || envelope.Checksum != digestDocument(recordBytes) {
		return zero, fmt.Errorf("invocation journal checksum mismatch")
	}
	if envelope.Record.InvocationID != invocationID {
		return zero, fmt.Errorf("invocation journal identity mismatch")
	}
	if err := validateJournalRecord(envelope.Record); err != nil {
		return zero, err
	}
	return envelope.Record, nil
}

func (journal *InvocationJournal) recordPath(invocationID string) string {
	return filepath.Join(journal.directory, strings.TrimPrefix(digestDocument([]byte(invocationID)), "sha256:")+".json")
}

func validateJournalRecord(record JournalRecord) error {
	if record.SchemaVersion != journalV10 {
		return fmt.Errorf("unsupported invocation journal version")
	}
	if !invocationIDPattern.MatchString(record.InvocationID) || record.TargetID == "" || !sha256Pattern.MatchString(record.RequestDigest) || !nodeNamePattern.MatchString(record.Node) || !sha256Pattern.MatchString(record.OwnershipDigest) {
		return fmt.Errorf("invocation journal binding is invalid")
	}
	switch record.Action {
	case "apply", "destroy", "start", "stop", "pause", "resume":
	default:
		return fmt.Errorf("invocation journal action is invalid")
	}
	if !validJournalPhase(record.Phase) || len(record.TaskUPID) > 512 || len(record.LastSafeObservation) > 4096 || strings.ContainsAny(record.LastSafeObservation, "\r\n") {
		return fmt.Errorf("invocation journal phase or observation is invalid")
	}
	if record.TaskUPID != "" {
		upid, err := ParseUPID(record.TaskUPID)
		if err != nil || record.VMID < 100 || validateJournalTask(record, upid) != nil {
			return fmt.Errorf("invocation journal task identity is invalid")
		}
	}
	if record.Result != nil {
		if record.Phase != "ready" && record.Phase != "destroyed" && record.Phase != "failed" && record.Phase != "partial" || record.Result.Observed == nil || record.Result.State != nil && record.Result.Error != nil || record.Result.State == nil && record.Result.Error == nil && !record.Result.Observed.Destroyed {
			return fmt.Errorf("invocation journal result is invalid")
		}
		if record.Result.State != nil {
			state := record.Result.State
			if err := state.validate(); err != nil || state.TargetID != record.TargetID || state.Node != record.Node || state.VMID != record.VMID {
				return fmt.Errorf("invocation journal state is invalid")
			}
			if state.Ownership != nil {
				marker, err := encodeOwnershipMarker(*state.Ownership)
				if err != nil || digestDocument([]byte(marker)) != record.OwnershipDigest {
					return fmt.Errorf("invocation journal ownership binding is invalid")
				}
			}
		}
		if err := record.Result.Observed.validate(); err != nil || record.Result.Observed.Node != record.Node || record.Result.Observed.VMID != record.VMID {
			return fmt.Errorf("invocation journal observation is invalid")
		}
		if failure := record.Result.Error; failure != nil {
			if !validField(failure.Code) || !validField(failure.Class) || !validField(failure.SafeMessage) {
				return fmt.Errorf("invocation journal failure is invalid")
			}
		}
	}
	return nil
}

func validateJournalTask(record JournalRecord, upid UPID) error {
	// PVE identifies qmclone tasks by the source template node and VMID, while
	// the journal's node and VMID deliberately bind the destination resource.
	// The record has no duplicate source fields: apply replay validates this
	// parsed identity against the exact source in the persisted provider plan
	// before polling it. Every other task remains destination-bound here.
	if record.Action == "apply" && upid.Type == "qmclone" {
		return upid.validate(upid.Node, "qmclone")
	}
	return upid.validateObject(record.Node, record.VMID, journalTaskTypes(record.Action)...)
}

func journalTaskTypes(action string) []string {
	switch action {
	case "apply":
		return []string{"qmclone", "qmconfig", "qmstart"}
	case "destroy":
		return []string{"qmresume", "qmshutdown", "qmdestroy"}
	case "start":
		return []string{"qmstart"}
	case "stop":
		return []string{"qmshutdown"}
	case "pause":
		return []string{"qmpause"}
	case "resume":
		return []string{"qmresume"}
	default:
		return nil
	}
}

func validJournalPhase(phase string) bool {
	switch phase {
	case "prepared", "clone-submitted", "clone-complete", "config-submitted", "configured", "lifecycle-submitted", "ready", "destroy-resume-submitted", "destroy-stop-submitted", "destroy-submitted", "destroyed", "failed", "partial":
		return true
	default:
		return false
	}
}

func decodeClosedJSON(contents []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON document must contain exactly one value")
	}
	return nil
}

// AcquireClaims permits independent work while protecting duplicate invocation
// IDs and actual provider allocations across processes. The shared compatibility
// lock excludes older providers and journal-wide maintenance.
func (journal *InvocationJournal) AcquireClaims(ctx context.Context, invocation string, claims ...string) (*InvocationLease, error) {
	if ctx == nil || !invocationIDPattern.MatchString(invocation) || len(claims) == 0 || len(claims) > 32 {
		return nil, fmt.Errorf("invalid invocation claim locks")
	}
	file, err := journal.openLock()
	if err != nil {
		return nil, err
	}
	lease := &InvocationLease{journal: journal, file: file}
	if err = waitJournalLock(ctx, file, unix.LOCK_SH); err != nil {
		lease.Close()
		return nil, err
	}
	keys := append([]string{"invocation:" + invocation}, claims...)
	sort.Strings(keys)
	previous := ""
	for _, key := range keys {
		if key == previous {
			continue
		}
		previous = key
		if key == "" || len(key) > 512 {
			lease.Close()
			return nil, fmt.Errorf("invalid claim lock identity")
		}
		digest := sha256.Sum256([]byte(key))
		lock, err := journal.openNamedLock(fmt.Sprintf(".labcentral-claim-%x.lock", digest))
		if err != nil {
			lease.Close()
			return nil, err
		}
		lease.locks = append(lease.locks, lock)
		if err = waitJournalLock(ctx, lock, unix.LOCK_EX); err != nil {
			lease.Close()
			return nil, err
		}
	}
	return lease, nil
}
func waitJournalLock(ctx context.Context, file *os.File, mode int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		timer := time.NewTimer(journalLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
