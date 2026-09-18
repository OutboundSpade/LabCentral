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
	recorder   application.GovernanceRecordStore
	now        func() time.Time
}

func New(repository application.AuthRepository) (*Evaluator, error) {
	if repository == nil {
		return nil, fmt.Errorf("authorization repository is required")
	}
	recorder, _ := repository.(application.GovernanceRecordStore)
	return &Evaluator{repository: repository, recorder: recorder, now: time.Now}, nil
}

func (e *Evaluator) Authorize(ctx context.Context, request application.AuthorizationRequest) error {
	scope, decisionErr := e.evaluate(ctx, request)
	if e.recorder != nil {
		if err := e.recordDecision(ctx, request, scope, decisionErr); err != nil {
			return fmt.Errorf("record authorization decision: %w", err)
		}
	}
	return decisionErr
}

func (e *Evaluator) evaluate(ctx context.Context, request application.AuthorizationRequest) (application.RecordScope, error) {
	if request.SubjectID == "" || request.Action == "" || request.ScopeKind == "" || request.ScopeID == "" {
		return application.RecordScope{}, ErrDenied
	}
	if request.Now.IsZero() {
		request.Now = e.now().UTC()
	}
	snapshot, err := e.repository.AuthorizationSnapshot(ctx, request.SubjectID, request.Now)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return application.RecordScope{}, ErrDenied
		}
		return application.RecordScope{}, fmt.Errorf("load authorization snapshot: %w", err)
	}
	hierarchy, err := e.repository.ResolveScope(ctx, request.ScopeKind, request.ScopeID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			return application.RecordScope{}, ErrDenied
		}
		return application.RecordScope{}, fmt.Errorf("resolve authorization scope: %w", err)
	}
	recordScope := application.RecordScope{OrganizationID: hierarchy.OrganizationID, ProjectID: hierarchy.ProjectID}
	if request.OwnerKind != "" && validatesRequestedOwner(request.Action) {
		belongs, err := e.repository.OwnerBelongsToScope(ctx, request.OwnerKind, request.OwnerID, hierarchy, request.Now)
		if err != nil {
			return recordScope, fmt.Errorf("resolve owner relationship: %w", err)
		}
		if !belongs {
			return recordScope, ErrDenied
		}
	}
	if requiresTargetGrant(request.Action) && request.TargetID == "" {
		return recordScope, ErrDenied
	}
	for _, assignment := range snapshot.Assignments {
		if !roleAllows(assignment.Role, request.Action) || !assignmentCovers(assignment, hierarchy, request, snapshot) {
			continue
		}
		if requiresOwnerRelationship(assignment.Role, request.Action) && !ownsResource(request, hierarchy, snapshot) {
			continue
		}
		if request.TargetID != "" && requiresTargetGrant(request.Action) && !hasTargetGrant(request, hierarchy, snapshot) {
			continue
		}
		if request.Action == application.ActionProviderTargetHealthRead && request.TargetID != "" &&
			assignment.Role != application.RolePlatformAdministrator && assignment.Role != application.RoleProviderAdministrator &&
			!hasTargetGrant(request, hierarchy, snapshot) {
			continue
		}
		return recordScope, nil
	}
	return recordScope, ErrDenied
}

type authorizationDecisionPayload struct {
	Action      application.PermissionAction `json:"action"`
	Decision    string                       `json:"decision"`
	ScopeKind   application.ScopeKind        `json:"scopeKind"`
	TargetBound bool                         `json:"targetBound"`
	OwnerKind   application.OwnerKind        `json:"ownerKind,omitempty"`
}

