package controlplane

import (
	"context"
	"errors"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var (
	ErrNoWork   = errors.New("no claimable local operation")
	ErrNotFound = errors.New("local control-plane record not found")
)

type OperationKind string

const (
	OperationApply   OperationKind = "apply"
	OperationDestroy OperationKind = "destroy"
)

type InstanceRecord struct {
	ID                           domain.InstanceID
	ProjectID                    domain.ProjectID
	OwnerKind                    application.OwnerKind
	OwnerID                      string
	CreatedBy                    domain.UserID
	ProviderTargetID             domain.ProviderTargetID
	ProviderOverrideJSON         []byte
	CanonicalDigest              domain.SHA256Digest
	CanonicalLabJSON             []byte
	SourceYAML                   []byte
	Phase                        domain.InstancePhase
	DesiredLifecycle             domain.DesiredLifecycle
	Generation                   domain.Generation
	ResourceVersion              domain.ResourceVersion
	IsolationWarningAcknowledged bool
	OutputsJSON                  []byte
	ErrorCode                    string
	ErrorMessage                 string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type TargetSnapshot struct {
	ID                domain.ProviderTargetID
	ProviderKind      string
	ConfigFingerprint string
	ProtocolMajor     uint32
	ProtocolMinor     uint32
	Features          []string
	DiscoveryJSON     []byte
	DiscoveredAt      time.Time
	ValidUntil        time.Time
}

type ProviderTargetStatus struct {
	ID                domain.ProviderTargetID
	ProviderKind      string
	ConfigFingerprint string
	ProtocolMajor     uint32
	ProtocolMinor     uint32
	Features          []string
	Status            string
	DiscoveryJSON     []byte
	DiscoveredAt      time.Time
	ValidUntil        time.Time
}

type ResourceRecord struct {
	InstanceID        domain.InstanceID
	ID                domain.LogicalResourceID
	Kind              domain.ResourceKind
	Ordinal           int
	CanonicalJSON     []byte
	ProviderPlan      []byte
	Phase             domain.ResourcePhase
	OwnershipToken    string
	ProviderState     []byte
	ObservedStateJSON []byte
	ErrorCode         string
	ErrorMessage      string
	UpdatedAt         time.Time
}

type OperationRecord struct {
	ID         domain.OperationID
	InstanceID domain.InstanceID
	ActorID    domain.UserID
	Kind       OperationKind
	State      domain.OperationState
	Generation domain.Generation
	Attempt    uint32
	Message    string
	ErrorCode  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ResourceSeed struct {
	ID                 domain.LogicalResourceID
	Kind               domain.ResourceKind
	Ordinal            int
	CanonicalJSON      []byte
	ProviderPlan       []byte
	ProviderPlanDigest domain.SHA256Digest
	Allocations        []application.AllocationKey
	OwnershipToken     string
}

type CreateInstanceRequest struct {
	InstanceID           domain.InstanceID
	ProjectID            domain.ProjectID
	OwnerKind            application.OwnerKind
	OwnerID              string
	CreatedBy            domain.UserID
	ProviderTargetID     domain.ProviderTargetID
	ProviderOverrideJSON []byte
	OperationID          domain.OperationID
	CanonicalDigest      domain.SHA256Digest
	CanonicalJSON        []byte
	SourceYAML           []byte
	Resources            []ResourceSeed
	Acknowledged         bool
	SubjectID            domain.UserID
	IdempotencyKey       string
	RequestDigest        domain.SHA256Digest
	Payload              []byte
	AuditID              string
	EventID              string
}

type QueueDestroyRequest struct {
	InstanceID     domain.InstanceID
	OperationID    domain.OperationID
	SubjectID      domain.UserID
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	Payload        []byte
	AuditID        string
	EventID        string
}

type EnqueueResult struct {
	InstanceID  domain.InstanceID
	OperationID domain.OperationID
	Replay      bool
}

type WorkClaim struct {
	Operation  domain.Operation
	LeaseOwner string
	Payload    []byte
}

type Fence struct {
	OperationID domain.OperationID
	LeaseOwner  string
	Token       domain.FencingToken
	Generation  domain.Generation
	Attempt     uint32
}

type InvocationRecord struct {
	ID             domain.InvocationID
	OperationID    domain.OperationID
	ResourceID     domain.LogicalResourceID
	Action         string
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	State          string
	ProviderResult []byte
}

type PrepareInvocationRequest struct {
	Fence          Fence
	InvocationID   domain.InvocationID
	ResourceID     domain.LogicalResourceID
	Action         string
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
}

type ResourceResultUpdate struct {
	Fence             Fence
	InvocationID      domain.InvocationID
	ResourceID        domain.LogicalResourceID
	InvocationState   string
	Phase             domain.ResourcePhase
	ProviderState     []byte
	ObservedStateJSON []byte
	ErrorCode         string
	ErrorMessage      string
}

type FinalizeRequest struct {
	Fence         Fence
	ActorID       domain.UserID
	OperationKind OperationKind
	State         domain.OperationState
	InstancePhase domain.InstancePhase
	Message       string
	ErrorCode     string
	ErrorMessage  string
	OutputsJSON   []byte
	AuditID       string
	EventID       string
}

type Store interface {
	CreateInstance(context.Context, CreateInstanceRequest) (EnqueueResult, error)
	SaveTargetSnapshot(context.Context, TargetSnapshot) error
	GetTargetSnapshot(context.Context, domain.ProviderTargetID) (TargetSnapshot, error)
	QueueDestroy(context.Context, QueueDestroyRequest) (EnqueueResult, error)
	GetInstance(context.Context, domain.InstanceID) (InstanceRecord, error)
	ListInstances(context.Context) ([]InstanceRecord, error)
	ListInstancesByID(context.Context, []domain.InstanceID) ([]InstanceRecord, error)
	ListResources(context.Context, domain.InstanceID) ([]ResourceRecord, error)
	GetOperation(context.Context, domain.OperationID) (OperationRecord, error)
	Claim(context.Context, string, time.Duration) (WorkClaim, error)
	Renew(context.Context, Fence, time.Duration) error
	MarkInstancePhase(context.Context, Fence, domain.InstancePhase, string) error
	PrepareInvocation(context.Context, PrepareInvocationRequest) (InvocationRecord, error)
	SaveCoreResource(context.Context, Fence, domain.LogicalResourceID, domain.ResourcePhase, []byte) error
	SaveResourceResult(context.Context, ResourceResultUpdate) error
	Finalize(context.Context, FinalizeRequest) error
	RetainClaims(context.Context, application.MutateAllocationClaimsRequest) error
	OrphanClaims(context.Context, application.MutateAllocationClaimsRequest) error
	ReleaseClaims(context.Context, application.ReleaseAllocationClaimsRequest) error
	ListClaims(context.Context, domain.InstanceID) ([]application.AllocationClaim, error)
	Close() error
}
