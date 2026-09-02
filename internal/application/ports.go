package application

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type UnitOfWork interface {
	Do(context.Context, func(context.Context, Repositories) error) error
}

type Repositories interface {
	Identity() IdentityRepository
	Tenancy() TenancyRepository
	Providers() ProviderRepository
	Templates() TemplateRepository
	Instances() InstanceRepository
	Operations() OperationRepository
	Governance() GovernanceRepository
	Events() EventRepository
}

type IdentityRepository interface {
	SubjectExists(context.Context, domain.UserID) (bool, error)
}

type TenancyRepository interface {
	ProjectExists(context.Context, domain.ProjectID) (bool, error)
}

type ProviderRepository interface {
	GetTarget(context.Context, domain.ProviderTargetID) (domain.ProviderTarget, error)
	SaveTarget(context.Context, domain.ProviderTarget, domain.ResourceVersion) error
}

type TemplateRepository interface {
	GetRevision(context.Context, domain.RevisionID) (domain.TemplateRevision, error)
}

type InstanceRepository interface {
	Get(context.Context, domain.InstanceID) (domain.LabInstance, error)
	Save(context.Context, domain.LabInstance, domain.ResourceVersion) error
	ListResources(context.Context, domain.InstanceID) ([]domain.LogicalResourceRecord, error)
	SaveResource(context.Context, domain.LogicalResourceRecord, domain.ResourceVersion) error
}

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

type GovernanceRepository interface {
	Reserve(context.Context, QuotaReservation) error
	CommitReservation(context.Context, string, domain.FencingToken) error
	ReleaseReservation(context.Context, string, domain.FencingToken) error
}

type AuditIntent struct {
	ActorID       domain.UserID
	Action        string
	AggregateID   string
	CorrelationID string
}

type OutboxMessage struct {
	ID          string
	Topic       string
	AggregateID string
	Payload     []byte
}

type EventRepository interface {
	Append(context.Context, domain.DomainEvent, AuditIntent, []OutboxMessage) error
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

type TargetInvocation struct {
	InvocationID domain.InvocationID
	TargetID     domain.ProviderTargetID
	Deadline     time.Time
}

type ValidateInvocation struct {
	TargetInvocation
	Lab domain.CanonicalLab
}

type ProviderValidation struct {
	Accepted bool
	Warnings []domain.ReportIssue
}

type PlanInvocation struct {
	TargetInvocation
	InstanceID domain.InstanceID
	Resource   domain.LogicalResource
}

type ProviderPlan struct {
	ResourceID domain.LogicalResourceID
	Summary    string
	Warnings   []domain.ReportIssue
}

type MutationKind string

const (
	MutationApply    MutationKind = "apply"
	MutationDestroy  MutationKind = "destroy"
	MutationStart    MutationKind = "start"
	MutationStop     MutationKind = "stop"
	MutationPause    MutationKind = "pause"
	MutationResume   MutationKind = "resume"
	MutationSnapshot MutationKind = "snapshot"
	MutationRestore  MutationKind = "restore"
)

type MutationInvocation struct {
	TargetInvocation
	Kind          MutationKind
	InstanceID    domain.InstanceID
	Resource      domain.LogicalResource
	ProviderState *domain.ProviderStateEnvelope
	Generation    domain.Generation
}

type InvocationProgress struct {
	InvocationID domain.InvocationID
	Sequence     uint64
	Message      string
	Done         bool
}

type ProgressStream interface {
	Receive() (InvocationProgress, error)
	Close() error
}

type InvocationQuery struct {
	TargetInvocation
	QueriedInvocationID domain.InvocationID
}

type InvocationObservation struct {
	Known         bool
	Complete      bool
	Resource      domain.ResourceObservation
	ProviderState *domain.ProviderStateEnvelope
}

type InvocationCancel struct {
	TargetInvocation
	CanceledInvocationID domain.InvocationID
}

type CancelOutcome string

const (
	CancelRequested CancelOutcome = "requested"
	CancelTooLate   CancelOutcome = "too_late"
	CancelUnknown   CancelOutcome = "unknown"
)

type PluginInvoker interface {
	DiscoverTarget(context.Context, TargetInvocation) (domain.CapabilitySnapshot, error)
	ValidateLab(context.Context, ValidateInvocation) (ProviderValidation, error)
	PlanResource(context.Context, PlanInvocation) (ProviderPlan, error)
	Invoke(context.Context, MutationInvocation) (ProgressStream, error)
	GetInvocation(context.Context, InvocationQuery) (InvocationObservation, error)
	CancelInvocation(context.Context, InvocationCancel) (CancelOutcome, error)
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
