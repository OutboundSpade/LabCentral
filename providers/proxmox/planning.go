package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func (service *Service) ValidateLab(_ context.Context, request *pluginv1.ValidateLabRequest) (*pluginv1.ValidateLabResponse, error) {
	response := &pluginv1.ValidateLabResponse{}
	if request == nil || request.GetTarget() == nil {
		response.Errors = append(response.Errors, mutationError("invalid_target", "invalid_declaration", "Proxmox target context is required", "target", ""))
		return response, nil
	}
	config, err := mutationConfig(request.GetTarget())
	if err != nil {
		response.Errors = append(response.Errors, mutationError("invalid_target_config", "invalid_declaration", "Proxmox mutation target configuration is invalid", "target.configEnvelope", ""))
		return response, nil
	}
	if !config.MutationEnabled {
		response.Errors = append(response.Errors, mutationError("mutation_disabled", "policy_denied", "Proxmox mutation is disabled for this target", "target.configEnvelope.mutationEnabled", ""))
		return response, nil
	}
	var lab canonicalLabDocument
	if err := decodeEnvelope(request.GetCanonicalLabJson(), maximumConfigBytes, &lab); err != nil {
		response.Errors = append(response.Errors, mutationError("invalid_canonical_lab", "invalid_declaration", "Canonical Lab JSON is invalid", "canonicalLab", ""))
		return response, nil
	}
	var override ProviderOverride
	if len(request.GetProviderOverrideJson()) != 0 {
		if err := decodeEnvelope(request.GetProviderOverrideJson(), maximumConfigBytes, &override); err != nil {
			response.Errors = append(response.Errors, mutationError("invalid_provider_override", "invalid_declaration", "Proxmox provider override is invalid", "providerOverride", ""))
			return response, nil
		}
	}
	selection, errors := validateCanonicalLab(config, lab, override)
	response.Errors = append(response.Errors, errors...)
	if len(response.Errors) != 0 {
		return response, nil
	}
	plan := ValidationPlan{
		SchemaVersion: validationPlanV10, TargetID: request.GetTarget().GetTargetId(),
		ConfigFingerprint: request.GetTarget().GetConfigFingerprint(), CanonicalLabDigest: digestDocument(request.GetCanonicalLabJson()),
		CapabilityDigest: config.Qualification.CapabilityDigest, InventoryDigest: config.Qualification.InventoryDigest,
		PolicyDigest: config.Qualification.PolicyDigest, TemplateAlias: selection.template,
		StorageAlias: selection.storage, NetworkAlias: selection.network,
	}
	aliases := map[string]bool{}
	for _, binding := range selection.resources {
		aliases[binding.TemplateAlias] = true
	}
	if len(aliases) > 1 {
		plan.SchemaVersion = validationPlanV11
		plan.TemplateAlias = ""
		plan.ResourceTemplates = selection.resources
		sort.Slice(plan.ResourceTemplates, func(i, j int) bool {
			return plan.ResourceTemplates[i].ResourceID < plan.ResourceTemplates[j].ResourceID
		})
	}
	contents, digest, err := marshalDigest(plan)
	if err != nil {
		return nil, fmt.Errorf("encode Proxmox validation plan: %w", err)
	}
	response.ProviderPlan = contents
	response.ProviderPlanDigest = strings.TrimPrefix(digest, "sha256:")
	return response, nil
}

type profileSelection struct {
	resources []ResourceTemplateSelection
	template  string
	storage   string
	network   string
}

