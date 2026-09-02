package fakeprovider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
)

const (
	pluginID      = "io.labcentral.provider.fake"
	pluginVersion = "1.0.0"
)

type Fake struct {
	mu                sync.Mutex
	scenario          contract.Scenario
	resources         map[string]contract.ProviderState
	resourceFences    map[string]uint64
	invocations       map[string]contract.InvocationResult
	idempotency       map[string]contract.MutationResult
	requestSignatures map[string]string
	snapshot          contract.Snapshot
	clock             time.Time
}

func New(scenario contract.Scenario) contract.Harness {
	fake := &Fake{
		scenario:          cloneScenario(scenario),
		resources:         make(map[string]contract.ProviderState),
		resourceFences:    make(map[string]uint64),
		invocations:       make(map[string]contract.InvocationResult),
		idempotency:       make(map[string]contract.MutationResult),
		requestSignatures: make(map[string]string),
		clock:             scenario.VirtualNow.UTC(),
	}
	if fake.clock.IsZero() {
		fake.clock = time.Unix(0, 0).UTC()
	}
	fake.snapshot.LogicalResources = append([]string(nil), scenario.PriorLogicalResources...)
	if scenario.InFlightInvocationID != "" {
		progress := []contract.Progress{{Sequence: 1, Phase: "provider_task_created", Message: "provider task recorded", At: fake.nextTimeLocked()}}
		fake.invocations[scenario.InFlightInvocationID] = contract.InvocationResult{Known: true, Complete: false, Result: contract.MutationResult{Progress: progress}}
		fake.snapshot.Progress = append(fake.snapshot.Progress, progress...)
	}
	return fake
}

