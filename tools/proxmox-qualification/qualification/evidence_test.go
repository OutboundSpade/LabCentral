package qualification

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestContractTESTPROXMOX024SignedQualificationEvidenceFailsClosed(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	evidence := passingEvidence()
	policy := verificationPolicy(publicKey)

	first, err := Sign(evidence, "qualification.test", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Sign(evidence, "qualification.test", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical evidence did not produce deterministic canonical bytes")
	}
	verification, err := Verify(first, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Qualifies || len(verification.Reasons) != 0 || !strings.HasPrefix(verification.EvidenceDigest, "sha256:") {
		t.Fatalf("verification = %#v", verification)
	}

	t.Run("signature binds every exact target and build fact", func(t *testing.T) {
		var document SignedEvidence
		if err := json.Unmarshal(first, &document); err != nil {
			t.Fatal(err)
		}
		document.Evidence.Target.ConfigurationDigest = digest("f")
		tampered, err := canonicalJSON(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(tampered, policy); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
			t.Fatalf("tampered evidence error = %v", err)
		}
	})

	t.Run("closed parser rejects duplicate and unknown fields", func(t *testing.T) {
		duplicate := bytes.Replace(first, []byte(`"schemaVersion":`), []byte(`"schemaVersion":"duplicate","schemaVersion":`), 1)
		if _, err := ParseSigned(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate field error = %v", err)
		}
		unknown := bytes.Replace(first, []byte(`{"evidence":`), []byte(`{"unexpected":true,"evidence":`), 1)
		if _, err := ParseSigned(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown field error = %v", err)
		}
	})

	t.Run("isolation failure remains signed but cannot qualify", func(t *testing.T) {
		failed := passingEvidence()
		failed.Probes[1].Successes = 1
		document, err := Sign(failed, "qualification.test", privateKey)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Verify(document, policy)
		if err != nil {
			t.Fatal(err)
		}
		assertReasons(t, result, "isolation_probe_failed:management")
	})

	t.Run("ambiguous cleanup retains claims and cannot qualify", func(t *testing.T) {
		failed := passingEvidence()
		failed.Cleanup.ProviderAbsenceConfirmed = false
		failed.Cleanup.Claims[0].State = "retained"
		failed.Cleanup.Leftovers = []LeftoverEvidence{{Kind: "qemu", ProviderID: "101"}}
		document, err := Sign(failed, "qualification.test", privateKey)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Verify(document, policy)
		if err != nil {
			t.Fatal(err)
		}
		assertReasons(t, result,
			"cleanup_absence_unconfirmed",
			"cleanup_claim_not_released:proxmox.qemu.vmid:101",
			"cleanup_leftovers_present",
		)
	})

	t.Run("required case matrix cannot be silently omitted", func(t *testing.T) {
		incomplete := passingEvidence()
		incomplete.Cases = incomplete.Cases[:len(incomplete.Cases)-1]
		if _, err := Sign(incomplete, "qualification.test", privateKey); err == nil || !strings.Contains(err.Error(), "case matrix is incomplete") {
			t.Fatalf("incomplete matrix error = %v", err)
		}
	})

	t.Run("verification binds consumer expectations and validity", func(t *testing.T) {
		mismatched := policy
		mismatched.ExpectedConfigurationDigest = digest("f")
		mismatched.ExpectedInventoryDigest = digest("f")
		mismatched.Now = time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
		result, err := Verify(first, mismatched)
		if err != nil {
			t.Fatal(err)
		}
		assertReasons(t, result, "configuration_digest_mismatch", "inventory_digest_mismatch", "evidence_not_current")
	})
}

func TestQualificationPermissionPathAcceptsExactVMRootAndRejectsOtherRoots(t *testing.T) {
	if !validPermissionPath("/vms") {
		t.Fatal("exact Proxmox /vms ACL root was rejected")
	}
	for _, path := range []string{"/", "/nodes", "/pool", "/storage", "/sdn/zones/localnetwork"} {
		if validPermissionPath(path) {
			t.Fatalf("unscoped permission root %q was accepted", path)
		}
	}
}

func passingEvidence() Evidence {
	started := "2026-09-02T18:00:00Z"
	completed := "2026-09-02T18:01:00Z"
	cases := make([]CaseEvidence, 0, len(requiredT3Cases))
	for _, id := range requiredT3Cases {
		cases = append(cases, CaseEvidence{ID: id, Outcome: "passed", StartedAt: started, CompletedAt: completed, TranscriptHash: digest("a")})
	}
	return Evidence{
		SchemaVersion: EvidenceSchema,
		RunID:         "qualification.run-001",
		IssuedAt:      "2026-09-02T18:05:00Z",
		ValidUntil:    "2026-09-03T18:05:00Z",
		Target: TargetEvidence{
			TargetID: "target.proxmox", InstallationID: "installation.test",
			ConfigurationDigest: digest("1"), CapabilityDigest: digest("2"), InventoryDigest: digest("3"), PolicyDigest: digest("4"),
			PVERelease: "9.0.3", PVEVersion: "pve-manager/9.0.3/abcd1234",
		},
		Build:    BuildEvidence{PluginID: ProviderPlugin, PluginVersion: "0.1.0", BinaryDigest: digest("5"), SourceTreeDigest: digest("6")},
		Protocol: ProtocolEvidence{Major: 1, Minor: 1, Features: append([]string(nil), requiredFeatures...)},
		Profile: ProfileEvidence{
			DestinationNode: "pve-a",
			VMIDRanges:      []IntegerRange{{Start: 100, End: 110}},
			Storages:        []StorageEvidence{{Alias: "primary", ID: "local-lvm", Type: "lvmthin", Format: "raw"}},
			Networks:        []NetworkEvidence{{Alias: "isolated", Bridge: "vmbr1", VLANAware: true, VLANRanges: []IntegerRange{{Start: 200, End: 210}}}},
			Templates:       []TemplateEvidence{{Alias: "debian", SourceNode: "pve-a", VMID: 9000, ConfigDigest: strings.Repeat("c", 40), ContentDigest: digest("7")}},
			Permissions: []PermissionEvidence{
				{Path: "/nodes/pve-a", Privileges: []string{"Sys.Audit"}},
				{Path: "/pool/lab", Privileges: []string{"VM.Allocate", "VM.Audit", "VM.Config.CPU", "VM.Config.Cloudinit", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Options", "VM.GuestAgent.Audit", "VM.PowerMgmt"}},
				{Path: "/sdn/zones/localnetwork/vmbr1", Privileges: []string{"SDN.Use"}},
				{Path: "/storage/local-lvm", Privileges: []string{"Datastore.AllocateSpace", "Datastore.Audit"}},
				{Path: "/vms/9000", Privileges: []string{"VM.Audit", "VM.Clone"}},
			},
		},
		Cases: cases,
		Probes: []ProbeEvidence{
			{Kind: "intended_segment", SourceInstanceID: "instance.one", SourceVMID: 101, EndpointDigest: digest("8"), Protocol: "tcp", Attempts: 3, Successes: 3, StartedAt: started, CompletedAt: completed, AgentImageDigest: digest("9"), TranscriptDigest: digest("a")},
			{Kind: "management", SourceInstanceID: "instance.one", SourceVMID: 101, EndpointDigest: digest("b"), Protocol: "tcp", Attempts: 3, Successes: 0, StartedAt: started, CompletedAt: completed, AgentImageDigest: digest("9"), TranscriptDigest: digest("c")},
			{Kind: "cross_instance", SourceInstanceID: "instance.one", SourceVMID: 101, DestinationInstance: "instance.two", EndpointDigest: digest("d"), Protocol: "tcp", Attempts: 3, Successes: 0, StartedAt: started, CompletedAt: completed, AgentImageDigest: digest("9"), TranscriptDigest: digest("e")},
		},
		Cleanup: CleanupEvidence{
			ProviderAbsenceConfirmed: true, CheckedAt: "2026-09-02T18:04:00Z", TranscriptDigest: digest("f"),
			Claims:    []ClaimEvidence{{Namespace: "proxmox.qemu.vmid", Value: "101", State: "released"}, {Namespace: "proxmox.network.vlan", Value: "200", State: "released"}},
			Leftovers: []LeftoverEvidence{},
		},
	}
}

func verificationPolicy(publicKey ed25519.PublicKey) VerificationPolicy {
	return VerificationPolicy{
		Now:                          time.Date(2026, 9, 2, 19, 0, 0, 0, time.UTC),
		TrustedKeys:                  map[string]ed25519.PublicKey{"qualification.test": publicKey},
		ExpectedTargetID:             "target.proxmox",
		ExpectedInstallationID:       "installation.test",
		ExpectedConfigurationDigest:  digest("1"),
		ExpectedCapabilityDigest:     digest("2"),
		ExpectedInventoryDigest:      digest("3"),
		ExpectedPolicyDigest:         digest("4"),
		ExpectedPVERelease:           "9.0.3",
		ExpectedPVEVersion:           "pve-manager/9.0.3/abcd1234",
		ExpectedProviderBinaryDigest: digest("5"),
		ExpectedSourceTreeDigest:     digest("6"),
	}
}

func assertReasons(t *testing.T, result Verification, expected ...string) {
	t.Helper()
	if result.Qualifies {
		t.Fatalf("unexpected qualification: %#v", result)
	}
	for _, reason := range expected {
		found := false
		for _, actual := range result.Reasons {
			if actual == reason {
				found = true
			}
		}
		if !found {
			t.Errorf("reason %q missing from %#v", reason, result.Reasons)
		}
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