func validateCanonicalLab(config TargetConfig, lab canonicalLabDocument, override ProviderOverride) (profileSelection, []*pluginv1.StructuredError) {
	selection := profileSelection{storage: config.Storages[0].Alias, network: config.Networks[0].Alias}
	if lab.APIVersion != "canonical.labcentral.io/v1alpha1" || lab.Metadata.Name == "" || len(lab.Resources) == 0 {
		return selection, []*pluginv1.StructuredError{mutationError("unsupported_canonical_lab", "invalid_declaration", "Canonical Lab identity or version is unsupported", "apiVersion", "")}
	}
	if lab.Requirements.IsolationClass != "" && lab.Requirements.IsolationClass != "virtualMachine" {
		return selection, []*pluginv1.StructuredError{mutationError("unsupported_isolation", "unsupported_capability", "Proxmox QEMU requires virtualMachine isolation", "requirements.isolationClass", "")}
	}
	if override.StorageAlias != "" {
		selection.storage = override.StorageAlias
	}
	if override.NetworkAlias != "" {
		selection.network = override.NetworkAlias
	}
	if _, ok := findStorageProfile(config, selection.storage); !ok {
		return selection, []*pluginv1.StructuredError{mutationError("unknown_provider_profile", "invalid_declaration", "Provider override references an unavailable profile", "providerOverride", "")}
	}
	if _, ok := findNetworkProfile(config, selection.network); !ok {
		return selection, []*pluginv1.StructuredError{mutationError("unknown_provider_profile", "invalid_declaration", "Provider override references an unavailable profile", "providerOverride", "")}
	}

	// Validate translated physical specs without changing the portable declaration.
	lab.Resources = append([]canonicalResource(nil), lab.Resources...)
	for i, resource := range lab.Resources {
		if resource.Kind != "Container" {
			continue
		}
		translated, _, err := containerVM(config, resource)
		if err != nil {
			return selection, []*pluginv1.StructuredError{mutationError("unsupported_container", "unsupported_capability", err.Error(), "resources", resource.ID)}
		}
		lab.Resources[i] = translated
	}
	networks := make(map[string]canonicalNetworkSpec)
	resourceIDs := make(map[string]string)
	vmCount := uint32(0)
	var result []*pluginv1.StructuredError
	for index, resource := range lab.Resources {
		field := fmt.Sprintf("resources[%d]", index)
		if !logicalIDPattern.MatchString(resource.ID) {
			result = append(result, mutationError("invalid_resource_id", "invalid_declaration", "Logical resource ID is invalid", field+".id", resource.ID))
			continue
		}
		if _, duplicate := resourceIDs[resource.ID]; duplicate {
			result = append(result, mutationError("duplicate_resource_id", "invalid_declaration", "Logical resource ID is duplicated", field+".id", resource.ID))
			continue
		}
		resourceIDs[resource.ID] = resource.Kind
		switch resource.Kind {
		case "Network":
			var spec canonicalNetworkSpec
			if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil {
				result = append(result, mutationError("invalid_network", "invalid_declaration", "Network specification is invalid", field+".spec", resource.ID))
				continue
			}
			if err := validateNetworkSpec(spec); err != nil {
				result = append(result, mutationError("unsupported_network", "unsupported_capability", err.Error(), field+".spec", resource.ID))
				continue
			}
			networks[resource.ID] = spec
		case "VM":
			vmCount++
			var spec canonicalVMSpec
			if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil {
				result = append(result, mutationError("invalid_vm", "invalid_declaration", "VM specification is invalid", field+".spec", resource.ID))
				continue
			}
			result = append(result, validateVMSpec(config, resource, spec, field)...)
			matched := matchingTemplate(config, spec.Image.Digest, override.TemplateAlias)
			if matched == "" && strings.HasPrefix(spec.Image.Repository, "labcentral.local/images/") && sha256Pattern.MatchString(spec.Image.Digest) {
				if profile, ok := importedTemplateProfile(config, strings.TrimPrefix(spec.Image.Repository, "labcentral.local/images/")); ok && profile.ContentDigest == spec.Image.Digest && (override.TemplateAlias == "" || override.TemplateAlias == profile.Alias) {
					matched = profile.Alias
				}
			}
			if matched == "" {
				result = append(result, mutationError("unsupported_image", "unsupported_capability", "VM image does not match an approved Proxmox template", field+".spec.image", resource.ID))
			} else {
				selection.resources = append(selection.resources, ResourceTemplateSelection{ResourceID: resource.ID, TemplateAlias: matched})
				if selection.template == "" {
					selection.template = matched
				}
			}
		case "Volume":
			result = append(result, mutationError("unsupported_volume", "unsupported_capability", "Portable Volume resources are not supported by the Proxmox QEMU subset", field, resource.ID))
		default:
			result = append(result, mutationError("unsupported_resource_kind", "unsupported_capability", "Resource kind is not supported by the Proxmox QEMU subset", field+".kind", resource.ID))
		}
	}
	if vmCount > config.Limits.MaxVMs {
		result = append(result, mutationError("vm_limit_exceeded", "quota_exceeded", "Lab exceeds the configured Proxmox VM limit", "resources", ""))
	}
	for index, resource := range lab.Resources {
		if resource.Kind != "VM" {
			continue
		}
		var spec canonicalVMSpec
		if decodeEnvelope(resource.Spec, maximumConfigBytes, &spec) != nil {
			continue
		}
		dependencies := stringSet(resource.DependsOn)
		for interfaceIndex, networkInterface := range spec.Interfaces {
			if _, exists := networks[networkInterface.NetworkRef]; !exists || !dependencies[networkInterface.NetworkRef] {
				result = append(result, mutationError("invalid_network_dependency", "invalid_declaration", "VM interface must reference an earlier isolated Network dependency", fmt.Sprintf("resources[%d].spec.interfaces[%d].networkRef", index, interfaceIndex), resource.ID))
			}
		}
	}
	if selection.template == "" && len(config.Templates) > 0 {
		templates := append([]TemplateProfile(nil), config.Templates...)
		sort.Slice(templates, func(i, j int) bool { return templates[i].Alias < templates[j].Alias })
		selection.template = templates[0].Alias
	}
	return selection, result
}