func (f *Fake) Apply(request contract.ApplyRequest) (contract.MutationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	signature := applySignature(request)
	if prior, exists := f.idempotency[request.Context.IdempotencyKey]; exists {
		if f.requestSignatures[request.Context.IdempotencyKey] != signature {
			return mutationError("idempotency_conflict", "invalid_declaration", "idempotency key is bound to different mutation input"), nil
		}
		return cloneMutationResult(prior), nil
	}
	if request.Context.DesiredGeneration < f.scenario.ResourceGeneration || request.Context.FencingToken < f.scenario.FencingToken {
		return mutationError("stale_generation", "state_version_incompatible", "desired generation or fence is stale"), nil
	}
	if existing, exists := f.resources[request.Context.ResourceID]; exists {
		if request.Context.DesiredGeneration < existing.Generation ||
			(request.Context.DesiredGeneration == existing.Generation && request.Context.FencingToken < f.resourceFences[request.Context.ResourceID]) {
			return mutationError("stale_generation", "state_version_incompatible", "desired generation or fence is stale"), nil
		}
	}
	if request.PriorState != nil && !f.readableMajor(request.PriorState.SchemaMajor) {
		return mutationError("state_version_incompatible", "state_version_incompatible", "provider state major is not readable"), nil
	}
	if request.PlanCapabilityDigest != "" && request.CurrentCapabilityDigest != "" && request.PlanCapabilityDigest != request.CurrentCapabilityDigest {
		return mutationError("stale_plan", "invalid_declaration", "capability digest changed after planning"), nil
	}
	if f.scenario.CurrentCapabilityDigest != "" && request.PlanCapabilityDigest != "" && request.PlanCapabilityDigest != f.scenario.CurrentCapabilityDigest {
		return mutationError("stale_plan", "invalid_declaration", "capability digest changed after planning"), nil
	}
	if f.scenario.CollisionDisplayName != "" && request.DisplayName == f.scenario.CollisionDisplayName {
		return mutationError("ownership_mismatch", "authorization", "display name is owned by another installation"), nil
	}
	if f.scenario.Fault == contract.FaultFailDeleteDisk && len(f.scenario.CreatedPrimitives) > 0 {
		result := f.compensate(f.scenario.CreatedPrimitives, true)
		result.Error = &contract.StructuredError{Code: "partial_cleanup", Class: "partial_cleanup", Message: "compensation left owned primitives"}
		result.OperationState = "partial"
		result.InstancePhase = "degraded"
		f.recordResult(request, result)
		return cloneMutationResult(result), nil
	}
	steps := append([]string(nil), f.scenario.PhysicalSteps...)
	if len(steps) == 0 {
		steps = []string{"resource"}
	}
	created := make([]contract.Primitive, 0, len(steps))
	for _, step := range steps {
		if f.scenario.Fault == contract.FaultFailBootstrap && step == "bootstrap" {
			result := f.compensate(created, false)
			result.Error = &contract.StructuredError{Code: "provider_step_failed", Class: "permanent_provider", Message: "bootstrap failed"}
			result.OperationState = "failed"
			result.InstancePhase = "degraded"
			f.recordResult(request, result)
			return cloneMutationResult(result), nil
		}
		primitive := contract.Primitive{
			ID:             request.Context.ResourceID + "-" + step,
			Kind:           step,
			DisplayName:    request.DisplayName,
			OwnershipToken: ownershipToken(request.Context.ResourceID, request.Context.DesiredGeneration, step),
		}
		created = append(created, primitive)
		f.snapshot.CreateCount++
		f.snapshot.MutationCount++
	}
	state := newProviderState(request.Context.ResourceID, request.Context.DesiredGeneration, created, f.writableMajor())
	progress := []contract.Progress{
		{Sequence: 1, Phase: "creating", Message: "creating owned primitives", At: f.nextTimeLocked()},
		{Sequence: 2, Phase: "ready", Message: "logical resource ready", At: f.nextTimeLocked()},
	}
	result := contract.MutationResult{State: &state, ResourceState: "ready", OperationState: "succeeded", InstancePhase: "ready", Progress: progress}
	f.resources[request.Context.ResourceID] = cloneProviderState(state)
	f.resourceFences[request.Context.ResourceID] = request.Context.FencingToken
	f.snapshot.LogicalResources = appendUnique(f.snapshot.LogicalResources, request.Context.ResourceID)
	f.snapshot.Progress = append(f.snapshot.Progress, progress...)
	f.snapshot.SafeSinks = append(f.snapshot.SafeSinks,
		[]byte(`{"message":"logical resource ready"}`),
		append([]byte(nil), state.Opaque...),
	)
	f.recordResult(request, result)
	if f.scenario.Fault == contract.FaultCrashAfterCreate || f.scenario.Fault == contract.FaultDropFinalResponse {
		return contract.MutationResult{}, contract.ErrOutcomeUnknown
	}
	return cloneMutationResult(result), nil
}

func (f *Fake) recordResult(request contract.ApplyRequest, result contract.MutationResult) {
	f.idempotency[request.Context.IdempotencyKey] = cloneMutationResult(result)
	f.requestSignatures[request.Context.IdempotencyKey] = applySignature(request)
	f.invocations[request.Context.InvocationID] = contract.InvocationResult{Known: true, Complete: true, Result: cloneMutationResult(result)}
}

func (f *Fake) compensate(created []contract.Primitive, failDisk bool) contract.MutationResult {
	result := contract.MutationResult{CompensationAttempted: len(created) > 0, CompensationComplete: true}
	for index := len(created) - 1; index >= 0; index-- {
		primitive := created[index]
		if failDisk && primitive.Kind == "disk" {
			result.CompensationComplete = false
			result.Leftovers = append(result.Leftovers, primitive.ID)
			continue
		}
		result.CompensationOrder = append(result.CompensationOrder, primitive.Kind)
		f.snapshot.DeleteCount++
		f.snapshot.MutationCount++
	}
	return result
}

func (f *Fake) GetInvocation(invocationID string) contract.InvocationResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, exists := f.invocations[invocationID]
	if !exists {
		return contract.InvocationResult{}
	}
	return cloneInvocationResult(result)
}

