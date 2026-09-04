package controlplane

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
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
	failure := &pluginv1.StructuredError{Code: "provider_failure", ErrorClass: "provider_failure", SafeMessage: "Provider operation failed"}
	for _, testCase := range []struct {
		name   string
		result *pluginv1.ResourceResult
		want   bool
	}{
		{name: "no provider result", result: nil, want: false},
		{name: "unproven prewrite failure is retained", result: &pluginv1.ResourceResult{Error: failure, ObservedStateJson: []byte(`{"leftovers":[]}`)}, want: true},
		{name: "explicitly compensated prewrite failure", result: &pluginv1.ResourceResult{Error: failure, CompensationComplete: true, ObservedStateJson: []byte(`{"leftovers":[]}`)}, want: false},
		{name: "reported owned leftover", result: &pluginv1.ResourceResult{Error: failure, ObservedStateJson: []byte(`{"leftovers":["qemu/101"]}`)}, want: true},
		{name: "incomplete attempted compensation", result: &pluginv1.ResourceResult{Error: failure, CompensationAttempted: true}, want: true},
		{name: "confirmed compensation overrides stale observation", result: &pluginv1.ResourceResult{Error: failure, CompensationAttempted: true, CompensationComplete: true, ObservedStateJson: []byte(`{"leftovers":["qemu/101"]}`)}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resultHasOwnedLeftovers(testCase.result); got != testCase.want {
				t.Fatalf("resultHasOwnedLeftovers() = %t, want %t", got, testCase.want)
			}
		})
	}
}
