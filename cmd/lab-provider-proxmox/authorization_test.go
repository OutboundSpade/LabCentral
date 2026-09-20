package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/providers/proxmox"
	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/qualification"
)

func TestQualificationProfileIncludesExactEgressPolicyAndProbe(t *testing.T) {
	config, inventory, _ := authorizationFixture()
	config.Networks[0].Egress = &proxmox.NetworkEgressProfile{Mode: "allow", IPv4CIDR: "10.44.0.0/24", Gateway: "10.44.0.1", PolicyDigest: authorizationDigest("a"), ProbeDigest: authorizationDigest("b"), InternetReachable: true}
	profile, err := qualificationProfile(config, inventory)
	if err != nil {
		t.Fatal(err)
	}
	e := profile.Networks[0].Egress
	if e == nil || e.PolicyDigest != config.Networks[0].Egress.PolicyDigest || e.ProbeDigest != config.Networks[0].Egress.ProbeDigest || e.Gateway != "10.44.0.1" || e.IPv4CIDR != "10.44.0.0/24" || e.Mode != "allow" || !e.InternetReachable || e.ManagementReachable || e.CrossInstanceReachable {
		t.Fatalf("missing exact profile bindings %+v", e)
	}
	config.Networks[0].Egress.PolicyDigest = authorizationDigest("c")
	changed, err := qualificationProfile(config, inventory)
	if err != nil || changed.Networks[0].Egress.PolicyDigest == profile.Networks[0].Egress.PolicyDigest {
		t.Fatal("policy drift did not change expected signed profile")
	}
}

