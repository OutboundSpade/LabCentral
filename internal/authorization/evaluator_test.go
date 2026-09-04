package authorization

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var implementedActions = []application.PermissionAction{
	application.ActionProviderTargetHealthRead,
	application.ActionProviderTargetGrant,
	application.ActionTemplateCreate,
	application.ActionTemplateRead,
	application.ActionTemplateUpdate,
	application.ActionTemplateRevisionPublish,
	application.ActionInstanceValidate,
	application.ActionInstancePlan,
	application.ActionInstanceCreate,
	application.ActionInstanceBatchCreate,
	application.ActionInstanceRead,
	application.ActionInstanceTTLExtend,
	application.ActionInstanceDestroy,
	application.ActionAccessSessionCreate,
	application.ActionAccessSessionRevoke,
	application.ActionQuotaManage,
	application.ActionAuditRead,
	application.ActionMembershipManage,
	application.ActionRoleAssignmentManage,
	application.ActionOrganizationManage,
	application.ActionProjectManage,
	application.ActionTeamManage,
}

type roleFixture struct {
	role      application.Role
	subjectID domain.UserID
	scopeKind application.ScopeKind
	scopeID   string
	allowed   map[application.PermissionAction]bool
}

func TestRoleMaximumPermissionMatrix(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	roles := []roleFixture{
		{
			role: application.RolePlatformAdministrator, subjectID: "user.platform-admin",
			scopeKind: application.ScopePlatform, scopeID: "platform", allowed: actionSet(implementedActions...),
		},
		{
			role: application.RoleOrganizationAdministrator, subjectID: "user.organization-admin",
			scopeKind: application.ScopeOrganization, scopeID: "org.alpha", allowed: actionSet(
				application.ActionOrganizationManage, application.ActionProjectManage, application.ActionTeamManage,
				application.ActionMembershipManage, application.ActionRoleAssignmentManage,
				application.ActionTemplateCreate, application.ActionTemplateRead, application.ActionTemplateUpdate,
				application.ActionTemplateRevisionPublish, application.ActionQuotaManage, application.ActionAuditRead,
			),
		},
		{
			role: application.RoleProviderAdministrator, subjectID: "user.provider-admin",
			scopeKind: application.ScopePlatform, scopeID: "platform", allowed: actionSet(
				application.ActionProviderTargetHealthRead, application.ActionProviderTargetGrant,
			),
		},
		{
			role: application.RoleLabAuthor, subjectID: "user.lab-author",
			scopeKind: application.ScopeProject, scopeID: "project.alpha", allowed: actionSet(
				application.ActionTemplateCreate, application.ActionTemplateRead, application.ActionTemplateUpdate,
				application.ActionTemplateRevisionPublish, application.ActionInstanceValidate, application.ActionInstancePlan,
			),
		},
		{
			role: application.RoleInstructor, subjectID: "user.instructor",
			scopeKind: application.ScopeProject, scopeID: "project.alpha", allowed: actionSet(
				application.ActionTemplateRead, application.ActionInstanceValidate, application.ActionInstancePlan,
				application.ActionInstanceCreate, application.ActionInstanceBatchCreate, application.ActionInstanceRead,
				application.ActionInstanceTTLExtend, application.ActionInstanceDestroy, application.ActionAccessSessionCreate,
				application.ActionAccessSessionRevoke, application.ActionProviderTargetHealthRead,
			),
		},
		{
			role: application.RoleParticipant, subjectID: "user.participant",
			scopeKind: application.ScopeProject, scopeID: "project.alpha", allowed: actionSet(
				application.ActionTemplateRead, application.ActionInstanceValidate, application.ActionInstancePlan,
				application.ActionInstanceCreate, application.ActionInstanceRead, application.ActionInstanceTTLExtend,
				application.ActionInstanceDestroy, application.ActionAccessSessionCreate, application.ActionAccessSessionRevoke,
				application.ActionProviderTargetHealthRead,
			),
		},
		{
			role: application.RoleViewer, subjectID: "user.viewer",
			scopeKind: application.ScopeProject, scopeID: "project.alpha", allowed: actionSet(
				application.ActionTemplateRead, application.ActionInstanceRead, application.ActionProviderTargetHealthRead,
				application.ActionAuditRead,
			),
		},
	}

	for _, role := range roles {
		role := role
		fixture.addUserRole(role)
		if assignments := fixture.repository.snapshots[role.subjectID].Assignments; len(assignments) != 1 {
			t.Fatalf("%s fixture has %d assignments, want exactly one", role.role, len(assignments))
		}
		t.Run(string(role.role), func(t *testing.T) {
			for _, action := range implementedActions {
				action := action
				t.Run(string(action), func(t *testing.T) {
					err := fixture.evaluator.Authorize(context.Background(), fixture.request(role.subjectID, action))
					assertDecision(t, err, role.allowed[action])
				})
			}
		})
	}

	err := fixture.evaluator.Authorize(context.Background(), application.AuthorizationRequest{
		SubjectID: "user.platform-admin", Action: "future.permission", ScopeKind: application.ScopePlatform,
		ScopeID: "platform", Now: fixture.now,
	})
	assertDecision(t, err, false)
}