func validateNetworkSpec(spec canonicalNetworkSpec) error {
	prefix, err := netip.ParsePrefix(spec.IPv4CIDR)
	if spec.Kind != "isolated" || err != nil || !prefix.Addr().Is4() || spec.IPv6CIDR != "" || spec.IPAM.Mode != "static" || spec.Egress.Mode != "deny" || len(spec.Egress.Destinations) != 0 {
		return fmt.Errorf("only isolated static IPv4 networks with denied egress are supported")
	}
	if spec.MTU != 0 && (spec.MTU < 576 || spec.MTU > 9216) {
		return fmt.Errorf("network MTU is outside the supported range")
	}
	return nil
}

func validateVMSpec(config TargetConfig, resource canonicalResource, spec canonicalVMSpec, field string) []*pluginv1.StructuredError {
	var result []*pluginv1.StructuredError
	if spec.OSFamily != "linux" || spec.Compute.Architecture != "amd64" {
		result = append(result, mutationError("unsupported_guest", "unsupported_capability", "Only Linux amd64 VMs are supported", field+".spec", resource.ID))
	}
	if spec.Compute.CPUMillis == 0 || spec.Compute.CPUMillis > config.Limits.MaxCPUMillis || spec.Compute.MemoryBytes == 0 || spec.Compute.MemoryBytes > config.Limits.MaxMemoryBytes {
		result = append(result, mutationError("compute_limit_exceeded", "quota_exceeded", "VM compute exceeds configured target limits", field+".spec.compute", resource.ID))
	}
	if len(spec.Interfaces) > int(config.Limits.MaxNICs) || len(spec.Interfaces) > 16 {
		result = append(result, mutationError("nic_limit_exceeded", "quota_exceeded", "VM interface count exceeds configured target limits", field+".spec.interfaces", resource.ID))
	}
	for index, networkInterface := range spec.Interfaces {
		address, err := netip.ParseAddr(networkInterface.IPv4)
		if err != nil || !address.Is4() || networkInterface.IPv6 != "" || networkInterface.MAC != "" {
			result = append(result, mutationError("unsupported_network_address", "unsupported_capability", "VM interfaces require provider-derived MAC and one static IPv4 address", fmt.Sprintf("%s.spec.interfaces[%d]", field, index), resource.ID))
		}
	}
	if len(spec.Mounts) != 0 {
		result = append(result, mutationError("unsupported_volume", "unsupported_capability", "VM mounts are not supported", field+".spec.mounts", resource.ID))
	}
	if spec.Provisioning != nil {
		result = append(result, mutationError("unsupported_guest_artifact", "unsupported_capability", "Author-supplied guest provisioning is not supported", field+".spec.provisioning", resource.ID))
	}
	if spec.Lifecycle.Snapshot || spec.Lifecycle.Reset != "" {
		result = append(result, mutationError("unsupported_snapshot", "unsupported_capability", "Snapshot and reset lifecycle requests are not supported", field+".spec.lifecycle", resource.ID))
	}
	if spec.Image.Ref != "" || spec.Image.Repository == "" || !sha256Pattern.MatchString(spec.Image.Digest) {
		result = append(result, mutationError("unsupported_image", "unsupported_capability", "VM image must be an approved digest-pinned template reference", field+".spec.image", resource.ID))
	}
	return result
}

func matchingTemplate(config TargetConfig, contentDigest, override string) string {
	for _, profile := range config.Templates {
		if profile.ContentDigest == contentDigest && (override == "" || profile.Alias == override) {
			return profile.Alias
		}
	}
	return ""
}

