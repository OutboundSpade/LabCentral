package localdb

import (
	"context"
	"errors"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestSQLiteParticipantPreparationAuthority(t *testing.T) {
	participantPreparationContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLParticipantPreparationAuthority(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "participant_preparation")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	participantPreparationContract(t, s)
}
func participantPreparationContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	if _, err := s.CreateLabTemplate(ctx, app.CreateLabTemplateRequest{GovernanceRecordIDs: governanceIDs("participant-template"), ID: "template.participant", ProjectID: "project.alpha", Name: "Participant", CreatedBy: "user.quota", IdempotencyKey: "participant-template", RequestDigest: testRequestDigest("participant-template")}); err != nil {
		t.Fatal(err)
	}
	rev := testRevisionRequest("revision.participant", "template.participant", "participant-bundle", "participant-canonical")
	rev.PublishedBy = "user.quota"
	rev.IdempotencyKey = "participant-revision"
	rev.RequestDigest = testRequestDigest("participant-revision")
	if _, err := s.PublishTemplateRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	users := []domain.UserID{"user.participant-one", "user.participant-two"}
	for i, user := range users {
		name := fmt.Sprint(i)
		if err := s.PutUser(ctx, app.UserRecord{ID: user, Username: "participant-" + name, DisplayName: "Participant"}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutMembership(ctx, app.MembershipRecord{ID: "membership.participant-" + name, UserID: user, OrganizationID: "org.quota", ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRoleAssignment(ctx, app.RoleAssignmentRecord{ID: "role.participant-" + name, SubjectKind: app.SubjectUser, SubjectID: string(user), ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Role: app.RoleParticipant, ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	makeRequest := func(id string, actor domain.UserID, r app.ExtensionWorkRequest) app.CreateExtensionWork {
		return app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: "request-" + id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.alpha", ActorID: actor, InvocationID: domain.InvocationID("inv." + id), Request: r}}
	}
	prepRequest := func() app.ExtensionWorkRequest {
		return app.ExtensionWorkRequest{Version: 2, Kind: domain.WorkPreparation, PluginInstance: "core-preparation", BindingID: "default", TemplateID: rev.TemplateID, Preparation: &app.PreparationWork{Version: 2, RevisionID: rev.ID, TargetID: "target.quota"}}
	}
	preps := []app.ExtensionWork{}
	for i, user := range users {
		r := makeRequest(fmt.Sprintf("prep.participant-%d", i), user, prepRequest())
		p, err := s.CreateExtensionWork(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		preps = append(preps, p)
		if _, err = s.CreateExtensionWork(ctx, r); err != nil {
			t.Fatal("idempotent preparation", err)
		}
	}
	forbidden := prepRequest()
	forbidden.Preparation.Owner = &app.PreparationOwner{Kind: app.OwnerUser, ID: string(users[1])}
	if _, err := s.CreateExtensionWork(ctx, makeRequest("prep.foreign-owner", users[0], forbidden)); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("foreign owner", err)
	}
	forbidden = prepRequest()
	forbidden.Preparation.RevisionID = "revision.missing"
	if _, err := s.CreateExtensionWork(ctx, makeRequest("prep.missing-revision", users[0], forbidden)); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("missing published revision", err)
	}
	forbidden = prepRequest()
	forbidden.Version = 1
	forbidden.Preparation.Version = 1
	if _, err := s.CreateExtensionWork(ctx, makeRequest("prep.legacy-author", users[0], forbidden)); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("legacy author permissions weakened", err)
	}
	childRequest := func(parent app.ExtensionWork) app.ExtensionWorkRequest {
		return app.ExtensionWorkRequest{Version: 2, PreparationID: parent.ID, Kind: domain.WorkBuild, PluginInstance: "builder", BindingID: "default", TemplateID: rev.TemplateID, DedupKey: "participant-shared-base", Build: &app.BuildWork{RevisionID: rev.ID, ContextArtifact: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	}
	var wg sync.WaitGroup
	works := make([]app.ExtensionWork, 2)
	for i, p := range preps {
		wg.Add(1)
		go func(i int, p app.ExtensionWork) {
			defer wg.Done()
			w, err := s.CreateExtensionWork(ctx, makeRequest(fmt.Sprintf("build.participant-%d", i), p.ActorID, childRequest(p)))
			if err != nil {
				t.Error(err)
				return
			}
			works[i] = w
		}(i, p)
	}
	wg.Wait()
	if works[0].ID == "" || works[0].ID != works[1].ID {
		t.Fatal("shared base duplicated")
	}
	for _, user := range users {
		ok, err := s.CanReadPreparationWork(ctx, works[0].ID, user)
		if err != nil || !ok {
			t.Fatal("waiter status inaccessible", err)
		}
	}
	if ok, err := s.CanReadPreparationWork(ctx, preps[1].ID, users[0]); err != nil || ok {
		t.Fatal("foreign preparation exposed", err)
	}
	// Both preparation links must exist even when a simultaneous request deduplicates.
	var count int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_extension_dependencies WHERE child_id=?`), works[0].ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("atomic waiter links", count, err)
	}
	if _, err := s.CreateExtensionWork(ctx, makeRequest("build.spoof-parent", users[1], childRequest(preps[0]))); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("spoofed preparation", err)
	}
	// Claim before revocation, then check the admission transaction independently.
	claim, err := s.ClaimExtensionWork(ctx, "worker", "core-preparation/default", domain.WorkPreparation, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	original := claim.Work.ActorID
	if _, err = s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_role_assignments SET valid_until_us=`+s.nowMicros()+`-1 WHERE subject_id=?`), original); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthorizePreparationWork(ctx, claim.Work.ID); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("revoked preparation authorized", err)
	}
	actor, err := s.AuthorizePreparationWork(ctx, works[0].ID)
	if err != nil || actor == original {
		t.Fatal("shared build lost remaining authorized waiter", actor, err)
	}
	req := quotaTestCreateRequest("inst.revoked-preparation", "project.alpha", 3600)
	req.CreatedBy = original
	req.SubjectID = original
	req.OwnerID = string(original)
	req.RevisionID = rev.ID
	req.WorkFence = &app.Fence{OperationID: claim.Work.ID, LeaseOwner: claim.LeaseOwner, Token: claim.Operation.FencingToken, Attempt: claim.Operation.Attempt, Generation: 1}
	if _, err = s.CreateInstance(ctx, req); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("revoked preparation admitted", err)
	}
	if _, err = s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_users SET disabled=1 WHERE user_id=?`), actor); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthorizePreparationWork(ctx, works[0].ID); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("build authorized without active waiter", err)
	}
}
