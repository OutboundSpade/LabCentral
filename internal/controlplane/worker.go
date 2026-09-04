package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const providerInvocationTimeout = 10 * time.Minute

var errProviderOutcomeUnknown = errors.New("provider outcome is unknown")

type Worker struct {
	store         application.WorkerStore
	targets       *TargetRegistry
	ids           IDSource
	owner         string
	leaseDuration time.Duration
	pollInterval  time.Duration
	onError       func(error)
	onComplete    func(OperationKind, domain.OperationState, time.Duration)
	authorizer    application.Authorizer
}

type WorkerOptions struct {
	Owner         string
	LeaseDuration time.Duration
	PollInterval  time.Duration
	OnError       func(error)
	OnComplete    func(OperationKind, domain.OperationState, time.Duration)
	Authorizer    application.Authorizer
}

func NewWorker(store application.WorkerStore, provider application.Provider, ids IDSource, options WorkerOptions) (*Worker, error) {
	registry, err := NewDockerTargetRegistry(provider)
	if err != nil {
		return nil, err
	}
	return NewWorkerWithTargets(store, registry, ids, options)
}

func NewWorkerWithTargets(store application.WorkerStore, targets *TargetRegistry, ids IDSource, options WorkerOptions) (*Worker, error) {
	if store == nil || targets == nil || ids == nil {
		return nil, fmt.Errorf("worker store, target registry, and ID source are required")
	}
	if options.Owner == "" {
		options.Owner = "local-worker"
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if options.OnError == nil {
		options.OnError = func(error) {}
	}
	if options.OnComplete == nil {
		options.OnComplete = func(OperationKind, domain.OperationState, time.Duration) {}
	}
	return &Worker{
		store: store, targets: targets, ids: ids, owner: options.Owner,
		leaseDuration: options.LeaseDuration, pollInterval: options.PollInterval,
		onError: options.OnError, onComplete: options.OnComplete, authorizer: options.Authorizer,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		err := w.RunOnce(ctx)
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, context.Canceled) {
			w.onError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	claim, err := w.store.ClaimTargets(ctx, w.owner, w.leaseDuration, w.targets.AvailableIDs())
	if err != nil {
		return err
	}
	fence := Fence{
		OperationID: claim.Operation.ID,
		LeaseOwner:  claim.LeaseOwner,
		Token:       claim.Operation.FencingToken,
		Generation:  claim.Operation.Generation,
		Attempt:     claim.Operation.Attempt,
	}
	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewalErrors := make(chan error, 1)
	go w.renewLease(workContext, fence, renewalErrors)
	started := time.Now()
	err = w.process(workContext, fence, claim.Action, claim.Recovery, claim.Payload)
	cancel()
	select {
	case renewalErr := <-renewalErrors:
		if renewalErr != nil {
			return renewalErr
		}
	default:
	}
	if err == nil {
		completed, loadErr := w.store.GetOperation(ctx, fence.OperationID)
		if loadErr == nil && completed.State.Terminal() {
			w.onComplete(completed.Kind, completed.State, time.Since(started))
		}
	}
	return err
}

func (w *Worker) renewLease(ctx context.Context, fence Fence, failures chan<- error) {
	interval := w.leaseDuration / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.Renew(ctx, fence, w.leaseDuration); err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, fence Fence, claimAction application.ClaimAction, recovery *InvocationRecord, payloadBytes []byte) error {
	var payload jobPayload
	if err := strictDecode(payloadBytes, &payload); err != nil || payload.Version != jobPayloadVersion || payload.InstanceID == "" {
		return fmt.Errorf("unsupported local job payload")
	}
	instance, err := w.store.GetInstance(ctx, payload.InstanceID)
	if err != nil {
		return err
	}
	if payload.InstanceID != instance.ID {
		return fmt.Errorf("job instance identity mismatch")
	}
	operation, err := w.store.GetOperation(ctx, fence.OperationID)
	if err != nil || operation.InstanceID != instance.ID || operation.ActorID == "" {
		return fmt.Errorf("load queued operation actor: %w", err)
	}
	if operation.Kind != payload.Kind {
		return fmt.Errorf("queued operation kind does not match its payload")
	}
	if err := w.authorizeOperation(ctx, operation, instance); err != nil {
		return err
	}
	if claimAction == application.ClaimGetInvocation {
		if recovery == nil {
			return fmt.Errorf("invocation recovery claim has no invocation")
		}
		return w.recoverInvocation(ctx, fence, operation, instance, *recovery)
	}
	switch payload.Kind {
	case OperationApply:
		return w.apply(ctx, fence, operation.ActorID, instance)
	case OperationDestroy:
		return w.destroy(ctx, fence, operation.ActorID, instance)
	default:
		return fmt.Errorf("unsupported local operation kind %q", payload.Kind)
	}
}

func (w *Worker) authorizeOperation(ctx context.Context, operation OperationRecord, instance InstanceRecord) error {
	if w.authorizer == nil {
		return nil
	}
	if operation.ActorID == SystemExpirationActorID {
		if operation.Kind != OperationDestroy {
			return fmt.Errorf("expiration system actor may only execute destroy operations")
		}
		return nil
	}
	var action application.PermissionAction
	switch operation.Kind {
	case OperationApply:
		action = application.ActionInstanceCreate
	case OperationDestroy:
		action = application.ActionInstanceDestroy
	default:
		return fmt.Errorf("queued operation kind is invalid")
	}
	if err := w.authorizer.Authorize(ctx, application.AuthorizationRequest{
		SubjectID: operation.ActorID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(instance.ProjectID),
		TargetID: instance.ProviderTargetID, OwnerKind: instance.OwnerKind, OwnerID: instance.OwnerID,
	}); err != nil {
		return fmt.Errorf("queued operation authorization is no longer valid: %w", err)
	}
	return nil
}

func (w *Worker) authorizeActor(ctx context.Context, actorID domain.UserID, providerAction string, instance InstanceRecord) error {
	if w.authorizer == nil || actorID == SystemExpirationActorID {
		return nil
	}
	action := application.ActionInstanceDestroy
	if providerAction == "apply" {
		action = application.ActionInstanceCreate
	}
	if err := w.authorizer.Authorize(ctx, application.AuthorizationRequest{
		SubjectID: actorID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(instance.ProjectID),
		TargetID: instance.ProviderTargetID, OwnerKind: instance.OwnerKind, OwnerID: instance.OwnerID,
	}); err != nil {
		return fmt.Errorf("provider credential authorization is no longer valid: %w", err)
	}
	return nil
}

func (w *Worker) apply(ctx context.Context, fence Fence, actorID domain.UserID, instance InstanceRecord) error {
	allowed, err := w.store.ApplyExecutionAllowed(ctx, fence)
	if err != nil {
		return err
	}
	if !allowed {
		return w.compensateAndFinalize(ctx, fence, actorID, instance, "", nil, fmt.Errorf("instance lifetime expired before provider work began"))
	}
	for _, transition := range []struct {
		phase   domain.InstancePhase
		message string
	}{{domain.InstanceValidating, "Revalidating pinned Lab"}, {domain.InstancePlanned, "Using persisted provider plans"}, {domain.InstanceApplying, "Applying provider resources"}} {
		if err := w.store.MarkInstancePhase(ctx, fence, transition.phase, transition.message); err != nil {
			return err
		}
	}
	session, err := w.targets.Acquire(ctx, instance.ProviderTargetID, application.InvocationScope{}, false)
	if err != nil {
		return fmt.Errorf("acquire persisted provider target: %w", err)
	}
	defer session.Close()
	validationContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	validation, err := session.Provider.Validate(validationContext, application.ProviderValidationRequest{
		Call:   application.ProviderCall{RequestID: "worker-validate", OperationID: string(fence.OperationID), InstanceID: string(instance.ID), TargetID: string(instance.ProviderTargetID)},
		Target: session.Context, Canonical: instance.CanonicalLabJSON,
		Override: instance.ProviderOverrideJSON,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("provider revalidation unavailable: %w", err)
	}
	if len(validation.Errors) > 0 {
		first := validation.Errors[0]
		return w.compensateAndFinalize(ctx, fence, actorID, instance, "", nil, fmt.Errorf("provider validation failed: %s", first.Message))
	}
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Phase == domain.ResourceReady {
			continue
		}
		if err := w.store.MarkInstancePhase(ctx, fence, domain.InstanceApplying, "Applying "+string(resource.ID)); err != nil {
			return err
		}
		if !providerManagedKind(resource.Kind) {
			observed, projectionErr := projectCoreResource(resource, resources)
			if projectionErr != nil {
				_ = w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceFailed, []byte(`{}`))
				return w.compensateAndFinalize(ctx, fence, actorID, instance, resource.ID, nil, projectionErr)
			}
			if err := w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceReady, observed); err != nil {
				return err
			}
			continue
		}
		allowed, err := w.store.ApplyExecutionAllowed(ctx, fence)
		if err != nil {
			return err
		}
		if !allowed {
			return w.compensateAndFinalize(ctx, fence, actorID, instance, "", nil, fmt.Errorf("instance lifetime expired during apply"))
		}
		result, callErr := w.invoke(ctx, fence, actorID, instance, resource, "apply")
		if callErr != nil {
			return callErr
		}
		if result.Error != nil {
			return w.compensateAndFinalize(ctx, fence, actorID, instance, resource.ID, result, fmt.Errorf("%s", result.Error.Message))
		}
		resources, err = w.store.ListResources(ctx, instance.ID)
		if err != nil {
			return err
		}
	}
	resources, err = w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	allowed, err = w.store.ApplyExecutionAllowed(ctx, fence)
	if err != nil {
		return err
	}
	if !allowed {
		return w.compensateAndFinalize(ctx, fence, actorID, instance, "", nil, fmt.Errorf("instance lifetime expired during apply"))
	}
	outputs, err := collectOutputs(resources)
	if err != nil {
		return w.compensateAndFinalize(ctx, fence, actorID, instance, "", nil, err)
	}
	return w.finalize(ctx, fence, actorID, OperationApply, domain.OperationSucceeded, domain.InstanceReady, "Lab is ready", "", "", outputs)
}