func TestParticipantOwnershipIsRequiredForEveryInstanceAction(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	role := roleFixture{
		role: application.RoleParticipant, subjectID: "user.participant", scopeKind: application.ScopeProject,
		scopeID: "project.alpha",
	}
	fixture.addUserRole(role)

	instanceActions := []application.PermissionAction{
		application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
		application.ActionInstanceRead, application.ActionInstanceDestroy,
	}
	owners := []struct {
		name string
		kind application.OwnerKind
		id   string
		want bool
	}{
		{name: "own user", kind: application.OwnerUser, id: "user.participant", want: true},
		{name: "active team", kind: application.OwnerTeam, id: "team.alpha", want: true},
		{name: "another user", kind: application.OwnerUser, id: "user.other", want: false},
		{name: "another team", kind: application.OwnerTeam, id: "team.other", want: false},
		{name: "project owner", kind: application.OwnerProject, id: "project.alpha", want: false},
		{name: "missing owner", want: false},
	}
	for _, action := range instanceActions {
		for _, owner := range owners {
			t.Run(fmt.Sprintf("%s/%s", action, owner.name), func(t *testing.T) {
				request := fixture.request(role.subjectID, action)
				request.OwnerKind, request.OwnerID = owner.kind, owner.id
				assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), owner.want)
			})
		}
	}
}

func TestInstanceBatchReadReauthorizesEveryOwner(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	fixture.evaluator.now = func() time.Time { return fixture.now }
	fixture.addUserRole(roleFixture{
		role: application.RoleInstructor, subjectID: "user.instructor",
		scopeKind: application.ScopeProject, scopeID: "project.alpha",
	})
	fixture.addUserRole(roleFixture{
		role: application.RoleParticipant, subjectID: "user.participant",
		scopeKind: application.ScopeProject, scopeID: "project.alpha",
	})
	fixture.repository.batches = map[domain.InstanceBatchID]application.InstanceBatchAccessRecord{
		"batch.alpha": {
			BatchID: "batch.alpha", ProjectID: "project.alpha", TargetID: "target.alpha",
			Owners: []application.InstanceBatchOwnerAccess{
				{OwnerKind: application.OwnerUser, OwnerID: "user.other"},
				{OwnerKind: application.OwnerTeam, OwnerID: "team.alpha"},
			},
		},
	}
	if _, err := fixture.evaluator.AuthorizeInstanceBatch(context.Background(), "user.instructor", application.ActionInstanceBatchCreate, "batch.alpha"); err != nil {
		t.Fatalf("instructor batch authorization returned %v", err)
	}
	if _, err := fixture.evaluator.AuthorizeInstanceBatch(context.Background(), "user.participant", application.ActionInstanceBatchCreate, "batch.alpha"); !errors.Is(err, ErrDenied) {
		t.Fatalf("participant batch authorization returned %v", err)
	}
	batch := fixture.repository.batches["batch.alpha"]
	batch.Owners[1] = application.InstanceBatchOwnerAccess{OwnerKind: application.OwnerTeam, OwnerID: "team.other"}
	fixture.repository.batches["batch.alpha"] = batch
	if _, err := fixture.evaluator.AuthorizeInstanceBatch(context.Background(), "user.instructor", application.ActionInstanceBatchCreate, "batch.alpha"); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-organization batch owner authorization returned %v", err)
	}
}

