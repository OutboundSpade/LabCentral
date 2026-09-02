package domain

type InstancePhase string

const (
	InstancePending    InstancePhase = "pending"
	InstanceValidating InstancePhase = "validating"
	InstancePlanned    InstancePhase = "planned"
	InstanceApplying   InstancePhase = "applying"
	InstanceReady      InstancePhase = "ready"
	InstanceStopped    InstancePhase = "stopped"
	InstancePaused     InstancePhase = "paused"
	InstanceDegraded   InstancePhase = "degraded"
	InstanceFailed     InstancePhase = "failed"
	InstanceDestroying InstancePhase = "destroying"
	InstanceDestroyed  InstancePhase = "destroyed"
)

type ResourcePhase string

const (
	ResourcePending       ResourcePhase = "pending"
	ResourceApplying      ResourcePhase = "applying"
	ResourceReady         ResourcePhase = "ready"
	ResourceFailed        ResourcePhase = "failed"
	ResourceUnknown       ResourcePhase = "unknown"
	ResourceStopped       ResourcePhase = "stopped"
	ResourcePaused        ResourcePhase = "paused"
	ResourceDestroying    ResourcePhase = "destroying"
	ResourceDestroyed     ResourcePhase = "destroyed"
	ResourceCleanupFailed ResourcePhase = "cleanup_failed"
)

type OperationState string

const (
	OperationQueued          OperationState = "queued"
	OperationRunning         OperationState = "running"
	OperationCancelRequested OperationState = "cancel_requested"
	OperationCompensating    OperationState = "compensating"
	OperationSucceeded       OperationState = "succeeded"
	OperationFailed          OperationState = "failed"
	OperationCanceled        OperationState = "canceled"
	OperationTimedOut        OperationState = "timed_out"
	OperationPartial         OperationState = "partial"
)

var instanceTransitions = map[InstancePhase]map[InstancePhase]struct{}{
	InstancePending:    setOf(InstanceValidating),
	InstanceValidating: setOf(InstancePlanned),
	InstancePlanned:    setOf(InstanceApplying),
	InstanceApplying:   setOf(InstanceReady, InstanceDegraded, InstanceFailed),
	InstanceReady:      setOf(InstanceStopped, InstancePaused, InstanceDegraded, InstanceDestroying),
	InstanceStopped:    setOf(InstanceApplying, InstanceDestroying),
	InstancePaused:     setOf(InstanceApplying, InstanceDestroying),
	InstanceDegraded:   setOf(InstanceApplying, InstanceDestroying),
	InstanceFailed:     setOf(InstanceDestroying),
	InstanceDestroying: setOf(InstanceDestroyed, InstanceDegraded),
}

var resourceTransitions = map[ResourcePhase]map[ResourcePhase]struct{}{
	ResourcePending:       setOf(ResourceApplying, ResourceDestroying),
	ResourceApplying:      setOf(ResourceReady, ResourceFailed, ResourceUnknown, ResourceDestroying),
	ResourceReady:         setOf(ResourceStopped, ResourcePaused, ResourceApplying, ResourceDestroying),
	ResourceFailed:        setOf(ResourceApplying, ResourceDestroying),
	ResourceUnknown:       setOf(ResourceApplying, ResourceDestroying),
	ResourceStopped:       setOf(ResourceApplying, ResourceDestroying),
	ResourcePaused:        setOf(ResourceApplying, ResourceDestroying),
	ResourceDestroying:    setOf(ResourceDestroyed, ResourceCleanupFailed),
	ResourceCleanupFailed: setOf(ResourceDestroying),
}

var operationTransitions = map[OperationState]map[OperationState]struct{}{
	OperationQueued:          setOf(OperationRunning, OperationCancelRequested, OperationCanceled),
	OperationRunning:         setOf(OperationCancelRequested, OperationCompensating, OperationSucceeded, OperationFailed, OperationCanceled, OperationTimedOut, OperationPartial),
	OperationCancelRequested: setOf(OperationRunning, OperationCompensating, OperationCanceled, OperationFailed, OperationTimedOut, OperationPartial),
	OperationCompensating:    setOf(OperationFailed, OperationCanceled, OperationPartial),
}

func setOf[T comparable](values ...T) map[T]struct{} {
	result := make(map[T]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func ValidateInstanceTransition(from, to InstancePhase) error {
	if _, ok := instanceTransitions[from][to]; !ok {
		return invalidTransition("instance", string(from), string(to))
	}
	return nil
}

func ValidateResourceTransition(from, to ResourcePhase) error {
	if _, ok := resourceTransitions[from][to]; !ok {
		return invalidTransition("resource", string(from), string(to))
	}
	return nil
}

func ValidateOperationTransition(from, to OperationState) error {
	if _, ok := operationTransitions[from][to]; !ok {
		return invalidTransition("operation", string(from), string(to))
	}
	return nil
}

func (s OperationState) Terminal() bool {
	switch s {
	case OperationSucceeded, OperationFailed, OperationCanceled, OperationTimedOut, OperationPartial:
		return true
	default:
		return false
	}
}
