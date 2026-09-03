// Package authorization implements LabCentral's default-deny, scope-aware
// role and relationship evaluator. It never grants beyond the fixed core role
// permissions accepted by ADR-0010.
package authorization

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var ErrDenied = errors.New("authorization denied")

type Evaluator struct {
	repository application.AuthRepository
	now        func() time.Time
}

func New(repository application.AuthRepository) (*Evaluator, error) {
	if repository == nil {
		return nil, fmt.Errorf("authorization repository is required")
	}
	return &Evaluator{repository: repository, now: time.Now}, nil
}

func (e *Evaluator) Authorize(ctx context.Context, request application.AuthorizationRequest) error {
	if request.SubjectID == "" || request.Action == "" || request.ScopeKind == "" || request.ScopeID == "" {
		return ErrDenied
	}
	if request.Now.IsZero() {
		request.Now = e.now().UTC()
	}
	snapshot, err := e.repository.AuthorizationSnapshot(ctx, request.SubjectID, request.Now)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return ErrDenied
		}
		return fmt.Errorf("load authorization snapshot: %w", err)
	}
	hierarchy, err := e.repository.ResolveScope(ctx, request.ScopeKind, request.ScopeID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return ErrDenied
		}
		return fmt.Errorf("resolve authorization scope: %w", err)
	}
	if request.OwnerKind != "" && validatesRequestedOwner(request.Action) {
		belongs, err := e.repository.OwnerBelongsToScope(ctx, request.OwnerKind, request.OwnerID, hierarchy, request.Now)
		if err != nil {
			return fmt.Errorf("resolve owner relationship: %w", err)
		}
		if !belongs {
			return ErrDenied
		}
	}
	if requiresTargetGrant(request.Action) && request.TargetID == "" {
		return ErrDenied
	}
	for _, assignment := range snapshot.Assignments {
		if !roleAllows(assignment.Role, request.Action) || !assignmentCovers(assignment, hierarchy, request, snapshot) {
			continue
		}
		if requiresOwnerRelationship(assignment.Role, request.Action) && !ownsResource(request, snapshot) {
			continue
		}
		if request.TargetID != "" && requiresTargetGrant(request.Action) && !hasTargetGrant(request, hierarchy, snapshot) {
			continue
		}
		return nil
	}
	return ErrDenied
}

func validatesRequestedOwner(action application.PermissionAction) bool {
	return action == application.ActionInstanceValidate || action == application.ActionInstancePlan || action == application.ActionInstanceCreate
}

func (e *Evaluator) AuthorizeInstance(ctx context.Context, subjectID domain.UserID, action application.PermissionAction, instanceID domain.InstanceID) (application.InstanceAccessRecord, error) {
	access, err := e.repository.InstanceAccess(ctx, instanceID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return application.InstanceAccessRecord{}, ErrDenied
		}
		return application.InstanceAccessRecord{}, err
	}
	return access, e.Authorize(ctx, instanceRequest(subjectID, action, access))
}

func (e *Evaluator) AuthorizeOperation(ctx context.Context, subjectID domain.UserID, action application.PermissionAction, operationID domain.OperationID) (application.InstanceAccessRecord, error) {
	access, err := e.repository.OperationInstanceAccess(ctx, operationID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return application.InstanceAccessRecord{}, ErrDenied
		}
		return application.InstanceAccessRecord{}, err
	}
	return access, e.Authorize(ctx, instanceRequest(subjectID, action, access))
}

