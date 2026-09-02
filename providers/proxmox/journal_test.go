package proxmox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, err = %v", info.Mode(), err)
	}
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
