package localdb

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestSQLiteBuildDedupAndWaiterCancellation(t *testing.T) {
	buildWorkContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLBuildDedupAndWaiterCancellation(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "build_work")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	buildWorkContract(t, s)
}
func buildWorkContract(t *testing.T, s *Store) {
	ctx := context.Background()
	var mu sync.Mutex
	var works []app.ExtensionWork
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("build.%d", i)
			w, e := s.CreateExtensionWork(ctx, app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: "request-" + id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: domain.InvocationID("inv." + id), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkBuild, PluginInstance: "builder", BindingID: "default", DedupKey: "same-build", Build: &app.BuildWork{ContextArtifact: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}})
			if e != nil {
				t.Errorf("dedup: %v", e)
				return
			}
			mu.Lock()
			works = append(works, w)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(works) != 8 {
		t.Fatal("missing successful requests")
	}
	id := works[0].ID
	for _, w := range works {
		if w.ID != id {
			t.Fatal("duplicate build")
		}
	}
	var count int
	if e := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_extension_artifact_refs WHERE operation_id=?`), id).Scan(&count); e != nil || count != 1 {
		t.Fatal("input artifact not atomically retained", e, count)
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("preparation.%d", i)
		w, e := s.CreateExtensionWork(ctx, app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(name), IdempotencyKey: "request-" + name, RequestDigest: testRequestDigest(name), Work: app.ExtensionWork{ID: domain.OperationID(name), ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: domain.InvocationID("inv." + name), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkPreparation, PluginInstance: "core-preparation", BindingID: "default"}}})
		if e != nil {
			t.Fatal(e)
		}
		if e = s.LinkExtensionWork(ctx, w.ID, id); e != nil {
			t.Fatal(e)
		}
	}
	preparations, err := s.ListOwnPreparations(ctx, "project.local", app.LocalAdminID)
	if err != nil || len(preparations) != 2 {
		t.Fatalf("own preparation list: %v %v", preparations, err)
	}
	for _, w := range preparations {
		if w.Request.Kind != domain.WorkPreparation {
			t.Fatal("build escaped preparation filter")
		}
	}
	preparations, err = s.ListOwnPreparations(ctx, "project.local", "user.other")
	if err != nil || len(preparations) != 0 {
		t.Fatal("foreign actor preparation disclosure", err)
	}
	preparations, err = s.ListOwnPreparations(ctx, "project.other", app.LocalAdminID)
	if err != nil || len(preparations) != 0 {
		t.Fatal("foreign project preparation disclosure", err)
	}
	if e := s.CancelExtensionWork(ctx, "preparation.0", app.LocalAdminID, governanceIDs("cancel-prep")); e != nil {
		t.Fatal(e)
	}
	if e := s.CancelExtensionWork(ctx, id, app.LocalAdminID, governanceIDs("cancel-build")); e == nil {
		t.Fatal("canceled shared build while another waiter remains")
	}
	if e := s.CancelExtensionWork(ctx, "preparation.1", app.LocalAdminID, governanceIDs("cancel-second-prep")); e != nil {
		t.Fatal(e)
	}
	if e := s.CancelExtensionWork(ctx, id, app.LocalAdminID, governanceIDs("cancel-direct-shared")); e == nil {
		t.Fatal("canceled a build shared by explicit requests")
	}
	claim, e := s.ClaimExtensionWork(ctx, "worker", "builder/default", domain.WorkBuild, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if claim.Work.ID != id || claim.Work.CancelRequested {
		t.Fatal("waiter cancellation reached build")
	}
	if e = s.FinishExtensionWork(ctx, claim, domain.OperationSucceeded, json.RawMessage(`{"image":{"digest":"test"}}`), "", governanceIDs("finish-build")); e != nil {
		t.Fatal(e)
	}
}

func TestSQLitePreparedBatchAdmissionRemainsAtomic(t *testing.T) {
	preparedBatchContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLPreparedBatchAdmissionRemainsAtomic(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "prepared_batch")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	preparedBatchContract(t, s)
}
func preparedBatchContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	if _, e := s.CreateLabTemplate(ctx, app.CreateLabTemplateRequest{GovernanceRecordIDs: governanceIDs("prepared-template"), ID: "template.batch", ProjectID: "project.alpha", Name: "prepared-batch", CreatedBy: "user.quota", IdempotencyKey: "prepared-template", RequestDigest: testRequestDigest("prepared-template")}); e != nil {
		t.Fatal(e)
	}
	rev := testRevisionRequest("revision.batch-one", "template.batch", "prepared-bundle", "prepared-canonical")
	rev.PublishedBy = "user.quota"
	rev.IdempotencyKey = "prepared-revision"
	rev.RequestDigest = testRequestDigest("prepared-revision")
	if _, e := s.PublishTemplateRevision(ctx, rev); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := s.PutTeam(ctx, app.TeamRecord{ID: domain.TeamID(fmt.Sprintf("team.batch-%03d", i)), OrganizationID: "org.quota", Name: fmt.Sprintf("prepared-team-%d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	create := func(id string, kind domain.WorkSubjectKind, binding string) app.ExtensionWorkClaim {
		t.Helper()
		_, e := s.CreateExtensionWork(ctx, app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: "request-" + id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.alpha", ActorID: "user.quota", InvocationID: domain.InvocationID("inv." + id), Request: app.ExtensionWorkRequest{Version: 1, Kind: kind, PluginInstance: "fixture", BindingID: binding}}})
		if e != nil {
			t.Fatal(e)
		}
		c, e := s.ClaimExtensionWork(ctx, "worker", "fixture/"+binding, kind, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	prep := create("prepared.operation", domain.WorkPreparation, "prep")
	fence := &app.Fence{OperationID: prep.Work.ID, LeaseOwner: prep.LeaseOwner, Token: prep.Operation.FencingToken, Attempt: prep.Operation.Attempt, Generation: 1}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("import.%d", i)
		c := create(id, domain.WorkImageImport, id)
		b := app.StoredImageBinding{ID: id, ProjectID: "project.alpha", TargetID: "target.quota", ProviderDigest: "fixture", ImportOperationID: c.Work.ID, Binding: app.ImageBinding{ImageRef: "labcentral.local/images/img-fixture-" + fmt.Sprint(i) + "@sha256:" + testRequestDigest(id).Hex(), ArtifactDigest: testRequestDigest(id), OpaqueBinding: []byte("fixture")}}
		if i == 1 {
			// A packaged container's manifest and containing VM disk have distinct
			// identities; admission must match the approved image reference.
			b.Binding.ArtifactDigest = testRequestDigest("container-qcow2")
		}
		if e := s.PutImageBinding(ctx, c, b); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			if e := s.SetImageBindingApproval(ctx, id, "user.quota", true, false, governanceIDs("approve-first")); e != nil {
				t.Fatal(e)
			}
		}
	}
	request := batchTestRequest("batch.prepared", 2)
	for i := range request.Instances {
		request.Instances[i].WorkFence = fence
		// Deliberately omit ImageBindingIDs, as a direct YAML client would.
		kind := "VM"
		if i == 1 {
			kind = "Container"
		}
		request.Instances[i].CanonicalJSON = []byte(fmt.Sprintf(`{"resources":[{"kind":%q,"spec":{"image":{"repository":"labcentral.local/images/img-fixture-%d","digest":"sha256:%s"}}}]}`, kind, i, testRequestDigest(fmt.Sprintf("import.%d", i)).Hex()))
	}
	if _, e := s.CreateInstanceBatch(ctx, request); e == nil {
		t.Fatal("batch admitted an unapproved image")
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_local_instances WHERE project_id='project.alpha'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial batch committed", count, e)
	}
	if e := s.SetImageBindingApproval(ctx, "import.1", "user.quota", true, false, governanceIDs("approve-second")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CreateInstanceBatch(ctx, request); e != nil {
		t.Fatal(e)
	}
	assertTableCount(t, s, "labcentral_instance_image_bindings", 2)
	if e := s.SetImageBindingApproval(ctx, "import.0", "user.quota", false, true, governanceIDs("retire-first")); e != nil {
		t.Fatal(e)
	}
	retirement := app.ExtensionWork{ID: "retirement.first", ProjectID: "project.alpha", ActorID: "user.quota", InvocationID: "inv.retirement", Request: app.ExtensionWorkRequest{Version: 2, Kind: domain.WorkImageImport, PluginInstance: "core-import", PluginDigest: "fixture", BindingID: "target.quota", Import: &app.ImportWork{TargetID: "target.quota", RetireBindingID: "import.0"}}}
	if _, e := s.CreateExtensionWork(ctx, app.CreateExtensionWork{Work: retirement, GovernanceRecordIDs: governanceIDs("retirement-create"), IdempotencyKey: "retirement-create", RequestDigest: testRequestDigest("retirement")}); e != nil {
		t.Fatal(e)
	}
	retirementClaim, e := s.ClaimExtensionWork(ctx, "retirement-worker", "core-import/target.quota", domain.WorkImageImport, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if ready, e := s.CanRetireImage(ctx, retirementClaim, "import.0"); e != nil || ready {
		t.Fatal("retirement ignored retained lab", ready, e)
	}
	if e := s.ReleaseImageImportClaims(ctx, retirementClaim, "import.0"); e == nil {
		t.Fatal("released image allocation with active lab")
	}
	if _, e := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET phase='destroyed' WHERE instance_id=?`), request.Instances[0].InstanceID); e != nil {
		t.Fatal(e)
	}
	if ready, e := s.CanRetireImage(ctx, retirementClaim, "import.0"); e != nil || !ready {
		t.Fatal("destroyed lab blocked retirement", ready, e)
	}

	if e := s.CancelExtensionWork(ctx, prep.Work.ID, "user.quota", governanceIDs("cancel-prepared")); e != nil {
		t.Fatal(e)
	}
	other := batchTestRequest("batch.canceled", 2)
	for i := range other.Instances {
		other.Instances[i].WorkFence = fence
	}
	if _, e := s.CreateInstanceBatch(ctx, other); e == nil {
		t.Fatal("canceled preparation admitted a batch")
	}
}

