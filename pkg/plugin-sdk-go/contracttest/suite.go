package contracttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
)

type Case struct {
	ID  string
	Run func(*testing.T, contract.Factory)
}

var RequiredCases = []Case{
	{"TEST-PROVIDER-001", Case001IdempotentApplyReplay},
	{"TEST-PROVIDER-002", Case002StaleGenerationIsFenced},
	{"TEST-PROVIDER-003", Case003NameCollisionIsNotAdopted},
	{"TEST-PROVIDER-004", Case004CrashAfterCreateBeforeResponse},
	{"TEST-PROVIDER-005", Case005LostResponseAfterMutation},
	{"TEST-PROVIDER-006", Case006PartialCreationCompensates},
	{"TEST-PROVIDER-007", Case007CompensationFailureReportsLeftovers},
	{"TEST-PROVIDER-008", Case008DestroyContinuesAfterFailure},
	{"TEST-PROVIDER-009", Case009OldStateMigratesExplicitly},
	{"TEST-PROVIDER-010", Case010UnknownStateFailsClosed},
	{"TEST-PROVIDER-011", Case011CapabilityDowngradeInvalidatesPlan},
	{"TEST-PROVIDER-012", Case012CancellationBeforeUnsafeBoundary},
	{"TEST-PROVIDER-013", Case013CancellationAfterSideEffect},
	{"TEST-PROVIDER-014", Case014SecretCanaryAbsent},
	{"TEST-PROVIDER-015", Case015PlanIsDeterministic},
	{"TEST-PROVIDER-016", Case016RestartPreservesInvocation},
	{"TEST-PROVIDER-017", Case017ReverseDependencyDestroyOrder},
	{"TEST-PROVIDER-018", Case018OwnershipMismatchBlocksDestroy},
}

func Run(t *testing.T, factory contract.Factory) {
	t.Helper()
	for _, testCase := range RequiredCases {
		t.Run(testCase.ID, func(t *testing.T) { testCase.Run(t, factory) })
	}
}

func Case001IdempotentApplyReplay(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{})
	request := applyRequest("container.app")
	first, err := harness.Apply(request)
	assertMutationSuccess(t, first, err)
	second, err := harness.Apply(request)
	assertMutationSuccess(t, second, err)
	if !reflect.DeepEqual(first.State, second.State) {
		t.Fatalf("provider state changed on idempotent replay\nfirst=%#v\nsecond=%#v", first.State, second.State)
	}
	snapshot := harness.Snapshot()
	if snapshot.CreateCount != 1 || snapshot.DuplicateMutations != 0 {
		t.Fatalf("replay mutation counts = %#v", snapshot)
	}
}

func Case002StaleGenerationIsFenced(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{ResourceGeneration: 2, FencingToken: 9})
	request := applyRequest("vm.router")
	request.Context.DesiredGeneration = 1
	request.Context.FencingToken = 8
	result, err := harness.Apply(request)
	if err != nil || result.Error == nil || result.Error.Code != "stale_generation" {
		t.Fatalf("stale apply = %#v, %v", result, err)
	}
	if harness.Snapshot().MutationCount != 0 {
		t.Fatal("stale apply mutated provider state")
	}
}

func Case003NameCollisionIsNotAdopted(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{CollisionDisplayName: "router", CollisionOwnershipToken: "another-installation"})
	request := applyRequest("vm.router")
	request.DisplayName = "router"
	result, err := harness.Apply(request)
	if err != nil || result.Error == nil || result.Error.Code != "ownership_mismatch" {
		t.Fatalf("collision apply = %#v, %v", result, err)
	}
	snapshot := harness.Snapshot()
	if snapshot.MutationCount != 0 || snapshot.DeleteCount != 0 {
		t.Fatalf("collision was adopted or deleted: %#v", snapshot)
	}
}

func Case004CrashAfterCreateBeforeResponse(t *testing.T, factory contract.Factory) {
	t.Helper()
	unknownOutcomeRecovery(t, factory, contract.FaultCrashAfterCreate)
}

func Case005LostResponseAfterMutation(t *testing.T, factory contract.Factory) {
	t.Helper()
	unknownOutcomeRecovery(t, factory, contract.FaultDropFinalResponse)
}

