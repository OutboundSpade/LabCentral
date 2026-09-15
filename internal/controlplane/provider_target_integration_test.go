package controlplane_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	plugincontract "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	_ "modernc.org/sqlite"
)

const providerTargetLab = `apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata:
  name: provider-target-test
spec:
  requirements:
    isolationClass: virtualMachine
  resources:
    networks:
      - name: lab
        kind: isolated
        ipv4Cidr: 10.20.0.0/24
        ipam: static
        egress: {mode: deny}
    vms:
      - name: server
        imageRef: labcentral/test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        osFamily: linux
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        interfaces:
          - {networkRef: networks.lab, ipv4: 10.20.0.10}
`

type fixedSecretResolver struct{ value []byte }

func (resolver fixedSecretResolver) Resolve(context.Context, application.AuthorizedSecretRef, application.InvocationScope) (*application.EphemeralSecret, error) {
	return application.NewEphemeralSecret(resolver.value), nil
}

type allocationProvider struct {
	pluginv1.UnimplementedProviderServiceServer
	mu             sync.Mutex
	plans          []*pluginv1.PlanResourceRequest
	mutations      []*pluginv1.ResourceMutationInput
	credential     string
	configuration  string
	support        pluginv1.Capability_Support
	sharedVMClaims bool
}

func (provider *allocationProvider) DiscoverTarget(_ context.Context, request *pluginv1.DiscoverTargetRequest) (*pluginv1.DiscoverTargetResponse, error) {
	provider.captureTarget(request.GetTarget())
	provider.mu.Lock()
	support := provider.support
	provider.mu.Unlock()
	if support == pluginv1.Capability_SUPPORT_UNSPECIFIED {
		support = pluginv1.Capability_SUPPORT_NATIVE
	}
	now := time.Now().UTC()
	return &pluginv1.DiscoverTargetResponse{
		Capabilities: []*pluginv1.Capability{{FeatureId: "workload.vm", Support: support, Evidence: "test qualification"}},
		DiscoveredAt: timestamppb.New(now), ValidUntil: timestamppb.New(now.Add(time.Hour)),
		OverrideJsonSchema: []byte(`{"type":"object"}`), OverrideSchemaDigest: stringsOf('a', 64), CapacityJson: []byte(`{}`),
	}, nil
}

func (provider *allocationProvider) ValidateLab(context.Context, *pluginv1.ValidateLabRequest) (*pluginv1.ValidateLabResponse, error) {
	plan := []byte(`{"validated":true}`)
	return &pluginv1.ValidateLabResponse{ProviderPlan: plan, ProviderPlanDigest: directDigest(plan)}, nil
}

func (provider *allocationProvider) PlanResource(_ context.Context, request *pluginv1.PlanResourceRequest) (*pluginv1.PlanResourceResponse, error) {
	provider.mu.Lock()
	provider.plans = append(provider.plans, proto.Clone(request).(*pluginv1.PlanResourceRequest))
	sharedVMClaims := provider.sharedVMClaims
	provider.mu.Unlock()
	var resource struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request.GetInput().GetCanonicalResourceJson(), &resource); err != nil {
		return nil, err
	}
	namespace := "test." + resource.ID
	if sharedVMClaims && strings.HasPrefix(resource.ID, "vm.") {
		namespace = "test.vm"
	}
	unavailable := map[string]bool{}
	for _, excluded := range request.GetExcludedAllocations() {
		if excluded.GetNamespace() == namespace {
			unavailable[excluded.GetValue()] = true
		}
	}
	candidate := 100
	for unavailable[strconv.Itoa(candidate)] {
		candidate++
	}
	value := strconv.Itoa(candidate)
	plan := []byte(fmt.Sprintf(`{"allocation":%q,"resource":%q}`, value, resource.ID))
	return &pluginv1.PlanResourceResponse{
		ProviderPlan: plan, ProviderPlanDigest: directDigest(plan),
		ProposedAllocations: []*pluginv1.AllocationClaim{{Namespace: namespace, Value: value}},
	}, nil
}

func (provider *allocationProvider) ApplyResource(request *pluginv1.ApplyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResourceResponse]) error {
	provider.captureMutation(request.GetInput())
	state := &pluginv1.ProviderState{
		Schema: &pluginv1.SchemaVersion{Name: "test.state", Major: 1}, PluginId: "test.provider", PluginVersion: "1.0.0",
		Opaque: []byte(request.GetInput().GetContext().GetLogicalResourceId()), OwnershipIds: append([]string(nil), request.GetInput().GetOwnershipTokens()...),
	}
	return stream.Send(&pluginv1.ApplyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: &pluginv1.ResourceResult{
		ProviderState: state, ObservedStateJson: []byte(`{"ready":true}`),
	}}}})
}