func (e *Evaluator) recordDecision(ctx context.Context, request application.AuthorizationRequest, scope application.RecordScope, decisionErr error) error {
	// Recording a successful read into the same stream being paged would make
	// cursor exhaustion impossible: every next-page request would append a new
	// tail record. Denied and failed audit reads remain security events.
	if request.Action == application.ActionAuditRead && decisionErr == nil {
		return nil
	}
	outcome := application.AuditSucceeded
	decision := "allowed"
	if decisionErr != nil {
		outcome = application.AuditFailed
		decision = "failed"
		if errors.Is(decisionErr, ErrDenied) {
			outcome = application.AuditDenied
			decision = "denied"
		}
	}
	actor := application.RecordActor{Kind: application.RecordActorAnonymous}
	aggregateID := "anonymous"
	if request.SubjectID != "" {
		actor = application.RecordActor{Kind: application.RecordActorUser, ID: string(request.SubjectID)}
		aggregateID = string(request.SubjectID)
	}
	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		Category: application.AuditAuthorization, Action: "authorization.evaluate", Outcome: outcome,
		EventType: "authorization.decision", Scope: scope, Actor: actor,
		AggregateType: "authorization-subject", AggregateID: aggregateID,
		Payload: authorizationDecisionPayload{Action: request.Action, Decision: decision, ScopeKind: request.ScopeKind, TargetBound: request.TargetID != "", OwnerKind: request.OwnerKind},
	})
	if err != nil {
		return err
	}
	_, _, err = e.recorder.AppendGovernanceRecords(ctx, audit, event)
	return err
}

func validatesRequestedOwner(action application.PermissionAction) bool {
	return action == application.ActionInstanceValidate || action == application.ActionInstancePlan ||
		action == application.ActionInstanceCreate || action == application.ActionInstanceBatchCreate
}

func (e *Evaluator) AuthorizeInstance(ctx context.Context, subjectID domain.UserID, action application.PermissionAction, instanceID domain.InstanceID) (application.InstanceAccessRecord, error) {
	access, err := e.repository.InstanceAccess(ctx, instanceID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			if recordErr := e.recordDecisionIfConfigured(ctx, application.AuthorizationRequest{SubjectID: subjectID, Action: action}, application.RecordScope{}, ErrDenied); recordErr != nil {
				return application.InstanceAccessRecord{}, recordErr
			}
			return application.InstanceAccessRecord{}, ErrDenied
		}
		if recordErr := e.recordDecisionIfConfigured(ctx, application.AuthorizationRequest{SubjectID: subjectID, Action: action}, application.RecordScope{}, err); recordErr != nil {
			return application.InstanceAccessRecord{}, recordErr
		}
		return application.InstanceAccessRecord{}, err
	}
	return access, e.Authorize(ctx, instanceRequest(subjectID, action, access))
}

func (e *Evaluator) AuthorizeOperation(ctx context.Context, subjectID domain.UserID, action application.PermissionAction, operationID domain.OperationID) (application.InstanceAccessRecord, error) {
	access, err := e.repository.OperationInstanceAccess(ctx, operationID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			if recordErr := e.recordDecisionIfConfigured(ctx, application.AuthorizationRequest{SubjectID: subjectID, Action: action}, application.RecordScope{}, ErrDenied); recordErr != nil {
				return application.InstanceAccessRecord{}, recordErr
			}
			return application.InstanceAccessRecord{}, ErrDenied
		}
		if recordErr := e.recordDecisionIfConfigured(ctx, application.AuthorizationRequest{SubjectID: subjectID, Action: action}, application.RecordScope{}, err); recordErr != nil {
			return application.InstanceAccessRecord{}, recordErr
		}
		return application.InstanceAccessRecord{}, err
	}
	return access, e.Authorize(ctx, instanceRequest(subjectID, action, access))
}

