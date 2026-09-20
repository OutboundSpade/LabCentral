package application

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

const ProjectGroupVersion = "labcentral.project-group/v1"
const MaximumGroupBatch = 100

var ErrGroupHasLabs = errors.New("end all active group labs before deleting the group")

var ErrGroupEnrollmentClosed = errors.New("group enrollment is closed")
var ErrGroupMembershipConflict = errors.New("already enrolled in another group in this project")

type ProjectGroup struct {
	APIVersion      string                 `json:"apiVersion"`
	ID              domain.TeamID          `json:"id"`
	ProjectID       domain.ProjectID       `json:"projectId"`
	Name            string                 `json:"name"`
	EnrollmentOpen  bool                   `json:"enrollmentOpen"`
	MemberCount     uint32                 `json:"memberCount"`
	Joined          bool                   `json:"joined"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	CreatedAt       time.Time              `json:"createdAt"`
}
type ProjectGroupSeed struct {
	ID   domain.TeamID
	Name string
}
type CreateProjectGroups struct {
	ProjectID      domain.ProjectID
	ActorID        domain.UserID
	Groups         []ProjectGroupSeed
	EnrollmentOpen bool
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
}
type ChangeProjectGroupEnrollment struct {
	ProjectID       domain.ProjectID
	GroupID         domain.TeamID
	ActorID         domain.UserID
	Open            bool
	ExpectedVersion domain.ResourceVersion
	IdempotencyKey  string
	RequestDigest   domain.SHA256Digest
}
type ChangeProjectGroupMembership struct {
	ProjectID      domain.ProjectID
	GroupID        domain.TeamID
	ActorID        domain.UserID
	Join           bool
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
}
type ProjectGroupPage struct {
	Items      []ProjectGroup `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}
type DeleteProjectGroup struct {
	ProjectID       domain.ProjectID
	GroupID         domain.TeamID
	ActorID         domain.UserID
	ExpectedVersion domain.ResourceVersion
	IdempotencyKey  string
	RequestDigest   domain.SHA256Digest
}
type ProjectGroupStore interface {
	DeleteProjectGroup(context.Context, DeleteProjectGroup) error
	ListProjectGroups(context.Context, domain.ProjectID, domain.UserID, string, uint32) (ProjectGroupPage, error)
	CreateProjectGroups(context.Context, CreateProjectGroups) ([]ProjectGroup, error)
	ChangeProjectGroupEnrollment(context.Context, ChangeProjectGroupEnrollment) (ProjectGroup, error)
	ChangeProjectGroupMembership(context.Context, ChangeProjectGroupMembership) (ProjectGroup, error)
}

// ProjectGroupMember exposes only the display identity of a collaborator.
type ProjectGroupMember struct {
	ID          domain.UserID `json:"id"`
	DisplayName string        `json:"displayName"`
}
type ProjectGroupMemberPage struct {
	Items      []ProjectGroupMember `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}
type ProjectGroupMemberStore interface {
	ListProjectGroupMembers(context.Context, domain.ProjectID, domain.TeamID, domain.UserID, string, uint32) (ProjectGroupMemberPage, error)
}

// InstancePresentation is authorized through its owning lab, never independently.
type InstancePresentation struct {
	Name         string
	Instructions string
	OwnerName    string
}