func TestNonParticipantRolesDoNotInheritParticipantOwnership(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	for _, role := range []roleFixture{
		{role: application.RoleLabAuthor, subjectID: "user.lab-author", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
		{role: application.RoleInstructor, subjectID: "user.instructor", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
	} {
		fixture.addUserRole(role)
	}

	for _, action := range []application.PermissionAction{application.ActionInstanceValidate, application.ActionInstancePlan} {
		request := fixture.request("user.lab-author", action)
		request.OwnerKind, request.OwnerID = application.OwnerUser, "user.other"
		assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), true)
	}
	for _, action := range []application.PermissionAction{
		application.ActionInstanceValidate, application.ActionInstancePlan, application.ActionInstanceCreate,
		application.ActionInstanceRead, application.ActionInstanceDestroy,
	} {
		request := fixture.request("user.instructor", action)
		request.OwnerKind, request.OwnerID = application.OwnerProject, "project.alpha"
		assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), true)
	}
}

func TestScopeAncestryDoesNotCrossProjectOrOrganization(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	roles := []roleFixture{
		{role: application.RolePlatformAdministrator, subjectID: "user.platform-admin", scopeKind: application.ScopePlatform, scopeID: "platform"},
		{role: application.RoleOrganizationAdministrator, subjectID: "user.organization-admin", scopeKind: application.ScopeOrganization, scopeID: "org.alpha"},
		{role: application.RoleLabAuthor, subjectID: "user.lab-author", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
		{role: application.RoleInstructor, subjectID: "user.instructor", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
		{role: application.RoleParticipant, subjectID: "user.participant", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
		{role: application.RoleViewer, subjectID: "user.viewer", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
	}
	for _, role := range roles {
		fixture.addUserRole(role)
	}

	tests := []struct {
		name    string
		request application.AuthorizationRequest
		want    bool
	}{
		{name: "platform spans organizations", request: fixture.request("user.platform-admin", application.ActionInstanceRead), want: true},
		{name: "organization admin manages child project", request: fixture.request("user.organization-admin", application.ActionProjectManage), want: true},
		{name: "organization admin manages child team", request: fixture.request("user.organization-admin", application.ActionTeamManage), want: true},
		{name: "project lab author stays in project", request: fixture.request("user.lab-author", application.ActionInstancePlan), want: true},
		{name: "project instructor stays in project", request: fixture.request("user.instructor", application.ActionInstanceRead), want: true},
		{name: "project participant stays in project", request: fixture.request("user.participant", application.ActionInstanceRead), want: true},
		{name: "project viewer stays in project", request: fixture.request("user.viewer", application.ActionInstanceRead), want: true},
	}
	tests[0].request.ScopeID = "project.beta"
	tests[0].request.OwnerID = "user.beta"
	tests[1].request.ScopeID = "project.sibling"
	tests[2].request.ScopeKind, tests[2].request.ScopeID = application.ScopeTeam, "team.alpha"

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertDecision(t, fixture.evaluator.Authorize(context.Background(), tc.request), tc.want)
		})
	}

	for _, tc := range []struct {
		name      string
		subjectID domain.UserID
		action    application.PermissionAction
	}{
		{name: "organization admin other organization", subjectID: "user.organization-admin", action: application.ActionOrganizationManage},
		{name: "lab author sibling project", subjectID: "user.lab-author", action: application.ActionInstancePlan},
		{name: "instructor sibling project", subjectID: "user.instructor", action: application.ActionInstanceRead},
		{name: "participant sibling project", subjectID: "user.participant", action: application.ActionInstanceRead},
		{name: "viewer sibling project", subjectID: "user.viewer", action: application.ActionInstanceRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := fixture.request(tc.subjectID, tc.action)
			if tc.action == application.ActionOrganizationManage {
				request.ScopeKind, request.ScopeID = application.ScopeOrganization, "org.beta"
			} else {
				request.ScopeID = "project.sibling"
			}
			assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), false)
		})
	}
}