func (f *Fake) Destroy(context contract.CallContext, state contract.ProviderState, observedOwnershipToken string) contract.MutationResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if context.DesiredGeneration < f.scenario.ResourceGeneration || context.FencingToken < f.scenario.FencingToken {
		return mutationError("stale_generation", "state_version_incompatible", "destroy generation or fence is stale")
	}
	if !f.readableMajor(state.SchemaMajor) {
		return mutationError("state_version_incompatible", "state_version_incompatible", "provider state major is not readable")
	}
	for _, primitive := range state.Ownership {
		if primitive.OwnershipToken != observedOwnershipToken {
			f.snapshot.AuditAlerts = append(f.snapshot.AuditAlerts, "ownership_mismatch:"+primitive.ID)
			return mutationError("ownership_mismatch", "authorization", "observed ownership token does not match recorded state")
		}
	}
	for _, primitive := range state.Ownership {
		f.snapshot.AttemptedDeletes = append(f.snapshot.AttemptedDeletes, primitive.ID)
		f.snapshot.DeleteCount++
		f.snapshot.MutationCount++
	}
	delete(f.resources, context.ResourceID)
	delete(f.resourceFences, context.ResourceID)
	return contract.MutationResult{}
}

func (f *Fake) DestroyGraph(nodes []contract.GraphNode) contract.DestroySummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	order := reverseLayers(nodes)
	leftovers := make(map[string]struct{})
	summary := contract.DestroySummary{OperationState: "succeeded", InstancePhase: "destroyed", DestroyedClaimed: true}
	for _, id := range order {
		summary.Attempted = append(summary.Attempted, id)
		if f.scenario.Fault == contract.FaultFailDeleteVMAttacker && id == "vm.attacker" {
			leftovers[id] = struct{}{}
			continue
		}
		blocked := false
		for _, node := range nodes {
			for _, dependency := range node.Dependencies {
				if dependency == id {
					if _, remains := leftovers[node.ID]; remains {
						blocked = true
					}
				}
			}
		}
		if blocked {
			leftovers[id] = struct{}{}
			continue
		}
		f.snapshot.DeleteCount++
		f.snapshot.MutationCount++
	}
	if len(leftovers) > 0 {
		summary.OperationState = "partial"
		summary.InstancePhase = "degraded"
		summary.DestroyedClaimed = false
		for id := range leftovers {
			summary.Leftovers = append(summary.Leftovers, id)
		}
		sort.Strings(summary.Leftovers)
	}
	f.snapshot.AttemptedDeletes = append(f.snapshot.AttemptedDeletes, summary.Attempted...)
	return summary
}

func reverseLayers(nodes []contract.GraphNode) []string {
	byID := make(map[string]contract.GraphNode, len(nodes))
	remainingDependents := make(map[string]int, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
		remainingDependents[node.ID] = 0
	}
	for _, node := range nodes {
		for _, dependency := range node.Dependencies {
			remainingDependents[dependency]++
		}
	}
	result := make([]string, 0, len(nodes))
	for len(result) < len(nodes) {
		layer := make([]string, 0)
		for id, count := range remainingDependents {
			if count == 0 {
				layer = append(layer, id)
			}
		}
		sort.Strings(layer)
		if len(layer) == 0 {
			break
		}
		for _, id := range layer {
			result = append(result, id)
			delete(remainingDependents, id)
			for _, dependency := range byID[id].Dependencies {
				remainingDependents[dependency]--
			}
		}
	}
	return result
}

func (f *Fake) MigrateState(state contract.ProviderState, destinationMajor uint32, dryRun bool) contract.MigrationResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.readableMajor(state.SchemaMajor) {
		return contract.MigrationResult{SourceUnchanged: true, Error: &contract.StructuredError{Code: "state_version_incompatible", Class: "state_version_incompatible", Message: "source state major is not readable"}}
	}
	if destinationMajor != f.writableMajor() {
		return contract.MigrationResult{SourceUnchanged: true, Error: &contract.StructuredError{Code: "migration_destination_unsupported", Class: "state_version_incompatible", Message: "destination state major is not writable"}}
	}
	result := contract.MigrationResult{Possible: true, SourceUnchanged: true}
	if !dryRun {
		migrated := cloneProviderState(state)
		migrated.SchemaMajor = destinationMajor
		migrated.Digest = stateDigest(migrated)
		result.State = &migrated
	}
	return result
}

