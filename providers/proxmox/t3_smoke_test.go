package proxmox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/protobuf/proto"
)

const (
	t3ExecuteSentinel = "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE"
	t3ProfileSchema   = "labcentral.proxmox-t3-smoke-profile/v1"
)

type t3Activation struct {
	profilePath     string
	configPath      string
	credentialPath  string
	confirmedTarget string
}

type t3SmokeProfile struct {
	SchemaVersion     string `json:"schemaVersion"`
	RunID             string `json:"runID"`
	TargetID          string `json:"targetID"`
	InstanceID        string `json:"instanceID"`
	NetworkResourceID string `json:"networkResourceID"`
	VMResourceID      string `json:"vmResourceID"`
	NetworkCIDR       string `json:"networkCIDR"`
	GuestIPv4         string `json:"guestIPv4"`
	ImageRepository   string `json:"imageRepository"`
	ImageDigest       string `json:"imageDigest"`
	TemplateAlias     string `json:"templateAlias"`
	StorageAlias      string `json:"storageAlias"`
	NetworkAlias      string `json:"networkAlias"`
	ExpectedVMID      uint32 `json:"expectedVMID"`
	ExpectedVLAN      uint32 `json:"expectedVLAN"`
	CPUMillis         uint32 `json:"cpuMillis"`
	MemoryBytes       uint64 `json:"memoryBytes"`
}

func TestContractTESTPROXMOX025CredentialedSmokeRequiresExplicitTargetConfirmation(t *testing.T) {
	empty, err := readT3Activation(func(string) string { return "" })
	if err != nil || empty != (t3Activation{}) {
		t.Fatalf("empty activation = %#v, %v", empty, err)
	}

	partial := map[string]string{"LABCENTRAL_PROXMOX_T3_EXECUTE": t3ExecuteSentinel}
	if _, err := readT3Activation(func(name string) string { return partial[name] }); err == nil {
		t.Fatal("partial T3 activation was accepted")
	}

	complete := map[string]string{
		"LABCENTRAL_PROXMOX_T3_EXECUTE":         t3ExecuteSentinel,
		"LABCENTRAL_PROXMOX_T3_PROFILE":         "/qualification/profile.json",
		"LABCENTRAL_PROXMOX_T3_TARGET_CONFIG":   "/qualification/target.json",
		"LABCENTRAL_PROXMOX_T3_CREDENTIAL_FILE": "/qualification/credential.json",
		"LABCENTRAL_PROXMOX_T3_CONFIRM_TARGET":  "target.proxmox-t3",
	}
	activation, err := readT3Activation(func(name string) string { return complete[name] })
	if err != nil || activation.confirmedTarget != "target.proxmox-t3" {
		t.Fatalf("complete activation = %#v, %v", activation, err)
	}

	complete["LABCENTRAL_PROXMOX_T3_EXECUTE"] = "yes"
	if _, err := readT3Activation(func(name string) string { return complete[name] }); err == nil {
		t.Fatal("inexact destructive sentinel was accepted")
	}

	t.Run("closed profile plans only the confirmed disposable claims", func(t *testing.T) {
		target := mutationTestTarget(t)
		var config TargetConfig
		if err := decodeEnvelope(target.GetConfigEnvelope(), maximumConfigBytes, &config); err != nil {
			t.Fatal(err)
		}
		profile := t3TestProfile(target.GetTargetId())
		if err := validateT3Profile(profile, config, target.GetTargetId()); err != nil {
			t.Fatal(err)
		}
		labJSON, networkJSON, vmJSON, overrideJSON, err := t3Documents(profile)
		if err != nil {
			t.Fatal(err)
		}
		service := NewService()
		validation, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: labJSON, ProviderOverrideJson: overrideJSON})
		if err != nil || len(validation.GetErrors()) != 0 {
			t.Fatalf("validation = %#v, %v", validation.GetErrors(), err)
		}
		networkInput := &pluginv1.ResourceMutationInput{
			Context: t3Call(profile, "plan-network", profile.NetworkResourceID), Target: target,
			CanonicalResourceJson: networkJSON, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"network-token"},
		}
		networkPlan, err := t3ReservedPlan(t, service, context.Background(), &pluginv1.PlanResourceRequest{Input: networkInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
		if err != nil || networkPlan.GetError() != nil || !claimSetEquals(networkPlan.GetProposedAllocations(), vlanClaimNamespace, profile.ExpectedVLAN) {
			t.Fatalf("network plan = %#v, %v", networkPlan, err)
		}
		vmInput := &pluginv1.ResourceMutationInput{
			Context: t3Call(profile, "plan-vm", profile.VMResourceID), Target: target,
			CanonicalResourceJson: vmJSON, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"vm-token"},
			Dependencies: []*pluginv1.ResourceDependency{{
				LogicalResourceId: profile.NetworkResourceID, ProviderPlan: networkPlan.GetProviderPlan(), ProviderPlanDigest: networkPlan.GetProviderPlanDigest(),
			}},
		}
		vmPlan, err := t3ReservedPlan(t, service, context.Background(), &pluginv1.PlanResourceRequest{Input: vmInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
		if err != nil || vmPlan.GetError() != nil || !claimSetEquals(vmPlan.GetProposedAllocations(), vmidClaimNamespace, profile.ExpectedVMID) {
			t.Fatalf("VM plan = %#v, %v", vmPlan, err)
		}

		profile.TargetID = "target.other"
		if err := validateT3Profile(profile, config, target.GetTargetId()); err == nil {
			t.Fatal("profile target mismatch was accepted")
		}
	})
}