func TestTargetGrantScopeAndExpiryAreEnforced(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	for _, role := range []roleFixture{
		{role: application.RolePlatformAdministrator, subjectID: "user.platform-admin", scopeKind: application.ScopePlatform, scopeID: "platform"},
		{role: application.RoleProviderAdministrator, subjectID: "user.provider-admin", scopeKind: application.ScopePlatform, scopeID: "platform"},
		{role: application.RoleInstructor, subjectID: "user.instructor", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
		{role: application.RoleViewer, subjectID: "user.viewer", scopeKind: application.ScopeProject, scopeID: "project.alpha"},
	} {
		fixture.addUserRole(role)
	}

	withoutGrant := fixture.request("user.instructor", application.ActionInstanceCreate)
	fixture.setGrants("user.instructor", nil)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), withoutGrant), false)

	wrongTarget := withoutGrant
	wrongTarget.TargetID = "target.other"
	fixture.setGrants("user.instructor", []application.TargetGrantRecord{fixture.projectGrant()})
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), wrongTarget), false)

	crossProject := withoutGrant
	crossProject.ScopeID = "project.sibling"
	crossProject.OwnerKind, crossProject.OwnerID = application.OwnerProject, "project.sibling"
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), crossProject), false)

	organizationGrant := fixture.projectGrant()
	organizationGrant.ScopeKind, organizationGrant.ScopeID = application.ScopeOrganization, "org.alpha"
	fixture.setGrants("user.instructor", []application.TargetGrantRecord{organizationGrant})
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), withoutGrant), true)

	expiredGrant := fixture.projectGrant()
	expiredGrant.ValidUntil = fixture.now
	fixture.setGrants("user.instructor", []application.TargetGrantRecord{expiredGrant})
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), withoutGrant), false)

	viewerHealth := fixture.request("user.viewer", application.ActionProviderTargetHealthRead)
	fixture.setGrants("user.viewer", nil)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), viewerHealth), false)
	fixture.setGrants("user.viewer", []application.TargetGrantRecord{fixture.projectGrant()})
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), viewerHealth), true)

	providerHealth := fixture.request("user.provider-admin", application.ActionProviderTargetHealthRead)
	fixture.setGrants("user.provider-admin", nil)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), providerHealth), true)

	platformHealth := fixture.request("user.platform-admin", application.ActionProviderTargetHealthRead)
	fixture.setGrants("user.platform-admin", nil)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), platformHealth), true)
	platformCreate := fixture.request("user.platform-admin", application.ActionInstanceCreate)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), platformCreate), false)
	fixture.setGrants("user.platform-admin", []application.TargetGrantRecord{fixture.projectGrant()})
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), platformCreate), true)
}

