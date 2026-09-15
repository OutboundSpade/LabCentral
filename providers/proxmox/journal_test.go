package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvocationJournalSurvivesRestartWithProtectedChecksummedRecords(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	record := JournalRecord{
		SchemaVersion: journalV10, InvocationID: "invocation.test", TargetID: "target.test", Action: "apply",
		RequestDigest: sha("a"), Node: "pve-a", VMID: 101, OwnershipDigest: sha("b"), Phase: "prepared",
		LastSafeObservation: "destination absent",
	}
	if err := journal.Write(record); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Read(record.InvocationID)
	if err != nil || got != record {
		t.Fatalf("record = %#v, err = %v", got, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("entry %s mode = %v", entry.Name(), info.Mode())
		}
	}
}

func TestInvocationJournalLeaseSerializesIndependentHandlesAndHonorsCancellation(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := first.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blockedContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.Acquire(blockedContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second journal acquired a held process-safe lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	retryContext, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	retried, err := second.Acquire(retryContext)
	if err != nil {
		t.Fatalf("second journal did not acquire the released lease: %v", err)
	}
	if err := retried.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInvocationJournalLeaseSerializesProviderProcesses(t *testing.T) {
	const directoryEnvironment = "LABCENTRAL_TEST_PROXMOX_JOURNAL_DIRECTORY"
	const expectedEnvironment = "LABCENTRAL_TEST_PROXMOX_JOURNAL_EXPECTED"
	if directory := os.Getenv(directoryEnvironment); directory != "" {
		journal, err := OpenInvocationJournal(directory)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		lease, err := journal.Acquire(ctx)
		switch os.Getenv(expectedEnvironment) {
		case "blocked":
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("child acquired parent-held lease: %v", err)
			}
		case "available":
			if err != nil {
				t.Fatalf("child did not acquire released lease: %v", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("child lock expectation is invalid")
		}
		return
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := journal.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runJournalChild := func(expected string) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestInvocationJournalLeaseSerializesProviderProcesses$")
		command.Env = append(os.Environ(), directoryEnvironment+"="+directory, expectedEnvironment+"="+expected)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("journal child %s failed: %v\n%s", expected, err, output)
		}
	}
	runJournalChild("blocked")
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	runJournalChild("available")
}

func TestInvocationJournalRejectsCorruptionUnknownVersionAndUnsafeModes(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	record := JournalRecord{SchemaVersion: journalV10, InvocationID: "invocation.test", TargetID: "target.test", Action: "apply", RequestDigest: sha("a"), Node: "pve-a", VMID: 101, OwnershipDigest: sha("b"), Phase: "prepared"}
	if err := journal.Write(record); err != nil {
		t.Fatal(err)
	}
	path := journal.recordPath(record.InvocationID)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope journalEnvelope
	if err := json.Unmarshal(contents, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Record.SchemaVersion = "proxmox.invocation-journal/v2.0"
	recordBytes, _ := json.Marshal(envelope.Record)
	envelope.Checksum = digestDocument(recordBytes)
	contents, _ = json.Marshal(envelope)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Read(record.InvocationID); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("corrupt journal accepted: %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInvocationJournal(directory); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("unsafe directory mode accepted: %v", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, journalLockName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInvocationJournal(directory); err == nil || !strings.Contains(err.Error(), "lock type or mode") {
		t.Fatalf("unsafe lock mode accepted: %v", err)
	}
}

func TestInvocationJournalDoesNotPersistCanariesOutsideTypedFields(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	record := JournalRecord{SchemaVersion: journalV10, InvocationID: "invocation.test", TargetID: "target.test", Action: "apply", RequestDigest: sha("a"), Node: "pve-a", VMID: 101, OwnershipDigest: sha("b"), Phase: "prepared"}
	if err := journal.Write(record); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, filepath.Base(journal.recordPath(record.InvocationID))))
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"PVEAPIToken", "tokenSecret", "cloud-config", "upstream-body"} {
		if strings.Contains(string(contents), canary) {
			t.Fatalf("journal contains forbidden canary %q", canary)
		}
	}
}

func TestInvocationJournalAcceptsSourceBoundCloneTaskOnlyForApply(t *testing.T) {
	record := JournalRecord{
		SchemaVersion: journalV10, InvocationID: "invocation.test", TargetID: "target.test", Action: "apply",
		RequestDigest: sha("a"), Node: "pve-destination", VMID: 101, OwnershipDigest: sha("b"), Phase: "clone-submitted",
		TaskUPID: "UPID:pve-source:00000001:00000002:00000003:qmclone:9000:root@pam:",
	}
	if err := validateJournalRecord(record); err != nil {
		t.Fatalf("source-bound clone task rejected: %v", err)
	}

	record.Action = "start"
	if err := validateJournalRecord(record); err == nil || !strings.Contains(err.Error(), "task identity") {
		t.Fatalf("clone task accepted for lifecycle action: %v", err)
	}
	record.Action = "apply"
	record.TaskUPID = "UPID:pve-source:00000001:00000002:00000003:qmstart:101:root@pam:"
	if err := validateJournalRecord(record); err == nil || !strings.Contains(err.Error(), "task identity") {
		t.Fatalf("source-bound non-clone task accepted: %v", err)
	}
	record.TaskUPID = "UPID:pve-destination:00000001:00000002:00000003:qmconfig:101:root@pam:"
	if err := validateJournalRecord(record); err != nil {
		t.Fatalf("destination-bound configuration task rejected: %v", err)
	}
}

func TestInvocationClaimLocksAllowIndependentWorkAndFenceConflicts(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenInvocationJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := first.AcquireClaims(context.Background(), "invocation.one", "proxmox.qemu.vmid:101")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	independent, err := second.AcquireClaims(context.Background(), "invocation.two", "proxmox.qemu.vmid:102")
	if err != nil {
		t.Fatal(err)
	}
	independent.Close()
	for _, conflict := range []struct{ invocation, claim string }{{"invocation.three", "proxmox.qemu.vmid:101"}, {"invocation.one", "proxmox.qemu.vmid:103"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		got, err := second.AcquireClaims(ctx, conflict.invocation, conflict.claim)
		cancel()
		if got != nil {
			got.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("conflicting work acquired lock: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, err = second.Acquire(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("legacy exclusive lock overlapped: %v", err)
	}
	lease.Close()
	legacy, err := first.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, err = second.AcquireClaims(ctx, "invocation.four", "proxmox.qemu.vmid:104")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("claim bypassed legacy lock: %v", err)
	}
	legacy.Close()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	retry, err := second.AcquireClaims(ctx, "invocation.one", "proxmox.qemu.vmid:101", "proxmox.qemu.vmid:103")
	if err != nil {
		t.Fatalf("cancelled acquisition leaked locks: %v", err)
	}
	retry.Close()
}