// TestCredentialedProxmoxT3Smoke is deliberately test-only so the
// package-private mutation constructor cannot become a production bypass. It
// performs one exact-profile Network/VM create, stop/start/pause/resume, owned
// reverse destroy, and positive provider absence check. It is not the complete
// T3 matrix and does not produce qualifying evidence by itself.
func TestCredentialedProxmoxT3Smoke(t *testing.T)     { runCredentialedSmoke(t, false) }
func TestCredentialedProxmoxT3Container(t *testing.T) { runCredentialedSmoke(t, true) }
func runCredentialedSmoke(t *testing.T, container bool) {
	activation, err := readT3Activation(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if activation == (t3Activation{}) {
		t.Skip("credentialed Proxmox T3 smoke is not explicitly enabled")
	}

	profileBytes, err := readT3File(activation.profilePath, 1<<20, false)
	if err != nil {
		t.Fatalf("read T3 profile: %v", err)
	}
	var profile t3SmokeProfile
	if err := decodeEnvelope(profileBytes, 1<<20, &profile); err != nil {
		t.Fatalf("decode T3 profile: %v", err)
	}
	configBytes, err := readT3File(activation.configPath, maximumConfigBytes, false)
	if err != nil {
		t.Fatalf("read target config: %v", err)
	}
	credentialBytes, err := readT3File(activation.credentialPath, maximumCredentialBytes, true)
	if err != nil {
		t.Fatalf("read target credential: %v", err)
	}
	defer zeroBytesForT3(credentialBytes)

	var config TargetConfig
	if err := decodeEnvelope(configBytes, maximumConfigBytes, &config); err != nil {
		t.Fatalf("decode target config: %v", err)
	}
	if err := validateT3Profile(profile, config, activation.confirmedTarget); err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(configBytes)
	target := &pluginv1.TargetContext{
		TargetId: profile.TargetID, ConfigEnvelope: configBytes, CredentialEnvelope: credentialBytes,
		ConfigFingerprint: "sha256:" + hex.EncodeToString(fingerprint[:]),
	}
	service := newT0MutationService(ClientFactoryFunc(func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
		return NewClientFromEnvelopes(configEnvelope, credentialEnvelope)
	}))
	service.sleep = time.Sleep

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	discovery, err := service.DiscoverTarget(ctx, &pluginv1.DiscoverTargetRequest{Target: target})
	if err != nil {
		t.Fatalf("target discovery failed: %v", err)
	}
	if capabilitySupport(discovery, "workload.vm") != pluginv1.Capability_SUPPORT_NATIVE {
		t.Fatal("exact target failed the package-private mutation preflight gate")
	}

	labJSON, networkJSON, vmJSON, overrideJSON, err := t3Documents(profile)
	if container {
		if config.ContainerRuntime == nil || profile.TemplateAlias != config.ContainerRuntime.TemplateAlias {
			t.Fatal("container runtime qualification profile required")
		}
		var lab canonicalLabDocument
		json.Unmarshal(labJSON, &lab)
		var vm canonicalVMSpec
		json.Unmarshal(lab.Resources[1].Spec, &vm)
		spec := canonicalContainerSpec{Compute: vm.Compute, Isolation: "virtualMachine", RealizationPreference: []string{"viaVM"}, Interfaces: vm.Interfaces, Command: []string{"python3"}, Args: []string{"-m", "http.server", "8080"}}
		image := strings.Split(config.ContainerRuntime.Images[0], "@")
		spec.Image.Repository = image[0]
		spec.Image.Digest = image[1]
		lab.Resources[1].Kind = "Container"
		lab.Resources[1].Spec, _ = json.Marshal(spec)
		vmJSON, _ = json.Marshal(lab.Resources[1])
		labJSON, _ = json.Marshal(lab)
	}

	if err != nil {
		t.Fatal(err)
	}
	validation, err := service.ValidateLab(ctx, &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: labJSON, ProviderOverrideJson: overrideJSON})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatalf("T3 validation failed: %v %#v", err, validation.GetErrors())
	}

	networkToken := randomT3Token(t)
	networkInput := &pluginv1.ResourceMutationInput{
		Context: t3Call(profile, "plan-network", profile.NetworkResourceID), Target: target,
		CanonicalResourceJson: networkJSON, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{networkToken},
	}
	networkPlan, err := t3ReservedPlan(t, service, ctx, &pluginv1.PlanResourceRequest{Input: networkInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
	if err != nil || networkPlan.GetError() != nil {
		t.Fatalf("T3 network plan failed: %v %#v", err, networkPlan.GetError())
	}
	if !claimSetEquals(networkPlan.GetProposedAllocations(), vlanClaimNamespace, profile.ExpectedVLAN) {
		t.Fatalf("network plan selected outside the confirmed disposable VLAN: %#v", networkPlan.GetProposedAllocations())
	}
	networkInput.ProviderPlan = networkPlan.GetProviderPlan()
	networkInput.CommittedAllocations = cloneClaims(networkPlan.GetProposedAllocations())
	networkInput.Context = t3Call(profile, "apply-network", profile.NetworkResourceID)
	networkStream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: networkInput}, networkStream); err != nil {
		t.Fatal(err)
	}
	networkResult := finalApplyResult(networkStream.events)
	if networkResult == nil || networkResult.GetError() != nil {
		t.Fatalf("T3 network apply failed: %#v", networkResult)
	}
	networkState := networkResult.GetProviderState()
	networkDestroyed := false
	defer func() {
		if !networkDestroyed {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()
			if cleanupErr := destroyT3Network(cleanupCtx, service, profile, target, networkInput, networkState); cleanupErr != nil {
				t.Errorf("T3 network cleanup: %v", cleanupErr)
			}
		}
	}()

	vmToken := randomT3Token(t)
	vmPlanningInput := &pluginv1.ResourceMutationInput{
		Context: t3Call(profile, "plan-vm", profile.VMResourceID), Target: target,
		CanonicalResourceJson: vmJSON, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{vmToken},
		Dependencies: []*pluginv1.ResourceDependency{{
			LogicalResourceId: profile.NetworkResourceID, ProviderPlan: networkPlan.GetProviderPlan(),
			ProviderPlanDigest: networkPlan.GetProviderPlanDigest(), ProviderState: networkState,
			CommittedAllocations: cloneClaims(networkPlan.GetProposedAllocations()),
		}},
	}
	vmPlan, err := t3ReservedPlan(t, service, ctx, &pluginv1.PlanResourceRequest{Input: vmPlanningInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
	if err != nil || vmPlan.GetError() != nil {
		t.Fatalf("T3 VM plan failed: %v %#v", err, vmPlan.GetError())
	}
	if !claimSetEquals(vmPlan.GetProposedAllocations(), vmidClaimNamespace, profile.ExpectedVMID) {
		t.Fatalf("VM plan selected outside the confirmed disposable VMID: %#v", vmPlan.GetProposedAllocations())
	}
	vmInput := proto.Clone(vmPlanningInput).(*pluginv1.ResourceMutationInput)
	vmInput.Context = t3Call(profile, "apply-vm", profile.VMResourceID)
	vmInput.ProviderPlan = vmPlan.GetProviderPlan()
	vmInput.CommittedAllocations = cloneClaims(vmPlan.GetProposedAllocations())
	vmStream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: vmInput}, vmStream); err != nil {
		t.Fatal(err)
	}
	vmResult := finalApplyResult(vmStream.events)
	if vmResult == nil || vmResult.GetError() != nil {
		t.Fatalf("T3 VM apply failed: %v", vmResult.GetError())
	}
	vmState := vmResult.GetProviderState()
	vmDestroyed := false
	defer func() {
		if !vmDestroyed {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()
			if cleanupErr := destroyT3VM(cleanupCtx, service, profile, target, vmInput, vmState); cleanupErr != nil {
				t.Errorf("T3 VM cleanup: %v", cleanupErr)
			}
		}
	}()

	for index, action := range []pluginv1.LifecycleAction{
		pluginv1.LifecycleAction_LIFECYCLE_ACTION_STOP,
		pluginv1.LifecycleAction_LIFECYCLE_ACTION_START,
		pluginv1.LifecycleAction_LIFECYCLE_ACTION_PAUSE,
		pluginv1.LifecycleAction_LIFECYCLE_ACTION_RESUME,
	} {
		stream := &lifecycleCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
		request := &pluginv1.LifecycleRequest{
			Context: t3Call(profile, fmt.Sprintf("lifecycle-%d", index), profile.VMResourceID), Target: target,
			Action: action, PriorState: vmState, ActionParametersJson: []byte(`{}`),
		}
		if err := service.Lifecycle(request, stream); err != nil {
			t.Fatal(err)
		}
		result := finalLifecycleResult(stream.events)
		if result == nil || result.GetError() != nil {
			t.Fatalf("T3 lifecycle %s failed: %#v", action, result)
		}
		vmState = result.GetProviderState()
	}

	if container {
		var plan QEMUPlan
		json.Unmarshal(vmInput.ProviderPlan, &plan)
		runtimeClient, e := NewClientFromEnvelopes(configBytes, credentialBytes)
		if e != nil {
			t.Fatal(e)
		}
		defer runtimeClient.CloseIdleConnections()
		for i := 0; i < 60; i++ {
			if runtimeClient.GuestAgentPing(ctx, config.DestinationNode, profile.ExpectedVMID) == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if e = runtimeClient.EnsureContainerRuntime(ctx, config.DestinationNode, profile.ExpectedVMID, *plan.Container); e != nil {
			t.Fatal("container restart/replay", e)
		}
		t.Log("container runtime ready after stop/start/pause/resume and idempotent bootstrap replay")
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cleanupCancel()
	if err := destroyT3VM(cleanupCtx, service, profile, target, vmInput, vmState); err != nil {
		t.Fatal(err)
	}
	vmDestroyed = true
	client, err := NewClientFromEnvelopes(configBytes, credentialBytes)
	if err != nil {
		t.Fatal("reopen target client for absence check")
	}
	_, exists, absenceErr := readOptionalVMConfig(cleanupCtx, client, config.DestinationNode, profile.ExpectedVMID)
	client.CloseIdleConnections()
	if absenceErr != nil || exists {
		t.Fatalf("provider absence was not confirmed for VMID %d", profile.ExpectedVMID)
	}
	if err := destroyT3Network(cleanupCtx, service, profile, target, networkInput, networkState); err != nil {
		t.Fatal(err)
	}
	networkDestroyed = true
	t.Logf("credentialed T3 smoke passed for target %s VMID %d VLAN %d; this is not complete qualification", profile.TargetID, profile.ExpectedVMID, profile.ExpectedVLAN)
}

func readT3Activation(getenv func(string) string) (t3Activation, error) {
	names := []string{
		"LABCENTRAL_PROXMOX_T3_EXECUTE",
		"LABCENTRAL_PROXMOX_T3_PROFILE",
		"LABCENTRAL_PROXMOX_T3_TARGET_CONFIG",
		"LABCENTRAL_PROXMOX_T3_CREDENTIAL_FILE",
		"LABCENTRAL_PROXMOX_T3_CONFIRM_TARGET",
	}
	values := make(map[string]string, len(names))
	configured := false
	for _, name := range names {
		values[name] = strings.TrimSpace(getenv(name))
		configured = configured || values[name] != ""
	}
	if !configured {
		return t3Activation{}, nil
	}
	if values["LABCENTRAL_PROXMOX_T3_EXECUTE"] != t3ExecuteSentinel {
		return t3Activation{}, errors.New("credentialed T3 requires the exact destructive execution sentinel")
	}
	for _, name := range names[1:] {
		if values[name] == "" {
			return t3Activation{}, fmt.Errorf("credentialed T3 requires %s", name)
		}
	}
	for _, name := range names[1:4] {
		if !filepath.IsAbs(values[name]) || filepath.Clean(values[name]) != values[name] {
			return t3Activation{}, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if values["LABCENTRAL_PROXMOX_T3_PROFILE"] == values["LABCENTRAL_PROXMOX_T3_TARGET_CONFIG"] || values["LABCENTRAL_PROXMOX_T3_PROFILE"] == values["LABCENTRAL_PROXMOX_T3_CREDENTIAL_FILE"] || values["LABCENTRAL_PROXMOX_T3_TARGET_CONFIG"] == values["LABCENTRAL_PROXMOX_T3_CREDENTIAL_FILE"] {
		return t3Activation{}, errors.New("credentialed T3 profile, target config, and credential paths must be distinct")
	}
	if !logicalIDPattern.MatchString(values["LABCENTRAL_PROXMOX_T3_CONFIRM_TARGET"]) {
		return t3Activation{}, errors.New("credentialed T3 target confirmation is invalid")
	}
	return t3Activation{
		profilePath: values["LABCENTRAL_PROXMOX_T3_PROFILE"], configPath: values["LABCENTRAL_PROXMOX_T3_TARGET_CONFIG"],
		credentialPath: values["LABCENTRAL_PROXMOX_T3_CREDENTIAL_FILE"], confirmedTarget: values["LABCENTRAL_PROXMOX_T3_CONFIRM_TARGET"],
	}, nil
}

func validateT3Profile(profile t3SmokeProfile, config TargetConfig, confirmedTarget string) error {
	if profile.SchemaVersion != t3ProfileSchema || !logicalIDPattern.MatchString(profile.RunID) || !logicalIDPattern.MatchString(profile.TargetID) || profile.TargetID != confirmedTarget || !logicalIDPattern.MatchString(profile.InstanceID) || !logicalIDPattern.MatchString(profile.NetworkResourceID) || !logicalIDPattern.MatchString(profile.VMResourceID) || profile.NetworkResourceID == profile.VMResourceID || !aliasPattern.MatchString(profile.TemplateAlias) || !aliasPattern.MatchString(profile.StorageAlias) || !aliasPattern.MatchString(profile.NetworkAlias) || !sha256Pattern.MatchString(profile.ImageDigest) || len(profile.ImageRepository) == 0 || len(profile.ImageRepository) > 256 || strings.ContainsAny(profile.ImageRepository, "\x00\r\n") {
		return errors.New("credentialed T3 smoke profile identity or selector is invalid")
	}
	prefix, err := netip.ParsePrefix(profile.NetworkCIDR)
	address, addressErr := netip.ParseAddr(profile.GuestIPv4)
	if err != nil || addressErr != nil || !prefix.Addr().Is4() || !address.Is4() || !prefix.Contains(address) || prefix.Addr() == address {
		return errors.New("credentialed T3 smoke IPv4 profile is invalid")
	}
	if profile.ExpectedVMID < 100 || profile.ExpectedVLAN == 0 || profile.ExpectedVLAN > 4094 || profile.CPUMillis == 0 || profile.CPUMillis > config.Limits.MaxCPUMillis || profile.MemoryBytes < 16<<20 || profile.MemoryBytes > config.Limits.MaxMemoryBytes {
		return errors.New("credentialed T3 disposable allocation or compute profile is invalid")
	}
	if err := config.validateMutation(); err != nil {
		return fmt.Errorf("credentialed T3 target config: %w", err)
	}
	if !rangeContains(config.VMIDRanges, profile.ExpectedVMID) {
		return errors.New("confirmed disposable VMID is outside the target configuration")
	}
	templateOK, storageOK, networkOK := false, false, false
	for _, template := range config.Templates {
		if template.Alias == profile.TemplateAlias && template.ContentDigest == profile.ImageDigest {
			templateOK = true
		}
	}
	for _, storage := range config.Storages {
		storageOK = storageOK || storage.Alias == profile.StorageAlias
	}
	for _, network := range config.Networks {
		if network.Alias == profile.NetworkAlias && rangeContains(network.VLANRanges, profile.ExpectedVLAN) {
			networkOK = true
		}
	}
	if !templateOK || !storageOK || !networkOK {
		return errors.New("credentialed T3 selectors do not match the closed target configuration")
	}
	return nil
}

func t3TestProfile(targetID string) t3SmokeProfile {
	return t3SmokeProfile{
		SchemaVersion: t3ProfileSchema, RunID: "qualification.smoke-test", TargetID: targetID,
		InstanceID: "instance.smoke-test", NetworkResourceID: "network.smoke", VMResourceID: "vm.smoke",
		NetworkCIDR: "10.20.0.0/24", GuestIPv4: "10.20.0.10", ImageRepository: "labcentral/proxmox/debian",
		ImageDigest: testTemplateDigest, TemplateAlias: "debian", StorageAlias: "primary", NetworkAlias: "isolated",
		ExpectedVMID: 100, ExpectedVLAN: 200, CPUMillis: 2000, MemoryBytes: 2 << 30,
	}
}

func t3Documents(profile t3SmokeProfile) ([]byte, []byte, []byte, []byte, error) {
	type networkSpec struct {
		Kind     string `json:"kind"`
		IPv4CIDR string `json:"ipv4Cidr"`
		IPAM     struct {
			Mode string `json:"mode"`
		} `json:"ipam"`
		Egress struct {
			Mode string `json:"mode"`
		} `json:"egress"`
	}
	type vmSpec struct {
		Image struct {
			Repository string `json:"repository"`
			Digest     string `json:"digest"`
		} `json:"image"`
		OSFamily string `json:"osFamily"`
		Compute  struct {
			CPUMillis    uint32 `json:"cpuMillis"`
			MemoryBytes  uint64 `json:"memoryBytes"`
			Architecture string `json:"architecture"`
		} `json:"compute"`
		Interfaces []struct {
			NetworkRef string `json:"networkRef"`
			IPv4       string `json:"ipv4"`
		} `json:"interfaces"`
	}
	type resource struct {
		ID        string          `json:"id"`
		Kind      string          `json:"kind"`
		DependsOn []string        `json:"dependsOn,omitempty"`
		Spec      json.RawMessage `json:"spec"`
	}
	network := networkSpec{Kind: "isolated", IPv4CIDR: profile.NetworkCIDR}
	network.IPAM.Mode = "static"
	network.Egress.Mode = "deny"
	vm := vmSpec{OSFamily: "linux"}
	vm.Image.Repository, vm.Image.Digest = profile.ImageRepository, profile.ImageDigest
	vm.Compute.CPUMillis, vm.Compute.MemoryBytes, vm.Compute.Architecture = profile.CPUMillis, profile.MemoryBytes, "amd64"
	vm.Interfaces = append(vm.Interfaces, struct {
		NetworkRef string `json:"networkRef"`
		IPv4       string `json:"ipv4"`
	}{NetworkRef: profile.NetworkResourceID, IPv4: profile.GuestIPv4})
	networkSpecJSON, err := json.Marshal(network)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	vmSpecJSON, err := json.Marshal(vm)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	networkResource := resource{ID: profile.NetworkResourceID, Kind: "Network", Spec: networkSpecJSON}
	vmResource := resource{ID: profile.VMResourceID, Kind: "VM", DependsOn: []string{profile.NetworkResourceID}, Spec: vmSpecJSON}
	lab := struct {
		APIVersion string `json:"apiVersion"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Requirements struct {
			IsolationClass string `json:"isolationClass"`
		} `json:"requirements"`
		Resources []resource `json:"resources"`
	}{APIVersion: "canonical.labcentral.io/v1alpha1", Resources: []resource{networkResource, vmResource}}
	lab.Metadata.Name = profile.RunID
	lab.Requirements.IsolationClass = "virtualMachine"
	override := ProviderOverride{TemplateAlias: profile.TemplateAlias, StorageAlias: profile.StorageAlias, NetworkAlias: profile.NetworkAlias}
	labJSON, err := json.Marshal(lab)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	networkJSON, err := json.Marshal(networkResource)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	vmJSON, err := json.Marshal(vmResource)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	overrideJSON, err := json.Marshal(override)
	return labJSON, networkJSON, vmJSON, overrideJSON, err
}

func t3Call(profile t3SmokeProfile, suffix, resourceID string) *pluginv1.CallContext {
	return &pluginv1.CallContext{
		RequestId: profile.RunID + ".request", OperationId: profile.RunID + ".operation",
		InvocationId: profile.RunID + "." + suffix, LabInstanceId: profile.InstanceID,
		LogicalResourceId: resourceID, ProviderTargetId: profile.TargetID,
		IdempotencyKey: profile.RunID + "." + suffix, DesiredGeneration: 1, FencingToken: 1,
	}
}

func destroyT3VM(ctx context.Context, service *Service, profile t3SmokeProfile, target *pluginv1.TargetContext, input *pluginv1.ResourceMutationInput, state *pluginv1.ProviderState) error {
	destroyInput := proto.Clone(input).(*pluginv1.ResourceMutationInput)
	destroyInput.Context = t3Call(profile, "destroy-vm", profile.VMResourceID)
	destroyInput.Target = target
	destroyInput.PriorState = state
	stream := &destroyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: destroyInput}, stream); err != nil {
		return err
	}
	result := finalDestroyResult(stream.events)
	if result == nil || result.GetError() != nil {
		return fmt.Errorf("owned VM destroy failed: %#v", result)
	}
	return nil
}

func destroyT3Network(ctx context.Context, service *Service, profile t3SmokeProfile, target *pluginv1.TargetContext, input *pluginv1.ResourceMutationInput, state *pluginv1.ProviderState) error {
	destroyInput := proto.Clone(input).(*pluginv1.ResourceMutationInput)
	destroyInput.Context = t3Call(profile, "destroy-network", profile.NetworkResourceID)
	destroyInput.Target = target
	destroyInput.PriorState = state
	stream := &destroyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: destroyInput}, stream); err != nil {
		return err
	}
	result := finalDestroyResult(stream.events)
	if result == nil || result.GetError() != nil {
		return fmt.Errorf("network binding destroy failed: %#v", result)
	}
	return nil
}

func finalApplyResult(events []*pluginv1.ApplyResourceResponse) *pluginv1.ResourceResult {
	for index := len(events) - 1; index >= 0; index-- {
		if result := events[index].GetEvent().GetResult(); result != nil {
			return result
		}
	}
	return nil
}

func finalDestroyResult(events []*pluginv1.DestroyResourceResponse) *pluginv1.ResourceResult {
	for index := len(events) - 1; index >= 0; index-- {
		if result := events[index].GetEvent().GetResult(); result != nil {
			return result
		}
	}
	return nil
}

func finalLifecycleResult(events []*pluginv1.LifecycleResponse) *pluginv1.ResourceResult {
	for index := len(events) - 1; index >= 0; index-- {
		if result := events[index].GetEvent().GetResult(); result != nil {
			return result
		}
	}
	return nil
}

func capabilitySupport(response *pluginv1.DiscoverTargetResponse, feature string) pluginv1.Capability_Support {
	for _, capability := range response.GetCapabilities() {
		if capability.GetFeatureId() == feature {
			return capability.GetSupport()
		}
	}
	return pluginv1.Capability_SUPPORT_UNSUPPORTED
}

func claimSetEquals(claims []*pluginv1.AllocationClaim, namespace string, value uint32) bool {
	return len(claims) == 1 && claims[0].GetNamespace() == namespace && claims[0].GetValue() == fmt.Sprintf("%d", value)
}

func rangeContains(ranges []IntegerRange, value uint32) bool {
	for _, candidate := range ranges {
		if value >= candidate.Start && value <= candidate.End {
			return true
		}
	}
	return false
}

func randomT3Token(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func readT3File(path string, maximum int, private bool) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > int64(maximum) {
		return nil, errors.New("T3 input must be a bounded non-symlink regular file")
	}
	if private && before.Mode().Perm()&0o077 != 0 || !private && before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("T3 input permissions are unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || private && after.Mode().Perm()&0o077 != 0 || !private && after.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("T3 input identity changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(contents) == 0 || len(contents) > maximum {
		zeroBytesForT3(contents)
		return nil, errors.New("T3 input could not be read within its bound")
	}
	return contents, nil
}

func zeroBytesForT3(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

// t3ReservedPlan keeps qualification away from existing allocations without
// changing the exact target configuration being qualified. Core admissions must
// reserve these test claims for the run; expected-claim assertions still apply.
func t3ReservedPlan(t *testing.T, service *Service, ctx context.Context, request *pluginv1.PlanResourceRequest, firstVMID, firstVLAN uint32) (*pluginv1.PlanResourceResponse, error) {
	t.Helper()
	var config TargetConfig
	if err := decodeEnvelope(request.Input.Target.ConfigEnvelope, maximumConfigBytes, &config); err != nil {
		t.Fatal(err)
	}
	excluded := cloneClaims(request.ExcludedAllocations)
	seen := map[string]bool{}
	for _, c := range excluded {
		seen[c.Namespace+"/"+c.Value] = true
	}
	add := func(namespace string, value uint32) {
		key := fmt.Sprint(value)
		if !seen[namespace+"/"+key] {
			excluded = append(excluded, &pluginv1.AllocationClaim{Namespace: namespace, Value: key})
			seen[namespace+"/"+key] = true
		}
	}
	for _, r := range config.VMIDRanges {
		for v := r.Start; v < firstVMID && v <= r.End; v++ {
			add(vmidClaimNamespace, v)
		}
	}
	for _, n := range config.Networks {
		for _, r := range n.VLANRanges {
			for v := r.Start; v < firstVLAN && v <= r.End; v++ {
				add(vlanClaimNamespace, v)
			}
		}
	}
	request.ExcludedAllocations = excluded
	return service.PlanResource(ctx, request)
}