func TestTeamTargetGrantRequiresCurrentTeamMembership(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	role := roleFixture{
		role: application.RoleParticipant, subjectID: "user.participant", scopeKind: application.ScopeProject, scopeID: "project.alpha",
	}
	fixture.addUserRole(role)
	teamGrant := fixture.projectGrant()
	teamGrant.ScopeKind, teamGrant.ScopeID = application.ScopeTeam, "team.alpha"
	fixture.setGrants(role.subjectID, []application.TargetGrantRecord{teamGrant})
	request := fixture.request(role.subjectID, application.ActionInstanceCreate)
	request.OwnerKind, request.OwnerID = application.OwnerTeam, "team.alpha"
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), true)

	snapshot := fixture.repository.snapshots[role.subjectID]
	snapshot.Memberships = snapshot.Memberships[:1]
	fixture.repository.snapshots[role.subjectID] = snapshot
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), false)
}

func TestMembershipExpiryImmediatelyRemovesOrganizationScopedAuthority(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	tests := []struct {
		role   roleFixture
		action application.PermissionAction
	}{
		{role: roleFixture{role: application.RoleOrganizationAdministrator, subjectID: "user.organization-admin", scopeKind: application.ScopeOrganization, scopeID: "org.alpha"}, action: application.ActionOrganizationManage},
		{role: roleFixture{role: application.RoleLabAuthor, subjectID: "user.lab-author", scopeKind: application.ScopeProject, scopeID: "project.alpha"}, action: application.ActionInstancePlan},
		{role: roleFixture{role: application.RoleInstructor, subjectID: "user.instructor", scopeKind: application.ScopeProject, scopeID: "project.alpha"}, action: application.ActionInstanceRead},
		{role: roleFixture{role: application.RoleParticipant, subjectID: "user.participant", scopeKind: application.ScopeProject, scopeID: "project.alpha"}, action: application.ActionInstanceRead},
		{role: roleFixture{role: application.RoleViewer, subjectID: "user.viewer", scopeKind: application.ScopeProject, scopeID: "project.alpha"}, action: application.ActionInstanceRead},
	}
	for _, tc := range tests {
		fixture.addUserRole(tc.role)
		request := fixture.request(tc.role.subjectID, tc.action)
		assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), true)
		request.Now = fixture.now.Add(2 * time.Hour)
		t.Run(string(tc.role.role), func(t *testing.T) {
			assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), false)
		})
	}
}

func TestTeamRoleAssignmentExpiresWithMembership(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	userID := domain.UserID("user.team-member")
	snapshot := application.AuthorizationSnapshot{
		User: application.UserRecord{ID: userID},
		Memberships: []application.MembershipRecord{
			{ID: "membership.org", UserID: userID, OrganizationID: "org.alpha", ValidFrom: fixture.now.Add(-time.Hour)},
			{ID: "membership.team", UserID: userID, OrganizationID: "org.alpha", TeamID: "team.alpha", ValidFrom: fixture.now.Add(-time.Hour), ValidUntil: fixture.now.Add(time.Hour)},
		},
		Assignments: []application.RoleAssignmentRecord{{
			ID: "role.team-participant", SubjectKind: application.SubjectTeam, SubjectID: "team.alpha",
			ScopeKind: application.ScopeProject, ScopeID: "project.alpha", Role: application.RoleParticipant,
			ValidFrom: fixture.now.Add(-time.Hour),
		}},
		Grants: []application.TargetGrantRecord{fixture.projectGrant()},
	}
	fixture.repository.snapshots[userID] = snapshot
	request := fixture.request(userID, application.ActionInstanceRead)
	request.OwnerKind, request.OwnerID = application.OwnerTeam, "team.alpha"
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), true)
	request.Now = fixture.now.Add(2 * time.Hour)
	assertDecision(t, fixture.evaluator.Authorize(context.Background(), request), false)
}

type authorizationFixture struct {
	now        time.Time
	repository *matrixRepository
	evaluator  *Evaluator
}

func newAuthorizationFixture(t *testing.T) *authorizationFixture {
	t.Helper()
	repository := &matrixRepository{snapshots: make(map[domain.UserID]application.AuthorizationSnapshot)}
	evaluator, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	return &authorizationFixture{
		now: time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC), repository: repository, evaluator: evaluator,
	}
}