func (e *Evaluator) AuthorizeInstanceBatch(ctx context.Context, subjectID domain.UserID, action application.PermissionAction, batchID domain.InstanceBatchID) (application.InstanceBatchAccessRecord, error) {
	decisionRequest := application.AuthorizationRequest{SubjectID: subjectID, Action: action}
	access, err := e.repository.InstanceBatchAccess(ctx, batchID)
	if err != nil {
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			if recordErr := e.recordDecisionIfConfigured(ctx, decisionRequest, application.RecordScope{}, ErrDenied); recordErr != nil {
				return application.InstanceBatchAccessRecord{}, recordErr
			}
			return application.InstanceBatchAccessRecord{}, ErrDenied
		}
		return application.InstanceBatchAccessRecord{}, err
	}
	decisionRequest.ScopeKind, decisionRequest.ScopeID, decisionRequest.TargetID = application.ScopeProject, string(access.ProjectID), access.TargetID
	if action != application.ActionInstanceBatchCreate || len(access.Owners) == 0 {
		if recordErr := e.recordDecisionIfConfigured(ctx, decisionRequest, application.RecordScope{}, ErrDenied); recordErr != nil {
			return application.InstanceBatchAccessRecord{}, recordErr
		}
		return application.InstanceBatchAccessRecord{}, ErrDenied
	}
	var decisionScope application.RecordScope
	for _, owner := range access.Owners {
		request := application.AuthorizationRequest{
			SubjectID: subjectID, Action: action, ScopeKind: application.ScopeProject,
			ScopeID: string(access.ProjectID), TargetID: access.TargetID,
			OwnerKind: owner.OwnerKind, OwnerID: owner.OwnerID,
		}
		scope, err := e.evaluate(ctx, request)
		decisionScope = scope
		if err != nil {
			if recordErr := e.recordDecisionIfConfigured(ctx, decisionRequest, decisionScope, err); recordErr != nil {
				return application.InstanceBatchAccessRecord{}, recordErr
			}
			return application.InstanceBatchAccessRecord{}, err
		}
	}
	if err := e.recordDecisionIfConfigured(ctx, decisionRequest, decisionScope, nil); err != nil {
		return application.InstanceBatchAccessRecord{}, err
	}
	return access, nil
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
		_, err := e.evaluate(ctx, request)
		if err == nil {
			result = append(result, access.InstanceID)
			continue
		}
		if !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	if err := e.recordDecisionIfConfigured(ctx, application.AuthorizationRequest{
		SubjectID: subjectID, Action: action, ScopeKind: application.ScopePlatform, ScopeID: "platform",
	}, application.RecordScope{}, nil); err != nil {
		return nil, err
	}
	return result, nil
}

func (e *Evaluator) recordDecisionIfConfigured(ctx context.Context, request application.AuthorizationRequest, scope application.RecordScope, decisionErr error) error {
	if e.recorder == nil {
		return nil
	}
	if err := e.recordDecision(ctx, request, scope, decisionErr); err != nil {
		return fmt.Errorf("record authorization decision: %w", err)
	}
	return nil
}

func instanceAccessFilter(snapshot application.AuthorizationSnapshot, action application.PermissionAction) application.InstanceAccessFilter {
	filter := application.InstanceAccessFilter{OwnerUserID: snapshot.User.ID, ProjectTeamOwners: snapshot.ProjectTeams}
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
			application.ActionTemplateCreate, application.ActionTemplateRead, application.ActionTemplateUpdate,
			application.ActionTemplateRevisionPublish, application.ActionQuotaManage, application.ActionAuditRead:
			return true
		}
	case application.RoleProviderAdministrator:
		return action == application.ActionProviderTargetHealthRead || action == application.ActionProviderTargetGrant
	case application.RoleLabAuthor:
		switch action {
		case application.ActionTemplateCreate, application.ActionTemplateRead, application.ActionTemplateUpdate,
			application.ActionTemplateRevisionPublish, application.ActionInstanceValidate, application.ActionInstancePlan:
			return true
		}
	case application.RoleInstructor:
		switch action {
		case application.ActionTemplateRead, application.ActionInstanceValidate, application.ActionInstancePlan,
			application.ActionInstanceCreate, application.ActionInstanceBatchCreate, application.ActionInstanceRead,
			application.ActionInstanceTTLExtend, application.ActionInstanceDestroy, application.ActionAccessSessionCreate,
			application.ActionAccessSessionRevoke, application.ActionProviderTargetHealthRead:
			return true
		}
	case application.RoleParticipant:
		switch action {
		case application.ActionTemplateRead, application.ActionInstanceValidate, application.ActionInstancePlan,
			application.ActionInstanceCreate, application.ActionInstanceRead, application.ActionInstanceTTLExtend,
			application.ActionInstanceDestroy, application.ActionAccessSessionCreate, application.ActionAccessSessionRevoke,
			application.ActionProviderTargetHealthRead:
			return true
		}
	case application.RoleViewer:
		return action == application.ActionTemplateRead || action == application.ActionInstanceRead ||
			action == application.ActionProviderTargetHealthRead || action == application.ActionAuditRead
	}
	return false
}

