package jobsdb

import (
	"errors"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type Dialect string

const (
	DialectSQLite     Dialect = "sqlite"
	DialectPostgreSQL Dialect = "postgresql"
)

var (
	ErrNoClaimableJob   = errors.New("no claimable job")
	ErrNoOutboxDelivery = errors.New("no claimable outbox delivery")
	ErrFencingConflict  = &domain.DomainError{
		Code:    "stale_job_fence",
		Class:   domain.ErrorStateVersionIncompatible,
		Message: "lease owner, fencing token, generation, or attempt no longer matches",
	}
	ErrVersionConflict = &domain.DomainError{
		Code:    "resource_version_conflict",
		Class:   domain.ErrorStateVersionIncompatible,
		Message: "aggregate resource version no longer matches",
	}
	ErrIdempotencyConflict = &domain.DomainError{
		Code:    "idempotency_conflict",
		Class:   domain.ErrorInvalidDeclaration,
		Message: "idempotency key is already bound to a different request digest",
	}
)

const CurrentPayloadVersion uint32 = 1

type AtomicMutation struct {
	AggregateID             string
	ExpectedResourceVersion domain.ResourceVersion
	NewPhase                string
	AuditID                 string
	ActorID                 domain.UserID
	Action                  string
	EventID                 string
	EventType               string
	EventPayload            []byte
	OutboxSink              string
}

type ClaimedJob struct {
	Operation  domain.Operation
	LeaseOwner string
	Action     application.ClaimAction
	Payload    []byte
}

type InvocationState string

const (
	InvocationPrepared   InvocationState = "prepared"
	InvocationDispatched InvocationState = "dispatched"
	InvocationUnknown    InvocationState = "unknown"
	InvocationSucceeded  InvocationState = "succeeded"
	InvocationFailed     InvocationState = "failed"
)

type InvocationRecord struct {
	ID             domain.InvocationID
	OperationID    domain.OperationID
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	State          InvocationState
}

type IdempotencyBinding struct {
	SubjectID     domain.UserID
	RouteAction   string
	Key           string
	RequestDigest domain.SHA256Digest
	ResultID      string
	ExpiresAt     time.Time
}

type IdempotencyResult struct {
	ResultID string
	Replay   bool
}

type QuotaOutcome string

const (
	QuotaOutcomeUnknown     QuotaOutcome = "unknown"
	QuotaOutcomeRealized    QuotaOutcome = "realized"
	QuotaOutcomeKnownAbsent QuotaOutcome = "known_absent"
)

type QuotaResolution struct {
	ReleaseAllowed         bool
	ReconciliationRequired bool
}

type DestroyResult struct {
	OperationState domain.OperationState
	InstancePhase  domain.InstancePhase
}

type OutboxClaim struct {
	EventID           string
	Sink              string
	AggregateID       string
	AggregateSequence domain.EventSequence
	Payload           []byte
	Attempts          uint32
	LeaseOwner        string
}

type faultPoint string

const (
	faultNone         faultPoint = ""
	faultBeforeCommit faultPoint = "before_commit"
	faultDuringCommit faultPoint = "during_commit"
)

var errInjectedTransactionFault = errors.New("injected transaction fault")