func (f *authorizationFixture) addUserRole(role roleFixture) {
	memberships := []application.MembershipRecord(nil)
	if role.scopeKind != application.ScopePlatform {
		memberships = []application.MembershipRecord{
			{ID: "membership.org", UserID: role.subjectID, OrganizationID: "org.alpha", ValidFrom: f.now.Add(-time.Hour), ValidUntil: f.now.Add(time.Hour)},
			{ID: "membership.team", UserID: role.subjectID, OrganizationID: "org.alpha", TeamID: "team.alpha", ValidFrom: f.now.Add(-time.Hour), ValidUntil: f.now.Add(time.Hour)},
		}
	}
	f.repository.snapshots[role.subjectID] = application.AuthorizationSnapshot{
		User:        application.UserRecord{ID: role.subjectID},
		Memberships: memberships,
		Assignments: []application.RoleAssignmentRecord{{
			ID: "role." + string(role.role), SubjectKind: application.SubjectUser, SubjectID: string(role.subjectID),
			ScopeKind: role.scopeKind, ScopeID: role.scopeID, Role: role.role, ValidFrom: f.now.Add(-time.Hour),
		}},
		Grants: []application.TargetGrantRecord{f.projectGrant()},
	}
}

func (f *authorizationFixture) request(subjectID domain.UserID, action application.PermissionAction) application.AuthorizationRequest {
	request := application.AuthorizationRequest{
		SubjectID: subjectID, Action: action, ScopeKind: application.ScopeProject, ScopeID: "project.alpha",
		TargetID: "target.alpha", OwnerKind: application.OwnerUser, OwnerID: string(subjectID), Now: f.now,
	}
	switch action {
	case application.ActionProviderTargetGrant:
		request.ScopeKind, request.ScopeID = application.ScopePlatform, "platform"
		request.OwnerKind, request.OwnerID = "", ""
	case application.ActionOrganizationManage, application.ActionMembershipManage, application.ActionRoleAssignmentManage:
		request.ScopeKind, request.ScopeID = application.ScopeOrganization, "org.alpha"
		request.OwnerKind, request.OwnerID = "", ""
	case application.ActionProjectManage:
		request.OwnerKind, request.OwnerID = "", ""
	case application.ActionTeamManage:
		request.ScopeKind, request.ScopeID = application.ScopeTeam, "team.alpha"
		request.OwnerKind, request.OwnerID = "", ""
	case application.ActionProviderTargetHealthRead:
		request.OwnerKind, request.OwnerID = "", ""
	}
	return request
}

func (f *authorizationFixture) projectGrant() application.TargetGrantRecord {
	return application.TargetGrantRecord{
		ID: "grant.alpha", TargetID: "target.alpha", ScopeKind: application.ScopeProject, ScopeID: "project.alpha",
		ValidFrom: f.now.Add(-time.Hour), ValidUntil: f.now.Add(time.Hour),
	}
}

func (f *authorizationFixture) setGrants(subjectID domain.UserID, grants []application.TargetGrantRecord) {
	snapshot := f.repository.snapshots[subjectID]
	snapshot.Grants = grants
	f.repository.snapshots[subjectID] = snapshot
}

func actionSet(actions ...application.PermissionAction) map[application.PermissionAction]bool {
	result := make(map[application.PermissionAction]bool, len(actions))
	for _, action := range actions {
		result[action] = true
	}
	return result
}

func assertDecision(t *testing.T, err error, want bool) {
	t.Helper()
	if want && err != nil {
		t.Fatalf("authorization denied: %v", err)
	}
	if !want && !errors.Is(err, ErrDenied) {
		t.Fatalf("authorization returned %v, want %v", err, ErrDenied)
	}
}

