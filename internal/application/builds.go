package application

import (
	"context"
	"encoding/json"
	"github.com/OutboundSpade/LabCentral/internal/domain"

	"io"
)

type BuildLimits struct {
	TimeoutSeconds uint32   `json:"timeoutSeconds"`
	CPUCount       uint32   `json:"cpuCount"`
	MemoryBytes    uint64   `json:"memoryBytes"`
	InputBytes     uint64   `json:"inputBytes"`
	OutputBytes    uint64   `json:"outputBytes"`
	Egress         []string `json:"egress"`
}
type BuildRequest struct {
	BindingConfiguration []byte
	Call                 ProviderCall
	BindingID            string
	Recipe               domain.BuildRecipe
	Digest               domain.SHA256Digest
	Inputs               []ExtensionArtifact
	Limits               BuildLimits
}
type BuildResult struct {
	Image            ExtensionArtifact   `json:"image"`
	ArtifactRef      domain.ArtifactRef  `json:"artifactRef"`
	ProvenanceDigest domain.SHA256Digest `json:"provenanceDigest"`
}
type ArtifactTransfer interface {
	ReadArtifact(context.Context, ProviderCall, ExtensionArtifact) (io.ReadCloser, error)
	WriteArtifact(context.Context, ProviderCall, ExtensionArtifact, io.Reader) (ExtensionArtifact, error)
}
type Builder interface {
	ArtifactTransfer
	ValidateBuild(context.Context, BuildRequest) error
	Build(context.Context, BuildRequest) (string, BuildResult, error)
	RecoverBuild(context.Context, ProviderCall) (string, BuildResult, error)
	CancelBuild(context.Context, ProviderCall) error
}
type ImageBinding struct {
	ImageRef                  string              `json:"imageRef"`
	ProviderImageID           string              `json:"providerImageId"`
	ArtifactDigest            domain.SHA256Digest `json:"artifactDigest"`
	TargetConfigurationDigest string              `json:"targetConfigurationDigest"`
	Architecture              string              `json:"architecture"`
	OpaqueBinding             []byte              `json:"-"`
}
type ImportRequest struct {
	Call             ProviderCall
	Target           ProviderTargetContext
	Artifact         ExtensionArtifact
	Architecture     string
	ProvenanceDigest domain.SHA256Digest
}
type ImageImporter interface {
	ArtifactTransfer
	ImportImage(context.Context, ImportRequest) (string, ImageBinding, error)
	RecoverImport(context.Context, ImportRequest) (string, ImageBinding, error)
	ValidateImageBinding(context.Context, ImportRequest, ImageBinding) error
}
type BuildWork struct {
	RevisionID      domain.RevisionID   `json:"revisionId"`
	Recipe          domain.BuildRecipe  `json:"recipe"`
	RecipeDigest    domain.SHA256Digest `json:"recipeDigest"`
	ContextArtifact domain.ArtifactRef  `json:"contextArtifact"`
	ContextDigest   domain.SHA256Digest `json:"contextDigest"`
	ContextSize     uint64              `json:"contextSize"`
	Limits          BuildLimits         `json:"limits"`
}
type ImportWork struct {
	BuildID           domain.OperationID      `json:"buildId"`
	TargetID          domain.ProviderTargetID `json:"targetId"`
	Architecture      string                  `json:"architecture"`
	TargetFingerprint string                  `json:"targetFingerprint"`
}
type PreparationWork struct {
	Owners       []InstanceOwner         `json:"owners,omitempty"`
	RevisionID   domain.RevisionID       `json:"revisionId"`
	TargetID     domain.ProviderTargetID `json:"targetId"`
	Inputs       domain.TypedInputs      `json:"inputs"`
	Acknowledged bool                    `json:"acknowledged"`
	Lifetime     InstanceLifetimeRequest `json:"lifetime"`
}
type StoredImageBinding struct {
	ID                string                  `json:"id"`
	ProjectID         domain.ProjectID        `json:"projectId"`
	TargetID          domain.ProviderTargetID `json:"targetId"`
	ProviderDigest    string                  `json:"providerDigest"`
	ImportOperationID domain.OperationID      `json:"importOperationId"`
	Binding           ImageBinding            `json:"binding"`
	Approved          bool                    `json:"approved"`
	Retired           bool                    `json:"retired"`
}
type BuildStore interface {
	ExtensionWorkStore
	YieldExtensionWork(context.Context, ExtensionWorkClaim) error
	ReferenceExtensionArtifact(context.Context, domain.OperationID, domain.ArtifactRef) error
	LinkExtensionWork(context.Context, domain.OperationID, domain.OperationID) error
	PutImageBinding(context.Context, ExtensionWorkClaim, StoredImageBinding) error
	GetImageBinding(context.Context, string) (StoredImageBinding, error)
	SetImageBindingApproval(context.Context, string, domain.UserID, bool, bool, GovernanceRecordIDs) error
}

// Durable results contain no transfer capability or provider credentials.
type PreparationResult struct {
	BuildIDs   []domain.OperationID `json:"buildIds"`
	ImportIDs  []domain.OperationID `json:"importIds"`
	Deployment json.RawMessage      `json:"deployment,omitempty"`
}
