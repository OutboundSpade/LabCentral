package controlplane

import (
	"context"
	"errors"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var (
	ErrNoWork           = errors.New("no claimable local operation")
	ErrNotFound         = errors.New("local control-plane record not found")
	ErrAlreadyExists    = errors.New("control-plane record already exists")
	ErrVersionConflict  = errors.New("control-plane resource version conflict")
	ErrAuthorityChanged = errors.New("control-plane authority changed before mutation")
)

type OperationKind string

const (
	OperationApply   OperationKind = "apply"
	OperationDestroy OperationKind = "destroy"
)

type InstanceRecord struct {
	ID                           domain.InstanceID
	RevisionID                   domain.RevisionID
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
	ExpiresAt                    time.Time
	MaximumExpiresAt             time.Time
	ExpirationWarningAt          time.Time
	ExpirationWarningEmittedAt   time.Time
	ExpirationGraceSeconds       uint64
	LifetimeRenewalAllowed       bool
	LifetimeRenewalWindowSeconds uint64
	LifetimePolicyDigest         domain.SHA256Digest
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type LabTemplateRecord struct {
	ID              domain.TemplateID
	ProjectID       domain.ProjectID
	Name            string
	Description     string
	ResourceVersion domain.ResourceVersion
	CreatedBy       domain.UserID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type GovernanceRecordIDs struct {
	AuditID string
	EventID string
}

type CreateLabTemplateRequest struct {
	GovernanceRecordIDs
	ID             domain.TemplateID
	ProjectID      domain.ProjectID
	Name           string
	Description    string
	CreatedBy      domain.UserID
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
}

type UpdateLabTemplateRequest struct {
	GovernanceRecordIDs
	ID                      domain.TemplateID
	UpdatedBy               domain.UserID
	Name                    string
	Description             string
	ExpectedResourceVersion domain.ResourceVersion
	IdempotencyKey          string
	RequestDigest           domain.SHA256Digest
}

type TemplateRevisionRecord struct {
	ID               domain.RevisionID
	TemplateID       domain.TemplateID
	RevisionNumber   uint64
	BundleArtifact   domain.ArtifactRef
	BundleDigest     domain.SHA256Digest
	BundleSizeBytes  uint64
	AuthorAPIVersion string
	RendererVersion  string
	CanonicalVersion string
	CanonicalDigest  domain.SHA256Digest
	PublishedBy      domain.UserID
	CreatedAt        time.Time
}

const MaximumTemplatePageSize uint32 = 200

type LabTemplateCursor struct {
	CreatedAt time.Time
	ID        domain.TemplateID
}

type LabTemplateListRequest struct {
	ProjectID domain.ProjectID
	After     *LabTemplateCursor
	Limit     uint32
}

type LabTemplatePage struct {
	Records    []LabTemplateRecord
	NextCursor *LabTemplateCursor
}

type TemplateRevisionCursor struct {
	RevisionNumber uint64
	ID             domain.RevisionID
}

type TemplateRevisionListRequest struct {
	TemplateID domain.TemplateID
	After      *TemplateRevisionCursor
	Limit      uint32
}

type TemplateRevisionPage struct {
	Records    []TemplateRevisionRecord
	NextCursor *TemplateRevisionCursor
}

type PublishTemplateRevisionRequest struct {
	GovernanceRecordIDs
	ID               domain.RevisionID
	TemplateID       domain.TemplateID
	BundleArtifact   domain.ArtifactRef
	BundleDigest     domain.SHA256Digest
	BundleSizeBytes  uint64
	AuthorAPIVersion string
	RendererVersion  string
	CanonicalVersion string
	CanonicalDigest  domain.SHA256Digest
	PublishedBy      domain.UserID
	IdempotencyKey   string
	RequestDigest    domain.SHA256Digest
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
	RevisionID           domain.RevisionID
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
	Quota                application.InstanceQuotaRequest
}

const MaximumInstanceBatchSize = 100

type InstanceOwner struct {
	Kind application.OwnerKind
	ID   string
}

type InstanceBatchItem struct {
	Ordinal     uint32
	OwnerKind   application.OwnerKind
	OwnerID     string
	InstanceID  domain.InstanceID
	OperationID domain.OperationID
}

type InstanceBatchRecord struct {
	ID               domain.InstanceBatchID
	RevisionID       domain.RevisionID
	ProjectID        domain.ProjectID
	CreatedBy        domain.UserID
	ProviderTargetID domain.ProviderTargetID
	Items            []InstanceBatchItem
	CreatedAt        time.Time
	Replay           bool
}

type CreateInstanceBatchRequest struct {
	GovernanceRecordIDs
	ID             domain.InstanceBatchID
	RevisionID     domain.RevisionID
	ProjectID      domain.ProjectID
	CreatedBy      domain.UserID
	TargetID       domain.ProviderTargetID
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	Instances      []CreateInstanceRequest
}

type QueueDestroyRequest struct {
	InstanceID      domain.InstanceID
	OperationID     domain.OperationID
	SubjectID       domain.UserID
	IdempotencyKey  string
	RequestDigest   domain.SHA256Digest
	Payload         []byte
	AuditID         string
	EventID         string
	ExpirationDueAt time.Time
}

type PutQuotaLimitRequest struct {
	GovernanceRecordIDs
	Scope                   application.QuotaScope
	Dimension               application.QuotaDimension
	Limit                   uint64
	ExpectedResourceVersion domain.ResourceVersion
	UpdatedBy               domain.UserID
	IdempotencyKey          string
	RequestDigest           domain.SHA256Digest
}

type PutLifetimePolicyRequest struct {
	GovernanceRecordIDs
	Policy                  application.LifetimePolicyRecord
	ExpectedResourceVersion domain.ResourceVersion
	UpdatedBy               domain.UserID
	IdempotencyKey          string
	RequestDigest           domain.SHA256Digest
}

type RenewInstanceLifetimeRequest struct {
	GovernanceRecordIDs
	InstanceID              domain.InstanceID
	TTLSeconds              uint64
	ExpectedResourceVersion domain.ResourceVersion
	ActorID                 domain.UserID
	IdempotencyKey          string
	RequestDigest           domain.SHA256Digest
}

type ExpirationCandidate struct {
	InstanceID      domain.InstanceID
	ExpiresAt       time.Time
	WarningAt       time.Time
	GraceSeconds    uint64
	ResourceVersion domain.ResourceVersion
}

type MarkExpirationWarningRequest struct {
	GovernanceRecordIDs
	Candidate ExpirationCandidate
}

type EnqueueResult struct {
	InstanceID  domain.InstanceID
	OperationID domain.OperationID
	Replay      bool
}

type WorkClaim struct {
	Operation  domain.Operation
	LeaseOwner string
	Action     application.ClaimAction
	Recovery   *InvocationRecord
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
	application.AuditEventStore
	CreateLabTemplate(context.Context, CreateLabTemplateRequest) (LabTemplateRecord, error)
	UpdateLabTemplate(context.Context, UpdateLabTemplateRequest) (LabTemplateRecord, error)
	GetLabTemplate(context.Context, domain.TemplateID) (LabTemplateRecord, error)
	ListLabTemplates(context.Context, LabTemplateListRequest) (LabTemplatePage, error)
	PublishTemplateRevision(context.Context, PublishTemplateRevisionRequest) (TemplateRevisionRecord, error)
	GetTemplateRevision(context.Context, domain.RevisionID) (TemplateRevisionRecord, error)
	ListTemplateRevisions(context.Context, TemplateRevisionListRequest) (TemplateRevisionPage, error)
	PutQuotaLimit(context.Context, PutQuotaLimitRequest) (application.QuotaLimitRecord, error)
	GetQuotaLimit(context.Context, application.QuotaScope, application.QuotaDimension) (application.QuotaLimitRecord, error)
	ListQuotaUsage(context.Context, application.QuotaScope) ([]application.QuotaUsageRecord, error)
	PutLifetimePolicy(context.Context, PutLifetimePolicyRequest) (application.LifetimePolicyRecord, error)
	GetLifetimePolicy(context.Context, application.QuotaScope) (application.LifetimePolicyRecord, error)
	RenewInstanceLifetime(context.Context, RenewInstanceLifetimeRequest) (InstanceRecord, error)
	ListExpirationWarnings(context.Context, uint32) ([]ExpirationCandidate, error)
	MarkExpirationWarning(context.Context, MarkExpirationWarningRequest) (bool, error)
	ListExpiredInstances(context.Context, uint32) ([]ExpirationCandidate, error)
	ExpireStaleQuotaReservations(context.Context, uint32) (uint32, error)
	RetainInstanceQuota(context.Context, Fence) error
	ApplyExecutionAllowed(context.Context, Fence) (bool, error)
	CreateInstance(context.Context, CreateInstanceRequest) (EnqueueResult, error)
	CreateInstanceBatch(context.Context, CreateInstanceBatchRequest) (InstanceBatchRecord, error)
	GetInstanceBatch(context.Context, domain.InstanceBatchID) (InstanceBatchRecord, error)
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