func (w *Worker) destroy(ctx context.Context, fence Fence, actorID domain.UserID, instance InstanceRecord) error {
	if err := w.store.MarkInstancePhase(ctx, fence, domain.InstanceDestroying, "Destroying provider resources"); err != nil {
		return err
	}
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	leftovers := make([]string, 0)
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if resource.Phase == domain.ResourceDestroyed {
			if err := w.releaseResourceClaims(ctx, fence, resource); err != nil {
				return err
			}
			continue
		}
		if providerManagedKind(resource.Kind) &&
			(resource.Phase == domain.ResourceUnknown || resource.Phase == domain.ResourceCleanupFailed) {
			// A dispatched invocation with no returned provider state is not
			// positive absence evidence. Keep its claims and instance quota until
			// GetInvocation or an ownership-verified provider query resolves it.
			leftovers = append(leftovers, string(resource.ID))
			continue
		}
		if !providerManagedKind(resource.Kind) || len(resource.ProviderState) == 0 {
			if err := w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceDestroyed, []byte(`{"destroyed":true}`)); err != nil {
				return err
			}
			continue
		}
		result, callErr := w.invoke(ctx, fence, actorID, instance, resource, "destroy")
		if callErr != nil {
			if errors.Is(callErr, errProviderOutcomeUnknown) {
				return callErr
			}
			leftovers = append(leftovers, string(resource.ID))
			continue
		}
		if result.Error != nil {
			leftovers = append(leftovers, string(resource.ID))
		}
	}
	if len(leftovers) > 0 {
		return w.finalize(ctx, fence, actorID, OperationDestroy, domain.OperationPartial, domain.InstanceDegraded,
			"Destroy completed with owned leftovers", "partial_cleanup", "Cleanup failed for: "+strings.Join(leftovers, ", "), nil)
	}
	return w.finalize(ctx, fence, actorID, OperationDestroy, domain.OperationSucceeded, domain.InstanceDestroyed, "Lab was destroyed", "", "", []byte(`{}`))
}