func (provider *allocationProvider) DestroyResource(request *pluginv1.DestroyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResourceResponse]) error {
	provider.captureMutation(request.GetInput())
	return stream.Send(&pluginv1.DestroyResourceResponse{Event: &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: &pluginv1.ResourceResult{
		ObservedStateJson: []byte(`{"absent":true}`), CompensationAttempted: true, CompensationComplete: true,
	}}}})
}

func (provider *allocationProvider) captureTarget(target *pluginv1.TargetContext) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.credential = string(target.GetCredentialEnvelope())
	provider.configuration = string(target.GetConfigEnvelope())
}

func (provider *allocationProvider) captureMutation(input *pluginv1.ResourceMutationInput) {
	provider.captureTarget(input.GetTarget())
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.mutations = append(provider.mutations, proto.Clone(input).(*pluginv1.ResourceMutationInput))
}

func TestContractTESTPROXMOX023ProviderTargetPlansClaimsRoutesWorkerAndReleasesAfterAbsence(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:provider-target-integration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runProviderTargetStoreConformance(t, store)
}

func TestPostgreSQLProviderTargetStoreConformance(t *testing.T) {
	openStore, _, _ := postgreSQLTestSchema(t, "targets")
	store, _ := openStore()
	runProviderTargetStoreConformance(t, store)
}