func (f *Fake) ValidatePlan(planCapabilityDigest, currentCapabilityDigest string) *contract.StructuredError {
	if planCapabilityDigest != currentCapabilityDigest {
		return &contract.StructuredError{Code: "stale_plan", Class: "invalid_declaration", Message: "capability digest changed; revalidation is required", RevalidationRequired: true}
	}
	return nil
}

func (f *Fake) Cancel(_ string) contract.CancelResult {
	if f.scenario.InvocationPhase == contract.PhaseSideEffectCommitted {
		return contract.CancelResult{Outcome: contract.CancelTooLate, OperationState: "running", QueryRequired: true}
	}
	return contract.CancelResult{Outcome: contract.CancelRequested, OperationState: "canceled"}
}

func (f *Fake) Plan(request contract.PlanRequest) contract.PlanResult {
	hash := sha256.New()
	for _, value := range []string{request.CanonicalDigest, request.CapabilityDigest, request.PolicyDigest} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	bytes, _ := json.Marshal(struct {
		Canonical  string `json:"canonicalDigest"`
		Capability string `json:"capabilityDigest"`
		Policy     string `json:"policyDigest"`
		Digest     string `json:"planDigest"`
	}{request.CanonicalDigest, request.CapabilityDigest, request.PolicyDigest, digest})
	return contract.PlanResult{Bytes: bytes, Digest: digest}
}

func (f *Fake) AdvanceClock(duration time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(duration)
}

func (f *Fake) Restart() contract.Harness {
	f.mu.Lock()
	defer f.mu.Unlock()
	restarted := &Fake{
		scenario:          cloneScenario(f.scenario),
		resources:         make(map[string]contract.ProviderState, len(f.resources)),
		resourceFences:    make(map[string]uint64, len(f.resourceFences)),
		invocations:       make(map[string]contract.InvocationResult, len(f.invocations)),
		idempotency:       make(map[string]contract.MutationResult, len(f.idempotency)),
		requestSignatures: make(map[string]string, len(f.requestSignatures)),
		snapshot:          cloneSnapshot(f.snapshot),
		clock:             f.clock,
	}
	for id, state := range f.resources {
		restarted.resources[id] = cloneProviderState(state)
	}
	for id, token := range f.resourceFences {
		restarted.resourceFences[id] = token
	}
	for id, result := range f.invocations {
		restarted.invocations[id] = cloneInvocationResult(result)
	}
	for key, result := range f.idempotency {
		restarted.idempotency[key] = cloneMutationResult(result)
	}
	for key, signature := range f.requestSignatures {
		restarted.requestSignatures[key] = signature
	}
	if f.scenario.InFlightInvocationID != "" {
		entry := restarted.invocations[f.scenario.InFlightInvocationID]
		next := contract.Progress{Sequence: uint64(len(entry.Result.Progress) + 1), Phase: "resumed", Message: "invocation resumed", At: restarted.nextTimeLocked()}
		entry.Result.Progress = append(entry.Result.Progress, next)
		restarted.invocations[f.scenario.InFlightInvocationID] = entry
		restarted.snapshot.Progress = append(restarted.snapshot.Progress, next)
	}
	return restarted
}

func (f *Fake) nextTimeLocked() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *Fake) Snapshot() contract.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneSnapshot(f.snapshot)
}

func (f *Fake) readableMajor(major uint32) bool {
	readable := f.scenario.ReadableStateMajors
	if len(readable) == 0 {
		readable = []uint32{1, 2}
	}
	for _, candidate := range readable {
		if candidate == major {
			return true
		}
	}
	return false
}

func (f *Fake) writableMajor() uint32 {
	if f.scenario.WritableStateMajor == 0 {
		return 2
	}
	return f.scenario.WritableStateMajor
}

func mutationError(code, class, message string) contract.MutationResult {
	return contract.MutationResult{Error: &contract.StructuredError{Code: code, Class: class, Message: message}}
}

func ownershipToken(resourceID string, generation uint64, kind string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", resourceID, generation, kind)))
	return fmt.Sprintf("%x", digest[:16])
}