func providerInvocationDigest(generation domain.Generation, action string, instance InstanceRecord, resource ResourceRecord) domain.SHA256Digest {
	return digestBytes("labcentral.provider-invocation/v2",
		[]byte(action), []byte(instance.ProviderTargetID), []byte(instance.ID), []byte(resource.ID),
		[]byte(fmt.Sprint(generation)), resource.CanonicalJSON, resource.ProviderPlan, resource.ProviderState)
}

func legacyProviderInvocationDigest(action string, instance InstanceRecord, resource ResourceRecord) domain.SHA256Digest {
	return digestBytes("labcentral.provider-invocation/v1",
		[]byte(action), []byte(instance.ProviderTargetID), resource.CanonicalJSON, resource.ProviderPlan, resource.ProviderState)
}

type invocationPreparer interface {
	PrepareInvocation(context.Context, PrepareInvocationRequest) (InvocationRecord, error)
}

func prepareProviderInvocation(ctx context.Context, store invocationPreparer, request PrepareInvocationRequest, legacyDigest domain.SHA256Digest) (InvocationRecord, error) {
	prepared, err := store.PrepareInvocation(ctx, request)
	var conflict *domain.DomainError
	if errors.As(err, &conflict) && conflict.Code == "idempotency_conflict" {
		// An operation prepared by a pre-v2 worker must retain its original
		// provider key across a rolling upgrade. Only the exact legacy digest
		// can satisfy the existing operation/resource/action binding.
		request.IdempotencyKey = legacyDigest.Hex()
		request.RequestDigest = legacyDigest
		return store.PrepareInvocation(ctx, request)
	}
	return prepared, err
}