func runProviderTargetStoreConformance(t *testing.T, store *localdb.Store) {
	t.Helper()
	ctx := context.Background()
	server := &allocationProvider{}
	client, closeProvider := testProviderClient(t, server)
	defer closeProvider()
	targetID := domain.ProviderTargetID("target.proxmox-test")
	resolver := fixedSecretResolver{value: []byte(`{"token":"canary"}`)}
	registry, err := controlplane.NewTargetRegistry([]controlplane.TargetBinding{{
		ID: targetID, ProviderKind: "proxmox", Policy: controlplane.TargetPolicy{MinimumMinor: 1, RequiredFeatures: []string{"opaque-allocation-claims-v1", "dependency-resource-input-v1"}, NativeCapabilities: []string{"workload.vm"}}, Provider: providergrpc.New(client), ConfigEnvelope: []byte(`{"endpoint":"test"}`),
		CredentialRef: &domain.SecretRef{Source: "file", Name: "/test/credential", Key: "value"}, CredentialResolver: resolver,
		ProtocolMajor: 1, ProtocolMinor: 1, Features: []string{plugincontract.FeatureOpaqueAllocationClaimsV1, plugincontract.FeatureDependencyResourceInputV1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ids := &sequenceIDs{}
	service, err := controlplane.NewServiceWithTargets(testRenderer(t), store, registry, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshTarget(ctx, targetID); err != nil {
		t.Fatal(err)
	}
	statuses, err := service.ListProviderTargets(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].ID != targetID || statuses[0].Status != "fresh" || len(statuses[0].DiscoveryJSON) == 0 {
		t.Fatalf("provider target status = %#v, %v", statuses, err)
	}
	snapshot, err := store.GetTargetSnapshot(ctx, targetID)
	if err != nil || snapshot.ProviderKind != "proxmox" || snapshot.ProtocolMinor != 1 || !snapshot.ValidUntil.After(time.Now()) {
		t.Fatalf("persisted target snapshot = %#v, %v", snapshot, err)
	}
	created, err := service.CreateInstanceTarget(ctx, []byte(providerTargetLab), false, "provider-target-first", targetID, []byte(`{"profile":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := controlplane.NewWorkerWithTargets(store, registry, ids, controlplane.WorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	instance, resources, err := service.GetInstance(ctx, created.InstanceID)
	if err != nil || instance.Phase != domain.InstanceReady || instance.ProviderTargetID != targetID || string(instance.ProviderOverrideJSON) != `{"profile":"test"}` || len(resources) != 2 {
		t.Fatalf("ready target instance = %#v resources=%d err=%v", instance, len(resources), err)
	}
	claims, err := store.ListClaims(ctx, created.InstanceID)
	if err != nil || len(claims) != 2 {
		t.Fatalf("committed claims = %#v, %v", claims, err)
	}
	for _, claim := range claims {
		if claim.State != application.AllocationCommitted || claim.Key.TargetID != targetID || claim.Key.Value != "100" {
			t.Fatalf("unexpected committed claim %#v", claim)
		}
	}

	second, err := service.CreateInstanceTarget(ctx, []byte(providerTargetLab), false, "provider-target-second", targetID, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondClaims, err := store.ListClaims(ctx, second.InstanceID)
	if err != nil || len(secondClaims) != 2 {
		t.Fatalf("replanned claims = %#v, %v", secondClaims, err)
	}
	for _, claim := range secondClaims {
		if claim.Key.Value != "101" {
			t.Fatalf("claim was not conflict-replanned: %#v", claim)
		}
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	destroyed, err := service.DestroyInstance(ctx, created.InstanceID, "provider-target-destroy")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := service.GetOperation(ctx, destroyed.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("destroy operation = %#v, %v", operation, err)
	}
	claims, err = store.ListClaims(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.State != application.AllocationReleased {
			t.Fatalf("destroy did not release absence-confirmed claim: %#v", claim)
		}
	}
	server.mu.Lock()
	server.sharedVMClaims = true
	server.mu.Unlock()
	multiSource := providerTargetLab + `      - name: peer
        imageRef: labcentral/test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        osFamily: linux
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        interfaces: [{networkRef: networks.lab, ipv4: 10.20.0.11}]
      - name: another
        imageRef: labcentral/test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        osFamily: linux
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        interfaces: [{networkRef: networks.lab, ipv4: 10.20.0.12}]
`
	multi, err := service.CreateInstanceTarget(ctx, []byte(multiSource), false, "multi-resource-claims", targetID, nil)
	if err != nil {
		t.Fatalf("multi-VM admission: %v", err)
	}
	multiClaims, err := store.ListClaims(ctx, multi.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	vmValues := map[string]bool{}
	for _, claim := range multiClaims {
		if claim.Key.Namespace == "test.vm" {
			vmValues[claim.Key.Value] = true
		}
	}
	if len(vmValues) != 3 || !vmValues["100"] || !vmValues["101"] || !vmValues["102"] {
		t.Fatalf("VM claims collided inside one lab: %v", vmValues)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	multiInstance, _, err := service.GetInstance(ctx, multi.InstanceID)
	if err != nil || multiInstance.Phase != domain.InstanceReady {
		t.Fatalf("multi-VM execution: %#v, %v", multiInstance, err)
	}
	if _, err := service.DestroyInstance(ctx, multi.InstanceID, "multi-resource-cleanup"); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// More than eight occupied allocations must not exhaust the collision retry
	// budget. This shared conformance case runs against SQLite and PostgreSQL.
	for i := 0; i < 12; i++ {
		if _, err := service.CreateInstanceTarget(ctx, []byte(providerTargetLab), false, fmt.Sprintf("many-existing-%d", i), targetID, nil); err != nil {
			t.Fatalf("lab %d beyond existing claims: %v", i+1, err)
		}
	}

	server.mu.Lock()
	server.support = pluginv1.Capability_SUPPORT_UNSUPPORTED
	server.mu.Unlock()
	if err := service.RefreshTarget(ctx, targetID); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateInstanceTarget(ctx, []byte(providerTargetLab), false, "provider-target-gate", targetID, nil)
	var validationError *controlplane.ValidationError
	if !errors.As(err, &validationError) || len(validationError.Issues) == 0 || validationError.Issues[0].Code != "provider_capability_unavailable" {
		t.Fatalf("unsupported persisted capability did not close mutation gate: %#v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if server.credential != `{"token":"canary"}` || server.configuration != `{"endpoint":"test"}` {
		t.Fatalf("worker target envelopes = config %q credential %q", server.configuration, server.credential)
	}
	foundDependency := false
	for _, mutation := range server.mutations {
		if mutation.GetContext().GetProviderTargetId() != string(targetID) || len(mutation.GetCommittedAllocations()) != 1 {
			t.Fatalf("mutation lost target or committed claim: %#v", mutation)
		}
		if mutation.GetContext().GetLogicalResourceId() == "vm.server" {
			if len(mutation.GetDependencies()) != 1 || mutation.GetDependencies()[0].GetProviderState() == nil || len(mutation.GetDependencies()[0].GetCommittedAllocations()) != 1 {
				t.Fatalf("VM mutation dependency was incomplete: %#v", mutation.GetDependencies())
			}
			foundDependency = true
		}
	}
	if !foundDependency {
		t.Fatal("worker did not deliver the committed Network dependency to the VM")
	}

}

func directDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func stringsOf(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
