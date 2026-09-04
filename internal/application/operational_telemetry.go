package application

import (
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// OperationalSnapshot is a bounded, aggregate-only view of durable runtime
// state. It intentionally contains no tenant, actor, target, or object
// identities so it can be exported safely as fixed-cardinality telemetry.
type OperationalSnapshot struct {
	Jobs       DurableJobSnapshot
	Outbox     OutboxSnapshot
	Targets    TargetCapabilitySnapshot
	Cleanup    CleanupSnapshot
	Operations []OperationStateCount
	Access     []AccessStateCount
	Resources  []ResourcePhaseCount
}

type DurableJobSnapshot struct {
	Queued                uint64
	Running               uint64
	RecoveryRequired      uint64
	OldestQueuedAge       time.Duration
	OldestExpiredLeaseAge time.Duration
}

type OutboxSnapshot struct {
	JobPending     uint64
	EventPending   uint64
	OldestJobAge   time.Duration
	OldestEventAge time.Duration
}

type TargetCapabilitySnapshot struct {
	Total     uint64
	Stale     uint64
	OldestAge time.Duration
}

type CleanupSnapshot struct {
	UnknownResources    uint64
	FailedResources     uint64
	OrphanedAllocations uint64
}

type OperationStateCount struct {
	State domain.OperationState
	Count uint64
}

type AccessStateCount struct {
	State domain.AccessSessionState
	Count uint64
}

type ResourcePhaseCount struct {
	Phase domain.ResourcePhase
	Count uint64
}