func (w *Worker) invoke(ctx context.Context, fence Fence, actorID domain.UserID, instance InstanceRecord, resource ResourceRecord, action string) (*application.ProviderResult, error) {
	if err := w.authorizeActor(ctx, actorID, action, instance); err != nil {
		return nil, err
	}
	requestDigest := providerInvocationDigest(fence.Generation, action, instance, resource)
	invocationValue, err := w.ids.New("inv")
	if err != nil {
		return nil, err
	}
	prepared, err := prepareProviderInvocation(ctx, w.store, PrepareInvocationRequest{
		Fence: fence, InvocationID: domain.InvocationID(invocationValue), ResourceID: resource.ID, Action: action,
		IdempotencyKey: requestDigest.Hex(), RequestDigest: requestDigest,
	}, legacyProviderInvocationDigest(action, instance, resource))
	if err != nil {
		return nil, err
	}
	scope := application.InvocationScope{
		InvocationID: prepared.ID, InstanceID: instance.ID, ResourceID: resource.ID, ExpiresAt: time.Now().Add(providerInvocationTimeout + time.Minute),
	}
	session, err := w.targets.AcquireForSubject(ctx, instance.ProviderTargetID, actorID, scope, true)
	if err != nil {
		return nil, fmt.Errorf("acquire provider target for invocation: %w", err)
	}
	defer session.Close()
	input := application.ProviderMutation{
		Call: application.ProviderCall{
			RequestID: string(prepared.ID), OperationID: string(fence.OperationID), InvocationID: string(prepared.ID),
			InstanceID: string(instance.ID), ResourceID: string(resource.ID), TargetID: string(instance.ProviderTargetID),
			IdempotencyKey: prepared.IdempotencyKey, Generation: uint64(fence.Generation), Fence: uint64(fence.Token),
		},
		Target: session.Context, Canonical: resource.CanonicalJSON,
		Plan: resource.ProviderPlan, PriorState: resource.ProviderState, Ownership: []string{resource.OwnershipToken},
	}
	input.Allocations, err = w.resourceClaims(ctx, instance.ID, resource)
	if err != nil {
		return nil, err
	}
	input.Dependencies, err = w.resourceDependencies(ctx, instance.ID, resource)
	if err != nil {
		return nil, err
	}
	callContext, cancel := context.WithTimeout(ctx, providerInvocationTimeout)
	defer cancel()
	result, err := session.Provider.Invoke(callContext, action, input)
	if err != nil {
		return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, err)
	}
	if err := w.persistProviderResult(ctx, fence, prepared.ID, resource, action, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (w *Worker) recoverInvocation(ctx context.Context, fence Fence, operation OperationRecord, instance InstanceRecord, invocation InvocationRecord) error {
	if invocation.OperationID != operation.ID || invocation.ResourceID == "" || invocation.ID == "" || invocation.State != "unknown" {
		return fmt.Errorf("invocation recovery record does not match the claimed operation")
	}
	if invocation.Action != "apply" && invocation.Action != "destroy" && invocation.Action != "compensate" {
		return fmt.Errorf("invocation recovery action %q is unsupported", invocation.Action)
	}
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	var resource *ResourceRecord
	for index := range resources {
		if resources[index].ID == invocation.ResourceID {
			copy := resources[index]
			resource = &copy
			break
		}
	}
	if resource == nil || resource.InstanceID != instance.ID {
		return fmt.Errorf("invocation recovery resource %s is unavailable", invocation.ResourceID)
	}
	if err := w.authorizeActor(ctx, operation.ActorID, invocation.Action, instance); err != nil {
		return err
	}
	scope := application.InvocationScope{
		InvocationID: invocation.ID, InstanceID: instance.ID, ResourceID: resource.ID,
		ExpiresAt: time.Now().Add(providerInvocationTimeout + time.Minute),
	}
	session, err := w.targets.AcquireForSubject(ctx, instance.ProviderTargetID, operation.ActorID, scope, true)
	if err != nil {
		return fmt.Errorf("acquire provider target for invocation recovery: %w", err)
	}
	defer session.Close()
	lookupContext, cancel := context.WithTimeout(ctx, providerInvocationTimeout)
	result, err := session.Provider.Lookup(lookupContext, application.ProviderCall{
		RequestID: string(invocation.ID) + ".recovery", OperationID: string(operation.ID), InvocationID: string(invocation.ID),
		InstanceID: string(instance.ID), ResourceID: string(resource.ID), TargetID: string(instance.ProviderTargetID),
		IdempotencyKey: invocation.IdempotencyKey, Generation: uint64(fence.Generation), Fence: uint64(fence.Token),
	}, session.Context, string(invocation.ID))
	cancel()
	if err != nil {
		return fmt.Errorf("%w: GetInvocation for %s failed: %v", errProviderOutcomeUnknown, invocation.ID, err)
	}
	if invocationLookupUnresolved(result) {
		return fmt.Errorf("%w: GetInvocation for %s did not return a durable terminal result", errProviderOutcomeUnknown, invocation.ID)
	}
	if err := w.persistProviderResult(ctx, fence, invocation.ID, *resource, invocation.Action, result); err != nil {
		return err
	}
	switch invocation.Action {
	case "apply":
		if result.Error != nil {
			return w.compensateAndFinalize(ctx, fence, operation.ActorID, instance, resource.ID, result, fmt.Errorf("%s", result.Error.Message))
		}
		return w.apply(ctx, fence, operation.ActorID, instance)
	case "destroy":
		return w.destroy(ctx, fence, operation.ActorID, instance)
	case "compensate":
		return w.compensateAndFinalize(ctx, fence, operation.ActorID, instance, resource.ID, result, fmt.Errorf("apply compensation resumed after an interrupted provider response"))
	default:
		panic("validated recovery action")
	}
}

func invocationLookupUnresolved(result *application.ProviderResult) bool {
	if result == nil {
		return true
	}
	providerError := result.Error
	if providerError == nil {
		return false
	}
	switch providerError.Code {
	case "invocation_incomplete", "invocation_not_found", "journal_unavailable", "journal_corrupt",
		"invalid_invocation_request", "invalid_target_config":
		return true
	default:
		return false
	}
}

func (w *Worker) persistProviderResult(ctx context.Context, fence Fence, invocationID domain.InvocationID, resource ResourceRecord, action string, result *application.ProviderResult) error {
	if result == nil {
		return fmt.Errorf("provider result is required")
	}
	providerClaims, err := w.resourceClaims(ctx, resource.InstanceID, resource)
	if err != nil {
		return err
	}
	providerState := resource.ProviderState
	if result.State != nil {
		providerState = result.State
	}
	phase := domain.ResourceReady
	invocationState := "succeeded"
	errorCode, errorMessage := "", ""
	if action != "apply" {
		phase = domain.ResourceDestroyed
	}
	if result.Error != nil {
		phase = domain.ResourceFailed
		if action != "apply" {
			phase = domain.ResourceCleanupFailed
		}
		invocationState = "failed"
		errorCode = result.Error.Code
		errorMessage = result.Error.Message
	}
	if err := w.store.SaveResourceResult(ctx, ResourceResultUpdate{
		Fence: fence, InvocationID: invocationID, ResourceID: resource.ID, InvocationState: invocationState,
		Phase: phase, ProviderState: providerState, ObservedStateJSON: result.Observation, ErrorCode: errorCode, ErrorMessage: errorMessage,
	}); err != nil {
		return err
	}
	if transitionErr := w.transitionClaims(ctx, fence, resource, providerClaims, action, result); transitionErr != nil {
		return transitionErr
	}
	return nil
}

func (w *Worker) resourceClaims(ctx context.Context, instanceID domain.InstanceID, resource ResourceRecord) ([]application.AllocationKey, error) {
	claims, err := w.store.ListClaims(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	planDigest := rawDigest(resource.ProviderPlan)
	result := make([]application.AllocationKey, 0)
	for _, claim := range claims {
		if claim.Binding.ResourceID != resource.ID || claim.State == application.AllocationReleased {
			continue
		}
		if claim.Binding.InstanceID != instanceID || claim.Binding.PlanDigest != planDigest {
			return nil, fmt.Errorf("allocation binding for %s does not match its persisted provider plan", resource.ID)
		}
		result = append(result, application.AllocationKey{Namespace: claim.Key.Namespace, Value: claim.Key.Value})
	}
	return result, nil
}

func (w *Worker) resourceDependencies(ctx context.Context, instanceID domain.InstanceID, resource ResourceRecord) ([]application.ProviderDependency, error) {
	var canonical struct {
		DependsOn []string `json:"dependsOn"`
	}
	if err := decodeJSON(resource.CanonicalJSON, &canonical); err != nil {
		return nil, fmt.Errorf("decode dependencies for %s: %w", resource.ID, err)
	}
	if len(canonical.DependsOn) == 0 {
		return nil, nil
	}
	resources, err := w.store.ListResources(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.LogicalResourceID]ResourceRecord, len(resources))
	for _, candidate := range resources {
		byID[candidate.ID] = candidate
	}
	result := make([]application.ProviderDependency, 0, len(canonical.DependsOn))
	for _, value := range canonical.DependsOn {
		dependencyID := domain.LogicalResourceID(value)
		dependency, exists := byID[dependencyID]
		if !exists {
			return nil, fmt.Errorf("persisted dependency %s for %s is unavailable", dependencyID, resource.ID)
		}
		claims, err := w.resourceClaims(ctx, instanceID, dependency)
		if err != nil {
			return nil, err
		}
		result = append(result, application.ProviderDependency{
			ResourceID: string(dependencyID), Plan: append([]byte(nil), dependency.ProviderPlan...),
			PlanDigest: rawDigest(dependency.ProviderPlan).Hex(), State: dependency.ProviderState,
			Observation: append([]byte(nil), dependency.ObservedStateJSON...), Allocations: claims,
		})
	}
	return result, nil
}

func (w *Worker) transitionClaims(ctx context.Context, fence Fence, resource ResourceRecord, protocolClaims []application.AllocationKey, action string, result *application.ProviderResult) error {
	if len(protocolClaims) == 0 {
		return nil
	}
	claims := make([]application.AllocationKey, 0, len(protocolClaims))
	for _, claim := range protocolClaims {
		claims = append(claims, application.AllocationKey{TargetID: "", Namespace: claim.Namespace, Value: claim.Value})
	}
	stored, err := w.store.ListClaims(ctx, resource.InstanceID)
	if err != nil {
		return err
	}
	byIdentity := make(map[string]application.AllocationKey, len(stored))
	for _, claim := range stored {
		if claim.Binding.ResourceID == resource.ID && claim.State != application.AllocationReleased {
			byIdentity[claim.Key.Namespace+"\x00"+claim.Key.Value] = claim.Key
		}
	}
	for index := range claims {
		key, exists := byIdentity[claims[index].Namespace+"\x00"+claims[index].Value]
		if !exists {
			return fmt.Errorf("provider claim for %s is not persisted", resource.ID)
		}
		claims[index] = key
	}
	mutation := application.MutateAllocationClaimsRequest{
		Binding: application.AllocationBinding{InstanceID: resource.InstanceID, ResourceID: resource.ID, PlanDigest: rawDigest(resource.ProviderPlan)},
		Claims:  claims, FencingToken: fence.Token,
	}
	if result.Error == nil {
		if action == "apply" {
			return nil
		}
		return w.store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{MutateAllocationClaimsRequest: mutation, ProviderAbsenceConfirmed: true})
	}
	if action == "apply" && result.CompensationComplete {
		return w.store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{MutateAllocationClaimsRequest: mutation, ProviderAbsenceConfirmed: true})
	}
	if result.CompensationAttempted {
		return w.store.OrphanClaims(ctx, mutation)
	}
	return w.store.RetainClaims(ctx, mutation)
}

