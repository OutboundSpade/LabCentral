package localdb

import (
	"context"
	"errors"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestSQLiteProjectGroups(t *testing.T) { projectGroupContract(t, newSQLiteAccessContractStore(t)) }
func TestPostgreSQLProjectGroups(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "project_groups")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	projectGroupContract(t, s)
}
func projectGroupContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	const user domain.UserID = "user.member"
	if err := s.PutUser(ctx, app.UserRecord{ID: user, Username: "member", DisplayName: "Member"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMembership(ctx, app.MembershipRecord{ID: "membership.member", UserID: user, OrganizationID: "org.quota", ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRoleAssignment(ctx, app.RoleAssignmentRecord{ID: "role.member", SubjectKind: app.SubjectUser, SubjectID: string(user), ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Role: app.RoleParticipant, ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	makeGroups := func(project domain.ProjectID, suffix string) app.CreateProjectGroups {
		return app.CreateProjectGroups{ProjectID: project, ActorID: "user.quota", Groups: []app.ProjectGroupSeed{{ID: domain.TeamID("team.one-" + suffix), Name: "Group one"}, {ID: domain.TeamID("team.two-" + suffix), Name: "Group two"}}, EnrollmentOpen: true, IdempotencyKey: "create-groups-" + suffix, RequestDigest: testRequestDigest(suffix)}
	}
	request := makeGroups("project.alpha", "alpha")
	groups, err := s.CreateProjectGroups(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateProjectGroups(ctx, request)
	if err != nil || len(replay) != 2 || replay[0].ID != groups[0].ID {
		t.Fatal("create replay", err)
	}
	if _, err = s.CreateProjectGroups(ctx, makeGroups("project.beta", "beta")); err != nil {
		t.Fatal("same display name in another project", err)
	}
	request.ActorID = user
	request.IdempotencyKey = "member-admin-denied"
	if _, err = s.CreateProjectGroups(ctx, request); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("member created groups", err)
	}
	if _, err = s.ListProjectGroups(ctx, "project.beta", user, "", 100); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("cross-project list", err)
	}
	member := func(group domain.TeamID, join bool, key string) (app.ProjectGroup, error) {
		return s.ChangeProjectGroupMembership(ctx, app.ChangeProjectGroupMembership{ProjectID: "project.alpha", GroupID: group, ActorID: user, Join: join, IdempotencyKey: key, RequestDigest: testRequestDigest(key)})
	}
	// Two distinct group rows must still admit exactly one enrollment for this user/project.
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range groups {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = member(groups[i].ID, true, fmt.Sprint("concurrent-join-", i))
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, e := range results {
		if e == nil {
			if winner != -1 {
				t.Fatal("multiple memberships")
			}
			winner = i
		} else if !errors.Is(e, app.ErrGroupMembershipConflict) {
			t.Fatal("concurrent join", e)
		}
	}
	if winner < 0 {
		t.Fatal("no membership")
	}
	group := groups[winner]
	page, err := s.ListProjectGroups(ctx, "project.alpha", user, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal("pagination", err)
	}
	closed, err := s.ChangeProjectGroupEnrollment(ctx, app.ChangeProjectGroupEnrollment{ProjectID: "project.alpha", GroupID: group.ID, ActorID: "user.quota", ExpectedVersion: 1, Open: false, IdempotencyKey: "close-enrollment", RequestDigest: testRequestDigest("close")})
	if err != nil || closed.EnrollmentOpen {
		t.Fatal(err)
	}
	if _, err = member(group.ID, true, "join-existing-closed"); err != nil {
		t.Fatal("existing closed join", err)
	}
	// Enrollment is never an organizational membership or an RBAC team subject.
	if err = s.PutRoleAssignment(ctx, app.RoleAssignmentRecord{ID: "role.escalation", SubjectKind: app.SubjectTeam, SubjectID: string(group.ID), ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Role: app.RoleInstructor, ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.AuthorizationSnapshot(ctx, user, time.Now())
	if err != nil || len(snapshot.ProjectTeams) != 1 || len(snapshot.Memberships) != 1 || len(snapshot.Assignments) != 1 {
		t.Fatalf("enrollment escalated role/membership: %+v %v", snapshot, err)
	}
	if err = s.PutMembership(ctx, app.MembershipRecord{ID: "membership.escalation", UserID: user, OrganizationID: "org.quota", TeamID: group.ID, ValidFrom: time.Now().Add(-time.Hour)}); err == nil {
		t.Fatal("legacy membership accepted project group")
	}
	evaluator, err := authorization.New(s)
	if err != nil {
		t.Fatal(err)
	}
	check := app.AuthorizationRequest{SubjectID: user, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: "project.alpha", OwnerKind: app.OwnerTeam, OwnerID: string(group.ID), TargetID: "target.quota", Now: time.Now()}
	if err = evaluator.Authorize(ctx, check); err != nil {
		t.Fatal("group owner denied", err)
	}
	check.Action = app.ActionTeamManage
	if err = evaluator.Authorize(ctx, check); err == nil {
		t.Fatal("group role escalated")
	}
	check.Action = app.ActionInstanceCreate
	check.ScopeID = "project.beta"
	check.SubjectID = "user.quota"
	if err = evaluator.Authorize(ctx, check); err == nil {
		t.Fatal("admin cross-project owner allowed")
	}
	// Existing lab becomes reachable on join and survives last-member departure.
	instance, digest := createReadyAccessTestInstance(t, s, "instance.group")
	if _, err = s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET owner_kind='team',owner_id=? WHERE instance_id=?`), group.ID, instance.ID); err != nil {
		t.Fatal(err)
	}
	instance.OwnerKind = app.OwnerTeam
	instance.OwnerID = string(group.ID)
	if _, err = evaluator.AuthorizeInstance(ctx, user, app.ActionInstanceRead, instance.ID); err != nil {
		t.Fatal(err)
	}
	visible, err := evaluator.AuthorizedInstanceIDs(ctx, user, app.ActionInstanceRead)
	if err != nil || len(visible) != 1 {
		t.Fatal("group lab visibility", err, visible)
	}
	access := accessBeginTestRequest(instance, digest, "access.group", "group")
	access.SubjectID = user
	pending, err := s.BeginAccessSession(ctx, access)
	if err != nil {
		t.Fatal(err)
	}
	ready := finalizeAccessTestSession(t, s, pending, digest, accessTestHash("group-launch"), "group")
	// Multiple launches use the same team owner, retain the initiating actor,
	// and replay without consuming quota twice.
	for i := 0; i < 2; i++ {
		launch := quotaTestCreateRequest(fmt.Sprintf("instance.group-launch-%d", i), "project.alpha", 3600)
		launch.OwnerKind = app.OwnerTeam
		launch.OwnerID = string(group.ID)
		launch.SubjectID = user
		launch.CreatedBy = user
		admitted, e := s.CreateInstance(ctx, launch)
		if e != nil {
			t.Fatal("team launch", e)
		}
		replay, e := s.CreateInstance(ctx, launch)
		if e != nil || !replay.Replay || replay.InstanceID != admitted.InstanceID {
			t.Fatal("team admission replay", e)
		}
		lab, e := s.GetInstance(ctx, admitted.InstanceID)
		if e != nil || lab.OwnerID != string(group.ID) || lab.CreatedBy != user {
			t.Fatal("team owner/actor", e)
		}
	}
	// Published group preparation carries the team owner and is rechecked before admission.
	if _, err = s.CreateLabTemplate(ctx, app.CreateLabTemplateRequest{GovernanceRecordIDs: governanceIDs("group-template"), ID: "template.group", ProjectID: "project.alpha", Name: "Group", CreatedBy: "user.quota", IdempotencyKey: "group-template", RequestDigest: testRequestDigest("template")}); err != nil {
		t.Fatal(err)
	}
	rev := testRevisionRequest("revision.group", "template.group", "bundle-group", "canonical-group")
	rev.PublishedBy = "user.quota"
	if _, err = s.PublishTemplateRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	prep := app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs("group-prep"), IdempotencyKey: "group-preparation", RequestDigest: testRequestDigest("prep"), Work: app.ExtensionWork{ID: "prep.group", ProjectID: "project.alpha", ActorID: user, InvocationID: "inv.group", Request: app.ExtensionWorkRequest{Version: 2, Kind: domain.WorkPreparation, PluginInstance: "core-preparation", BindingID: "default", TemplateID: rev.TemplateID, Preparation: &app.PreparationWork{Version: 2, RevisionID: rev.ID, TargetID: "target.quota", Owner: &app.PreparationOwner{Kind: app.OwnerTeam, ID: string(group.ID)}}}}}
	if _, err = s.CreateExtensionWork(ctx, prep); err != nil {
		t.Fatal("team preparation", err)
	}
	if _, err = s.AuthorizePreparationWork(ctx, prep.Work.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimExtensionWork(ctx, "group-worker", "core-preparation/default", domain.WorkPreparation, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = member(group.ID, false, "leave-current-group"); err != nil {
		t.Fatal(err)
	}
	if _, err = member(group.ID, false, "leave-again-group"); err != nil {
		t.Fatal("repeated leave", err)
	}
	if _, err = evaluator.AuthorizeInstance(ctx, user, app.ActionInstanceRead, instance.ID); err == nil {
		t.Fatal("former member retains access")
	}
	if _, err = s.AuthorizePreparationWork(ctx, prep.Work.ID); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("former member preparation survived", err)
	}
	prepared := quotaTestCreateRequest("instance.group-prepared-after-leave", "project.alpha", 3600)
	prepared.CreatedBy = user
	prepared.SubjectID = user
	prepared.OwnerKind = app.OwnerTeam
	prepared.OwnerID = string(group.ID)
	prepared.RevisionID = rev.ID
	prepared.WorkFence = &app.Fence{OperationID: claim.Work.ID, LeaseOwner: claim.LeaseOwner, Token: claim.Operation.FencingToken, Attempt: claim.Operation.Attempt, Generation: 1}
	if _, err = s.CreateInstance(ctx, prepared); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("pending preparation admitted after departure", err)
	}
	var state string
	var closure int
	if err = s.db.QueryRowContext(ctx, s.bind(`SELECT state,closure_required FROM labcentral_access_sessions WHERE access_session_id=?`), ready.ID).Scan(&state, &closure); err != nil || state != "revoked" || closure != 1 {
		t.Fatal("departure closure", state, closure, err)
	}
	got, err := s.GetInstance(ctx, instance.ID)
	if err != nil || got.OwnerID != string(group.ID) || got.Phase != domain.InstanceReady {
		t.Fatal("last departure changed lab", err)
	}
	if _, err = member(group.ID, true, "join-closed-group"); !errors.Is(err, app.ErrGroupEnrollmentClosed) {
		t.Fatal("closed enrollment", err)
	}
	// Replaying the original join must not resurrect a membership after departure.
	if _, err = member(group.ID, true, fmt.Sprint("concurrent-join-", winner)); err != nil {
		t.Fatal("join replay", err)
	}
	snapshot, err = s.AuthorizationSnapshot(ctx, user, time.Now())
	if err != nil || len(snapshot.ProjectTeams) != 0 {
		t.Fatal("replay resurrected membership", err)
	}
	other := groups[1-winner]
	if _, err = member(other.ID, true, "join-other-group"); err != nil {
		t.Fatal(err)
	}
	if _, err = member(group.ID, false, "leave-old-group"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.AuthorizationSnapshot(ctx, user, time.Now())
	if err != nil || len(snapshot.ProjectTeams) != 1 || snapshot.ProjectTeams[0].TeamID != other.ID {
		t.Fatal("old leave removed new enrollment", err)
	}
	// Admission rechecks membership even if a stale client already has a plan.
	stale := quotaTestCreateRequest("instance.stale-group", "project.alpha", 3600)
	stale.OwnerKind = app.OwnerTeam
	stale.OwnerID = string(group.ID)
	stale.SubjectID = user
	stale.CreatedBy = user
	if _, err = s.CreateInstance(ctx, stale); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("stale group admission", err)
	}
}