func knownAction(action application.PermissionAction) bool {
	switch action {
	case application.ActionProviderTargetHealthRead, application.ActionProviderTargetGrant,
		application.ActionTemplateCreate, application.ActionTemplateRead, application.ActionTemplateUpdate,
		application.ActionTemplateRevisionPublish,
		application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
		application.ActionInstanceBatchCreate, application.ActionInstanceRead, application.ActionInstanceTTLExtend,
		application.ActionInstanceDestroy, application.ActionAccessSessionCreate, application.ActionAccessSessionRevoke,
		application.ActionQuotaManage, application.ActionAuditRead, application.ActionMembershipManage,
		application.ActionRoleAssignmentManage, application.ActionOrganizationManage, application.ActionProjectManage,
		application.ActionTeamManage:
		return true
	default:
		return false
	}
}

func assignmentCovers(assignment application.RoleAssignmentRecord, hierarchy application.ScopeHierarchy, request application.AuthorizationRequest, snapshot application.AuthorizationSnapshot) bool {
	if assignment.SubjectKind == application.SubjectUser && assignment.ScopeKind != application.ScopePlatform && !isOrganizationMember(hierarchy.OrganizationID, snapshot) {
		return false
	}
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

func isOrganizationMember(organizationID domain.OrganizationID, snapshot application.AuthorizationSnapshot) bool {
	for _, membership := range snapshot.Memberships {
		if membership.OrganizationID == organizationID {
			return true
		}
	}
	return false
}

func requiresOwnerRelationship(role application.Role, action application.PermissionAction) bool {
	if role != application.RoleParticipant {
		return false
	}
	switch action {
	case application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
		application.ActionInstanceBatchCreate, application.ActionInstanceRead, application.ActionInstanceTTLExtend,
		application.ActionInstanceDestroy, application.ActionAccessSessionCreate, application.ActionAccessSessionRevoke:
		return true
	default:
		return false
	}
}

func ownsResource(request application.AuthorizationRequest, hierarchy application.ScopeHierarchy, snapshot application.AuthorizationSnapshot) bool {
	switch request.OwnerKind {
	case application.OwnerUser:
		return request.OwnerID == string(request.SubjectID)
	case application.OwnerTeam:
		return isOwnerTeamMember(request.OwnerID, hierarchy.ProjectID, snapshot)
	case application.OwnerProject:
		return false
	default:
		return false
	}
}

// Project enrollment grants ownership only, never a role-assignment subject.
func isOwnerTeamMember(teamID string, projectID domain.ProjectID, snapshot application.AuthorizationSnapshot) bool {
	if isTeamMember(teamID, snapshot) {
		return true
	}
	for _, r := range snapshot.ProjectTeams {
		if r.ProjectID == projectID && string(r.TeamID) == teamID {
			return true
		}
	}
	return false
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
	return action == application.ActionInstanceValidate || action == application.ActionInstancePlan ||
		action == application.ActionInstanceCreate || action == application.ActionInstanceBatchCreate ||
		action == application.ActionInstanceDestroy || action == application.ActionInstanceTTLExtend ||
		action == application.ActionAccessSessionCreate || action == application.ActionAccessSessionRevoke
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
			if grant.ScopeID == request.OwnerID && request.OwnerKind == application.OwnerTeam && isOwnerTeamMember(grant.ScopeID, hierarchy.ProjectID, snapshot) {
				return true
			}
		}
	}
	return false
}

var _ application.Authorizer = (*Evaluator)(nil)
