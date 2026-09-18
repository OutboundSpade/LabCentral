package application

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

type TemplateLibrary struct {
	ID              string                 `json:"id"`
	ProjectID       domain.ProjectID       `json:"projectId"`
	AuthorID        domain.UserID          `json:"authorId"`
	Binding         string                 `json:"binding"`
	Reference       string                 `json:"reference"`
	IntervalSeconds uint32                 `json:"intervalSeconds"`
	Paused          bool                   `json:"paused"`
	PauseReason     string                 `json:"pauseReason,omitempty"`
	NextSyncAt      time.Time              `json:"nextSyncAt"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
}
type CreateLibrary struct {
	Library        TemplateLibrary
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
	GovernanceRecordIDs
}
type LibraryEntry struct {
	Key        string            `json:"key"`
	TemplateID domain.TemplateID `json:"templateId"`
	RevisionID domain.RevisionID `json:"revisionId"`
	Available  bool              `json:"available"`
	Commit     string            `json:"commit"`
}
type LibraryPublication struct {
	Key      string
	Template CreateLabTemplateRequest
	Revision PublishTemplateRevisionRequest
}
type LibraryPublishRequest struct {
	Claim   ExtensionWorkClaim
	Commit  string
	Entries []LibraryPublication
}
type LibraryStore interface {
	DeferLibraryWork(context.Context, ExtensionWorkClaim, time.Duration) error
	CreateLibrary(context.Context, CreateLibrary) (TemplateLibrary, error)
	GetLibrary(context.Context, string) (TemplateLibrary, error)
	ListLibraries(context.Context, domain.ProjectID) ([]TemplateLibrary, error)
	DueLibraries(context.Context) ([]TemplateLibrary, error)
	SetLibraryPaused(context.Context, string, domain.UserID, bool, string, GovernanceRecordIDs) error
	LibraryEntries(context.Context, string) ([]LibraryEntry, error)
	PublishLibrary(context.Context, LibraryPublishRequest) error
	LibraryHistory(context.Context, string) ([]ExtensionWork, error)
}

var ErrTemplateManaged = errors.New("synchronized templates require an independent copy")
