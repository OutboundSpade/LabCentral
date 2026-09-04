package application

import (
	"context"
	"errors"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var (
	ErrAuthRecordNotFound        = errors.New("authentication or authorization record not found")
	ErrAuthVersionConflict       = errors.New("authentication or authorization resource version conflict")
	ErrLastPlatformAdministrator = errors.New("the last active platform administrator cannot be removed")
)

// PermissionAction values are stable authorization contract identifiers.
type PermissionAction string

const (
	ActionProviderTargetHealthRead PermissionAction = "providerTarget.health.read"
	ActionProviderTargetGrant      PermissionAction = "providerTarget.grant"
	ActionInstanceValidate         PermissionAction = "instance.validate"
	ActionInstancePlan             PermissionAction = "instance.plan"
	ActionInstanceCreate           PermissionAction = "instance.create"
	ActionInstanceRead             PermissionAction = "instance.read"
	ActionInstanceDestroy          PermissionAction = "instance.destroy"
	ActionMembershipManage         PermissionAction = "membership.manage"
	ActionRoleAssignmentManage     PermissionAction = "roleAssignment.manage"
	ActionOrganizationManage       PermissionAction = "organization.manage"
	ActionProjectManage            PermissionAction = "project.manage"
	ActionTeamManage               PermissionAction = "team.manage"
)

type Role string

const (
	RolePlatformAdministrator     Role = "platform-administrator"
	RoleOrganizationAdministrator Role = "organization-administrator"
	RoleProviderAdministrator     Role = "provider-administrator"
	RoleLabAuthor                 Role = "lab-author"
	RoleInstructor                Role = "instructor"
	RoleParticipant               Role = "participant"
	RoleViewer                    Role = "viewer"
)

type ScopeKind string

const (
	ScopePlatform     ScopeKind = "platform"
	ScopeOrganization ScopeKind = "organization"
	ScopeProject      ScopeKind = "project"
	ScopeTeam         ScopeKind = "team"
)

type SubjectKind string

const (
	SubjectUser SubjectKind = "user"
	SubjectTeam SubjectKind = "team"
)

type OwnerKind string

const (
	OwnerUser    OwnerKind = "user"
	OwnerTeam    OwnerKind = "team"
	OwnerProject OwnerKind = "project"
)

type UserRecord struct {
	ID              domain.UserID          `json:"id"`
	Username        string                 `json:"username"`
	DisplayName     string                 `json:"displayName"`
	PasswordHash    string                 `json:"-"`
	Disabled        bool                   `json:"disabled"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type OrganizationRecord struct {
	ID              domain.OrganizationID  `json:"id"`
	Name            string                 `json:"name"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type ProjectRecord struct {
	ID              domain.ProjectID       `json:"id"`
	OrganizationID  domain.OrganizationID  `json:"organizationId"`
	Name            string                 `json:"name"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type TeamRecord struct {
	ID              domain.TeamID          `json:"id"`
	OrganizationID  domain.OrganizationID  `json:"organizationId"`
	Name            string                 `json:"name"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type MembershipRecord struct {
	ID              string                 `json:"id"`
	UserID          domain.UserID          `json:"userId"`
	OrganizationID  domain.OrganizationID  `json:"organizationId"`
	TeamID          domain.TeamID          `json:"teamId,omitempty"`
	ValidFrom       time.Time              `json:"validFrom,omitempty"`
	ValidUntil      time.Time              `json:"validUntil,omitempty"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type RoleAssignmentRecord struct {
	ID              string                 `json:"id"`
	SubjectKind     SubjectKind            `json:"subjectKind"`
	SubjectID       string                 `json:"subjectId"`
	ScopeKind       ScopeKind              `json:"scopeKind"`
	ScopeID         string                 `json:"scopeId"`
	Role            Role                   `json:"role"`
	ValidFrom       time.Time              `json:"validFrom,omitempty"`
	ValidUntil      time.Time              `json:"validUntil,omitempty"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion,omitempty"`
}

type TargetGrantRecord struct {
	ID              string                  `json:"id"`
	TargetID        domain.ProviderTargetID `json:"targetId"`
	ScopeKind       ScopeKind               `json:"scopeKind"`
	ScopeID         string                  `json:"scopeId"`
	ValidFrom       time.Time               `json:"validFrom,omitempty"`
	ValidUntil      time.Time               `json:"validUntil,omitempty"`
	ResourceVersion domain.ResourceVersion  `json:"resourceVersion,omitempty"`
}

type ExternalIdentityRecord struct {
	Issuer  string
	Subject string
	UserID  domain.UserID
}

type SessionKind string

const (
	SessionKindBrowser SessionKind = "browser"
	SessionKindBearer  SessionKind = "bearer"
)

type SessionRecord struct {
	ID            string
	UserID        domain.UserID
	Kind          SessionKind
	TokenHash     string
	CSRFHash      string
	KeyID         string
	ExpiresAt     time.Time
	RotatedFromID string
	RevokedAt     time.Time
	CreatedAt     time.Time
}

type PasswordRecoveryRecord struct {
	ID        string
	UserID    domain.UserID
	TokenHash string
	KeyID     string
	ExpiresAt time.Time
}

type OIDCFlowRecord struct {
	ID             string
	StateHash      string
	BrowserHash    string
	NonceHash      string
	PKCECiphertext []byte
	KeyID          string
	RedirectPath   string
	ExpiresAt      time.Time
}

type AuthorizationSnapshot struct {
	User        UserRecord
	Memberships []MembershipRecord
	Assignments []RoleAssignmentRecord
	Projects    []ProjectRecord
	Teams       []TeamRecord
	Grants      []TargetGrantRecord
}

type ScopeHierarchy struct {
	Kind           ScopeKind
	ID             string
	OrganizationID domain.OrganizationID
	ProjectID      domain.ProjectID
	TeamID         domain.TeamID
}

type AuthorizationRequest struct {
	SubjectID domain.UserID
	Action    PermissionAction
	ScopeKind ScopeKind
	ScopeID   string
	TargetID  domain.ProviderTargetID
	OwnerKind OwnerKind
	OwnerID   string
	Now       time.Time
}

type InstanceAccessRecord struct {
	InstanceID domain.InstanceID
	ProjectID  domain.ProjectID
	OwnerKind  OwnerKind
	OwnerID    string
	TargetID   domain.ProviderTargetID
}

type InstanceAccessFilter struct {
	All                          bool
	OwnerRestrictedAll           bool
	UnrestrictedOrganizations    []domain.OrganizationID
	UnrestrictedProjects         []domain.ProjectID
	OwnerRestrictedOrganizations []domain.OrganizationID
	OwnerRestrictedProjects      []domain.ProjectID
	OwnerUserID                  domain.UserID
	OwnerTeamIDs                 []domain.TeamID
	TeamScopedOwnerIDs           []domain.TeamID
}

// Authorizer is the application policy boundary used by transport handlers.
// Resource helpers load only the attributes required for a policy decision.
type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) error
	AuthorizeInstance(context.Context, domain.UserID, PermissionAction, domain.InstanceID) (InstanceAccessRecord, error)
	AuthorizeOperation(context.Context, domain.UserID, PermissionAction, domain.OperationID) (InstanceAccessRecord, error)
	AuthorizedInstanceIDs(context.Context, domain.UserID, PermissionAction) ([]domain.InstanceID, error)
}

// AuthRepository is the cohesive identity, tenancy, session, and RBAC port.
// Password and session secrets reach it only as one-way encoded hashes.
type AuthRepository interface {
	HasUsers(context.Context) (bool, error)
	CreateBootstrapAdministrator(context.Context, UserRecord, RoleAssignmentRecord, *ExternalIdentityRecord) (bool, error)
	CreateUser(context.Context, UserRecord, *ExternalIdentityRecord) error
	UserByID(context.Context, domain.UserID) (UserRecord, error)
	UserByUsername(context.Context, string) (UserRecord, error)
	LinkExternalIdentity(context.Context, ExternalIdentityRecord) error
	UserByExternalIdentity(context.Context, string, string) (UserRecord, error)
	CreateOrganization(context.Context, OrganizationRecord) (OrganizationRecord, error)
	UpdateOrganization(context.Context, OrganizationRecord, domain.ResourceVersion) (OrganizationRecord, error)
	CreateProject(context.Context, ProjectRecord) (ProjectRecord, error)
	UpdateProject(context.Context, ProjectRecord, domain.ResourceVersion) (ProjectRecord, error)
	CreateTeam(context.Context, TeamRecord) (TeamRecord, error)
	UpdateTeam(context.Context, TeamRecord, domain.ResourceVersion) (TeamRecord, error)
	CreateMembership(context.Context, MembershipRecord) (MembershipRecord, error)
	UpdateMembership(context.Context, MembershipRecord, domain.ResourceVersion) (MembershipRecord, error)
	RevokeMembership(context.Context, string, domain.OrganizationID, domain.ResourceVersion, time.Time) (MembershipRecord, error)
	CreateRoleAssignment(context.Context, RoleAssignmentRecord) (RoleAssignmentRecord, error)
	UpdateRoleAssignment(context.Context, RoleAssignmentRecord, domain.ResourceVersion) (RoleAssignmentRecord, error)
	RevokeRoleAssignment(context.Context, string, ScopeKind, string, domain.ResourceVersion, time.Time) (RoleAssignmentRecord, error)
	CreateTargetGrant(context.Context, TargetGrantRecord) (TargetGrantRecord, error)
	UpdateTargetGrant(context.Context, TargetGrantRecord, domain.ResourceVersion) (TargetGrantRecord, error)
	RevokeTargetGrant(context.Context, string, domain.ResourceVersion, time.Time) (TargetGrantRecord, error)
	SetUserDisabled(context.Context, domain.UserID, bool, domain.ResourceVersion, time.Time) (UserRecord, error)
	AuthorizationSnapshot(context.Context, domain.UserID, time.Time) (AuthorizationSnapshot, error)
	ResolveScope(context.Context, ScopeKind, string) (ScopeHierarchy, error)
	CreateSession(context.Context, SessionRecord) error
	SessionByTokenHash(context.Context, string, time.Time) (SessionRecord, error)
	RotateSession(context.Context, string, SessionRecord, time.Time) error
	RevokeSession(context.Context, string, time.Time) error
	CreatePasswordRecovery(context.Context, PasswordRecoveryRecord) error
	ConsumePasswordRecovery(context.Context, string, string, string, time.Time) error
	ConsumeAuthenticationAttempt(context.Context, string, time.Time, time.Duration, uint32) (bool, error)
	ClearAuthenticationAttempts(context.Context, string) error
	CreateOIDCFlow(context.Context, OIDCFlowRecord) error
	ConsumeOIDCFlow(context.Context, string, string, string, time.Time) (OIDCFlowRecord, error)
	InstanceAccess(context.Context, domain.InstanceID) (InstanceAccessRecord, error)
	OperationInstanceAccess(context.Context, domain.OperationID) (InstanceAccessRecord, error)
	ListInstanceAccessFiltered(context.Context, InstanceAccessFilter) ([]InstanceAccessRecord, error)
	OwnerBelongsToScope(context.Context, OwnerKind, string, ScopeHierarchy, time.Time) (bool, error)
}
