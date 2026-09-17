package localdb

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestSQLiteExtensionWorkFencingAndRecovery(t *testing.T) {
	extensionWorkContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLExtensionWorkFencingAndRecovery(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "extension_work")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	extensionWorkContract(t, s)
}
func extensionWorkContract(t *testing.T, s *Store) {
	ctx := context.Background()
	template, e := s.CreateLabTemplate(ctx, app.CreateLabTemplateRequest{GovernanceRecordIDs: governanceIDs("ext-template"), ID: "template.ext", ProjectID: "project.local", Name: "extension-fixture", CreatedBy: app.LocalAdminID, IdempotencyKey: "extension-template", RequestDigest: testRequestDigest("template")})
	if e != nil {
		t.Fatal(e)
	}
	request := app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs("ext-create"), IdempotencyKey: "extension-create", RequestDigest: testRequestDigest("create"), Work: app.ExtensionWork{ID: "operation.ext", ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: "invocation.ext", Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkSource, PluginInstance: "fixture", PluginDigest: "sha256:fixture", BindingID: "binding", TemplateID: template.ID, Reference: "main"}}}
	w, e := s.CreateExtensionWork(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.CreateExtensionWork(ctx, request)
	if e != nil || replay.ID != w.ID {
		t.Fatalf("replay: %+v %v", replay, e)
	}
	bad := request
	bad.RequestDigest = testRequestDigest("changed")
	if _, e = s.CreateExtensionWork(ctx, bad); e == nil {
		t.Fatal("accepted changed idempotency request")
	}
	if _, e = s.jobs.ClaimJob(ctx, "instance-worker", time.Second); !errors.Is(e, jobsdb.ErrNoClaimableJob) {
		t.Fatalf("instance worker claimed extension: %v", e)
	}
	var claims []app.ExtensionWorkClaim
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := s.ClaimExtensionWork(ctx, "source-worker", "fixture/binding", domain.WorkSource, time.Minute)
			if e == nil {
				mu.Lock()
				claims = append(claims, c)
				mu.Unlock()
			} else if !errors.Is(e, jobsdb.ErrNoClaimableJob) {
				t.Errorf("claim: %v", e)
			}
		}()
	}
	wg.Wait()
	if len(claims) != 1 {
		t.Fatalf("claims=%d", len(claims))
	}
	old := claims[0]
	for _, q := range []string{"UPDATE labcentral_jobs SET lease_until_us=1 WHERE operation_id='operation.ext'", "UPDATE labcentral_extension_binding_leases SET lease_until_us=1"} {
		if _, e = s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	recovered, e := s.ClaimExtensionWork(ctx, "replacement", "fixture/binding", domain.WorkSource, time.Minute)
	if e != nil || !recovered.Recover || recovered.Work.InvocationID != old.Work.InvocationID || recovered.Operation.FencingToken <= old.Operation.FencingToken {
		t.Fatalf("recovery: %+v %v", recovered, e)
	}
	if e = s.FinishExtensionWork(ctx, old, domain.OperationSucceeded, []byte(`{}`), "", governanceIDs("stale")); !errors.Is(e, jobsdb.ErrFencingConflict) {
		t.Fatalf("stale finish: %v", e)
	}
	revision := testRevisionRequest("revision.ext", "template.ext", "ext-bundle", "ext-canonical")
	revision.PublishedBy = app.LocalAdminID
	revision.WorkFence = &app.Fence{OperationID: old.Work.ID, LeaseOwner: old.LeaseOwner, Token: old.Operation.FencingToken, Generation: old.Operation.Generation, Attempt: old.Operation.Attempt}
	if _, e = s.PublishTemplateRevision(ctx, revision); !errors.Is(e, jobsdb.ErrFencingConflict) {
		t.Fatalf("stale publication: %v", e)
	}
	if e = s.CancelExtensionWork(ctx, w.ID, app.LocalAdminID, governanceIDs("ext-cancel")); e != nil {
		t.Fatal(e)
	}
	revision.WorkFence = &app.Fence{OperationID: recovered.Work.ID, LeaseOwner: recovered.LeaseOwner, Token: recovered.Operation.FencingToken, Generation: recovered.Operation.Generation, Attempt: recovered.Operation.Attempt}
	if _, e = s.PublishTemplateRevision(ctx, revision); !errors.Is(e, jobsdb.ErrFencingConflict) {
		t.Fatalf("cancelled publication: %v", e)
	}
	if e = s.FinishExtensionWork(ctx, recovered, domain.OperationCanceled, nil, "", governanceIDs("ext-done")); e != nil {
		t.Fatal(e)
	}
	final, e := s.GetExtensionWork(ctx, w.ID)
	if e != nil || final.State != domain.OperationCanceled {
		t.Fatalf("final %+v %v", final, e)
	}
}