func TestSQLitePreparationYieldFairness(t *testing.T) {
	preparationYieldContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLPreparationYieldFairness(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "preparation_fairness")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	preparationYieldContract(t, s)
}
func preparationYieldContract(t *testing.T, s *Store) {
	ctx := context.Background()
	for _, id := range []string{"preparation.first", "preparation.second"} {
		_, e := s.CreateExtensionWork(ctx, app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: domain.InvocationID("inv." + id), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkPreparation, PluginInstance: "core-preparation", BindingID: "default"}}})
		if e != nil {
			t.Fatal(e)
		}
	}
	first, e := s.ClaimExtensionWork(ctx, "worker", "core-preparation/default", domain.WorkPreparation, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.YieldExtensionWork(ctx, first); e != nil {
		t.Fatal(e)
	}
	var remaining int64
	if e = s.db.QueryRowContext(ctx, s.bind("SELECT available_at_us - "+s.nowMicros()+" FROM labcentral_jobs WHERE operation_id=?"), first.Work.ID).Scan(&remaining); e != nil || remaining < (4*time.Second).Microseconds() {
		t.Fatalf("waiting work would churn durable writes: remaining=%d error=%v", remaining, e)
	}
	second, e := s.ClaimExtensionWork(ctx, "worker", "core-preparation/default", domain.WorkPreparation, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if first.Work.ID == second.Work.ID {
		t.Fatal("waiting preparation starved another deployment")
	}
}

func TestSQLiteBuildDependencyAdmissionAndCancellation(t *testing.T) {
	buildDependencyAdmissionContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLBuildDependencyAdmissionAndCancellation(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "build_dependency_admission")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	buildDependencyAdmissionContract(t, s)
}
func buildDependencyAdmissionContract(t *testing.T, s *Store) {
	ctx := context.Background()
	request := func(id string, deps ...app.BuildDependencyWork) app.CreateExtensionWork {
		return app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: "request-" + id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: domain.InvocationID("inv." + id), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkBuild, PluginInstance: "builder", BindingID: "default", Build: &app.BuildWork{Dependencies: deps, ContextArtifact: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	}
	if _, err := s.CreateExtensionWork(ctx, request("missing-parent", app.BuildDependencyWork{RecipeID: "base", BuildID: "absent"})); err == nil {
		t.Fatal("missing dependency accepted")
	}
	if _, err := s.GetExtensionWork(ctx, "missing-parent"); err == nil {
		t.Fatal("invalid dependency partially committed parent")
	}
	for i := 0; i < 12; i++ {
		child := fmt.Sprintf("base.%d", i)
		parent := fmt.Sprintf("parent.%d", i)
		if _, err := s.CreateExtensionWork(ctx, request(child)); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		admitted := make(chan error, 1)
		canceled := make(chan error, 1)
		go func() {
			<-start
			_, err := s.CreateExtensionWork(ctx, request(parent, app.BuildDependencyWork{RecipeID: "base", BuildID: domain.OperationID(child)}))
			admitted <- err
		}()
		go func() {
			<-start
			canceled <- s.CancelExtensionWork(ctx, domain.OperationID(child), app.LocalAdminID, governanceIDs("cancel-"+child))
		}()
		close(start)
		admitErr, cancelErr := <-admitted, <-canceled
		if admitErr == nil && cancelErr == nil {
			t.Fatal("shared base cancellation raced through dependency admission")
		}
		if admitErr != nil && cancelErr != nil {
			t.Fatalf("neither serialized operation completed: %v / %v", admitErr, cancelErr)
		}
		if admitErr == nil {
			childWork, err := s.GetExtensionWork(ctx, domain.OperationID(child))
			if err != nil || childWork.CancelRequested {
				t.Fatal("admitted parent lost base", err)
			}
			if err = s.CancelExtensionWork(ctx, domain.OperationID(parent), app.LocalAdminID, governanceIDs("cancel-"+parent)); err != nil {
				t.Fatal(err)
			}
			if err = s.CancelExtensionWork(ctx, domain.OperationID(child), app.LocalAdminID, governanceIDs("retry-cancel-"+child)); err != nil {
				t.Fatal("departed waiter retained exclusive base", err)
			}
		}
	}
}
