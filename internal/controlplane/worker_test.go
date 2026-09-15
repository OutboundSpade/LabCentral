package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestProviderInvocationDigestIsStableAndInstanceScoped(t *testing.T) {
	instance := InstanceRecord{ID: "inst.alpha", ProviderTargetID: "target.alpha"}
	resource := ResourceRecord{
		ID: "container.web", CanonicalJSON: []byte(`{"kind":"Container"}`),
		ProviderPlan: []byte(`{"physicalName":"web"}`), ProviderState: []byte(`{"generation":1}`),
	}
	want := providerInvocationDigest(3, "apply", instance, resource)
	if got := providerInvocationDigest(3, "apply", instance, resource); got != want {
		t.Fatalf("equivalent provider invocation digest changed: got %s want %s", got, want)
	}
	if legacy := legacyProviderInvocationDigest("apply", instance, resource); legacy == want {
		t.Fatalf("v2 provider invocation digest reused legacy digest %s", legacy)
	}
	legacy := legacyProviderInvocationDigest("apply", instance, resource)
	preparer := &invocationDigestPreparer{failFirst: true}
	prepared, err := prepareProviderInvocation(context.Background(), preparer, PrepareInvocationRequest{
		InvocationID: "inv.alpha", ResourceID: resource.ID, Action: "apply",
		IdempotencyKey: want.Hex(), RequestDigest: want,
	}, legacy)
	if err != nil || prepared.RequestDigest != legacy || len(preparer.requests) != 2 ||
		preparer.requests[0].RequestDigest != want || preparer.requests[1].RequestDigest != legacy {
		t.Fatalf("legacy prepared invocation fallback = %#v, %v; calls %#v", prepared, err, preparer.requests)
	}
	variants := []struct {
		name       string
		generation domain.Generation
		action     string
		instance   InstanceRecord
		resource   ResourceRecord
	}{
		{name: "instance", generation: 3, action: "apply", instance: InstanceRecord{ID: "inst.beta", ProviderTargetID: instance.ProviderTargetID}, resource: resource},
		{name: "resource", generation: 3, action: "apply", instance: instance, resource: ResourceRecord{ID: "container.client", CanonicalJSON: resource.CanonicalJSON, ProviderPlan: resource.ProviderPlan, ProviderState: resource.ProviderState}},
		{name: "target", generation: 3, action: "apply", instance: InstanceRecord{ID: instance.ID, ProviderTargetID: "target.beta"}, resource: resource},
		{name: "generation", generation: 4, action: "apply", instance: instance, resource: resource},
		{name: "action", generation: 3, action: "destroy", instance: instance, resource: resource},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			if got := providerInvocationDigest(variant.generation, variant.action, variant.instance, variant.resource); got == want {
				t.Fatalf("%s variant reused provider invocation digest %s", variant.name, got)
			}
		})
	}
}

type invocationDigestPreparer struct {
	failFirst bool
	requests  []PrepareInvocationRequest
}

func (p *invocationDigestPreparer) PrepareInvocation(_ context.Context, request PrepareInvocationRequest) (InvocationRecord, error) {
	p.requests = append(p.requests, request)
	if p.failFirst {
		p.failFirst = false
		return InvocationRecord{}, &domain.DomainError{Code: "idempotency_conflict", Class: domain.ErrorInvalidDeclaration, Message: "legacy record"}
	}
	return InvocationRecord{ID: request.InvocationID, ResourceID: request.ResourceID, Action: request.Action,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest, State: "prepared"}, nil
}

func TestProviderInvocationEnvelopeCoversBoundedLongRunningTasks(t *testing.T) {
	if providerInvocationTimeout < 10*time.Minute {
		t.Fatalf("provider invocation timeout = %s", providerInvocationTimeout)
	}
}

