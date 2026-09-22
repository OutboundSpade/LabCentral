package localdb

import (
	"context"
	"database/sql"
	"errors"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"sync"
	"testing"
	"time"
)

func TestSQLiteOIDCStudentProvisioning(t *testing.T) {
	studentProvisioningContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLOIDCStudentProvisioning(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "oidc_student")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	studentProvisioningContract(t, s)
}
func studentProvisioningContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	if _, e := s.SyncOIDCUser(ctx, "https://id.example.test", "new", "Student", ""); !errors.Is(e, app.ErrAuthRecordNotFound) {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.SyncOIDCUser(ctx, "https://id.example.test", "new", "Student", "project.alpha")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	u, e := s.SyncOIDCUser(ctx, "https://id.example.test", "new", "Updated Student", "project.alpha")
	if e != nil || u.DisplayName != "Updated Student" {
		t.Fatal(u, e)
	}
	snap, e := s.AuthorizationSnapshot(ctx, u.ID, time.Now())
	if e != nil || len(snap.Assignments) != 1 || snap.Assignments[0].Role != app.RoleParticipant || len(snap.Memberships) != 1 {
		t.Fatal(snap, e)
	}
	eval, e := authorization.New(s)
	if e != nil {
		t.Fatal(e)
	}
	request := app.AuthorizationRequest{SubjectID: u.ID, ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Action: app.ActionInstanceCreate, OwnerKind: app.OwnerUser, OwnerID: string(u.ID), TargetID: "target.quota"}
	// Admission rechecks deny participant execution even if an earlier request was accepted.
	if e = s.ConfigureParticipantDeployment(ctx, false); e != nil {
		t.Fatal(e)
	}
	if e = eval.Authorize(ctx, request); !errors.Is(e, authorization.ErrDenied) {
		t.Fatal("participant deployment allowed", e)
	}
	e = s.workTx(ctx, func(tx *sql.Tx) error { return s.requireDeploymentPolicyTx(ctx, tx, "project.alpha", u.ID) })
	if !errors.Is(e, app.ErrAuthorityChanged) {
		t.Fatal("transaction admitted student", e)
	}
	request.Action = app.ActionTemplateRead
	request.TargetID = ""
	request.OwnerKind = ""
	request.OwnerID = ""
	if e = eval.Authorize(ctx, request); e != nil {
		t.Fatal("student reads blocked", e)
	}
	if _, e = s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_role_assignments WHERE subject_id=?`), u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SyncOIDCUser(ctx, "https://id.example.test", "new", "New Name", "project.alpha"); e != nil {
		t.Fatal(e)
	}
	snap, e = s.AuthorizationSnapshot(ctx, u.ID, time.Now())
	if e != nil || len(snap.Assignments) != 0 {
		t.Fatal("revoked role restored", e)
	}
	if _, e = s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_users SET disabled=1 WHERE user_id=?`), u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SyncOIDCUser(ctx, "https://id.example.test", "new", "No", "project.alpha"); !errors.Is(e, app.ErrAuthRecordNotFound) {
		t.Fatal("disabled user accepted", e)
	}
}