func (service *Service) PlanResource(_ context.Context, request *pluginv1.PlanResourceRequest) (*pluginv1.PlanResourceResponse, error) {
	response := &pluginv1.PlanResourceResponse{}
	if request == nil || request.GetInput() == nil || request.GetInput().GetTarget() == nil || request.GetInput().GetContext() == nil {
		response.Error = mutationError("invalid_plan_request", "invalid_declaration", "Proxmox plan request is incomplete", "input", "")
		return response, nil
	}
	input := request.GetInput()
	config, err := mutationConfig(input.GetTarget())
	if err != nil || !config.MutationEnabled {
		response.Error = mutationError("mutation_disabled", "policy_denied", "Proxmox mutation is disabled or invalid for this target", "input.target", input.GetContext().GetLogicalResourceId())
		return response, nil
	}
	validation, err := decodeValidationPlan(input.GetProviderPlan())
	if err != nil || validation.TargetID != input.GetTarget().GetTargetId() || validation.ConfigFingerprint != input.GetTarget().GetConfigFingerprint() || validation.CapabilityDigest != config.Qualification.CapabilityDigest || validation.InventoryDigest != config.Qualification.InventoryDigest || validation.PolicyDigest != config.Qualification.PolicyDigest {
		response.Error = mutationError("stale_plan", "conflict", "Validated Proxmox plan binding is missing or stale", "input.providerPlan", input.GetContext().GetLogicalResourceId())
		return response, nil
	}
	var resource canonicalResource
	if err := decodeEnvelope(input.GetCanonicalResourceJson(), maximumConfigBytes, &resource); err != nil || resource.ID != input.GetContext().GetLogicalResourceId() {
		response.Error = mutationError("invalid_resource", "invalid_declaration", "Canonical resource JSON is invalid or mismatched", "input.canonicalResource", input.GetContext().GetLogicalResourceId())
		return response, nil
	}
	call := input.GetContext()
	if call.GetLabInstanceId() == "" || !logicalIDPattern.MatchString(call.GetLogicalResourceId()) || call.GetDesiredGeneration() == 0 || call.GetProviderTargetId() != input.GetTarget().GetTargetId() || len(input.GetOwnershipTokens()) != 1 || input.GetOwnershipTokens()[0] == "" {
		response.Error = mutationError("invalid_plan_context", "invalid_declaration", "Proxmox plan context is invalid", "input.context", resource.ID)
		return response, nil
	}
	plan := QEMUPlan{
		SchemaVersion: qemuPlanV10, Action: "ensure", ResourceKind: resource.Kind,
		TargetID: validation.TargetID, ConfigFingerprint: validation.ConfigFingerprint,
		CapabilityDigest: validation.CapabilityDigest, InventoryDigest: validation.InventoryDigest,
		PolicyDigest: validation.PolicyDigest, InputDigest: digestDocument(input.GetCanonicalResourceJson()),
		InstanceID: call.GetLabInstanceId(), LogicalResourceID: resource.ID, Generation: call.GetDesiredGeneration(),
		Allocations: []AllocationBinding{}, Networks: []NetworkBinding{}, Warnings: []PlanWarning{},
	}
	excluded, err := excludedClaimSet(request.GetExcludedAllocations())
	if err != nil {
		response.Error = mutationError("invalid_excluded_claim", "invalid_declaration", "Excluded allocation claims are invalid", "excludedAllocations", resource.ID)
		return response, nil
	}

	if resource.Kind == "Container" {
		translated, runtime, err := containerVM(config, resource)
		if err != nil {
			response.Error = mutationError("unsupported_container", "unsupported_capability", err.Error(), "resource", resource.ID)
			return response, nil
		}
		runtime.InstanceID = plan.InstanceID
		runtime.Generation = plan.Generation
		plan.Container = &runtime
		plan.SchemaVersion = qemuPlanV11
		plan.ResourceKind = "VM"
		resource = translated
	}
	switch resource.Kind {
	case "Network":
		if err := planNetwork(&plan, config, validation, resource, excluded); err != nil {
			response.Error = mutationError("allocation_exhausted", "quota_exceeded", err.Error(), "excludedAllocations", resource.ID)
			return response, nil
		}
	case "VM":
		if err := planVM(&plan, config, validation, input, resource, excluded); err != nil {
			response.Error = mutationError("invalid_vm_plan", "invalid_declaration", err.Error(), "input", resource.ID)
			return response, nil
		}
	default:
		response.Error = mutationError("unsupported_resource_kind", "unsupported_capability", "Resource kind is not supported by the Proxmox QEMU subset", "input.canonicalResource.kind", resource.ID)
		return response, nil
	}
	normalizeAllocations(plan.Allocations)
	if err := plan.validate(); err != nil {
		return nil, fmt.Errorf("constructed invalid Proxmox plan: %w", err)
	}
	if err := validatePlanBindings(config, plan, input.GetCanonicalResourceJson()); err != nil {
		return nil, fmt.Errorf("constructed unbound Proxmox plan: %w", err)
	}
	contents, digest, err := marshalDigest(plan)
	if err != nil {
		return nil, fmt.Errorf("encode Proxmox plan: %w", err)
	}
	response.ProviderPlan = contents
	response.ProviderPlanDigest = strings.TrimPrefix(digest, "sha256:")
	for _, allocation := range plan.Allocations {
		if allocation.Namespace == vmidClaimNamespace || resource.Kind == "Network" {
			response.ProposedAllocations = append(response.ProposedAllocations, &pluginv1.AllocationClaim{Namespace: allocation.Namespace, Value: allocation.Value})
		}
	}
	return response, nil
}

