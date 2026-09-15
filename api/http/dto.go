package httpapi

import (
	"encoding/json"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type versionedOutput[T any] struct {
	ETag string `header:"ETag"`
	Body T
}

type ProviderExample struct {
	Source           string          `json:"source"`
	ProviderOverride json.RawMessage `json:"providerOverride,omitempty"`
}

type ServerInfo struct {
	ServerVersion           string   `json:"serverVersion"`
	APIVersion              string   `json:"apiVersion"`
	APIBase                 string   `json:"apiBase"`
	AuthMode                string   `json:"authMode"`
	AuthMethods             []string `json:"authMethods"`
	EnabledProviderKinds    []string `json:"enabledProviderKinds"`
	DefaultProviderTargetID string   `json:"defaultProviderTargetId"`
	NoAuthWarning           string   `json:"noAuthWarning"`
	UIFeatures              []string `json:"uiFeatures"`
}

type Session struct {
	SubjectID   string   `json:"subjectId"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
}

type LabRequest struct {
	Inputs                         []InputValue    `json:"inputs,omitempty" maxItems:"256"`
	Source                         string          `json:"source,omitempty" maxLength:"8388608"`
	RevisionID                     string          `json:"revisionId,omitempty" maxLength:"128"`
	AcknowledgeDockerIsolationRisk bool            `json:"acknowledgeDockerIsolationRisk"`
	ProjectID                      string          `json:"projectId,omitempty" maxLength:"128"`
	OwnerKind                      string          `json:"ownerKind,omitempty" enum:"user,team,project"`
	OwnerID                        string          `json:"ownerId,omitempty" maxLength:"128"`
	TTLSeconds                     uint64          `json:"ttlSeconds,omitempty" maximum:"31622400"`
	Persistent                     bool            `json:"persistent,omitempty"`
	ProviderTargetID               string          `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage `json:"providerOverride,omitempty"`
}

type InstanceBatchOwnerRequest struct {
	OwnerKind string `json:"ownerKind" enum:"user,team"`
	OwnerID   string `json:"ownerId" minLength:"1" maxLength:"128"`
}

type InstanceBatchRequest struct {
	Inputs                         []InputValue                `json:"inputs,omitempty" maxItems:"256"`
	RevisionID                     string                      `json:"revisionId" minLength:"1" maxLength:"128"`
	ProjectID                      string                      `json:"projectId" minLength:"1" maxLength:"128"`
	Owners                         []InstanceBatchOwnerRequest `json:"owners" minItems:"1" maxItems:"100"`
	TTLSeconds                     uint64                      `json:"ttlSeconds,omitempty" maximum:"31622400"`
	Persistent                     bool                        `json:"persistent,omitempty"`
	AcknowledgeDockerIsolationRisk bool                        `json:"acknowledgeDockerIsolationRisk"`
	ProviderTargetID               string                      `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage             `json:"providerOverride,omitempty"`
}

type InstanceBatchItemDTO struct {
	Ordinal    uint32       `json:"ordinal"`
	OwnerKind  string       `json:"ownerKind"`
	OwnerID    string       `json:"ownerId"`
	InstanceID string       `json:"instanceId"`
	Operation  OperationDTO `json:"operation"`
}

type InstanceBatchDTO struct {
	ID               string                 `json:"id"`
	RevisionID       string                 `json:"revisionId"`
	ProjectID        string                 `json:"projectId"`
	CreatedBy        string                 `json:"createdBy"`
	ProviderTargetID string                 `json:"providerTargetId"`
	Items            []InstanceBatchItemDTO `json:"items"`
	CreatedAt        time.Time              `json:"createdAt"`
	Replay           bool                   `json:"replay,omitempty"`
}

type ProviderTargetDTO struct {
	ID                string           `json:"id"`
	ProviderKind      string           `json:"providerKind"`
	ConfigFingerprint string           `json:"configFingerprint"`
	ProtocolMajor     uint32           `json:"protocolMajor"`
	ProtocolMinor     uint32           `json:"protocolMinor"`
	Features          []string         `json:"features"`
	Status            string           `json:"status"`
	Discovery         json.RawMessage  `json:"discovery,omitempty"`
	Example           *ProviderExample `json:"example,omitempty"`
	DiscoveredAt      *time.Time       `json:"discoveredAt,omitempty"`
	ValidUntil        *time.Time       `json:"validUntil,omitempty"`
}

type ValidationBody struct {
	CanonicalDigest  string               `json:"canonicalDigest"`
	ResourceCount    int                  `json:"resourceCount"`
	ProviderTargetID string               `json:"providerTargetId"`
	ProviderKind     string               `json:"providerKind"`
	Warnings         []controlplane.Issue `json:"warnings"`
}

type OperationDTO struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instanceId"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Message    string    `json:"message"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	Attempt    uint32    `json:"attempt"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type ResourceDTO struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type InstanceDTO struct {
	ID               string         `json:"id"`
	RevisionID       string         `json:"revisionId,omitempty"`
	ProjectID        string         `json:"projectId"`
	OwnerKind        string         `json:"ownerKind"`
	OwnerID          string         `json:"ownerId"`
	CreatedBy        string         `json:"createdBy"`
	ProviderTargetID string         `json:"providerTargetId"`
	Phase            string         `json:"phase"`
	DesiredLifecycle string         `json:"desiredLifecycle"`
	CanonicalDigest  string         `json:"canonicalDigest"`
	ResourceVersion  uint64         `json:"resourceVersion"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []ResourceDTO  `json:"resources,omitempty"`
	ErrorCode        string         `json:"errorCode,omitempty"`
	ErrorMessage     string         `json:"errorMessage,omitempty"`
	ExpiresAt        *time.Time     `json:"expiresAt,omitempty"`
	MaximumExpiresAt *time.Time     `json:"maximumExpiresAt,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type AccessSessionDTO struct {
	ID               string     `json:"id"`
	SubjectID        string     `json:"subjectId"`
	InstanceID       string     `json:"instanceId"`
	ResourceID       string     `json:"resourceId"`
	Method           string     `json:"method"`
	State            string     `json:"state"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	LeaseExpiresAt   *time.Time `json:"leaseExpiresAt,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	RevocationReason string     `json:"revocationReason,omitempty"`
	ClosureRequired  bool       `json:"closureRequired"`
	ResourceVersion  uint64     `json:"resourceVersion"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type LabTemplateDTO struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	ResourceVersion uint64    `json:"resourceVersion"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type TemplateRevisionDTO struct {
	ID               string    `json:"id"`
	TemplateID       string    `json:"templateId"`
	RevisionNumber   uint64    `json:"revisionNumber"`
	BundleArtifact   string    `json:"bundleArtifact"`
	BundleDigest     string    `json:"bundleDigest"`
	BundleSizeBytes  uint64    `json:"bundleSizeBytes"`
	AuthorAPIVersion string    `json:"authorApiVersion"`
	RendererVersion  string    `json:"rendererVersion"`
	CanonicalVersion string    `json:"canonicalVersion"`
	CanonicalDigest  string    `json:"canonicalDigest"`
	PublishedBy      string    `json:"publishedBy"`
	CreatedAt        time.Time `json:"createdAt"`
}

type TemplateParameterDTO struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Default     json.RawMessage   `json:"default,omitempty"`
	Enum        []json.RawMessage `json:"enum,omitempty"`
	Min         *float64          `json:"min,omitempty"`
	Max         *float64          `json:"max,omitempty"`
	Pattern     string            `json:"pattern,omitempty"`
}
type TemplateContentDTO struct {
	Source     string                 `json:"source"`
	Parameters []TemplateParameterDTO `json:"parameters"`
}

type AuditRecordDTO struct {
	ID             string                    `json:"id"`
	StreamPosition uint64                    `json:"streamPosition"`
	SchemaVersion  string                    `json:"schemaVersion"`
	Category       application.AuditCategory `json:"category"`
	Action         string                    `json:"action"`
	Outcome        application.AuditOutcome  `json:"outcome"`
	Scope          application.RecordScope   `json:"scope"`
	Actor          application.RecordActor   `json:"actor"`
	AggregateType  string                    `json:"aggregateType"`
	AggregateID    string                    `json:"aggregateId"`
	Links          application.RecordLinks   `json:"links"`
	Payload        json.RawMessage           `json:"payload"`
	OccurredAt     time.Time                 `json:"occurredAt"`
}

type EventRecordDTO struct {
	ID                string                  `json:"id"`
	StreamPosition    uint64                  `json:"streamPosition"`
	AggregateSequence domain.EventSequence    `json:"aggregateSequence"`
	SchemaVersion     string                  `json:"schemaVersion"`
	Type              string                  `json:"type"`
	Scope             application.RecordScope `json:"scope"`
	Actor             application.RecordActor `json:"actor"`
	AggregateType     string                  `json:"aggregateType"`
	AggregateID       string                  `json:"aggregateId"`
	Links             application.RecordLinks `json:"links"`
	Payload           json.RawMessage         `json:"payload"`
	OccurredAt        time.Time               `json:"occurredAt"`
}

type QuotaLimitMutationBody struct {
	Limit uint64 `json:"limit" maximum:"9223372036854775807"`
}

type LifetimePolicyMutationBody struct {
	DefaultTTLSeconds       uint64 `json:"defaultTtlSeconds" minimum:"1" maximum:"31622400"`
	MaximumTTLSeconds       uint64 `json:"maximumTtlSeconds" minimum:"1" maximum:"31622400"`
	RenewalWindowSeconds    uint64 `json:"renewalWindowSeconds" maximum:"31622400"`
	WarningBeforeSeconds    uint64 `json:"warningBeforeSeconds" maximum:"31622400"`
	DestructionGraceSeconds uint64 `json:"destructionGraceSeconds" maximum:"86400"`
	RenewalAllowed          bool   `json:"renewalAllowed"`
	PersistentAllowed       bool   `json:"persistentAllowed"`
}

type QuotaUsageDTO struct {
	Dimension application.QuotaDimension `json:"dimension"`
	Reserved  uint64                     `json:"reserved"`
	Committed uint64                     `json:"committed"`
	Retained  uint64                     `json:"retained"`
	Total     uint64                     `json:"total"`
	UpdatedAt time.Time                  `json:"updatedAt"`
}

type InstanceLifetimeDTO struct {
	InstanceID                   string    `json:"instanceId"`
	ExpiresAt                    time.Time `json:"expiresAt"`
	MaximumExpiresAt             time.Time `json:"maximumExpiresAt"`
	ExpirationWarningAt          time.Time `json:"expirationWarningAt"`
	ExpirationGraceSeconds       uint64    `json:"expirationGraceSeconds"`
	LifetimeRenewalAllowed       bool      `json:"lifetimeRenewalAllowed"`
	LifetimeRenewalWindowSeconds uint64    `json:"lifetimeRenewalWindowSeconds"`
	LifetimePolicyDigest         string    `json:"lifetimePolicyDigest"`
	ResourceVersion              uint64    `json:"resourceVersion"`
	UpdatedAt                    time.Time `json:"updatedAt"`
}
