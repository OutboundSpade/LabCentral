package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	PluginID      = "io.labcentral.provider.docker"
	PluginVersion = "0.1.0"
	stateSchema   = "docker.resource-state"
	warningCode   = "docker_trusted_laptop_only"
)

type Service struct {
	pluginv1.UnimplementedProviderServiceServer
	pluginv1.UnimplementedPluginRuntimeServiceServer

	engine Engine

	mu          sync.Mutex
	invocations map[string]*pluginv1.ResourceResult
}

func NewService(engine Engine) (*Service, error) {
	if engine == nil {
		return nil, fmt.Errorf("Docker Engine client is required")
	}
	return &Service{engine: engine, invocations: make(map[string]*pluginv1.ResourceResult)}, nil
}

func (s *Service) Health(ctx context.Context, _ *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	if _, err := s.engine.Discover(ctx); err != nil {
		return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_UNHEALTHY, SafeMessage: "Docker Engine is unavailable"}, nil
	}
	return &pluginv1.HealthResponse{Status: pluginv1.HealthResponse_STATUS_HEALTHY, SafeMessage: "Docker Engine is reachable"}, nil
}

func (s *Service) DiscoverTarget(ctx context.Context, _ *pluginv1.DiscoverTargetRequest) (*pluginv1.DiscoverTargetResponse, error) {
	info, err := s.engine.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover Docker target: %w", err)
	}
	now := time.Now().UTC()
	evidence := fmt.Sprintf("Docker Engine %s API %s (%s/%s; %s)", info.EngineVersion, info.APIVersion, info.OSType, info.Architecture, info.OperatingSystem)
	capabilities := []*pluginv1.Capability{
		capability("workload.container.native", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("network.segment.isolated", pluginv1.Capability_SUPPORT_CONSTRAINED, evidence),
		capability("network.portPublication.host", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("network.ipv4", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("ipam.providerManaged", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("storage.volume.persistent", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("access.containerExec", pluginv1.Capability_SUPPORT_NATIVE, evidence),
		capability("isolation.process", pluginv1.Capability_SUPPORT_CONSTRAINED, evidence),
	}
	overrideSchema := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{"trustedLaptopIsolationAccepted":{"const":true}},"required":["trustedLaptopIsolationAccepted"]}`)
	digest := sha256.Sum256(overrideSchema)
	return &pluginv1.DiscoverTargetResponse{
		Capabilities:         capabilities,
		DiscoveredAt:         timestamppb.New(now),
		ValidUntil:           timestamppb.New(now.Add(5 * time.Minute)),
		OverrideJsonSchema:   overrideSchema,
		OverrideSchemaDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func capability(id string, support pluginv1.Capability_Support, evidence string) *pluginv1.Capability {
	return &pluginv1.Capability{FeatureId: id, Support: support, Evidence: evidence}
}

type canonicalLab struct {
	APIVersion        string                `json:"apiVersion"`
	Metadata          json.RawMessage       `json:"metadata"`
	Requirements      canonicalRequirements `json:"requirements"`
	Resources         []canonicalResource   `json:"resources"`
	Inputs            json.RawMessage       `json:"inputs"`
	ProviderOverrides json.RawMessage       `json:"providerOverrides"`
}

type canonicalRequirements struct {
	IsolationClass string   `json:"isolationClass"`
	Capabilities   []string `json:"capabilities"`
}

type canonicalResource struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	DependsOn []string        `json:"dependsOn"`
	Spec      json.RawMessage `json:"spec"`
}

type networkSpec struct {
	Kind     string `json:"kind"`
	IPv4CIDR string `json:"ipv4Cidr"`
	IPv6CIDR string `json:"ipv6Cidr"`
	MTU      uint16 `json:"mtu"`
	IPAM     struct {
		Mode string `json:"mode"`
	} `json:"ipam"`
	Egress struct {
		Mode         string   `json:"mode"`
		Destinations []string `json:"destinations"`
	} `json:"egress"`
}

type volumeSpec struct {
	Persistence string `json:"persistence"`
	SizeBytes   uint64 `json:"sizeBytes"`
	Snapshot    bool   `json:"snapshot"`
}

type canonicalImage struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

type containerSpec struct {
	Image   canonicalImage `json:"image"`
	Compute struct {
		CPUMillis    uint32 `json:"cpuMillis"`
		MemoryBytes  uint64 `json:"memoryBytes"`
		Architecture string `json:"architecture"`
	} `json:"compute"`
	Isolation  string `json:"isolation"`
	Interfaces []struct {
		NetworkRef string `json:"networkRef"`
	} `json:"interfaces"`
	Mounts []struct {
		VolumeRef string `json:"volumeRef"`
		Path      string `json:"path"`
		ReadOnly  bool   `json:"readOnly"`
	} `json:"mounts"`
	Environment map[string]struct {
		Literal   *string         `json:"literal"`
		SecretRef json.RawMessage `json:"secretRef"`
	} `json:"environment"`
	Command        []string `json:"command"`
	Args           []string `json:"args"`
	PublishedPorts []struct {
		Name         string `json:"name"`
		Port         uint16 `json:"port"`
		Protocol     string `json:"protocol"`
		Exposure     string `json:"exposure"`
		ExternalPort uint16 `json:"externalPort"`
	} `json:"publishedPorts"`
}

type dockerOverride struct {
	TrustedLaptopIsolationAccepted bool `json:"trustedLaptopIsolationAccepted"`
}

func (s *Service) ValidateLab(_ context.Context, request *pluginv1.ValidateLabRequest) (*pluginv1.ValidateLabResponse, error) {
	response := &pluginv1.ValidateLabResponse{}
	var lab canonicalLab
	if err := strictJSON(request.GetCanonicalLabJson(), &lab); err != nil {
		response.Errors = append(response.Errors, structuredError("invalid_canonical_lab", "invalid_declaration", "Canonical Lab JSON is invalid", false, "", ""))
		return response, nil
	}
	var override dockerOverride
	if err := strictJSON(request.GetProviderOverrideJson(), &override); err != nil || !override.TrustedLaptopIsolationAccepted {
		response.Errors = append(response.Errors, structuredError(
			"docker_isolation_acknowledgement_required",
			"policy_denied",
			"Docker laptop mode requires explicit acknowledgement that ordinary containers are not a hostile-workload security boundary",
			false,
			"providerOverride.trustedLaptopIsolationAccepted",
			"",
		))
	}
	if lab.APIVersion != "canonical.labcentral.io/v1alpha1" {
		response.Errors = append(response.Errors, structuredError("unsupported_canonical_version", "invalid_declaration", "Canonical Lab version is not supported", false, "apiVersion", ""))
	}
	if lab.Requirements.IsolationClass != "" && lab.Requirements.IsolationClass != "process" {
		response.Errors = append(response.Errors, structuredError("unsupported_isolation", "unsupported_capability", "Docker laptop mode supports only process isolation", false, "requirements.isolationClass", ""))
	}
	supportedCapabilities := map[string]bool{
		"workload.container.native":    true,
		"network.segment.isolated":     true,
		"network.portPublication.host": true,
	}
	for _, required := range lab.Requirements.Capabilities {
		if !supportedCapabilities[required] {
			response.Errors = append(response.Errors, structuredError("unsupported_capability", "unsupported_capability", "Required capability is not supported by Docker laptop mode", false, "requirements.capabilities", required))
		}
	}
	for _, resource := range lab.Resources {
		response.Errors = append(response.Errors, validateResource(resource)...)
	}
	response.Warnings = append(response.Warnings, isolationWarning())
	if len(response.Errors) == 0 {
		plan, _ := json.Marshal(map[string]any{"provider": PluginID, "resourceCount": len(lab.Resources), "trustedLaptop": true})
		digest := sha256.Sum256(plan)
		response.ProviderPlan = plan
		response.ProviderPlanDigest = hex.EncodeToString(digest[:])
	}
	return response, nil
}

func validateResource(resource canonicalResource) []*pluginv1.StructuredError {
	field := "resources." + resource.ID
	switch resource.Kind {
	case "Network":
		var spec networkSpec
		if err := strictJSON(resource.Spec, &spec); err != nil {
			return []*pluginv1.StructuredError{structuredError("invalid_network", "invalid_declaration", "Network specification is invalid", false, field, resource.ID)}
		}
		if spec.Kind != "isolated" || (spec.Egress.Mode != "deny" && spec.Egress.Mode != "allow") || spec.IPAM.Mode == "static" {
			return []*pluginv1.StructuredError{structuredError("unsupported_network", "unsupported_capability", "Docker laptop mode supports isolated networks with provider-managed IPAM and deny or allow egress", false, field, resource.ID)}
		}
	case "Volume":
		var spec volumeSpec
		if err := strictJSON(resource.Spec, &spec); err != nil || spec.Snapshot {
			return []*pluginv1.StructuredError{structuredError("unsupported_volume", "unsupported_capability", "Docker laptop mode supports volumes without snapshot semantics", false, field, resource.ID)}
		}
	case "Container":
		var spec containerSpec
		if err := strictJSON(resource.Spec, &spec); err != nil {
			return []*pluginv1.StructuredError{structuredError("invalid_container", "invalid_declaration", "Container specification is invalid", false, field, resource.ID)}
		}
		var result []*pluginv1.StructuredError
		if spec.Isolation != "process" {
			result = append(result, structuredError("unsupported_isolation", "unsupported_capability", "Docker native containers provide process isolation only", false, field+".isolation", resource.ID))
		}
		if spec.Image.Repository == "" || !strings.HasPrefix(spec.Image.Digest, "sha256:") {
			result = append(result, structuredError("unpinned_image", "policy_denied", "Docker images must be pinned by SHA-256 digest", false, field+".image", resource.ID))
		}
		for name, value := range spec.Environment {
			if len(value.SecretRef) > 0 {
				result = append(result, structuredError("secret_not_supported", "unsupported_capability", "The laptop slice does not inject guest secrets", false, field+".environment."+name, resource.ID))
			}
		}
		for _, port := range spec.PublishedPorts {
			if port.Exposure != "host" {
				result = append(result, structuredError("unsupported_port_exposure", "unsupported_capability", "Docker laptop mode publishes ports to loopback only", false, field+".publishedPorts", resource.ID))
			}
		}
	case "AccessEndpoint":
		// Access metadata is core-owned in the thin slice. Container exec is
		// advertised, but no browser terminal is minted by this provider.
	case "Output":
		// Outputs are projected by the core from observed provider state.
	default:
		return []*pluginv1.StructuredError{structuredError("unsupported_resource_kind", "unsupported_capability", "Resource kind is not supported by Docker laptop mode", false, field, resource.ID)}
	}
	return nil
}

func (s *Service) PlanResource(_ context.Context, request *pluginv1.PlanResourceRequest) (*pluginv1.PlanResourceResponse, error) {
	resource, err := mutationResource(request.GetInput())
	if err != nil {
		return &pluginv1.PlanResourceResponse{Error: structuredError("invalid_resource", "invalid_declaration", "Canonical resource JSON is invalid", false, "canonicalResource", "")}, nil
	}
	plan, err := json.Marshal(struct {
		Action string   `json:"action"`
		Kind   string   `json:"kind"`
		ID     string   `json:"resourceId"`
		Deps   []string `json:"dependencies,omitempty"`
	}{Action: "ensure", Kind: resource.Kind, ID: resource.ID, Deps: resource.DependsOn})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(plan)
	return &pluginv1.PlanResourceResponse{ProviderPlan: plan, ProviderPlanDigest: hex.EncodeToString(digest[:]), Warnings: []*pluginv1.Warning{isolationWarning()}}, nil
}

func (s *Service) ApplyResource(request *pluginv1.ApplyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResourceResponse]) error {
	input := request.GetInput()
	if err := validateMutationInput(input); err != nil {
		return stream.Send(&pluginv1.ApplyResourceResponse{Event: resultEvent(nil, err)})
	}
	context := input.GetContext()
	if cached := s.invocation(context.GetInvocationId()); cached != nil {
		return stream.Send(&pluginv1.ApplyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: cached}}})
	}
	if err := stream.Send(&pluginv1.ApplyResourceResponse{Event: progressEvent("ensuring", "Ensuring Docker resource")}); err != nil {
		return err
	}
	result := s.apply(stream.Context(), input)
	s.recordInvocation(context.GetInvocationId(), result)
	return stream.Send(&pluginv1.ApplyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: result}}})
}

func (s *Service) apply(ctx context.Context, input *pluginv1.ResourceMutationInput) *pluginv1.ResourceResult {
	resource, err := mutationResource(input)
	if err != nil {
		return resourceFailure("invalid_resource", "Canonical resource JSON is invalid", resource.ID)
	}
	call := input.GetContext()
	labels := ownershipLabels(call, input.GetOwnershipTokens()[0])
	name := physicalName(call.GetLabInstanceId(), resource.ID)
	var providerID string
	var observed any
	switch resource.Kind {
	case "Network":
		var spec networkSpec
		if err := strictJSON(resource.Spec, &spec); err != nil {
			return resourceFailure("invalid_network", "Network specification is invalid", resource.ID)
		}
		providerID, err = s.engine.EnsureNetwork(ctx, NetworkCreate{
			// Docker does not publish host ports from an --internal bridge. The
			// accepted trusted-laptop exception therefore uses a normal user-defined
			// bridge and reports denied egress as constrained, never as enforced.
			Name: name, Internal: false, IPv4CIDR: spec.IPv4CIDR,
			IPv6CIDR: spec.IPv6CIDR, MTU: spec.MTU, Labels: labels,
		})
		observed = map[string]any{"providerId": providerID, "internal": false, "requestedEgress": spec.Egress.Mode, "egressEnforcement": "constrained"}
	case "Volume":
		providerID, err = s.engine.EnsureVolume(ctx, VolumeCreate{Name: name, Labels: labels})
		observed = map[string]any{"providerId": providerID}
	case "Container":
		var spec containerSpec
		if decodeErr := strictJSON(resource.Spec, &spec); decodeErr != nil {
			return resourceFailure("invalid_container", "Container specification is invalid", resource.ID)
		}
		container := ContainerCreate{
			Name: name, Image: spec.Image.Repository + "@" + spec.Image.Digest,
			Entrypoint: spec.Command, Command: spec.Args, CPUMillis: spec.Compute.CPUMillis,
			MemoryBytes: spec.Compute.MemoryBytes, Labels: labels,
		}
		if err = s.engine.EnsureImage(ctx, container.Image); err != nil {
			return resourceFailure("docker_image_unavailable", safeEngineError(err), resource.ID)
		}
		if len(spec.Interfaces) > 0 {
			container.Network = physicalName(call.GetLabInstanceId(), spec.Interfaces[0].NetworkRef)
		}
		for _, mount := range spec.Mounts {
			container.Mounts = append(container.Mounts, Mount{Source: physicalName(call.GetLabInstanceId(), mount.VolumeRef), Target: mount.Path, ReadOnly: mount.ReadOnly})
		}
		environmentNames := make([]string, 0, len(spec.Environment))
		for envName := range spec.Environment {
			environmentNames = append(environmentNames, envName)
		}
		sort.Strings(environmentNames)
		for _, envName := range environmentNames {
			value := spec.Environment[envName]
			if value.Literal != nil {
				container.Environment = append(container.Environment, envName+"="+*value.Literal)
			}
		}
		for _, port := range spec.PublishedPorts {
			container.Published = append(container.Published, PublishedPort{ContainerPort: port.Port, Protocol: port.Protocol, HostIP: "127.0.0.1", HostPort: port.ExternalPort})
		}
		var observation ContainerObservation
		observation, err = s.engine.EnsureContainer(ctx, container)
		providerID = observation.ID
		observed = map[string]any{"providerId": observation.ID, "running": observation.Running, "ports": observation.Ports}
	default:
		return resourceFailure("unsupported_resource_kind", "Resource kind is core-owned or unsupported", resource.ID)
	}
	if err != nil {
		return resourceFailure("docker_apply_failed", safeEngineError(err), resource.ID)
	}
	return resourceSuccess(input, resource, providerID, labels, observed)
}

func (s *Service) ReadResource(ctx context.Context, request *pluginv1.ReadResourceRequest) (*pluginv1.ReadResourceResponse, error) {
	input := request.GetInput()
	resource, err := mutationResource(input)
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: resourceFailure("invalid_resource", "Canonical resource JSON is invalid", "")}, nil
	}
	state, err := decodeState(input.GetPriorState())
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: resourceFailure("invalid_provider_state", "Provider state is invalid", resource.ID)}, nil
	}
	if resource.Kind != "Container" {
		return &pluginv1.ReadResourceResponse{Result: resourceSuccess(input, resource, state.ProviderID, state.Labels, map[string]any{"providerId": state.ProviderID})}, nil
	}
	observation, err := s.engine.InspectContainer(ctx, state.ProviderID)
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: resourceFailure("docker_read_failed", safeEngineError(err), resource.ID)}, nil
	}
	return &pluginv1.ReadResourceResponse{Result: resourceSuccess(input, resource, state.ProviderID, state.Labels, map[string]any{"providerId": observation.ID, "running": observation.Running, "ports": observation.Ports})}, nil
}

func (s *Service) DestroyResource(request *pluginv1.DestroyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResourceResponse]) error {
	input := request.GetInput()
	if err := validateMutationInput(input); err != nil {
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultEvent(nil, err)})
	}
	call := input.GetContext()
	if cached := s.invocation(call.GetInvocationId()); cached != nil {
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: cached}}})
	}
	if err := stream.Send(&pluginv1.DestroyResourceResponse{Event: progressEvent("destroying", "Destroying Docker resource")}); err != nil {
		return err
	}
	result := s.destroy(stream.Context(), input)
	s.recordInvocation(call.GetInvocationId(), result)
	return stream.Send(&pluginv1.DestroyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: result}}})
}

func (s *Service) destroy(ctx context.Context, input *pluginv1.ResourceMutationInput) *pluginv1.ResourceResult {
	resource, err := mutationResource(input)
	if err != nil {
		return resourceFailure("invalid_resource", "Canonical resource JSON is invalid", "")
	}
	state, err := decodeState(input.GetPriorState())
	if err != nil {
		return resourceFailure("invalid_provider_state", "Destroy requires valid recorded provider state", resource.ID)
	}
	if state.Kind != resource.Kind || state.ResourceID != resource.ID {
		return resourceFailure("provider_state_ownership_mismatch", "Provider state does not match the logical resource", resource.ID)
	}
	expectedToken := input.GetOwnershipTokens()[0]
	if state.Labels[labelOwnership] != expectedToken {
		return resourceFailure("provider_state_ownership_mismatch", "Recorded ownership does not match the requested resource", resource.ID)
	}
	switch resource.Kind {
	case "Container":
		err = s.engine.RemoveContainer(ctx, state.ProviderID, state.Labels)
	case "Volume":
		err = s.engine.RemoveVolume(ctx, state.ProviderID, state.Labels)
	case "Network":
		err = s.engine.RemoveNetwork(ctx, state.ProviderID, state.Labels)
	default:
		return resourceFailure("unsupported_resource_kind", "Resource kind is core-owned or unsupported", resource.ID)
	}
	if err != nil {
		return resourceFailure("docker_destroy_failed", safeEngineError(err), resource.ID)
	}
	observed, _ := json.Marshal(map[string]any{"providerId": state.ProviderID, "destroyed": true})
	return &pluginv1.ResourceResult{ObservedStateJson: observed, CompensationComplete: true}
}

func (s *Service) GetInvocation(_ context.Context, request *pluginv1.GetInvocationRequest) (*pluginv1.GetInvocationResponse, error) {
	result := s.invocation(request.GetInvocationId())
	if result == nil {
		return &pluginv1.GetInvocationResponse{Result: resourceFailure("invocation_not_found", "Invocation is not known by this plugin process", "")}, nil
	}
	return &pluginv1.GetInvocationResponse{Result: result}, nil
}

func (s *Service) CancelInvocation(_ context.Context, request *pluginv1.CancelInvocationRequest) (*pluginv1.CancelInvocationResponse, error) {
	if s.invocation(request.GetInvocationId()) != nil {
		return &pluginv1.CancelInvocationResponse{Outcome: pluginv1.CancelInvocationResponse_OUTCOME_TOO_LATE}, nil
	}
	return &pluginv1.CancelInvocationResponse{Outcome: pluginv1.CancelInvocationResponse_OUTCOME_NOT_FOUND}, nil
}

func (s *Service) MigrateState(_ context.Context, request *pluginv1.MigrateStateRequest) (*pluginv1.MigrateStateResponse, error) {
	state := request.GetState()
	if state == nil || state.GetSchema().GetMajor() != 1 || request.GetDestination().GetMajor() != 1 {
		return &pluginv1.MigrateStateResponse{Error: structuredError("unsupported_state_version", "state_version_incompatible", "Docker provider state version is not readable", false, "state.schema", "")}, nil
	}
	return &pluginv1.MigrateStateResponse{State: state}, nil
}

type stateEnvelope struct {
	Kind       string            `json:"kind"`
	ResourceID string            `json:"resourceId"`
	ProviderID string            `json:"providerId"`
	Labels     map[string]string `json:"labels"`
}

func resourceSuccess(input *pluginv1.ResourceMutationInput, resource canonicalResource, providerID string, labels map[string]string, observed any) *pluginv1.ResourceResult {
	stateBytes, _ := json.Marshal(stateEnvelope{Kind: resource.Kind, ResourceID: resource.ID, ProviderID: providerID, Labels: labels})
	stateDigest := sha256.Sum256(stateBytes)
	observedBytes, _ := json.Marshal(observed)
	return &pluginv1.ResourceResult{
		ProviderState: &pluginv1.ProviderState{
			Schema:        &pluginv1.SchemaVersion{Name: stateSchema, Major: 1, Minor: 0},
			PluginId:      PluginID,
			PluginVersion: PluginVersion,
			Opaque:        stateBytes,
			OwnershipIds:  []string{labels[labelOwnership]},
			Digest:        stateDigest[:],
		},
		ObservedStateJson:    observedBytes,
		Warnings:             []*pluginv1.Warning{isolationWarning()},
		CompensationComplete: true,
	}
}

func decodeState(state *pluginv1.ProviderState) (stateEnvelope, error) {
	if state == nil || state.GetPluginId() != PluginID || state.GetSchema().GetMajor() != 1 {
		return stateEnvelope{}, fmt.Errorf("provider state metadata mismatch")
	}
	digest := sha256.Sum256(state.GetOpaque())
	if !equalBytes(digest[:], state.GetDigest()) {
		return stateEnvelope{}, fmt.Errorf("provider state digest mismatch")
	}
	var decoded stateEnvelope
	if err := strictJSON(state.GetOpaque(), &decoded); err != nil || decoded.ProviderID == "" || len(decoded.Labels) == 0 {
		return stateEnvelope{}, fmt.Errorf("provider state payload is invalid")
	}
	return decoded, nil
}

func mutationResource(input *pluginv1.ResourceMutationInput) (canonicalResource, error) {
	if input == nil {
		return canonicalResource{}, fmt.Errorf("mutation input is required")
	}
	var resource canonicalResource
	if err := strictJSON(input.GetCanonicalResourceJson(), &resource); err != nil || resource.ID == "" || resource.Kind == "" {
		return canonicalResource{}, fmt.Errorf("canonical resource is invalid")
	}
	return resource, nil
}

func validateMutationInput(input *pluginv1.ResourceMutationInput) *pluginv1.StructuredError {
	if input == nil || input.GetContext() == nil {
		return structuredError("invalid_mutation", "invalid_declaration", "Mutation context is required", false, "context", "")
	}
	call := input.GetContext()
	if call.GetOperationId() == "" || call.GetInvocationId() == "" || call.GetLabInstanceId() == "" || call.GetLogicalResourceId() == "" || call.GetProviderTargetId() == "" || call.GetIdempotencyKey() == "" || call.GetDesiredGeneration() == 0 || call.GetFencingToken() == 0 {
		return structuredError("invalid_mutation_context", "invalid_declaration", "Mutation context is incomplete", false, "context", call.GetLogicalResourceId())
	}
	if len(input.GetOwnershipTokens()) != 1 || input.GetOwnershipTokens()[0] == "" {
		return structuredError("invalid_ownership", "authorization", "Exactly one ownership token is required", false, "ownershipTokens", call.GetLogicalResourceId())
	}
	return nil
}

const (
	labelManaged      = "io.labcentral.managed"
	labelInstallation = "io.labcentral.installation"
	labelTarget       = "io.labcentral.target"
	labelInstance     = "io.labcentral.instance"
	labelResource     = "io.labcentral.resource"
	labelGeneration   = "io.labcentral.generation"
	labelOwnership    = "io.labcentral.ownership"
)

func ownershipLabels(call *pluginv1.CallContext, token string) map[string]string {
	return map[string]string{
		labelManaged:      "true",
		labelInstallation: "local",
		labelTarget:       call.GetProviderTargetId(),
		labelInstance:     call.GetLabInstanceId(),
		labelResource:     call.GetLogicalResourceId(),
		labelGeneration:   strconv.FormatUint(call.GetDesiredGeneration(), 10),
		labelOwnership:    token,
	}
}

func physicalName(instanceID, resourceID string) string {
	base := strings.ToLower(instanceID + "-" + resourceID)
	var clean strings.Builder
	clean.WriteString("lc-")
	for _, character := range base {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			clean.WriteRune(character)
		} else {
			clean.WriteByte('-')
		}
	}
	value := strings.Trim(clean.String(), "-_.")
	if len(value) <= 63 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return value[:50] + "-" + hex.EncodeToString(digest[:6])
}

func isolationWarning() *pluginv1.Warning {
	return &pluginv1.Warning{
		Code:        warningCode,
		SafeMessage: "Docker laptop mode uses ordinary process-isolated containers; published-port labs cannot prove denied egress and are not safe for arbitrary hostile workloads",
	}
}

func structuredError(code, class, message string, retryable bool, field, resource string) *pluginv1.StructuredError {
	return &pluginv1.StructuredError{Code: code, ErrorClass: class, SafeMessage: message, Retryable: retryable, FieldPath: field, ResourceRef: resource}
}

func resourceFailure(code, message, resource string) *pluginv1.ResourceResult {
	return &pluginv1.ResourceResult{Error: structuredError(code, "provider_error", message, false, "", resource)}
}

func resultEvent(result *pluginv1.ResourceResult, failure *pluginv1.StructuredError) *pluginv1.InvocationEvent {
	if result == nil {
		result = &pluginv1.ResourceResult{Error: failure}
	}
	return &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: result}}
}

func progressEvent(phase, message string) *pluginv1.InvocationEvent {
	return &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Progress{Progress: &pluginv1.Progress{Phase: phase, SafeMessage: message, OccurredAt: timestamppb.Now()}}}
}

func strictJSON(input []byte, output any) error {
	if len(input) == 0 {
		return fmt.Errorf("JSON input is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON input contains trailing values")
		}
		return err
	}
	return nil
}

func safeEngineError(err error) string {
	if err == nil {
		return "Docker Engine operation failed"
	}
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func (s *Service) invocation(id string) *pluginv1.ResourceResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneResult(s.invocations[id])
}

func (s *Service) recordInvocation(id string, result *pluginv1.ResourceResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invocations[id] = cloneResult(result)
}

func cloneResult(result *pluginv1.ResourceResult) *pluginv1.ResourceResult {
	if result == nil {
		return nil
	}
	return proto.Clone(result).(*pluginv1.ResourceResult)
}

var (
	_ pluginv1.ProviderServiceServer      = (*Service)(nil)
	_ pluginv1.PluginRuntimeServiceServer = (*Service)(nil)
)
