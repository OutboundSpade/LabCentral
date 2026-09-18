package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
type BuildDependency struct {
	RecipeID string
	Artifact ExtensionArtifact
}
type BuildRequest struct {
	Dependencies         []BuildDependency
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
	ReservedPlan     *ImageImportPlan
	Call             ProviderCall
	Target           ProviderTargetContext
	Artifact         ExtensionArtifact
	Architecture     string
	ProvenanceDigest domain.SHA256Digest
}
type ImageImportPlan struct {
	Opaque []byte              `json:"opaque"`
	Digest domain.SHA256Digest `json:"digest"`
	Claims []AllocationKey     `json:"claims"`
}
type ImageImportPlanner interface {
	PlanImageImport(context.Context, ImportRequest, []AllocationKey) (ImageImportPlan, error)
}
type ImageImportReservationStore interface {
	GetImageImportPlan(context.Context, domain.OperationID) (ImageImportPlan, error)
	ReserveImageImportPlan(context.Context, ExtensionWorkClaim, ImageImportPlan) error
}
type ImageImporter interface {
	ArtifactTransfer
	ImportImage(context.Context, ImportRequest) (string, ImageBinding, error)
	RecoverImport(context.Context, ImportRequest) (string, ImageBinding, error)
	ValidateImageBinding(context.Context, ImportRequest, ImageBinding) error
}
type BuildDependencyWork struct {
	RecipeID string             `json:"recipeId"`
	BuildID  domain.OperationID `json:"buildId"`
}
type BuildWork struct {
	Dependencies    []BuildDependencyWork `json:"dependencies,omitempty"`
	RevisionID      domain.RevisionID     `json:"revisionId"`
	Recipe          domain.BuildRecipe    `json:"recipe"`
	RecipeDigest    domain.SHA256Digest   `json:"recipeDigest"`
	ContextArtifact domain.ArtifactRef    `json:"contextArtifact"`
	ContextDigest   domain.SHA256Digest   `json:"contextDigest"`
	ContextSize     uint64                `json:"contextSize"`
	Limits          BuildLimits           `json:"limits"`
}
type ImportWork struct {
	RetireBindingID   string                  `json:"retireBindingId,omitempty"`
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
	Stage      string               `json:"stage,omitempty"`
	BuildIDs   []domain.OperationID `json:"buildIds"`
	ImportIDs  []domain.OperationID `json:"importIds"`
	Deployment json.RawMessage      `json:"deployment,omitempty"`
}

func ValidateImageImportPlan(p ImageImportPlan, target domain.ProviderTargetID) (ImageImportPlan, error) {
	if len(p.Opaque) == 0 || len(p.Opaque) > 1<<20 || len(p.Claims) == 0 || len(p.Claims) > 16 {
		return p, fmt.Errorf("invalid image import plan bounds")
	}
	sum := sha256.Sum256(p.Opaque)
	if fmt.Sprintf("%x", sum) != p.Digest.Hex() {
		return p, fmt.Errorf("image import plan digest mismatch")
	}
	claims, err := validateAllocationKeys(p.Claims)
	if err != nil {
		return p, err
	}
	for _, c := range claims {
		if c.TargetID != target {
			return p, fmt.Errorf("image import target mismatch")
		}
	}
	p.Claims = claims
	return p, nil
}

type ImageRetirer interface {
	RetireImage(context.Context, ImportRequest, ImageBinding) (string, error)
}
type ImageRetirementStore interface {
	CanRetireImage(context.Context, ExtensionWorkClaim, string) (bool, error)
	ReleaseImageImportClaims(context.Context, ExtensionWorkClaim, string) error
}

type ExtensionWorkProgressStore interface {
	YieldExtensionWorkProgress(context.Context, ExtensionWorkClaim, json.RawMessage) error
}