func TestFailedResourceLeftoverClassificationIsHonest(t *testing.T) {
	failure := &application.ProviderIssue{Code: "provider_failure", Message: "Provider operation failed"}
	for _, testCase := range []struct {
		name   string
		result *application.ProviderResult
		want   bool
	}{
		{name: "no provider result", result: nil, want: false},
		{name: "unproven prewrite failure is retained", result: &application.ProviderResult{Error: failure, Observation: []byte(`{"leftovers":[]}`)}, want: true},
		{name: "explicitly compensated prewrite failure", result: &application.ProviderResult{Error: failure, CompensationComplete: true, Observation: []byte(`{"leftovers":[]}`)}, want: false},
		{name: "reported owned leftover", result: &application.ProviderResult{Error: failure, Observation: []byte(`{"leftovers":["qemu/101"]}`)}, want: true},
		{name: "incomplete attempted compensation", result: &application.ProviderResult{Error: failure, CompensationAttempted: true}, want: true},
		{name: "confirmed compensation overrides stale observation", result: &application.ProviderResult{Error: failure, CompensationAttempted: true, CompensationComplete: true, Observation: []byte(`{"leftovers":["qemu/101"]}`)}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resultHasOwnedLeftovers(testCase.result); got != testCase.want {
				t.Fatalf("resultHasOwnedLeftovers() = %t, want %t", got, testCase.want)
			}
		})
	}
}

type availableProvider struct {
	application.Provider
	up bool
}

func (p *availableProvider) Available() bool { return p.up }

type filterClaimStore struct {
	application.WorkerStore
	targets []domain.ProviderTargetID
}

func (s *filterClaimStore) ClaimTargets(_ context.Context, _ string, _ time.Duration, targets []application.TargetExecutionLimit) (application.WorkClaim, error) {
	s.targets = nil
	for _, target := range targets {
		s.targets = append(s.targets, target.TargetID)
	}
	return application.WorkClaim{}, application.ErrNoWork
}
func TestWorkerClaimsOnlyAvailableTargets(t *testing.T) {
	healthy := &availableProvider{up: true}
	down := &availableProvider{up: false}
	registry, err := NewTargetRegistry([]TargetBinding{{ID: "target.healthy", ProviderKind: "external", Provider: healthy, ProtocolMajor: 1}, {ID: "target.down", ProviderKind: "external", Provider: down, ProtocolMajor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	store := &filterClaimStore{}
	worker, err := NewWorkerWithTargets(store, registry, CryptoIDs{}, WorkerOptions{Owner: "worker.filter"})
	if err != nil {
		t.Fatal(err)
	}
	_ = worker.RunOnce(context.Background())
	if len(store.targets) != 1 || store.targets[0] != "target.healthy" {
		t.Fatal("unavailable target was claimed", store.targets)
	}
	down.up = true
	_ = worker.RunOnce(context.Background())
	if len(store.targets) != 2 {
		t.Fatal("recovered target did not resume", store.targets)
	}
}

func TestWorkerPoolIncludesConfiguredUnavailableCapacity(t *testing.T) {
	registry, err := NewTargetRegistry([]TargetBinding{
		{ID: "target.healthy", ProviderKind: "external", Provider: &availableProvider{up: true}, ProtocolMajor: 1, Policy: TargetPolicy{MaxConcurrentDeployments: 2}},
		{ID: "target.recovering", ProviderKind: "external", Provider: &availableProvider{up: false}, ProtocolMajor: 1, Policy: TargetPolicy{MaxConcurrentDeployments: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingClaimStore{owners: make(chan string, 5)}
	worker, err := NewWorkerWithTargets(store, registry, CryptoIDs{}, WorkerOptions{Owner: "worker.pool"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	owners := map[string]bool{}
	for i := 0; i < 5; i++ {
		select {
		case owner := <-store.owners:
			owners[owner] = true
		case <-ctx.Done():
			t.Fatal("configured worker capacity unavailable")
		}
	}
	if len(owners) != 5 {
		t.Fatalf("workers reused lease owners: %v", owners)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker pool did not stop")
	}
}

type blockingClaimStore struct {
	application.WorkerStore
	owners chan string
}

func (s *blockingClaimStore) ClaimTargets(ctx context.Context, owner string, _ time.Duration, _ []application.TargetExecutionLimit) (application.WorkClaim, error) {
	s.owners <- owner
	<-ctx.Done()
	return application.WorkClaim{}, ctx.Err()
}