func (w *Worker) retainResourceClaims(ctx context.Context, fence Fence, resource ResourceRecord) error {
	claims, err := w.store.ListClaims(ctx, resource.InstanceID)
	if err != nil {
		return err
	}
	keys := make([]application.AllocationKey, 0)
	for _, claim := range claims {
		if claim.Binding.ResourceID == resource.ID && claim.State != application.AllocationReleased {
			keys = append(keys, claim.Key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return w.store.RetainClaims(ctx, application.MutateAllocationClaimsRequest{
		Binding: application.AllocationBinding{InstanceID: resource.InstanceID, ResourceID: resource.ID, PlanDigest: rawDigest(resource.ProviderPlan)},
		Claims:  keys, FencingToken: fence.Token,
	})
}

func (w *Worker) releaseResourceClaims(ctx context.Context, fence Fence, resource ResourceRecord) error {
	claims, err := w.store.ListClaims(ctx, resource.InstanceID)
	if err != nil {
		return err
	}
	keys := make([]application.AllocationKey, 0)
	for _, claim := range claims {
		if claim.Binding.ResourceID == resource.ID && claim.State != application.AllocationReleased {
			keys = append(keys, claim.Key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return w.store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{
		MutateAllocationClaimsRequest: application.MutateAllocationClaimsRequest{
			Binding: application.AllocationBinding{InstanceID: resource.InstanceID, ResourceID: resource.ID, PlanDigest: rawDigest(resource.ProviderPlan)},
			Claims:  keys, FencingToken: fence.Token,
		},
		ProviderAbsenceConfirmed: true,
	})
}

func (w *Worker) saveUnknown(ctx context.Context, fence Fence, invocationID domain.InvocationID, resource ResourceRecord, action string, cause error) error {
	phase := domain.ResourceUnknown
	if action != "apply" {
		phase = domain.ResourceCleanupFailed
	}
	if err := w.store.SaveResourceResult(ctx, ResourceResultUpdate{
		Fence: fence, InvocationID: invocationID, ResourceID: resource.ID, InvocationState: "unknown", Phase: phase,
		ProviderState: resource.ProviderState, ObservedStateJSON: resource.ObservedStateJSON,
		ErrorCode: "provider_outcome_unknown", ErrorMessage: "Provider response was interrupted; the operation will reconcile",
	}); err != nil {
		return err
	}
	if err := w.retainResourceClaims(ctx, fence, resource); err != nil {
		return err
	}
	if err := w.store.RetainInstanceQuota(ctx, fence); err != nil {
		return err
	}
	return fmt.Errorf("%w for %s: %v", errProviderOutcomeUnknown, resource.ID, cause)
}

func (w *Worker) compensateAndFinalize(ctx context.Context, fence Fence, actorID domain.UserID, instance InstanceRecord, failedResource domain.LogicalResourceID, failedResult *application.ProviderResult, cause error) error {
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	leftovers := make([]string, 0)
	if failedResource != "" && resultHasOwnedLeftovers(failedResult) {
		leftovers = append(leftovers, string(failedResource))
	}
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if resource.Phase == domain.ResourceDestroyed {
			if err := w.releaseResourceClaims(ctx, fence, resource); err != nil {
				return err
			}
			continue
		}
		if !providerManagedKind(resource.Kind) {
			continue
		}
		if len(resource.ProviderState) == 0 {
			if resource.ID == failedResource && resultHasOwnedLeftovers(failedResult) {
				continue
			}
			if resource.Phase == domain.ResourceUnknown || resource.Phase == domain.ResourceCleanupFailed {
				leftovers = append(leftovers, string(resource.ID))
				continue
			}
			// No invocation reached the provider for this planned resource, so
			// positive absence is established and its create-time claims are safe
			// to return to the allocation pool.
			if err := w.releaseResourceClaims(ctx, fence, resource); err != nil {
				return err
			}
			continue
		}
		if resource.Phase == domain.ResourceCleanupFailed {
			leftovers = append(leftovers, string(resource.ID))
			continue
		}
		if resource.Phase != domain.ResourceReady {
			continue
		}
		result, invokeErr := w.invoke(ctx, fence, actorID, instance, resource, "compensate")
		if invokeErr != nil {
			if errors.Is(invokeErr, errProviderOutcomeUnknown) {
				return invokeErr
			}
			leftovers = append(leftovers, string(resource.ID))
			continue
		}
		if result.Error != nil {
			leftovers = append(leftovers, string(resource.ID))
		}
	}
	state := domain.OperationFailed
	phase := domain.InstanceFailed
	errorCode := "apply_failed"
	message := "Apply failed and created resources were compensated"
	if len(leftovers) > 0 {
		state = domain.OperationPartial
		phase = domain.InstanceDegraded
		errorCode = "partial_cleanup"
		message = "Apply failed with owned leftovers: " + strings.Join(leftovers, ", ")
	}
	if failedResource != "" {
		message += " (failed at " + string(failedResource) + ")"
	}
	return w.finalize(ctx, fence, actorID, OperationApply, state, phase, message, errorCode, cause.Error(), nil)
}

func resultHasOwnedLeftovers(result *application.ProviderResult) bool {
	if result == nil || result.Error == nil || result.CompensationComplete {
		return false
	}
	// Without an explicit complete-compensation assertion, absence is not
	// proven. This deliberately matches transitionClaims, which retains or
	// orphans allocation claims for the same provider result.
	return true
}

func (w *Worker) finalize(ctx context.Context, fence Fence, actorID domain.UserID, kind OperationKind, state domain.OperationState, phase domain.InstancePhase, message, errorCode, errorMessage string, outputs []byte) error {
	auditID, err := w.ids.New("audit")
	if err != nil {
		return err
	}
	eventID, err := w.ids.New("event")
	if err != nil {
		return err
	}
	return w.store.Finalize(ctx, FinalizeRequest{
		Fence: fence, ActorID: actorID, OperationKind: kind, State: state, InstancePhase: phase, Message: message,
		ErrorCode: errorCode, ErrorMessage: errorMessage, OutputsJSON: outputs, AuditID: auditID, EventID: eventID,
	})
}

func projectCoreResource(resource ResourceRecord, resources []ResourceRecord) ([]byte, error) {
	var canonical struct {
		Kind string `json:"kind"`
		Spec struct {
			TargetRef string   `json:"targetRef"`
			Methods   []string `json:"methods"`
			ValueRef  string   `json:"valueRef"`
			Type      string   `json:"type"`
			Sensitive bool     `json:"sensitive"`
		} `json:"spec"`
	}
	if err := decodeJSON(resource.CanonicalJSON, &canonical); err != nil {
		return nil, err
	}
	switch resource.Kind {
	case domain.ResourceAccessEndpoint:
		return json.Marshal(map[string]any{"available": true, "methods": canonical.Spec.Methods, "targetRef": canonical.Spec.TargetRef})
	case domain.ResourceOutput:
		value, err := resolveOutput(canonical.Spec.ValueRef, resources)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"type": canonical.Spec.Type, "sensitive": canonical.Spec.Sensitive, "value": value})
	default:
		return nil, fmt.Errorf("resource %s is not core-projected", resource.ID)
	}
}

func resolveOutput(expression string, resources []ResourceRecord) (string, error) {
	parts := strings.Split(expression, ".")
	if len(parts) != 6 || parts[0] != "resources" || parts[1] != "containers" || parts[3] != "publishedPorts" || parts[5] != "url" {
		return "", fmt.Errorf("local slice cannot project output expression %q", expression)
	}
	targetID := domain.LogicalResourceID("container." + parts[2])
	portName := parts[4]
	for _, resource := range resources {
		if resource.ID != targetID {
			continue
		}
		var canonical struct {
			Spec struct {
				PublishedPorts []struct {
					Name     string `json:"name"`
					Port     uint16 `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"publishedPorts"`
			} `json:"spec"`
		}
		if err := decodeJSON(resource.CanonicalJSON, &canonical); err != nil {
			return "", err
		}
		var observed struct {
			Ports map[string]uint16 `json:"ports"`
		}
		if err := decodeJSON(resource.ObservedStateJSON, &observed); err != nil {
			return "", err
		}
		for _, port := range canonical.Spec.PublishedPorts {
			if port.Name != portName {
				continue
			}
			hostPort := observed.Ports[fmt.Sprintf("%d/%s", port.Port, port.Protocol)]
			if hostPort == 0 {
				return "", fmt.Errorf("provider did not observe host port %s", portName)
			}
			return fmt.Sprintf("http://127.0.0.1:%d/", hostPort), nil
		}
	}
	return "", fmt.Errorf("output target or published port is unavailable")
}

func collectOutputs(resources []ResourceRecord) ([]byte, error) {
	outputs := make(map[string]any)
	for _, resource := range resources {
		if resource.Kind != domain.ResourceOutput {
			continue
		}
		var observed struct {
			Value any `json:"value"`
		}
		if err := decodeJSON(resource.ObservedStateJSON, &observed); err != nil {
			return nil, err
		}
		outputs[strings.TrimPrefix(string(resource.ID), "output.")] = observed.Value
	}
	return json.Marshal(outputs)
}

func strictDecode(input []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func decodeJSON(input []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}