func TestProductionAuthorizerBindsExactLiveTargetAndTrust(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	config, inventory, profile := authorizationFixture()
	evidence := authorizationEvidence(now, profile)
	document, err := qualification.Sign(evidence, "reviewer.production", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &productionAuthorizer{
		document: document, evidence: evidence,
		trust: qualification.TrustPolicy{
			SchemaVersion: qualification.TrustPolicySchema, MaxEvidenceAgeSeconds: 7 * 24 * 60 * 60,
			Keys: []qualification.TrustKey{{
				KeyID: "reviewer.production", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				NotBefore: now.Add(-24 * time.Hour).Format(time.RFC3339), NotAfter: now.Add(365 * 24 * time.Hour).Format(time.RFC3339),
			}},
		},
		binaryDigest: authorizationDigest("5"), sourceTreeDigest: authorizationDigest("6"), now: func() time.Time { return now },
	}
	input := proxmox.MutationAuthorizationInput{
		Target: &pluginv1.TargetContext{TargetId: "target.proxmox", ConfigFingerprint: authorizationDigest("1")},
		Config: config, Inventory: inventory, MutationInventoryDigest: authorizationDigest("3"),
	}
	authorized, err := authorizer.AuthorizeMutation(context.Background(), input)
	if err != nil || !strings.Contains(authorized.Evidence, "reviewer.production") {
		t.Fatalf("exact production evidence was rejected: %#v, %v", authorized, err)
	}

	t.Run("full Proxmox version response", func(t *testing.T) {
		changed := input
		changed.Inventory = inventory
		changed.Inventory.Version.Version = "pve-manager/9.0.3/abcd1234"
		if _, err := authorizer.AuthorizeMutation(context.Background(), changed); err != nil {
			t.Fatalf("full pve-manager version was rejected: %v", err)
		}
	})

	t.Run("different target", func(t *testing.T) {
		changed := input
		changed.Target = &pluginv1.TargetContext{TargetId: "target.other", ConfigFingerprint: input.Target.ConfigFingerprint}
		if _, err := authorizer.AuthorizeMutation(context.Background(), changed); err == nil {
			t.Fatal("evidence authorized a different target")
		}
	})
	t.Run("revoked reviewer", func(t *testing.T) {
		authorizer.trust.Keys[0].Revoked = true
		if _, err := authorizer.AuthorizeMutation(context.Background(), input); err == nil {
			t.Fatal("revoked reviewer key authorized mutation")
		}
		authorizer.trust.Keys[0].Revoked = false
	})
	t.Run("live profile drift", func(t *testing.T) {
		changed := input
		changed.Inventory = inventory
		changed.Inventory.Storages = append([]proxmox.Storage(nil), inventory.Storages...)
		changed.Inventory.Storages[0].Type = "dir"
		if _, err := authorizer.AuthorizeMutation(context.Background(), changed); err == nil {
			t.Fatal("storage-type drift remained authorized")
		}
	})
}

func authorizationFixture() (proxmox.TargetConfig, proxmox.Inventory, qualification.ProfileEvidence) {
	permissions := []proxmox.PermissionScope{
		{Path: "/nodes/pve-a", Privileges: []string{"Sys.Audit"}},
		{Path: "/pool/lab", Privileges: []string{"VM.Allocate", "VM.Audit", "VM.Config.CPU", "VM.Config.Cloudinit", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Options", "VM.GuestAgent.Audit", "VM.PowerMgmt"}},
		{Path: "/sdn/zones/localnetwork/vmbr1", Privileges: []string{"SDN.Use"}},
		{Path: "/storage/local-lvm", Privileges: []string{"Datastore.AllocateSpace", "Datastore.Audit"}},
		{Path: "/vms/9000", Privileges: []string{"VM.Audit", "VM.Clone"}},
	}
	config := proxmox.TargetConfig{
		SchemaVersion: "proxmox.target-config/v1.1", MutationEnabled: true,
		InstallationID: "installation.test", DestinationNode: "pve-a", Pool: "lab",
		VMIDRanges:       []proxmox.IntegerRange{{Start: 100, End: 110}},
		Storages:         []proxmox.StorageProfile{{Alias: "primary", ID: "local-lvm", Format: "raw"}},
		Networks:         []proxmox.NetworkProfile{{Alias: "isolated", Bridge: "vmbr1", VLANRanges: []proxmox.IntegerRange{{Start: 200, End: 210}}}},
		Templates:        []proxmox.TemplateProfile{{Alias: "debian", SourceNode: "pve-a", VMID: 9000, ConfigDigest: strings.Repeat("c", 40), ContentDigest: authorizationDigest("7")}},
		PermissionScopes: permissions,
		Qualification: proxmox.TargetQualification{
			PVERelease: "9.0.3", CapabilityDigest: authorizationDigest("2"), InventoryDigest: authorizationDigest("3"),
			PolicyDigest: authorizationDigest("4"), EvidenceDigest: authorizationDigest("8"), Passed: true,
		},
	}
	inventory := proxmox.Inventory{
		Version:   proxmox.Version{Release: "9.0", Version: "9.0.3", RepoID: "abcd1234"},
		Nodes:     []proxmox.Node{{Name: "pve-a", Status: "online", QEMUAPI: true}},
		Storages:  []proxmox.Storage{{Node: "pve-a", ID: "local-lvm", Type: "lvmthin", Active: true, Enabled: true, Content: []string{"images"}, Formats: []string{"raw"}}},
		Bridges:   []proxmox.Bridge{{Node: "pve-a", Name: "vmbr1", Active: true, VLANAware: true}},
		Templates: []proxmox.Template{{Node: "pve-a", VMID: 9000, Name: "debian", Status: "stopped"}},
	}
	profile, _ := qualificationProfile(config, inventory)
	return config, inventory, profile
}

func authorizationEvidence(now time.Time, profile qualification.ProfileEvidence) qualification.Evidence {
	started := now.Add(-10 * time.Minute).Format(time.RFC3339)
	completed := now.Add(-9 * time.Minute).Format(time.RFC3339)
	cases := make([]qualification.CaseEvidence, 0, len(qualification.RequiredT3Cases()))
	for _, id := range qualification.RequiredT3Cases() {
		cases = append(cases, qualification.CaseEvidence{ID: id, Outcome: "passed", StartedAt: started, CompletedAt: completed, TranscriptHash: authorizationDigest("a")})
	}
	return qualification.Evidence{
		SchemaVersion: qualification.EvidenceSchema, RunID: "qualification.production-001",
		IssuedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), ValidUntil: now.Add(24 * time.Hour).Format(time.RFC3339),
		Target: qualification.TargetEvidence{
			TargetID: "target.proxmox", InstallationID: "installation.test", ConfigurationDigest: authorizationDigest("1"),
			CapabilityDigest: authorizationDigest("2"), InventoryDigest: authorizationDigest("3"), PolicyDigest: authorizationDigest("4"),
			PVERelease: "9.0.3", PVEVersion: "pve-manager/9.0.3/abcd1234",
		},
		Build: qualification.BuildEvidence{
			PluginID: qualification.ProviderPlugin, PluginVersion: proxmox.PluginVersion,
			BinaryDigest: authorizationDigest("5"), SourceTreeDigest: authorizationDigest("6"),
		},
		Protocol: qualification.ProtocolEvidence{Major: 1, Minor: 1, Features: qualification.RequiredProtocolFeatures()},
		Profile:  profile,
		Cases:    cases,
		Probes: []qualification.ProbeEvidence{
			{Kind: "intended_segment", SourceInstanceID: "instance.one", SourceVMID: 101, EndpointDigest: authorizationDigest("b"), Protocol: "tcp", Attempts: 3, Successes: 3, StartedAt: started, CompletedAt: completed, AgentImageDigest: authorizationDigest("c"), TranscriptDigest: authorizationDigest("d")},
			{Kind: "management", SourceInstanceID: "instance.one", SourceVMID: 101, EndpointDigest: authorizationDigest("e"), Protocol: "tcp", Attempts: 3, Successes: 0, StartedAt: started, CompletedAt: completed, AgentImageDigest: authorizationDigest("c"), TranscriptDigest: authorizationDigest("f")},
			{Kind: "cross_instance", SourceInstanceID: "instance.one", SourceVMID: 101, DestinationInstance: "instance.two", EndpointDigest: authorizationDigest("0"), Protocol: "tcp", Attempts: 3, Successes: 0, StartedAt: started, CompletedAt: completed, AgentImageDigest: authorizationDigest("c"), TranscriptDigest: authorizationDigest("1")},
		},
		Cleanup: qualification.CleanupEvidence{
			ProviderAbsenceConfirmed: true, CheckedAt: now.Add(-6 * time.Minute).Format(time.RFC3339), TranscriptDigest: authorizationDigest("2"),
			Claims:    []qualification.ClaimEvidence{{Namespace: "proxmox.qemu.vmid", Value: "101", State: "released"}, {Namespace: "proxmox.network.vlan", Value: "200", State: "released"}},
			Leftovers: []qualification.LeftoverEvidence{},
		},
	}
}

func authorizationDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