func mutationConfig(target *pluginv1.TargetContext) (TargetConfig, error) {
	var config TargetConfig
	if target == nil || target.GetTargetId() == "" || len(target.GetTargetId()) > 128 || !sha256Pattern.MatchString(target.GetConfigFingerprint()) {
		return config, fmt.Errorf("invalid target identity")
	}
	if err := decodeEnvelope(target.GetConfigEnvelope(), maximumConfigBytes, &config); err != nil {
		return config, err
	}
	if err := config.validateMutation(); err != nil {
		return config, err
	}
	return config, nil
}

func decodeValidationPlan(contents []byte) (ValidationPlan, error) {
	var plan ValidationPlan
	if err := decodeEnvelope(contents, maximumConfigBytes, &plan); err != nil {
		return plan, err
	}
	if (plan.SchemaVersion != validationPlanV10 && plan.SchemaVersion != validationPlanV11) || plan.TargetID == "" || !sha256Pattern.MatchString(plan.ConfigFingerprint) || !sha256Pattern.MatchString(plan.CanonicalLabDigest) || !sha256Pattern.MatchString(plan.CapabilityDigest) || !sha256Pattern.MatchString(plan.InventoryDigest) || !sha256Pattern.MatchString(plan.PolicyDigest) || !aliasPattern.MatchString(plan.StorageAlias) || !aliasPattern.MatchString(plan.NetworkAlias) {
		return plan, fmt.Errorf("invalid validation plan")
	}
	if plan.SchemaVersion == validationPlanV10 {
		if !aliasPattern.MatchString(plan.TemplateAlias) || len(plan.ResourceTemplates) != 0 {
			return plan, fmt.Errorf("invalid legacy template selection")
		}
	} else {
		if plan.TemplateAlias != "" || len(plan.ResourceTemplates) == 0 || len(plan.ResourceTemplates) > maximumInventoryItems {
			return plan, fmt.Errorf("invalid resource template selection")
		}
		seen := map[string]bool{}
		for _, binding := range plan.ResourceTemplates {
			if !logicalIDPattern.MatchString(binding.ResourceID) || !aliasPattern.MatchString(binding.TemplateAlias) || seen[binding.ResourceID] {
				return plan, fmt.Errorf("invalid resource template binding")
			}
			seen[binding.ResourceID] = true
		}
	}
	return plan, nil
}

func planNetwork(plan *QEMUPlan, config TargetConfig, validation ValidationPlan, resource canonicalResource, excluded map[string]struct{}) error {
	var spec canonicalNetworkSpec
	if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil || validateNetworkSpec(spec) != nil {
		return fmt.Errorf("network specification is outside the supported subset")
	}
	profile, ok := findNetworkProfile(config, validation.NetworkAlias)
	if !ok {
		return fmt.Errorf("validated network profile is unavailable")
	}
	value, source, ok := firstAvailable(profile.VLANRanges, vlanClaimNamespace, excluded)
	if !ok {
		return fmt.Errorf("approved VLAN ranges are exhausted")
	}
	claim := AllocationBinding{Namespace: vlanClaimNamespace, Value: strconv.FormatUint(uint64(value), 10), SourceRange: source}
	plan.Allocations = append(plan.Allocations, claim)
	plan.Networks = append(plan.Networks, NetworkBinding{LogicalResourceID: resource.ID, ProfileAlias: profile.Alias, Bridge: profile.Bridge, VLAN: value, IPv4CIDR: spec.IPv4CIDR})
	plan.Steps = []string{"bind-network"}
	return nil
}

