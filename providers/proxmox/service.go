package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const capabilityTTL = 5 * time.Minute

var emptyOverrideSchema = []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false}`)
var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Service struct {
	pluginv1.UnimplementedProviderServiceServer
	pluginv1.UnimplementedPluginRuntimeServiceServer

	factory         ClientFactory
	now             func() time.Time
	sleep           func(time.Duration)
	allowT0Mutation bool
	executionMu     sync.Mutex
}

func NewService() *Service {
	return NewServiceWithFactory(ClientFactoryFunc(func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
		return NewClientFromEnvelopes(configEnvelope, credentialEnvelope)
	}))
}

func NewServiceWithFactory(factory ClientFactory) *Service {
	if factory == nil {
		panic("Proxmox client factory is required")
	}
	return &Service{factory: factory, now: time.Now, sleep: time.Sleep}
}

func newT0MutationService(factory ClientFactory) *Service {
	service := NewServiceWithFactory(factory)
	service.allowT0Mutation = true
	service.sleep = func(time.Duration) {}
	return service
}

func (service *Service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{
		Status:      pluginv1.HealthResponse_STATUS_HEALTHY,
		SafeMessage: "Proxmox provider is ready for target discovery; mutation execution remains qualification-gated",
	}, nil
}

func (service *Service) DiscoverTarget(ctx context.Context, request *pluginv1.DiscoverTargetRequest) (*pluginv1.DiscoverTargetResponse, error) {
	if request == nil || request.GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "Proxmox target context is required")
	}
	client, err := service.factory.New(request.GetTarget().GetConfigEnvelope(), request.GetTarget().GetCredentialEnvelope())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Proxmox target configuration or credentials are invalid")
	}
	defer client.CloseIdleConnections()
	inventory, err := client.Discover(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Proxmox target discovery failed")
	}
	if err := inventory.normalize(); err != nil {
		return nil, status.Error(codes.Internal, "Proxmox target inventory was invalid")
	}
	digest, err := inventory.Digest()
	if err != nil {
		return nil, status.Error(codes.Internal, "Proxmox target inventory was invalid")
	}
	mutationDigest, err := inventory.MutationDigest()
	if err != nil {
		return nil, status.Error(codes.Internal, "Proxmox target mutation inventory was invalid")
	}
	discoveredAt := service.now().UTC()
	capacity, err := makeCapacity(inventory, digest)
	if err != nil {
		return nil, status.Error(codes.Internal, "encode Proxmox target capacity")
	}
	overrideDigest := sha256.Sum256(emptyOverrideSchema)
	capabilities := makeCapabilities(inventory, digest, request.GetTarget().GetConfigFingerprint())
	if enabled, evidence := service.mutationCapabilityGate(ctx, request.GetTarget(), client, inventory, mutationDigest); enabled {
		enableMutationCapabilities(capabilities, evidence)
	}
	return &pluginv1.DiscoverTargetResponse{
		Capabilities:         capabilities,
		DiscoveredAt:         timestamppb.New(discoveredAt),
		ValidUntil:           timestamppb.New(discoveredAt.Add(capabilityTTL)),
		OverrideJsonSchema:   append([]byte(nil), emptyOverrideSchema...),
		OverrideSchemaDigest: hex.EncodeToString(overrideDigest[:]),
		CapacityJson:         capacity,
	}, nil
}

func makeCapabilities(inventory Inventory, digest, fingerprint string) []*pluginv1.Capability {
	onlineNodes, qemuNodes, imageStorages, bridges, vlanBridges := 0, 0, 0, 0, 0
	for _, node := range inventory.Nodes {
		if node.Status == "online" {
			onlineNodes++
		}
		if node.QEMUAPI {
			qemuNodes++
		}
	}
	for _, storage := range inventory.Storages {
		if storage.Active && storage.Enabled && contains(storage.Content, "images") {
			imageStorages++
		}
	}
	for _, bridge := range inventory.Bridges {
		if bridge.Active {
			bridges++
			if bridge.VLANAware {
				vlanBridges++
			}
		}
	}
	baseEvidence := fmt.Sprintf("Proxmox VE %s; inventory %s; online nodes %d/%d; discovery issues %d", inventory.Version.Version, digest, onlineNodes, len(inventory.Nodes), len(inventory.Issues))
	if fingerprint = strings.TrimSpace(fingerprint); fingerprintPattern.MatchString(fingerprint) {
		baseEvidence += "; config fingerprint " + fingerprint
	}
	mutationConstraint := []string{"production mutation gate remains closed pending exact-target qualification"}
	result := []*pluginv1.Capability{
		capability("access.providerNativeURL", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; access discovery not implemented", mutationConstraint),
		capability("lifecycle.clone", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; clone compatibility not probed", mutationConstraint),
		capability("lifecycle.restore", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; restore compatibility not probed", mutationConstraint),
		capability("lifecycle.snapshot", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; snapshot compatibility is storage and disk-format specific", mutationConstraint),
		capability("lifecycle.start", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; mutation qualification gate is closed", mutationConstraint),
		capability("lifecycle.stop", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; mutation qualification gate is closed", mutationConstraint),
		capability("lifecycle.pause", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; mutation qualification gate is closed", mutationConstraint),
		capability("lifecycle.resume", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; mutation qualification gate is closed", mutationConstraint),
		capability("network.egress.enforced", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; firewall enforcement not probed", mutationConstraint),
		capability("network.segment.isolated", pluginv1.Capability_SUPPORT_UNSUPPORTED, fmt.Sprintf("%s; active bridges discovered %d", baseEvidence, bridges), mutationConstraint),
		capability("network.vlan", pluginv1.Capability_SUPPORT_UNSUPPORTED, fmt.Sprintf("%s; active VLAN-aware bridges discovered %d", baseEvidence, vlanBridges), mutationConstraint),
		capability("proxmox.discovery.bridge", supportForCount(bridges), fmt.Sprintf("%s; active bridges discovered %d", baseEvidence, bridges), nil, integerParameter("count", int64(bridges))),
		capability("proxmox.discovery.qemu", supportForCount(qemuNodes), fmt.Sprintf("%s; QEMU API reachable on %d online nodes; templates discovered %d", baseEvidence, qemuNodes, len(inventory.Templates)), nil, integerParameter("nodeCount", int64(qemuNodes)), integerParameter("templateCount", int64(len(inventory.Templates)))),
		capability("proxmox.discovery.vlanAwareBridge", supportForCount(vlanBridges), fmt.Sprintf("%s; active VLAN-aware bridges discovered %d", baseEvidence, vlanBridges), nil, integerParameter("count", int64(vlanBridges))),
		capability("proxmox.discovery.vmImageStorage", supportForCount(imageStorages), fmt.Sprintf("%s; active VM-image storage instances discovered %d", baseEvidence, imageStorages), nil, integerParameter("count", int64(imageStorages))),
		capability("scheduling.automaticPlacement", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; placement not implemented", mutationConstraint),
		capability("scheduling.capacity", capacitySupport(onlineNodes), baseEvidence+"; capacity is observational and not reserved", []string{"capacity is a point-in-time observation; no reservation or placement support"}, integerParameter("onlineNodeCount", int64(onlineNodes))),
		capability("volume.persistent", pluginv1.Capability_SUPPORT_UNSUPPORTED, fmt.Sprintf("%s; active VM-image storage instances discovered %d", baseEvidence, imageStorages), mutationConstraint),
		capability("volume.snapshot", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; storage presence does not prove volume snapshot compatibility", mutationConstraint),
		capability("workload.vm", pluginv1.Capability_SUPPORT_UNSUPPORTED, fmt.Sprintf("%s; QEMU API reachable on %d online nodes", baseEvidence, qemuNodes), mutationConstraint),
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GetFeatureId() < result[j].GetFeatureId() })
	return result
}

func (service *Service) mutationCapabilityGate(ctx context.Context, target *pluginv1.TargetContext, discovered DiscoveryClient, inventory Inventory, mutationDigest string) (bool, string) {
	if !service.allowT0Mutation {
		return false, "exact-target credentialed qualification is not enabled in this provider build"
	}
	config, err := mutationConfig(target)
	if err != nil || !config.MutationEnabled || config.Qualification.InventoryDigest != mutationDigest {
		return false, "mutation target configuration or inventory binding is invalid"
	}
	client, ok := discovered.(MutationAPI)
	if !ok {
		return false, "mutation API client is unavailable"
	}
	plan := QEMUPlan{
		Template:        &PlanTemplate{SourceNode: config.Templates[0].SourceNode, VMID: config.Templates[0].VMID, ConfigDigest: config.Templates[0].ConfigDigest},
		Destination:     &PlanDestination{Node: config.DestinationNode, StorageID: config.Storages[0].ID, StorageFormat: config.Storages[0].Format},
		InventoryDigest: mutationDigest,
	}
	if len(config.Networks) > 0 {
		plan.Networks = []NetworkBinding{{Bridge: config.Networks[0].Bridge}}
	}
	if err := verifyInventoryAndPermissions(ctx, client, config, plan, inventory); err != nil {
		return false, "fresh component or least-privilege evidence does not satisfy the mutation gate"
	}
	if !inventorySupportsConfiguredProfiles(config, inventory) {
		return false, "configured storage or network evidence does not satisfy the mutation gate"
	}
	for _, profile := range config.Templates {
		plan.Template = &PlanTemplate{SourceNode: profile.SourceNode, VMID: profile.VMID, ConfigDigest: profile.ConfigDigest}
		plan.Disks = nil
		for _, disk := range profile.Disks {
			plan.Disks = append(plan.Disks, PlanDisk{Device: disk.Device})
		}
		templateConfig, err := client.VMConfig(ctx, profile.SourceNode, profile.VMID)
		if err != nil || verifyTemplateConfig(plan, templateConfig) != nil {
			return false, "approved template evidence does not satisfy the mutation gate"
		}
	}
	if _, err := OpenInvocationJournal(config.JournalDirectory); err != nil {
		return false, "durable invocation journal is unavailable"
	}
	return true, "fixed-node PVE 9.x QEMU full-clone T0 qualification gates passed with mutation inventory " + mutationDigest + "; exact-target production T3 remains required"
}

func inventorySupportsConfiguredProfiles(config TargetConfig, inventory Inventory) bool {
	for _, profile := range config.Storages {
		matched := false
		for _, storage := range inventory.Storages {
			if storage.Node == config.DestinationNode && storage.ID == profile.ID && storage.Active && storage.Enabled && contains(storage.Content, "images") && (contains(storage.Formats, profile.Format) || storage.DefaultFormat == profile.Format) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	for _, profile := range config.Networks {
		matched := false
		for _, bridge := range inventory.Bridges {
			if bridge.Node == config.DestinationNode && bridge.Name == profile.Bridge && bridge.Active && bridge.VLANAware {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func enableMutationCapabilities(capabilities []*pluginv1.Capability, evidence string) {
	enabled := map[string]bool{"workload.vm": true, "lifecycle.start": true, "lifecycle.stop": true, "lifecycle.pause": true, "lifecycle.resume": true}
	for _, value := range capabilities {
		if enabled[value.GetFeatureId()] {
			value.Support = pluginv1.Capability_SUPPORT_NATIVE
			value.Evidence = strings.ReplaceAll(value.Evidence, "; mutation qualification gate is closed", "")
			value.Evidence += "; " + evidence
			value.Constraints = []string{"fixed node", "Linux amd64", "full clone", "approved storage and VLAN-aware bridge", "static IPv4", "T0 only"}
		}
	}
}

func capability(feature string, support pluginv1.Capability_Support, evidence string, constraints []string, parameters ...*pluginv1.Parameter) *pluginv1.Capability {
	return &pluginv1.Capability{
		FeatureId: feature, Support: support, Evidence: evidence,
		Constraints: append([]string(nil), constraints...), Parameters: parameters,
	}
}

func integerParameter(name string, value int64) *pluginv1.Parameter {
	return &pluginv1.Parameter{Name: name, Value: &pluginv1.Parameter_IntValue{IntValue: value}}
}

func supportForCount(count int) pluginv1.Capability_Support {
	if count == 0 {
		return pluginv1.Capability_SUPPORT_UNSUPPORTED
	}
	return pluginv1.Capability_SUPPORT_NATIVE
}

func capacitySupport(onlineNodes int) pluginv1.Capability_Support {
	if onlineNodes == 0 {
		return pluginv1.Capability_SUPPORT_UNSUPPORTED
	}
	return pluginv1.Capability_SUPPORT_CONSTRAINED
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type capacityDocument struct {
	FormatVersion   int              `json:"formatVersion"`
	InventoryDigest string           `json:"inventoryDigest"`
	Version         Version          `json:"version"`
	Nodes           []Node           `json:"nodes"`
	Storages        []Storage        `json:"storages"`
	Bridges         []Bridge         `json:"bridges"`
	Templates       []Template       `json:"templates"`
	Issues          []DiscoveryIssue `json:"issues,omitempty"`
}

func makeCapacity(inventory Inventory, digest string) ([]byte, error) {
	document := capacityDocument{
		FormatVersion: 1, InventoryDigest: digest, Version: inventory.Version,
		Nodes: append([]Node{}, inventory.Nodes...), Storages: append([]Storage{}, inventory.Storages...),
		Bridges: append([]Bridge{}, inventory.Bridges...), Templates: append([]Template{}, inventory.Templates...),
		Issues: append([]DiscoveryIssue(nil), inventory.Issues...),
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > defaultResponseLimit {
		return nil, fmt.Errorf("Proxmox capacity document exceeds %d bytes", defaultResponseLimit)
	}
	return contents, nil
}