func (e *Evaluator) AuthorizedInstanceIDs(ctx context.Context, subjectID domain.UserID, action application.PermissionAction) ([]domain.InstanceID, error) {
	now := e.now().UTC()
	snapshot, err := e.repository.AuthorizationSnapshot(ctx, subjectID, now)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return []domain.InstanceID{}, nil
		}
		return nil, err
	}
	filter := instanceAccessFilter(snapshot, action)
	accessRecords, err := e.repository.ListInstanceAccessFiltered(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := make([]domain.InstanceID, 0, len(accessRecords))
	for _, access := range accessRecords {
		request := instanceRequest(subjectID, action, access)
		request.Now = now
		err := e.Authorize(ctx, request)
		if err == nil {
			result = append(result, access.InstanceID)
			continue
		}
		if !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	return result, nil
}

func instanceAccessFilter(snapshot application.AuthorizationSnapshot, action application.PermissionAction) application.InstanceAccessFilter {
	filter := application.InstanceAccessFilter{OwnerUserID: snapshot.User.ID}
	teamMemberships := make(map[domain.TeamID]bool)
	for _, membership := range snapshot.Memberships {
		if membership.TeamID != "" {
			teamMemberships[membership.TeamID] = true
			filter.OwnerTeamIDs = appendUniqueTeam(filter.OwnerTeamIDs, membership.TeamID)
		}
	}
	for _, assignment := range snapshot.Assignments {
		if !roleAllows(assignment.Role, action) {
			continue
		}
		restricted := assignment.Role == application.RoleParticipant &&
			(action == application.ActionInstanceRead || action == application.ActionInstanceDestroy)
		switch assignment.ScopeKind {
		case application.ScopePlatform:
			if restricted {
				filter.OwnerRestrictedAll = true
			} else if assignment.ScopeID == "platform" {
				filter.All = true
			}
		case application.ScopeOrganization:
			organizationID := domain.OrganizationID(assignment.ScopeID)
			if restricted {
				filter.OwnerRestrictedOrganizations = appendUniqueOrganization(filter.OwnerRestrictedOrganizations, organizationID)
			} else {
				filter.UnrestrictedOrganizations = appendUniqueOrganization(filter.UnrestrictedOrganizations, organizationID)
			}
		case application.ScopeProject:
			projectID := domain.ProjectID(assignment.ScopeID)
			if restricted {
				filter.OwnerRestrictedProjects = appendUniqueProject(filter.OwnerRestrictedProjects, projectID)
			} else {
				filter.UnrestrictedProjects = appendUniqueProject(filter.UnrestrictedProjects, projectID)
			}
		case application.ScopeTeam:
			teamID := domain.TeamID(assignment.ScopeID)
			if teamMemberships[teamID] {
				filter.TeamScopedOwnerIDs = appendUniqueTeam(filter.TeamScopedOwnerIDs, teamID)
			}
		}
	}
	return filter
}

func appendUniqueOrganization(values []domain.OrganizationID, value domain.OrganizationID) []domain.OrganizationID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueProject(values []domain.ProjectID, value domain.ProjectID) []domain.ProjectID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueTeam(values []domain.TeamID, value domain.TeamID) []domain.TeamID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func instanceRequest(subjectID domain.UserID, action application.PermissionAction, access application.InstanceAccessRecord) application.AuthorizationRequest {
	return application.AuthorizationRequest{
		SubjectID: subjectID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(access.ProjectID),
		TargetID: access.TargetID, OwnerKind: access.OwnerKind, OwnerID: access.OwnerID,
	}
}

func roleAllows(role application.Role, action application.PermissionAction) bool {
	if role == application.RolePlatformAdministrator {
		return knownAction(action)
	}
	switch role {
	case application.RoleOrganizationAdministrator:
		switch action {
		case application.ActionOrganizationManage, application.ActionProjectManage, application.ActionTeamManage,
			application.ActionMembershipManage, application.ActionRoleAssignmentManage,
			application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
			application.ActionInstanceRead, application.ActionInstanceDestroy, application.ActionProviderTargetHealthRead:
			return true
		}
	case application.RoleProviderAdministrator:
		return action == application.ActionProviderTargetHealthRead || action == application.ActionProviderTargetGrant
	case application.RoleLabAuthor:
		return action == application.ActionInstanceValidate || action == application.ActionInstancePlan || action == application.ActionInstanceRead
	case application.RoleInstructor:
		switch action {
		case application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
			application.ActionInstanceRead, application.ActionInstanceDestroy, application.ActionProviderTargetHealthRead:
			return true
		}
	case application.RoleParticipant:
		switch action {
		case application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
			application.ActionInstanceRead, application.ActionInstanceDestroy, application.ActionProviderTargetHealthRead:
			return true
		}
	case application.RoleViewer:
		return action == application.ActionInstanceRead || action == application.ActionProviderTargetHealthRead
	}
	return false
}

func knownAction(action application.PermissionAction) bool {
	switch action {
	case application.ActionProviderTargetHealthRead, application.ActionProviderTargetGrant,
		application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
		application.ActionInstanceRead, application.ActionInstanceDestroy, application.ActionMembershipManage,
		application.ActionRoleAssignmentManage, application.ActionOrganizationManage, application.ActionProjectManage,
		application.ActionTeamManage:
		return true
	default:
		return false
	}
}

func assignmentCovers(assignment application.RoleAssignmentRecord, hierarchy application.ScopeHierarchy, request application.AuthorizationRequest, snapshot application.AuthorizationSnapshot) bool {
	switch assignment.ScopeKind {
	case application.ScopePlatform:
		return assignment.ScopeID == "platform"
	case application.ScopeOrganization:
		return assignment.ScopeID == string(hierarchy.OrganizationID)
	case application.ScopeProject:
		return assignment.ScopeID == string(hierarchy.ProjectID)
	case application.ScopeTeam:
		if assignment.ScopeID == string(hierarchy.TeamID) {
			return true
		}
		return request.OwnerKind == application.OwnerTeam && assignment.ScopeID == request.OwnerID && isTeamMember(request.OwnerID, snapshot)
	default:
		return false
	}
}

func requiresOwnerRelationship(role application.Role, action application.PermissionAction) bool {
	return role == application.RoleParticipant && (action == application.ActionInstanceCreate || action == application.ActionInstanceRead || action == application.ActionInstanceDestroy)
}

func ownsResource(request application.AuthorizationRequest, snapshot application.AuthorizationSnapshot) bool {
	switch request.OwnerKind {
	case application.OwnerUser:
		return request.OwnerID == string(request.SubjectID)
	case application.OwnerTeam:
		return isTeamMember(request.OwnerID, snapshot)
	case application.OwnerProject:
		return false
	default:
		return false
	}
}

func isTeamMember(teamID string, snapshot application.AuthorizationSnapshot) bool {
	for _, membership := range snapshot.Memberships {
		if string(membership.TeamID) == teamID {
			return true
		}
	}
	return false
}

func requiresTargetGrant(action application.PermissionAction) bool {
	return action == application.ActionInstanceValidate || action == application.ActionInstancePlan || action == application.ActionInstanceCreate || action == application.ActionInstanceDestroy
}

func hasTargetGrant(request application.AuthorizationRequest, hierarchy application.ScopeHierarchy, snapshot application.AuthorizationSnapshot) bool {
	for _, grant := range snapshot.Grants {
		if grant.TargetID != request.TargetID {
			continue
		}
		switch grant.ScopeKind {
		case application.ScopePlatform:
			if grant.ScopeID == "platform" {
				return true
			}
		case application.ScopeOrganization:
			if grant.ScopeID == string(hierarchy.OrganizationID) {
				return true
			}
		case application.ScopeProject:
			if grant.ScopeID == string(hierarchy.ProjectID) {
				return true
			}
		case application.ScopeTeam:
			if grant.ScopeID == request.OwnerID && request.OwnerKind == application.OwnerTeam && isTeamMember(grant.ScopeID, snapshot) {
				return true
			}
		}
	}
	return false
}

var _ application.Authorizer = (*Evaluator)(nil)