func planVM(plan *QEMUPlan, config TargetConfig, validation ValidationPlan, input *pluginv1.ResourceMutationInput, resource canonicalResource, excluded map[string]struct{}) error {
	var spec canonicalVMSpec
	if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil {
		return fmt.Errorf("VM specification is invalid")
	}
	if errors := validateVMSpec(config, resource, spec, "resource"); len(errors) != 0 {
		return fmt.Errorf("VM specification is outside the supported subset")
	}
	alias := validation.TemplateAlias
	if validation.SchemaVersion == validationPlanV11 {
		for _, binding := range validation.ResourceTemplates {
			if binding.ResourceID == resource.ID {
				alias = binding.TemplateAlias
			}
		}
	}
	template, ok := findTemplateProfile(config, alias)
	if !ok || template.ContentDigest != spec.Image.Digest {
		return fmt.Errorf("validated template profile is unavailable or mismatched")
	}
	storage, ok := findStorageProfile(config, validation.StorageAlias)
	if !ok {
		return fmt.Errorf("validated storage profile is unavailable")
	}
	vmid, source, ok := firstAvailable(config.VMIDRanges, vmidClaimNamespace, excluded)
	if !ok {
		return fmt.Errorf("approved VMID ranges are exhausted")
	}
	vmidClaim := AllocationBinding{Namespace: vmidClaimNamespace, Value: strconv.FormatUint(uint64(vmid), 10), SourceRange: source}
	plan.Allocations = append(plan.Allocations, vmidClaim)
	plan.Template = &PlanTemplate{Alias: template.Alias, SourceNode: template.SourceNode, VMID: template.VMID, ConfigDigest: template.ConfigDigest, ContentDigest: template.ContentDigest}
	plan.Destination = &PlanDestination{Node: config.DestinationNode, Pool: config.Pool, StorageAlias: storage.Alias, StorageID: storage.ID, StorageFormat: storage.Format, FullClone: true}
	plan.Ownership = &OwnershipMarker{InstallationID: config.InstallationID, TargetID: plan.TargetID, InstanceID: plan.InstanceID, LogicalResource: plan.LogicalResourceID, Generation: plan.Generation, Token: input.GetOwnershipTokens()[0]}
	plan.Hardware = &PlanHardware{Sockets: 1, Cores: uint32(math.Ceil(float64(spec.Compute.CPUMillis) / 1000)), CPULimit: formatCPULimit(spec.Compute.CPUMillis), MemoryMiB: bytesToMiBRoundUp(spec.Compute.MemoryBytes), CIUpgrade: false, OnBoot: false}
	for _, disk := range template.Disks {
		plan.Disks = append(plan.Disks, PlanDisk{Device: disk.Device, SizeBytes: disk.SizeBytes, Boot: disk.Boot, StorageID: storage.ID, StorageFormat: storage.Format})
	}
	dependencies := make(map[string]QEMUPlan)
	for _, dependency := range input.GetDependencies() {
		if dependency.GetLogicalResourceId() == "" || digestDocument(dependency.GetProviderPlan()) != "sha256:"+dependency.GetProviderPlanDigest() {
			return fmt.Errorf("dependency plan digest is invalid")
		}
		if _, duplicate := dependencies[dependency.GetLogicalResourceId()]; duplicate {
			return fmt.Errorf("network dependency plan is duplicated")
		}
		var dependencyPlan QEMUPlan
		if err := decodeEnvelope(dependency.GetProviderPlan(), maximumConfigBytes, &dependencyPlan); err != nil || dependencyPlan.validate() != nil || dependencyPlan.ResourceKind != "Network" || dependencyPlan.TargetID != plan.TargetID || dependencyPlan.ConfigFingerprint != plan.ConfigFingerprint || dependencyPlan.CapabilityDigest != plan.CapabilityDigest || dependencyPlan.InventoryDigest != plan.InventoryDigest || dependencyPlan.PolicyDigest != plan.PolicyDigest || dependencyPlan.InstanceID != plan.InstanceID || dependencyPlan.LogicalResourceID != dependency.GetLogicalResourceId() || validateNetworkPlanProfile(config, dependencyPlan) != nil {
			return fmt.Errorf("network dependency plan is invalid")
		}
		dependencies[dependency.GetLogicalResourceId()] = dependencyPlan
	}
	allocationSeen := map[string]bool{vmidClaim.Namespace + "\x00" + vmidClaim.Value: true}
	for index, networkInterface := range spec.Interfaces {
		dependency, exists := dependencies[networkInterface.NetworkRef]
		if !exists || len(dependency.Networks) != 1 {
			return fmt.Errorf("VM network dependency is missing")
		}
		binding := dependency.Networks[0]
		prefix, err := netip.ParsePrefix(binding.IPv4CIDR)
		address, addressErr := netip.ParseAddr(networkInterface.IPv4)
		if err != nil || addressErr != nil || !prefix.Contains(address) {
			return fmt.Errorf("VM IPv4 address is outside its dependency network")
		}
		binding.IPv4Address = address.String()
		binding.MAC = deterministicMAC(plan.TargetID, plan.InstanceID, plan.LogicalResourceID, index)
		plan.Networks = append(plan.Networks, binding)
		for _, allocation := range dependency.Allocations {
			key := allocation.Namespace + "\x00" + allocation.Value
			if !allocationSeen[key] {
				plan.Allocations = append(plan.Allocations, allocation)
				allocationSeen[key] = true
			}
		}
	}
	plan.Steps = []string{"preflight", "clone", "wait-clone", "configure", "verify-config", "start", "wait-start", "guest-ready"}
	return nil
}

