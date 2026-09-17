// Package providergrpc translates the application provider port to protocol v1.
package providergrpc

import (
	"context"
	"fmt"
	"io"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Provider struct {
	BuildDigest  string
	Client       wire.ProviderServiceClient
	Artifacts    wire.ArtifactTransferServiceClient
	ImageImports wire.ImageImportServiceClient
	Availability func() bool
}

func New(client wire.ProviderServiceClient) *Provider { return &Provider{Client: client} }
func call(c app.ProviderCall) *wire.CallContext {
	return &wire.CallContext{RequestId: c.RequestID, OperationId: c.OperationID, InvocationId: c.InvocationID, LabInstanceId: c.InstanceID, LogicalResourceId: c.ResourceID, ProviderTargetId: c.TargetID, IdempotencyKey: c.IdempotencyKey, DesiredGeneration: c.Generation, FencingToken: c.Fence}
}
func target(t app.ProviderTargetContext) *wire.TargetContext {
	return &wire.TargetContext{TargetId: t.ID, ConfigEnvelope: t.Config, CredentialEnvelope: t.Credential, ConfigFingerprint: t.ConfigFingerprint}
}
func issue(e *wire.StructuredError) *app.ProviderIssue {
	if e == nil {
		return nil
	}
	return &app.ProviderIssue{Code: e.GetCode(), Message: e.GetSafeMessage(), FieldPath: e.GetFieldPath(), ResourceID: e.GetResourceRef()}
}
func warnings(es []*wire.Warning) []app.ProviderIssue {
	r := make([]app.ProviderIssue, 0, len(es))
	for _, e := range es {
		if e != nil {
			r = append(r, app.ProviderIssue{Code: e.GetCode(), Message: e.GetSafeMessage(), ResourceID: e.GetResourceRef()})
		}
	}
	return r
}
func (p *Provider) Validate(ctx context.Context, r app.ProviderValidationRequest) (app.ProviderValidation, error) {
	v, err := p.Client.ValidateLab(ctx, &wire.ValidateLabRequest{Context: call(r.Call), Target: target(r.Target), CanonicalLabJson: r.Canonical, ProviderOverrideJson: r.Override})
	if err != nil {
		return app.ProviderValidation{}, err
	}
	if v == nil {
		return app.ProviderValidation{}, fmt.Errorf("provider returned no validation")
	}
	out := app.ProviderValidation{Plan: v.GetProviderPlan(), PlanDigest: v.GetProviderPlanDigest(), Warnings: warnings(v.GetWarnings())}
	for _, e := range v.GetErrors() {
		if e != nil {
			out.Errors = append(out.Errors, *issue(e))
		}
	}
	return out, nil
}
func claims(cs []app.AllocationKey) []*wire.AllocationClaim {
	r := make([]*wire.AllocationClaim, 0, len(cs))
	for _, c := range cs {
		r = append(r, &wire.AllocationClaim{Namespace: c.Namespace, Value: c.Value})
	}
	return r
}
func state(b []byte) (*wire.ProviderState, error) {
	if len(b) == 0 {
		return nil, nil
	}
	s := new(wire.ProviderState)
	if err := proto.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("invalid persisted provider state: %w", err)
	}
	return s, nil
}
func mutation(r app.ProviderMutation) (*wire.ResourceMutationInput, error) {
	s, err := state(r.PriorState)
	if err != nil {
		return nil, err
	}
	out := &wire.ResourceMutationInput{Context: call(r.Call), Target: target(r.Target), CanonicalResourceJson: r.Canonical, ProviderPlan: r.Plan, PriorState: s, OwnershipTokens: r.Ownership, CommittedAllocations: claims(r.Allocations)}
	for _, d := range r.Dependencies {
		ds, err := state(d.State)
		if err != nil {
			return nil, err
		}
		out.Dependencies = append(out.Dependencies, &wire.ResourceDependency{LogicalResourceId: d.ResourceID, ProviderPlan: d.Plan, ProviderPlanDigest: d.PlanDigest, ProviderState: ds, ObservedStateJson: d.Observation, CommittedAllocations: claims(d.Allocations)})
	}
	return out, nil
}
func (p *Provider) Plan(ctx context.Context, r app.ProviderMutation) (app.ProviderPlan, error) {
	m, err := mutation(r)
	if err != nil {
		return app.ProviderPlan{}, err
	}
	v, err := p.Client.PlanResource(ctx, &wire.PlanResourceRequest{Input: m, ExcludedAllocations: claims(r.ExcludedAllocations)})
	if err != nil {
		return app.ProviderPlan{}, err
	}
	if v == nil {
		return app.ProviderPlan{}, fmt.Errorf("provider returned no plan")
	}
	out := app.ProviderPlan{Plan: v.GetProviderPlan(), PlanDigest: v.GetProviderPlanDigest(), Error: issue(v.GetError()), Warnings: warnings(v.GetWarnings())}
	for _, c := range v.GetProposedAllocations() {
		if c == nil {
			return app.ProviderPlan{}, fmt.Errorf("provider returned empty allocation")
		}
		out.Allocations = append(out.Allocations, app.AllocationKey{Namespace: c.GetNamespace(), Value: c.GetValue()})
	}
	return out, nil
}
func result(v *wire.ResourceResult) (*app.ProviderResult, error) {
	if v == nil {
		return nil, nil
	}
	out := &app.ProviderResult{Observation: v.GetObservedStateJson(), Error: issue(v.GetError()), CompensationAttempted: v.GetCompensationAttempted(), CompensationComplete: v.GetCompensationComplete()}
	if v.GetProviderState() != nil {
		var err error
		out.State, err = proto.Marshal(v.GetProviderState())
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (p *Provider) Invoke(ctx context.Context, action string, r app.ProviderMutation) (*app.ProviderResult, error) {
	m, err := mutation(r)
	if err != nil {
		return nil, err
	}
	var recv func() (*wire.ResourceResult, error)
	switch action {
	case "apply":
		s, e := p.Client.ApplyResource(ctx, &wire.ApplyResourceRequest{Input: m})
		if e != nil {
			return nil, e
		}
		recv = func() (*wire.ResourceResult, error) { v, e := s.Recv(); return v.GetEvent().GetResult(), e }
	case "destroy", "compensate":
		s, e := p.Client.DestroyResource(ctx, &wire.DestroyResourceRequest{Input: m})
		if e != nil {
			return nil, e
		}
		recv = func() (*wire.ResourceResult, error) { v, e := s.Recv(); return v.GetEvent().GetResult(), e }
	default:
		return nil, fmt.Errorf("unsupported provider mutation %q", action)
	}
	var last *wire.ResourceResult
	for {
		v, e := recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if v != nil {
			if last != nil {
				return nil, fmt.Errorf("provider returned multiple terminal results")
			}
			last = v
		}
	}
	if last == nil {
		return nil, fmt.Errorf("provider stream ended without a result")
	}
	return result(last)
}
func (p *Provider) Lookup(ctx context.Context, c app.ProviderCall, t app.ProviderTargetContext, id string) (*app.ProviderResult, error) {
	v, err := p.Client.GetInvocation(ctx, &wire.GetInvocationRequest{Context: call(c), Target: target(t), InvocationId: id})
	if err != nil {
		return nil, err
	}
	return result(v.GetResult())
}
func discovery(v *wire.DiscoverTargetResponse) (app.ProviderDiscovery, error) {
	if v == nil || v.GetDiscoveredAt() == nil || v.GetValidUntil() == nil || !v.GetDiscoveredAt().IsValid() || !v.GetValidUntil().IsValid() {
		return app.ProviderDiscovery{}, fmt.Errorf("provider discovery timestamps are invalid")
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(v)
	if err != nil {
		return app.ProviderDiscovery{}, err
	}
	out := app.ProviderDiscovery{DiscoveredAt: v.GetDiscoveredAt().AsTime(), ValidUntil: v.GetValidUntil().AsTime(), JSON: b}
	for _, c := range v.GetCapabilities() {
		if c == nil {
			return app.ProviderDiscovery{}, fmt.Errorf("empty provider capability")
		}
		out.Capabilities = append(out.Capabilities, app.ProviderCapability{ID: c.GetFeatureId(), Support: c.GetSupport().String(), Evidence: c.GetEvidence()})
	}
	return out, nil
}
func (p *Provider) Discover(ctx context.Context, t app.ProviderTargetContext) (app.ProviderDiscovery, error) {
	v, err := p.Client.DiscoverTarget(ctx, &wire.DiscoverTargetRequest{Target: target(t)})
	if err != nil {
		return app.ProviderDiscovery{}, err
	}
	return discovery(v)
}
func (p *Provider) DecodeDiscovery(b []byte) (app.ProviderDiscovery, error) {
	v := new(wire.DiscoverTargetResponse)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(b, v); err != nil {
		return app.ProviderDiscovery{}, err
	}
	return discovery(v)
}
func (p *Provider) StateDigest(b []byte) ([]byte, error) {
	s, err := state(b)
	if err != nil {
		return nil, err
	}
	return s.GetDigest(), nil
}

var _ app.Provider = (*Provider)(nil)

func (p *Provider) DiscoverEndpoint(ctx context.Context, c app.ProviderCall, t app.ProviderTargetContext, b []byte, method string) (app.ProviderEndpoint, error) {
	s, err := state(b)
	if err != nil {
		return app.ProviderEndpoint{}, err
	}
	v, err := p.Client.DiscoverAccessEndpoint(ctx, &wire.DiscoverAccessEndpointRequest{Context: call(c), Target: target(t), ResourceState: s, Method: method})
	if err != nil {
		return app.ProviderEndpoint{}, err
	}
	if v == nil {
		return app.ProviderEndpoint{}, fmt.Errorf("provider returned no access endpoint")
	}
	out := app.ProviderEndpoint{Schema: app.ProviderSchema{Name: v.GetEndpointSchema().GetName(), Major: v.GetEndpointSchema().GetMajor(), Minor: v.GetEndpointSchema().GetMinor()}, TargetID: v.GetTargetId(), InstanceID: v.GetLabInstanceId(), ResourceID: v.GetLogicalResourceId(), Method: v.GetMethod(), StateDigest: v.GetProviderStateDigest(), Digest: v.GetEndpointDigest(), Opaque: v.GetOpaqueConnectionData(), Error: issue(v.GetError())}
	if v.GetValidUntil() != nil && v.GetValidUntil().IsValid() {
		out.ValidUntil = v.GetValidUntil().AsTime()
	}
	return out, nil
}

func (p *Provider) Available() bool { return p != nil && (p.Availability == nil || p.Availability()) }

func NewLive(client wire.ProviderServiceClient, available func() bool) *Provider {
	return &Provider{Client: client, Availability: available}
}

func (p *Provider) ReadUsage(ctx context.Context, r app.ProviderMutation) (*app.ResourceUsage, error) {
	m, err := mutation(r)
	if err != nil {
		return nil, err
	}
	v, err := p.Client.ReadResource(ctx, &wire.ReadResourceRequest{Input: m})
	if err != nil {
		return nil, err
	}
	if v.GetResult() == nil || v.GetResult().GetError() != nil {
		return nil, fmt.Errorf("resource measurement unavailable")
	}
	u := v.GetResult().GetUsage()
	if u == nil {
		return nil, nil
	}
	if u.GetSampledAt() == nil || !u.GetSampledAt().IsValid() {
		return nil, fmt.Errorf("invalid measurement time")
	}
	return &app.ResourceUsage{SampledAt: u.GetSampledAt().AsTime(), CPUFraction: u.CpuFraction, VirtualCPUs: u.VirtualCpus, MemoryUsedBytes: u.MemoryUsedBytes, MemoryCapacityBytes: u.MemoryCapacityBytes}, nil
}

func (p *Provider) ExtensionIdentity() string { return p.BuildDigest }