func unknownOutcomeRecovery(t *testing.T, factory contract.Factory, fault contract.Fault) {
	t.Helper()
	harness := factory(contract.Scenario{Fault: fault})
	request := applyRequest("container.app")
	if _, err := harness.Apply(request); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatalf("fault %s returned %v", fault, err)
	}
	if harness.Snapshot().CreateCount != 1 {
		t.Fatal("unknown outcome did not retain exactly one create")
	}
	observation := harness.GetInvocation(request.Context.InvocationID)
	if !observation.Known || !observation.Complete || observation.Result.State == nil || observation.Result.ResourceState != "ready" {
		t.Fatalf("invocation recovery = %#v", observation)
	}
	result, err := harness.Apply(request)
	assertMutationSuccess(t, result, err)
	if harness.Snapshot().CreateCount != 1 {
		t.Fatal("recovery replay duplicated provider mutation")
	}
}

func Case006PartialCreationCompensates(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{
		Fault:                 contract.FaultFailBootstrap,
		PhysicalSteps:         []string{"vm", "disk", "bootstrap"},
		PriorLogicalResources: []string{"network.app"},
	})
	result, err := harness.Apply(applyRequest("container.app"))
	if err != nil || result.Error == nil || !result.CompensationAttempted || !result.CompensationComplete {
		t.Fatalf("partial creation result = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(result.CompensationOrder, []string{"disk", "vm"}) || len(result.Leftovers) != 0 {
		t.Fatalf("compensation = %#v", result)
	}
	if result.OperationState != "failed" || result.InstancePhase != "degraded" {
		t.Fatalf("partial creation state = %#v", result)
	}
	if !reflect.DeepEqual(harness.Snapshot().LogicalResources, []string{"network.app"}) {
		t.Fatal("prior logical resources were not retained")
	}
}

func Case007CompensationFailureReportsLeftovers(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{
		Fault: contract.FaultFailDeleteDisk,
		CreatedPrimitives: []contract.Primitive{
			{ID: "vm-123", Kind: "vm", OwnershipToken: "owned"},
			{ID: "disk-456", Kind: "disk", OwnershipToken: "owned"},
		},
	})
	result, err := harness.Apply(applyRequest("vm.target"))
	if err != nil || result.Error == nil || result.Error.Class != "partial_cleanup" || result.CompensationComplete {
		t.Fatalf("failed compensation = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(result.Leftovers, []string{"disk-456"}) {
		t.Fatalf("leftovers = %v", result.Leftovers)
	}
	if result.OperationState != "partial" || result.InstancePhase != "degraded" {
		t.Fatalf("compensation failure state = %#v", result)
	}
}

func Case008DestroyContinuesAfterFailure(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{Fault: contract.FaultFailDeleteVMAttacker})
	nodes := []contract.GraphNode{
		{ID: "network.red"},
		{ID: "vm.attacker", Dependencies: []string{"network.red"}},
		{ID: "vm.router", Dependencies: []string{"network.red"}},
	}
	summary := harness.DestroyGraph(nodes)
	if !reflect.DeepEqual(summary.Attempted, []string{"vm.attacker", "vm.router", "network.red"}) {
		t.Fatalf("destroy attempts = %v", summary.Attempted)
	}
	if summary.OperationState != "partial" || summary.DestroyedClaimed || summary.InstancePhase != "degraded" {
		t.Fatalf("destroy summary = %#v", summary)
	}
}

func Case009OldStateMigratesExplicitly(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{ReadableStateMajors: []uint32{1}, WritableStateMajor: 2})
	source := contract.ProviderState{SchemaMajor: 1, ResourceID: "vm.router", Opaque: []byte(`{"state":1}`), Digest: "source-digest"}
	result := harness.MigrateState(source, 2, true)
	if result.Error != nil || !result.Possible || !result.SourceUnchanged || result.State != nil {
		t.Fatalf("dry-run migration = %#v", result)
	}
	if source.SchemaMajor != 1 || source.Digest != "source-digest" {
		t.Fatal("dry-run migration changed source state")
	}
}

func Case010UnknownStateFailsClosed(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{ReadableStateMajors: []uint32{1, 2}})
	request := applyRequest("vm.router")
	request.PriorState = &contract.ProviderState{SchemaMajor: 3, ResourceID: "vm.router"}
	result, err := harness.Apply(request)
	if err != nil || result.Error == nil || result.Error.Class != "state_version_incompatible" || harness.Snapshot().MutationCount != 0 {
		t.Fatalf("unknown state result = %#v, %v", result, err)
	}
}

func Case011CapabilityDowngradeInvalidatesPlan(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{})
	providerError := harness.ValidatePlan("old", "new")
	if providerError == nil || providerError.Code != "stale_plan" || !providerError.RevalidationRequired || harness.Snapshot().MutationCount != 0 {
		t.Fatalf("capability downgrade result = %#v", providerError)
	}
}

