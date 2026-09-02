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

	factory ClientFactory
	now     func() time.Time
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
	return &Service{factory: factory, now: time.Now}
}

func (service *Service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{
		Status:      pluginv1.HealthResponse_STATUS_HEALTHY,
		SafeMessage: "Proxmox provider is ready for target-scoped read-only discovery",
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
	discoveredAt := service.now().UTC()
	capacity, err := makeCapacity(inventory, digest)
	if err != nil {
		return nil, status.Error(codes.Internal, "encode Proxmox target capacity")
	}
	overrideDigest := sha256.Sum256(emptyOverrideSchema)
	return &pluginv1.DiscoverTargetResponse{
		Capabilities:         makeCapabilities(inventory, digest, request.GetTarget().GetConfigFingerprint()),
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
	mutationConstraint := []string{"read-only discovery slice; provider mutations are not implemented"}
	result := []*pluginv1.Capability{
		capability("access.providerNativeURL", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; access discovery not implemented", mutationConstraint),
		capability("lifecycle.clone", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; clone compatibility not probed", mutationConstraint),
		capability("lifecycle.restore", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; restore compatibility not probed", mutationConstraint),
		capability("lifecycle.snapshot", pluginv1.Capability_SUPPORT_UNSUPPORTED, baseEvidence+"; snapshot compatibility is storage and disk-format specific", mutationConstraint),
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
