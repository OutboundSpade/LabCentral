package proxmox

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

const testConfigFingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testTemplateDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func mutationTestTarget(t *testing.T) *pluginv1.TargetContext {
	t.Helper()
	journalDirectory := t.TempDir()
	if err := os.Chmod(journalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	inventory := mutationTestInventory()
	if err := inventory.normalize(); err != nil {
		t.Fatal(err)
	}
	inventoryDigest, err := inventory.MutationDigest()
	if err != nil {
		t.Fatal(err)
	}
	config := TargetConfig{
		SchemaVersion: targetConfigV11, Endpoint: "https://pve.example:8006", MutationEnabled: true,
		InstallationID: "installation.test", SupportedPVERange: supportedPVERange, DestinationNode: "pve-a", Pool: "lab",
		VMIDRanges: []IntegerRange{{Start: 100, End: 110}},
		Templates: []TemplateProfile{{
			Alias: "debian", SourceNode: "pve-a", VMID: 9000, ConfigDigest: strings.Repeat("c", 40),
			ContentDigest: testTemplateDigest, Architecture: "amd64", CloudInitDrive: true, GuestAgent: true,
			ForbidUnknownDevices: true, Disks: []TemplateDiskProfile{{Device: "scsi0", SizeBytes: 8 << 30, Boot: true}},
		}},
		Storages:            []StorageProfile{{Alias: "primary", ID: "local-lvm", Format: "raw"}},
		Networks:            []NetworkProfile{{Alias: "isolated", Bridge: "vmbr1", VLANRanges: []IntegerRange{{Start: 200, End: 210}}}},
		Limits:              MutationLimits{MaxCPUMillis: 8000, MaxMemoryBytes: 16 << 30, MaxNICs: 4, MaxVMs: 4},
		ManagementIsolation: ManagementIsolationEvidence{AttestationDigest: sha("d"), ProbeDigest: sha("e"), IntendedSegmentReachable: true},
		Qualification:       TargetQualification{PVERelease: "9.0.3", CapabilityDigest: sha("1"), InventoryDigest: inventoryDigest, PolicyDigest: sha("3"), EvidenceDigest: sha("4"), Passed: true},
		PermissionScopes:    mutationTestPermissionScopes(),
		JournalDirectory:    journalDirectory,
	}
	contents, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return &pluginv1.TargetContext{TargetId: "target.proxmox", ConfigEnvelope: contents, ConfigFingerprint: testConfigFingerprint}
}

func mutationTestPermissionScopes() []PermissionScope {
	return []PermissionScope{
		{Path: "/vms/9000", Privileges: []string{"VM.Audit", "VM.Clone"}},
		{Path: "/pool/lab", Privileges: []string{"VM.Allocate", "VM.Audit", "VM.Config.CPU", "VM.Config.Cloudinit", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Options", "VM.GuestAgent.Audit", "VM.PowerMgmt"}},
		{Path: "/storage/local-lvm", Privileges: []string{"Datastore.AllocateSpace", "Datastore.Audit"}},
		{Path: "/sdn/zones/localnetwork/vmbr1", Privileges: []string{"SDN.Use"}},
		{Path: "/nodes/pve-a", Privileges: []string{"Sys.Audit"}},
	}
}

func mutationTestInventory() Inventory {
	return Inventory{
		Version:   Version{Release: "9.0", Version: "pve-manager/9.0.3/abcd1234", RepoID: "abcd1234"},
		Nodes:     []Node{{Name: "pve-a", Status: "online", QEMUAPI: true}},
		Storages:  []Storage{{Node: "pve-a", ID: "local-lvm", Type: "lvmthin", Content: []string{"images"}, Formats: []string{"raw"}, DefaultFormat: "raw", Active: true, Enabled: true}},
		Bridges:   []Bridge{{Node: "pve-a", Name: "vmbr1", Active: true, VLANAware: true}},
		Templates: []Template{{Node: "pve-a", VMID: 9000, Name: "debian", Status: "stopped"}},
	}
}

func sha(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func canonicalMutationLab() []byte {
	return []byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","metadata":{"name":"test"},"requirements":{"isolationClass":"virtualMachine"},"resources":[{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}},{"id":"vm.lab","kind":"VM","dependsOn":["network.lab"],"spec":{"image":{"repository":"labcentral/proxmox/debian","digest":"` + testTemplateDigest + `"},"osFamily":"linux","compute":{"cpuMillis":2500,"memoryBytes":2148000000,"architecture":"amd64"},"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}]}}]}`)
}

func validateMutationLab(t *testing.T, service *Service, target *pluginv1.TargetContext) *pluginv1.ValidateLabResponse {
	t.Helper()
	response, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{
		Target: target, CanonicalLabJson: canonicalMutationLab(), ProviderOverrideJson: []byte(`{"templateAlias":"debian","storageAlias":"primary","networkAlias":"isolated"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetErrors()) != 0 {
		t.Fatalf("validation errors = %#v", response.GetErrors())
	}
	if len(response.GetProviderPlan()) == 0 || len(response.GetProviderPlanDigest()) != 64 {
		t.Fatalf("validation plan is missing: %#v", response)
	}
	return response
}

