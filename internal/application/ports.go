package application

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type OperationClaim struct {
	Operation domain.Operation
	Token     domain.FencingToken
	Action    ClaimAction
}

type ClaimAction string

const (
	ClaimDispatch      ClaimAction = "dispatch"
	ClaimGetInvocation ClaimAction = "get_invocation"
)

type OperationCheckpoint struct {
	OperationID domain.OperationID
	LeaseOwner  string
	Token       domain.FencingToken
	Generation  domain.Generation
	State       domain.OperationState
	Attempt     uint32
	Payload     []byte
}

type OperationRepository interface {
	Get(context.Context, domain.OperationID) (domain.Operation, error)
	Enqueue(context.Context, domain.Operation, string) error
	Claim(context.Context, string, time.Duration) (OperationClaim, error)
	RenewLease(context.Context, domain.OperationID, string, domain.FencingToken, time.Duration) error
	Checkpoint(context.Context, OperationCheckpoint) error
}

type QuotaReservation struct {
	ID          string
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	ExpiresAt   time.Time
}

type AllocationClaimState string

const (
	AllocationHeld      AllocationClaimState = "held"
	AllocationCommitted AllocationClaimState = "committed"
	AllocationOrphaned  AllocationClaimState = "orphaned"
	AllocationReleased  AllocationClaimState = "released"
)

type AllocationKey struct {
	TargetID  domain.ProviderTargetID
	Namespace string
	Value     string
}

type AllocationBinding struct {
	InstanceID domain.InstanceID
	ResourceID domain.LogicalResourceID
	PlanDigest domain.SHA256Digest
}

type AllocationClaim struct {
	Key          AllocationKey
	Binding      AllocationBinding
	State        AllocationClaimState
	FencingToken domain.FencingToken
}

type ReserveAllocationClaimsRequest struct {
	Binding AllocationBinding
	Claims  []AllocationKey
}

type MutateAllocationClaimsRequest struct {
	Binding      AllocationBinding
	Claims       []AllocationKey
	FencingToken domain.FencingToken
}

type ReleaseAllocationClaimsRequest struct {
	MutateAllocationClaimsRequest
	ProviderAbsenceConfirmed bool
}

type AllocationConflictError struct {
	Conflicts []AllocationKey
}

func (e *AllocationConflictError) Error() string {
	return "one or more target allocation claims conflict"
}

type AllocationRepository interface {
	ReserveClaims(context.Context, ReserveAllocationClaimsRequest) error
	CommitClaims(context.Context, MutateAllocationClaimsRequest) error
	RetainClaims(context.Context, MutateAllocationClaimsRequest) error
	OrphanClaims(context.Context, MutateAllocationClaimsRequest) error
	ReleaseClaims(context.Context, ReleaseAllocationClaimsRequest) error
	ListClaims(context.Context, domain.InstanceID) ([]AllocationClaim, error)
}

type VerifiedManifest struct {
	Digest    domain.SHA256Digest
	FileCount uint32
	ByteCount uint64
}

type MeasuredArtifact struct {
	Size   uint64
	Digest domain.SHA256Digest
}

type AuthorizedArtifactRef struct {
	Reference domain.ArtifactRef
	SubjectID domain.UserID
}

type ArtifactStore interface {
	Stage(context.Context, VerifiedManifest) (domain.UploadID, error)
	Write(context.Context, domain.UploadID, io.Reader) (MeasuredArtifact, error)
	Commit(context.Context, domain.UploadID, VerifiedManifest) (domain.ArtifactRef, error)
	Open(context.Context, AuthorizedArtifactRef) (io.ReadCloser, error)
}

type AuthorizedSecretRef struct {
	Reference domain.SecretRef
	SubjectID domain.UserID
}

type InvocationScope struct {
	InvocationID domain.InvocationID
	InstanceID   domain.InstanceID
	ResourceID   domain.LogicalResourceID
	ExpiresAt    time.Time
}

type EphemeralSecret struct {
	value []byte
}

func NewEphemeralSecret(value []byte) *EphemeralSecret {
	return &EphemeralSecret{value: append([]byte(nil), value...)}
}

func (s *EphemeralSecret) Bytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.value...)
}

func (s *EphemeralSecret) Destroy() {
	if s == nil {
		return
	}
	for index := range s.value {
		s.value[index] = 0
	}
	s.value = nil
}

func (*EphemeralSecret) String() string   { return "[REDACTED]" }
func (*EphemeralSecret) GoString() string { return "[REDACTED]" }
func (*EphemeralSecret) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED]")
}

type SecretResolver interface {
	Resolve(context.Context, AuthorizedSecretRef, InvocationScope) (*EphemeralSecret, error)
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	New() string
}
