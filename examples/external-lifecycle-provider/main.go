package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"os"
	"slices"
	"time"
)

type provider struct {
	wire.UnimplementedProviderServiceServer
	*builder
}
type importer struct {
	wire.UnimplementedImageImportServiceServer
	*builder
}

func digest(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func (*provider) DiscoverTarget(context.Context, *wire.DiscoverTargetRequest) (*wire.DiscoverTargetResponse, error) {
	out := &wire.DiscoverTargetResponse{DiscoveredAt: timestamppb.Now(), ValidUntil: timestamppb.New(time.Now().Add(time.Hour))}
	for _, id := range []string{"workload.vm.native", "workload.container.native", "network.segment.isolated", "network.egress.deny", "network.address.static", "storage.volume", "access.org.example.echo", "access.org.example.network"} {
		out.Capabilities = append(out.Capabilities, &wire.Capability{FeatureId: id, Support: wire.Capability_SUPPORT_NATIVE, Evidence: "fixture process-owned simulated resources; no production isolation claim"})
	}
	return out, nil
}
func (*provider) ValidateLab(_ context.Context, r *wire.ValidateLabRequest) (*wire.ValidateLabResponse, error) {
	return &wire.ValidateLabResponse{ProviderPlan: r.CanonicalLabJson, ProviderPlanDigest: digest(r.CanonicalLabJson)}, nil
}
func (*provider) PlanResource(_ context.Context, r *wire.PlanResourceRequest) (*wire.PlanResourceResponse, error) {
	b := r.GetInput().GetCanonicalResourceJson()
	return &wire.PlanResourceResponse{ProviderPlan: b, ProviderPlanDigest: digest(b)}, nil
}
func resourceKey(r *wire.ResourceMutationInput) string {
	return "resource." + digest([]byte(r.GetTarget().GetTargetId()+"/"+r.GetContext().GetLabInstanceId()+"/"+r.GetContext().GetLogicalResourceId()))
}
func (s *provider) mutate(r *wire.ResourceMutationInput, absent bool) (*wire.ResourceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.GetContext().GetInvocationId()
	if !idPattern.MatchString(id) || len(r.GetOwnershipTokens()) != 1 {
		return nil, fmt.Errorf("invalid mutation")
	}
	if old, e := s.read(id); e == nil {
		out := new(wire.ResourceResult)
		if e = proto.Unmarshal(old.Data, out); e != nil {
			return nil, e
		}
		return out, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	key := resourceKey(r)
	old, e := s.read(key)
	if e == nil && old.Reference != r.GetOwnershipTokens()[0] {
		return nil, fmt.Errorf("foreign owner")
	}
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if r.GetPriorState() != nil && !slices.Equal(r.GetPriorState().GetOwnershipIds(), r.GetOwnershipTokens()) {
		return nil, fmt.Errorf("foreign state")
	}
	out := &wire.ResourceResult{ObservedStateJson: []byte(`{"absent":true}`)}
	if !absent {
		data := []byte(key)
		sum := sha256.Sum256(data)
		out.ProviderState = &wire.ProviderState{Schema: &wire.SchemaVersion{Name: "fixture.state", Major: 1}, PluginId: "org.example.lifecycle", PluginVersion: "0.1.0", Opaque: data, OwnershipIds: r.GetOwnershipTokens(), Digest: sum[:]}
		out.ObservedStateJson = []byte(`{"ready":true}`)
	}
	raw, e := proto.Marshal(out)
	if e != nil {
		return nil, e
	}
	if e = s.save(key, record{Reference: r.GetOwnershipTokens()[0], Data: raw, Cancelled: absent}); e != nil {
		return nil, e
	}
	if e = s.save(id, record{Reference: key, Data: raw}); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *provider) ApplyResource(r *wire.ApplyResourceRequest, stream grpc.ServerStreamingServer[wire.ApplyResourceResponse]) error {
	out, e := s.mutate(r.GetInput(), false)
	if e != nil {
		return e
	}
	return stream.Send(&wire.ApplyResourceResponse{Event: &wire.InvocationEvent{Event: &wire.InvocationEvent_Result{Result: out}}})
}
func (s *provider) DestroyResource(r *wire.DestroyResourceRequest, stream grpc.ServerStreamingServer[wire.DestroyResourceResponse]) error {
	out, e := s.mutate(r.GetInput(), true)
	if e != nil {
		return e
	}
	return stream.Send(&wire.DestroyResourceResponse{Event: &wire.InvocationEvent{Event: &wire.InvocationEvent_Result{Result: out}}})
}
func (s *provider) ReadResource(_ context.Context, r *wire.ReadResourceRequest) (*wire.ReadResourceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, e := s.read(resourceKey(r.GetInput()))
	if os.IsNotExist(e) {
		return &wire.ReadResourceResponse{Result: &wire.ResourceResult{ObservedStateJson: []byte(`{"absent":true}`)}}, nil
	}
	if e != nil {
		return nil, e
	}
	if len(r.GetInput().GetOwnershipTokens()) != 1 || old.Reference != r.GetInput().GetOwnershipTokens()[0] {
		return nil, fmt.Errorf("foreign resource")
	}
	out := new(wire.ResourceResult)
	e = proto.Unmarshal(old.Data, out)
	return &wire.ReadResourceResponse{Result: out}, e
}
func (s *provider) GetInvocation(_ context.Context, r *wire.GetInvocationRequest) (*wire.GetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, e := s.read(r.GetInvocationId())
	if os.IsNotExist(e) {
		return &wire.GetInvocationResponse{Result: &wire.ResourceResult{Error: &wire.StructuredError{Code: "invocation_not_found", Retryable: true}}}, nil
	}
	if e != nil {
		return nil, e
	}
	out := new(wire.ResourceResult)
	e = proto.Unmarshal(old.Data, out)
	return &wire.GetInvocationResponse{Result: out}, e
}
func (s *provider) DiscoverAccessEndpoint(_ context.Context, r *wire.DiscoverAccessEndpointRequest) (*wire.DiscoverAccessEndpointResponse, error) {
	b, _ := json.Marshal(struct{ Instance, Resource, Method string }{r.GetContext().GetLabInstanceId(), r.GetContext().GetLogicalResourceId(), r.GetMethod()})
	return &wire.DiscoverAccessEndpointResponse{OpaqueConnectionData: b, ValidUntil: timestamppb.New(time.Now().Add(time.Minute)), EndpointSchema: &wire.SchemaVersion{Name: "fixture.endpoint", Major: 1}, EndpointDigest: digest(b), TargetId: r.GetTarget().GetTargetId(), LabInstanceId: r.GetContext().GetLabInstanceId(), LogicalResourceId: r.GetContext().GetLogicalResourceId(), Method: r.GetMethod(), ProviderStateDigest: hex.EncodeToString(r.GetResourceState().GetDigest())}, nil
}
func (s *importer) Import(_ context.Context, r *wire.ImageImportServiceImportRequest) (*wire.ImageImportServiceImportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.GetContext().GetInvocationId()
	if old, e := s.read(id + ".import"); e == nil {
		out := new(wire.ImageImportServiceImportResponse)
		e = proto.Unmarshal(old.Data, out)
		return out, e
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	a := r.GetArtifact()
	bound := s.handles[a.GetTransferToken()]
	delete(s.handles, a.GetTransferToken())
	input, e := s.read(id + ".input")
	if e != nil || bound != id+".input" || input.Reference != a.GetDigest() {
		return nil, fmt.Errorf("artifact denied")
	}
	image := &wire.TargetImageBinding{ImageRef: "fixture/image@sha256:" + a.GetDigest(), ProviderImageId: "fixture-image-" + a.GetDigest(), ArtifactDigest: a.GetDigest(), TargetConfigurationDigest: r.GetTarget().GetConfigFingerprint(), Architecture: r.GetArchitecture(), OpaqueBinding: []byte(id)}
	out := &wire.ImageImportServiceImportResponse{State: "succeeded", Binding: image}
	data, e := proto.Marshal(out)
	if e != nil {
		return nil, e
	}
	if e = s.save(id+".import", record{Reference: r.GetTarget().GetTargetId(), Data: data}); e != nil {
		return nil, e
	}
	if string(r.GetTarget().GetConfigEnvelope()) == `{"delayImport":true}` {
		time.Sleep(3 * time.Second)
	}
	return out, nil
}
func (s *importer) GetInvocation(_ context.Context, r *wire.ImageImportServiceGetInvocationRequest) (*wire.ImageImportServiceGetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, e := s.read(r.GetContext().GetInvocationId() + ".import")
	if os.IsNotExist(e) {
		return &wire.ImageImportServiceGetInvocationResponse{Result: &wire.ImageImportServiceImportResponse{State: "unknown"}}, nil
	}
	if e != nil {
		return nil, e
	}
	out := new(wire.ImageImportServiceImportResponse)
	e = proto.Unmarshal(old.Data, out)
	if old.Reference != r.GetTarget().GetTargetId() {
		return nil, fmt.Errorf("target mismatch")
	}
	return &wire.ImageImportServiceGetInvocationResponse{Result: out}, e
}
func (s *importer) ValidateBinding(_ context.Context, r *wire.ImageImportServiceValidateBindingRequest) (*wire.ImageImportServiceValidateBindingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, e := s.read(string(r.GetBinding().GetOpaqueBinding()) + ".import")
	if e != nil {
		return &wire.ImageImportServiceValidateBindingResponse{Valid: false}, nil
	}
	out := new(wire.ImageImportServiceImportResponse)
	e = proto.Unmarshal(old.Data, out)
	return &wire.ImageImportServiceValidateBindingResponse{Valid: e == nil && old.Reference == r.GetTarget().GetTargetId() && r.GetTarget().GetConfigFingerprint() == r.GetBinding().GetTargetConfigurationDigest() && proto.Equal(out.GetBinding(), r.GetBinding())}, nil
}
func main() {
	root := flag.String("state", "", "plugin-owned journal")
	flag.Parse()
	if *root == "" {
		panic("state required")
	}
	if e := os.MkdirAll(*root, 0700); e != nil {
		panic(e)
	}
	path, e := os.Executable()
	if e != nil {
		panic(e)
	}
	data, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	b := &builder{root: *root, handles: map[string]string{}}
	p := &provider{builder: b}
	i := &importer{builder: b}
	e = runtime.Serve(context.Background(), runtime.Services{Info: &wire.PluginInfo{PluginId: "org.example.lifecycle", PluginVersion: "0.1.0", BuildDigest: "sha256:" + digest(data), Categories: []string{"provider"}, ProviderKinds: []string{"fixture"}, SupportedProtocols: []*wire.ProtocolVersionRange{{Major: 1, MaxMinor: 1}}, Features: []string{"artifact-transfer-v1", "image-import-v1", "access-endpoint-v1"}}, Runtime: b, Provider: p, Artifacts: b, ImageImports: i})
	if e != nil {
		panic(e)
	}
}