func newProviderState(resourceID string, generation uint64, ownership []contract.Primitive, major uint32) contract.ProviderState {
	state := contract.ProviderState{SchemaMajor: major, PluginID: pluginID, PluginVersion: pluginVersion, ResourceID: resourceID, Generation: generation, Ownership: append([]contract.Primitive(nil), ownership...)}
	state.Opaque, _ = json.Marshal(struct {
		Generation uint64   `json:"generation"`
		IDs        []string `json:"ownershipIDs"`
	}{generation, primitiveIDs(ownership)})
	state.Digest = stateDigest(state)
	return state
}

func stateDigest(state contract.ProviderState) string {
	hash := sha256.New()
	writeFramed(hash, fmt.Sprintf("%d", state.SchemaMajor))
	writeFramed(hash, state.PluginID)
	writeFramed(hash, state.PluginVersion)
	writeFramed(hash, state.ResourceID)
	writeFramed(hash, fmt.Sprintf("%d", state.Generation))
	for _, primitive := range state.Ownership {
		writeFramed(hash, primitive.ID)
		writeFramed(hash, primitive.Kind)
		writeFramed(hash, primitive.DisplayName)
		writeFramed(hash, primitive.OwnershipToken)
	}
	writeFramedBytes(hash, state.Opaque)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeFramed(hash hashWriter, value string) { writeFramedBytes(hash, []byte(value)) }

func writeFramedBytes(hash hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write(value)
}

func applySignature(request contract.ApplyRequest) string {
	hash := sha256.New()
	for _, value := range []string{
		request.Context.OperationID,
		request.Context.InvocationID,
		request.Context.ResourceID,
		fmt.Sprintf("%d", request.Context.DesiredGeneration),
		fmt.Sprintf("%d", request.Context.FencingToken),
		request.DisplayName,
		request.PlanCapabilityDigest,
		request.CurrentCapabilityDigest,
	} {
		writeFramed(hash, value)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func primitiveIDs(primitives []contract.Primitive) []string {
	result := make([]string, len(primitives))
	for index := range primitives {
		result[index] = primitives[index].ID
	}
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneScenario(source contract.Scenario) contract.Scenario {
	result := source
	result.PhysicalSteps = append([]string(nil), source.PhysicalSteps...)
	result.CreatedPrimitives = append([]contract.Primitive(nil), source.CreatedPrimitives...)
	result.PriorLogicalResources = append([]string(nil), source.PriorLogicalResources...)
	result.ReadableStateMajors = append([]uint32(nil), source.ReadableStateMajors...)
	return result
}

func cloneProviderState(source contract.ProviderState) contract.ProviderState {
	result := source
	result.Ownership = append([]contract.Primitive(nil), source.Ownership...)
	result.Opaque = append([]byte(nil), source.Opaque...)
	return result
}

func cloneMutationResult(source contract.MutationResult) contract.MutationResult {
	result := source
	if source.State != nil {
		state := cloneProviderState(*source.State)
		result.State = &state
	}
	if source.Error != nil {
		errorCopy := *source.Error
		result.Error = &errorCopy
	}
	result.CompensationOrder = append([]string(nil), source.CompensationOrder...)
	result.Leftovers = append([]string(nil), source.Leftovers...)
	result.Progress = append([]contract.Progress(nil), source.Progress...)
	return result
}

func cloneInvocationResult(source contract.InvocationResult) contract.InvocationResult {
	result := source
	result.Result = cloneMutationResult(source.Result)
	return result
}

func cloneSnapshot(source contract.Snapshot) contract.Snapshot {
	result := source
	result.AttemptedDeletes = append([]string(nil), source.AttemptedDeletes...)
	result.LogicalResources = append([]string(nil), source.LogicalResources...)
	result.Progress = append([]contract.Progress(nil), source.Progress...)
	result.AuditAlerts = append([]string(nil), source.AuditAlerts...)
	result.SafeSinks = make([][]byte, len(source.SafeSinks))
	for index := range source.SafeSinks {
		result.SafeSinks[index] = append([]byte(nil), source.SafeSinks[index]...)
	}
	return result
}

var _ contract.Harness = (*Fake)(nil)