type matrixRepository struct {
	application.AuthRepository
	snapshots map[domain.UserID]application.AuthorizationSnapshot
	batches   map[domain.InstanceBatchID]application.InstanceBatchAccessRecord
}

func (r *matrixRepository) InstanceBatchAccess(_ context.Context, batchID domain.InstanceBatchID) (application.InstanceBatchAccessRecord, error) {
	record, ok := r.batches[batchID]
	if !ok {
		return application.InstanceBatchAccessRecord{}, application.ErrAuthRecordNotFound
	}
	return record, nil
}

func (r *matrixRepository) AuthorizationSnapshot(_ context.Context, userID domain.UserID, now time.Time) (application.AuthorizationSnapshot, error) {
	snapshot, ok := r.snapshots[userID]
	if !ok || snapshot.User.Disabled {
		return application.AuthorizationSnapshot{}, application.ErrAuthRecordNotFound
	}
	result := snapshot
	result.Memberships = nil
	for _, membership := range snapshot.Memberships {
		if activeAt(membership.ValidFrom, membership.ValidUntil, now) {
			result.Memberships = append(result.Memberships, membership)
		}
	}
	result.Assignments = nil
	for _, assignment := range snapshot.Assignments {
		if !activeAt(assignment.ValidFrom, assignment.ValidUntil, now) {
			continue
		}
		if assignment.SubjectKind == application.SubjectTeam && !snapshotHasTeam(result, assignment.SubjectID) {
			continue
		}
		result.Assignments = append(result.Assignments, assignment)
	}
	result.Grants = nil
	for _, grant := range snapshot.Grants {
		if activeAt(grant.ValidFrom, grant.ValidUntil, now) {
			result.Grants = append(result.Grants, grant)
		}
	}
	return result, nil
}

func (r *matrixRepository) ResolveScope(_ context.Context, kind application.ScopeKind, id string) (application.ScopeHierarchy, error) {
	switch kind {
	case application.ScopePlatform:
		if id == "platform" {
			return application.ScopeHierarchy{Kind: kind, ID: id}, nil
		}
	case application.ScopeOrganization:
		if id == "org.alpha" || id == "org.beta" {
			return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: domain.OrganizationID(id)}, nil
		}
	case application.ScopeProject:
		switch id {
		case "project.alpha", "project.sibling":
			return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: "org.alpha", ProjectID: domain.ProjectID(id)}, nil
		case "project.beta":
			return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: "org.beta", ProjectID: domain.ProjectID(id)}, nil
		}
	case application.ScopeTeam:
		switch id {
		case "team.alpha":
			return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: "org.alpha", TeamID: domain.TeamID(id)}, nil
		case "team.other":
			return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: "org.beta", TeamID: domain.TeamID(id)}, nil
		}
	}
	return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
}

func (r *matrixRepository) OwnerBelongsToScope(_ context.Context, kind application.OwnerKind, ownerID string, hierarchy application.ScopeHierarchy, _ time.Time) (bool, error) {
	switch kind {
	case application.OwnerUser:
		if ownerID == "user.beta" {
			return hierarchy.OrganizationID == "org.beta", nil
		}
		return hierarchy.OrganizationID == "org.alpha", nil
	case application.OwnerTeam:
		if ownerID == "team.other" {
			return hierarchy.OrganizationID == "org.beta", nil
		}
		return ownerID == "team.alpha" && hierarchy.OrganizationID == "org.alpha", nil
	case application.OwnerProject:
		return ownerID == string(hierarchy.ProjectID), nil
	default:
		return false, nil
	}
}

func activeAt(from, until, now time.Time) bool {
	return (from.IsZero() || !now.Before(from)) && (until.IsZero() || now.Before(until))
}

func snapshotHasTeam(snapshot application.AuthorizationSnapshot, teamID string) bool {
	for _, membership := range snapshot.Memberships {
		if string(membership.TeamID) == teamID {
			return true
		}
	}
	return false
}