func firstAvailable(ranges []IntegerRange, namespace string, excluded map[string]struct{}) (uint32, string, bool) {
	ordered := append([]IntegerRange(nil), ranges...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	for _, allocationRange := range ordered {
		for value := allocationRange.Start; value <= allocationRange.End; value++ {
			text := strconv.FormatUint(uint64(value), 10)
			if _, blocked := excluded[namespace+"\x00"+text]; !blocked {
				return value, fmt.Sprintf("%d-%d", allocationRange.Start, allocationRange.End), true
			}
			if value == math.MaxUint32 {
				break
			}
		}
	}
	return 0, "", false
}

func excludedClaimSet(claims []*pluginv1.AllocationClaim) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if claim == nil || (claim.GetNamespace() != vmidClaimNamespace && claim.GetNamespace() != vlanClaimNamespace) {
			return nil, fmt.Errorf("invalid claim namespace")
		}
		if _, err := strconv.ParseUint(claim.GetValue(), 10, 32); err != nil || strings.HasPrefix(claim.GetValue(), "0") {
			return nil, fmt.Errorf("invalid claim value")
		}
		key := claim.GetNamespace() + "\x00" + claim.GetValue()
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate claim")
		}
		result[key] = struct{}{}
	}
	return result, nil
}

func deterministicMAC(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%v\x00", part)
	}
	value := hash.Sum(nil)
	value[0] = (value[0] | 0x02) & 0xfe
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", value[0], value[1], value[2], value[3], value[4], value[5])
}

func formatCPULimit(millis uint32) string {
	whole := millis / 1000
	fraction := millis % 1000
	if fraction == 0 {
		return strconv.FormatUint(uint64(whole), 10)
	}
	return strings.TrimRight(fmt.Sprintf("%d.%03d", whole, fraction), "0")
}

func bytesToMiBRoundUp(value uint64) uint64 {
	if value == 0 {
		return 0
	}
	return (value-1)/(1<<20) + 1
}

func validatePlanBindings(config TargetConfig, plan QEMUPlan, resourceJSON []byte) error {
	var resource canonicalResource
	if err := decodeEnvelope(resourceJSON, maximumConfigBytes, &resource); err != nil || resource.ID != plan.LogicalResourceID {
		return fmt.Errorf("plan resource binding is invalid")
	}

	var bindingErr error
	resource, bindingErr = runtimeBinding(config, plan, resource)
	if bindingErr != nil || resource.Kind != plan.ResourceKind {
		return fmt.Errorf("plan runtime resource binding invalid")
	}
	if plan.ResourceKind == "Network" {
		var spec canonicalNetworkSpec
		if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil || validateNetworkSpec(spec) != nil {
			return fmt.Errorf("plan network specification is invalid")
		}
		binding := plan.Networks[0]
		if binding.IPv4CIDR != spec.IPv4CIDR || binding.MAC != "" || binding.IPv4Address != "" || binding.Gateway != "" || validateNetworkPlanProfile(config, plan) != nil {
			return fmt.Errorf("plan network profile binding is invalid")
		}
		return nil
	}

	var spec canonicalVMSpec
	if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil || len(validateVMSpec(config, resource, spec, "resource")) != 0 {
		return fmt.Errorf("plan VM specification is invalid")
	}
	template, ok := findTemplateProfile(config, plan.Template.Alias)
	if !ok || template.SourceNode != plan.Template.SourceNode || template.VMID != plan.Template.VMID || template.ConfigDigest != plan.Template.ConfigDigest || template.ContentDigest != plan.Template.ContentDigest || template.ContentDigest != spec.Image.Digest {
		return fmt.Errorf("plan template binding is invalid")
	}
	storage, ok := findStorageProfile(config, plan.Destination.StorageAlias)
	if !ok || plan.Destination.Node != config.DestinationNode || plan.Destination.Pool != config.Pool || plan.Destination.StorageID != storage.ID || plan.Destination.StorageFormat != storage.Format || plan.Ownership.InstallationID != config.InstallationID {
		return fmt.Errorf("plan destination binding is invalid")
	}
	expectedHardware := PlanHardware{Sockets: 1, Cores: uint32(math.Ceil(float64(spec.Compute.CPUMillis) / 1000)), CPULimit: formatCPULimit(spec.Compute.CPUMillis), MemoryMiB: bytesToMiBRoundUp(spec.Compute.MemoryBytes)}
	if *plan.Hardware != expectedHardware {
		return fmt.Errorf("plan hardware binding is invalid")
	}
	if len(plan.Disks) != len(template.Disks) {
		return fmt.Errorf("plan disk binding is invalid")
	}
	for index, disk := range template.Disks {
		expected := PlanDisk{Device: disk.Device, SizeBytes: disk.SizeBytes, Boot: disk.Boot, StorageID: storage.ID, StorageFormat: storage.Format}
		if plan.Disks[index] != expected {
			return fmt.Errorf("plan disk binding is invalid")
		}
	}
	if len(plan.Networks) != len(spec.Interfaces) {
		return fmt.Errorf("plan network count is invalid")
	}
	for index, networkInterface := range spec.Interfaces {
		binding := plan.Networks[index]
		address, err := netip.ParseAddr(networkInterface.IPv4)
		profile, profileOK := findNetworkProfile(config, binding.ProfileAlias)
		if err != nil || !profileOK || binding.LogicalResourceID != networkInterface.NetworkRef || binding.Bridge != profile.Bridge || binding.IPv4Address != address.String() || binding.MAC != deterministicMAC(plan.TargetID, plan.InstanceID, plan.LogicalResourceID, index) || binding.Gateway != "" || !valueInRanges(binding.VLAN, profile.VLANRanges) {
			return fmt.Errorf("plan network binding is invalid")
		}
	}
	for _, allocation := range plan.Allocations {
		switch allocation.Namespace {
		case vmidClaimNamespace:
			if !allocationMatchesRanges(allocation, config.VMIDRanges) {
				return fmt.Errorf("plan VMID allocation binding is invalid")
			}
		case vlanClaimNamespace:
			matched := false
			for _, binding := range plan.Networks {
				profile, _ := findNetworkProfile(config, binding.ProfileAlias)
				if allocation.Value == strconv.FormatUint(uint64(binding.VLAN), 10) && allocationMatchesRanges(allocation, profile.VLANRanges) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("plan VLAN allocation binding is invalid")
			}
		}
	}
	return nil
}