func Case012CancellationBeforeUnsafeBoundary(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{InvocationPhase: contract.PhaseBeforeProviderRequest})
	if result := harness.Cancel("invocation-cancel"); result.Outcome != contract.CancelRequested || result.OperationState != "canceled" || result.QueryRequired || harness.Snapshot().MutationCount != 0 {
		t.Fatalf("early cancellation outcome = %#v", result)
	}
}

func Case013CancellationAfterSideEffect(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{InvocationPhase: contract.PhaseSideEffectCommitted})
	if result := harness.Cancel("invocation-cancel"); result.Outcome != contract.CancelTooLate || result.OperationState != "running" || !result.QueryRequired {
		t.Fatalf("late cancellation outcome = %#v", result)
	}
}

func Case014SecretCanaryAbsent(t *testing.T, factory contract.Factory) {
	t.Helper()
	const canary = "LABCENTRAL_TEST_SECRET_DO_NOT_LOG"
	harness := factory(contract.Scenario{})
	request := applyRequest("container.secret")
	request.TargetCredential = []byte(canary)
	request.GuestSecret = []byte(canary)
	result, err := harness.Apply(request)
	assertMutationSuccess(t, result, err)
	encoded, err := json.Marshal(struct {
		Result   contract.MutationResult
		Snapshot contract.Snapshot
	}{result, harness.Snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(canary)) {
		t.Fatalf("secret canary escaped into provider outputs: %s", encoded)
	}
}

func Case015PlanIsDeterministic(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{})
	request := contract.PlanRequest{CanonicalDigest: "same", CapabilityDigest: "same", PolicyDigest: "same"}
	first := harness.Plan(request)
	second := harness.Plan(request)
	if !bytes.Equal(first.Bytes, second.Bytes) || first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("plans differ: first=%#v second=%#v", first, second)
	}
}

func Case016RestartPreservesInvocation(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{Fault: contract.FaultPluginProcessExit, InFlightInvocationID: "inv-123"})
	restarted := harness.Restart()
	observation := restarted.GetInvocation("inv-123")
	if !observation.Known || observation.Complete || len(observation.Result.Progress) != 2 {
		t.Fatalf("restarted invocation = %#v", observation)
	}
	if observation.Result.Progress[0].Sequence >= observation.Result.Progress[1].Sequence || restarted.Snapshot().DuplicateMutations != 0 {
		t.Fatalf("restart progress or mutation count invalid: %#v", restarted.Snapshot())
	}
}

func Case017ReverseDependencyDestroyOrder(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{})
	summary := harness.DestroyGraph([]contract.GraphNode{
		{ID: "network.red"},
		{ID: "vm.router", Dependencies: []string{"network.red"}},
		{ID: "access.router", Dependencies: []string{"vm.router"}},
	})
	if !reflect.DeepEqual(summary.Attempted, []string{"access.router", "vm.router", "network.red"}) {
		t.Fatalf("destroy order = %v", summary.Attempted)
	}
}

func Case018OwnershipMismatchBlocksDestroy(t *testing.T, factory contract.Factory) {
	t.Helper()
	harness := factory(contract.Scenario{})
	state := contract.ProviderState{SchemaMajor: 2, ResourceID: "vm.router", Ownership: []contract.Primitive{{ID: "vm-123", Kind: "vm", OwnershipToken: "token-a"}}}
	result := harness.Destroy(contract.CallContext{ResourceID: "vm.router", DesiredGeneration: 1, FencingToken: 1}, state, "token-b")
	if result.Error == nil || result.Error.Code != "ownership_mismatch" {
		t.Fatalf("ownership mismatch result = %#v", result)
	}
	snapshot := harness.Snapshot()
	if snapshot.DeleteCount != 0 || len(snapshot.AuditAlerts) != 1 {
		t.Fatalf("ownership mismatch effects = %#v", snapshot)
	}
}

func applyRequest(resourceID string) contract.ApplyRequest {
	return contract.ApplyRequest{
		Context: contract.CallContext{
			OperationID:       "operation-1",
			InvocationID:      "invocation-1",
			ResourceID:        resourceID,
			IdempotencyKey:    "operation-1/apply/" + resourceID + "/1",
			DesiredGeneration: 1,
			FencingToken:      1,
		},
		DisplayName: resourceID,
	}
}

func assertMutationSuccess(t *testing.T, result contract.MutationResult, err error) {
	t.Helper()
	if err != nil || result.Error != nil || result.State == nil {
		t.Fatalf("mutation failed: result=%#v err=%v", result, err)
	}
}