func TestValidateAndPlanClosedProxmoxSubsetDeterministically(t *testing.T) {
	service := NewService()
	target := mutationTestTarget(t)
	validation := validateMutationLab(t, service, target)
	networkResource := []byte(`{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}}`)
	networkInput := &pluginv1.ResourceMutationInput{
		Context: &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "network.lab", ProviderTargetId: target.GetTargetId(), DesiredGeneration: 1},
		Target:  target, CanonicalResourceJson: networkResource, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"network-ownership-token"},
	}
	network, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: networkInput})
	if err != nil || network.GetError() != nil {
		t.Fatalf("network plan = %#v, err = %v", network, err)
	}
	if got := network.GetProposedAllocations(); len(got) != 1 || got[0].GetNamespace() != vlanClaimNamespace || got[0].GetValue() != "200" {
		t.Fatalf("network allocations = %#v", got)
	}
	vmResource := []byte(`{"id":"vm.lab","kind":"VM","dependsOn":["network.lab"],"spec":{"image":{"repository":"labcentral/proxmox/debian","digest":"` + testTemplateDigest + `"},"osFamily":"linux","compute":{"cpuMillis":2500,"memoryBytes":2148000000,"architecture":"amd64"},"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}]}}`)
	vmInput := &pluginv1.ResourceMutationInput{
		Context: &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "vm.lab", ProviderTargetId: target.GetTargetId(), DesiredGeneration: 1},
		Target:  target, CanonicalResourceJson: vmResource, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"vm-ownership-token"},
		Dependencies: []*pluginv1.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: network.GetProviderPlan(), ProviderPlanDigest: network.GetProviderPlanDigest()}},
	}
	first, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: vmInput, ExcludedAllocations: []*pluginv1.AllocationClaim{{Namespace: vmidClaimNamespace, Value: "100"}}})
	if err != nil || first.GetError() != nil {
		t.Fatalf("VM plan = %#v, err = %v", first, err)
	}
	second, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: vmInput, ExcludedAllocations: []*pluginv1.AllocationClaim{{Namespace: vmidClaimNamespace, Value: "100"}}})
	if err != nil || string(first.GetProviderPlan()) != string(second.GetProviderPlan()) || first.GetProviderPlanDigest() != second.GetProviderPlanDigest() {
		t.Fatal("VM planning is not deterministic")
	}
	if got := first.GetProposedAllocations(); len(got) != 1 || got[0].GetValue() != "101" {
		t.Fatalf("VM allocations = %#v", got)
	}
	var plan QEMUPlan
	if err := decodeEnvelope(first.GetProviderPlan(), maximumConfigBytes, &plan); err != nil || plan.validate() != nil {
		t.Fatalf("closed plan invalid: %#v, %v", plan, err)
	}
	if plan.Hardware.Cores != 3 || plan.Hardware.CPULimit != "2.5" || plan.Hardware.MemoryMiB != 2049 || plan.Networks[0].MAC == "" || len(plan.Allocations) != 2 {
		t.Fatalf("computed VM plan = %#v", plan)
	}
}

func TestValidateAcceptsConfiguredNonDefaultProfileAliases(t *testing.T) {
	target := mutationTestTarget(t)
	var config TargetConfig
	if err := decodeEnvelope(target.GetConfigEnvelope(), maximumConfigBytes, &config); err != nil {
		t.Fatal(err)
	}
	config.Storages = append(config.Storages, StorageProfile{Alias: "secondary", ID: "local-dir", Format: "qcow2"})
	config.Networks = append(config.Networks, NetworkProfile{Alias: "isolated-two", Bridge: "vmbr2", VLANRanges: []IntegerRange{{Start: 300, End: 310}}})
	config.PermissionScopes = nil
	for path, privilegeSet := range expectedPermissionScopes(config) {
		scope := PermissionScope{Path: path}
		for privilege := range privilegeSet {
			scope.Privileges = append(scope.Privileges, privilege)
		}
		config.PermissionScopes = append(config.PermissionScopes, scope)
	}
	contents, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	target.ConfigEnvelope = contents
	response, err := NewService().ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{
		Target: target, CanonicalLabJson: canonicalMutationLab(), ProviderOverrideJson: []byte(`{"templateAlias":"debian","storageAlias":"secondary","networkAlias":"isolated-two"}`),
	})
	if err != nil || len(response.GetErrors()) != 0 {
		t.Fatalf("validation=%#v err=%v", response.GetErrors(), err)
	}
	plan, err := decodeValidationPlan(response.GetProviderPlan())
	if err != nil || plan.StorageAlias != "secondary" || plan.NetworkAlias != "isolated-two" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
}

func TestValidateRejectsUnsupportedProxmoxInputs(t *testing.T) {
	service := NewService()
	target := mutationTestTarget(t)
	lab := strings.Replace(string(canonicalMutationLab()), `"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}]`, `"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10","ipv6":"fd00::10"}],"mounts":[{"volumeRef":"volume.data","path":"/data"}],"provisioning":{"method":"cloudInit","artifactRef":"artifact.test"},"lifecycle":{"snapshot":true}`, 1)
	response, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: []byte(lab)})
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]bool)
	for _, issue := range response.GetErrors() {
		codes[issue.GetCode()] = true
	}
	for _, code := range []string{"unsupported_network_address", "unsupported_volume", "unsupported_guest_artifact", "unsupported_snapshot"} {
		if !codes[code] {
			t.Fatalf("missing %s in %#v", code, response.GetErrors())
		}
	}
}

func TestNestedDuplicateAndUnknownMutationConfigurationFieldsAreRejected(t *testing.T) {
	var destination TargetConfig
	if err := decodeEnvelope([]byte(`{"endpoint":"https://pve.example","limits":{"maxNICs":1,"maxNICs":2}}`), maximumConfigBytes, &destination); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("nested duplicate accepted: %v", err)
	}
	if err := decodeEnvelope([]byte(`{"endpoint":"https://pve.example","limits":{"rawQEMUArgs":"host"}}`), maximumConfigBytes, &destination); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("nested unknown field accepted: %v", err)
	}
}
