package application

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"io"
	"time"
)

type ExtensionArtifact struct {
	Digest    domain.SHA256Digest `json:"digest"`
	Size      uint64              `json:"size"`
	MediaType string              `json:"mediaType"`
	// TransferToken is ephemeral and excluded from every persistent work payload.
	TransferToken string `json:"-"`
}
type ExtensionSourceRequest struct {
	Call                      ProviderCall
	BindingID, Reference      string
	Configuration, Credential []byte
}
type ExtensionSourceResult struct {
	Collection       *SourceCollection   `json:"collection,omitempty"`
	ResolvedIdentity string              `json:"resolvedIdentity"`
	ProvenanceDigest domain.SHA256Digest `json:"provenanceDigest"`
	Bundle           ExtensionArtifact   `json:"bundle"`
}
type ExtensionSource interface {
	Resolve(context.Context, ExtensionSourceRequest) (ExtensionSourceResult, error)
	Recover(context.Context, ProviderCall) (string, ExtensionSourceResult, error)
	Cancel(context.Context, ProviderCall) error
	ReadArtifact(context.Context, ProviderCall, ExtensionArtifact) (io.ReadCloser, error)
}

// ExtensionWorkRequest is a versioned core-owned payload. Never put credentials,
// transfer handles, provider state or arbitrary plugin workflow nodes in it.
type ExtensionWorkRequest struct {
	PreparationID       domain.OperationID     `json:"preparationId,omitempty"`
	LibraryID           string                 `json:"libraryId,omitempty"`
	Build               *BuildWork             `json:"build,omitempty"`
	Import              *ImportWork            `json:"import,omitempty"`
	Preparation         *PreparationWork       `json:"preparation,omitempty"`
	DedupKey            string                 `json:"dedupKey,omitempty"`
	Version             uint32                 `json:"version"`
	Kind                domain.WorkSubjectKind `json:"kind"`
	PluginInstance      string                 `json:"pluginInstance"`
	PluginDigest        string                 `json:"pluginDigest"`
	BindingID           string                 `json:"bindingId"`
	ConfigurationDigest domain.SHA256Digest    `json:"configurationDigest"`
	TemplateID          domain.TemplateID      `json:"templateId,omitempty"`
	Reference           string                 `json:"reference,omitempty"`
}
type ExtensionWork struct {
	ID              domain.OperationID    `json:"id"`
	ProjectID       domain.ProjectID      `json:"projectId"`
	ActorID         domain.UserID         `json:"actorId"`
	InvocationID    domain.InvocationID   `json:"invocationId"`
	Request         ExtensionWorkRequest  `json:"request"`
	State           domain.OperationState `json:"state"`
	Result          json.RawMessage       `json:"result,omitempty"`
	FailureCode     string                `json:"failureCode,omitempty"`
	CancelRequested bool                  `json:"cancelRequested"`
	CreatedAt       time.Time             `json:"createdAt"`
}
type CreateExtensionWork struct {
	Work           ExtensionWork
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	GovernanceRecordIDs
}
type ExtensionWorkClaim struct {
	Work       ExtensionWork
	Operation  domain.Operation
	LeaseOwner string
	Recover    bool
}
type ExtensionWorkStore interface {
	CreateExtensionWork(context.Context, CreateExtensionWork) (ExtensionWork, error)
	GetExtensionWork(context.Context, domain.OperationID) (ExtensionWork, error)
	ListExtensionWork(context.Context, domain.ProjectID) ([]ExtensionWork, error)
	ClaimExtensionWork(context.Context, string, string, domain.WorkSubjectKind, time.Duration) (ExtensionWorkClaim, error)
	RenewExtensionWork(context.Context, ExtensionWorkClaim, time.Duration) error
	FinishExtensionWork(context.Context, ExtensionWorkClaim, domain.OperationState, json.RawMessage, string, GovernanceRecordIDs) error
	CancelExtensionWork(context.Context, domain.OperationID, domain.UserID, GovernanceRecordIDs) error
}

// Collection support is optional and negotiated independently of Resolve.
type SourceCollection struct {
	Entries []SourceCollectionEntry `json:"entries"`
}
type SourceCollectionEntry struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Bundle      ExtensionArtifact `json:"bundle"`
}
type SourceCollectionResolver interface {
	ResolveCollection(context.Context, ExtensionSourceRequest) (ExtensionSourceResult, error)
}

var ErrInvalidSource = errors.New("invalid source collection")