func validateNetworkPlanProfile(config TargetConfig, plan QEMUPlan) error {
	if plan.ResourceKind != "Network" || len(plan.Networks) != 1 || len(plan.Allocations) != 1 {
		return fmt.Errorf("network plan profile is invalid")
	}
	binding := plan.Networks[0]
	profile, ok := findNetworkProfile(config, binding.ProfileAlias)
	if !ok || binding.Bridge != profile.Bridge || !valueInRanges(binding.VLAN, profile.VLANRanges) || !allocationMatchesRanges(plan.Allocations[0], profile.VLANRanges) {
		return fmt.Errorf("network plan profile is not configured")
	}
	return nil
}

func allocationMatchesRanges(allocation AllocationBinding, ranges []IntegerRange) bool {
	value, err := strconv.ParseUint(allocation.Value, 10, 32)
	if err != nil {
		return false
	}
	for _, allocationRange := range ranges {
		if uint64(allocationRange.Start) <= value && value <= uint64(allocationRange.End) && allocation.SourceRange == fmt.Sprintf("%d-%d", allocationRange.Start, allocationRange.End) {
			return true
		}
	}
	return false
}

func valueInRanges(value uint32, ranges []IntegerRange) bool {
	for _, allocationRange := range ranges {
		if value >= allocationRange.Start && value <= allocationRange.End {
			return true
		}
	}
	return false
}

func findTemplateProfile(config TargetConfig, alias string) (TemplateProfile, bool) {
	for _, profile := range config.Templates {
		if profile.Alias == alias {
			return profile, true
		}
	}
	return importedTemplateProfile(config, alias)
}

func findStorageProfile(config TargetConfig, alias string) (StorageProfile, bool) {
	for _, profile := range config.Storages {
		if profile.Alias == alias {
			return profile, true
		}
	}
	return StorageProfile{}, false
}

func findNetworkProfile(config TargetConfig, alias string) (NetworkProfile, bool) {
	for _, profile := range config.Networks {
		if profile.Alias == alias {
			return profile, true
		}
	}
	return NetworkProfile{}, false
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func mutationError(code, class, message, field, resource string) *pluginv1.StructuredError {
	return &pluginv1.StructuredError{Code: code, ErrorClass: class, SafeMessage: message, FieldPath: field, ResourceRef: resource}
}

func bareDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

// Keep the compiler checking that the closed plans remain JSON documents and
// do not acquire provider-native maps through custom marshalers.
var _ = json.Marshal
