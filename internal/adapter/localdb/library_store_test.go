package localdb

import (
	"context"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteLibraryAtomicSync(t *testing.T) { libraryContract(t, newSQLiteAccessContractStore(t)) }
func TestPostgreSQLLibraryAtomicSync(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "library")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	libraryContract(t, s)
}
func libraryContract(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	l, err := s.CreateLibrary(ctx, app.CreateLibrary{Library: app.TemplateLibrary{ID: "library.test", ProjectID: "project.local", AuthorID: app.LocalAdminID, Binding: "source/github", Reference: "main", IntervalSeconds: 900}, IdempotencyKey: "create-library", RequestDigest: testRequestDigest("library"), GovernanceRecordIDs: governanceIDs("library")})
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	queue := func() app.ExtensionWorkClaim {
		t.Helper()
		sequence++
		name := fmt.Sprintf("sync-%d", sequence)
		r := app.CreateExtensionWork{Work: app.ExtensionWork{ID: domain.OperationID("operation." + name), ProjectID: l.ProjectID, ActorID: l.AuthorID, InvocationID: domain.InvocationID("invocation." + name), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkSource, LibraryID: l.ID, PluginInstance: "source", BindingID: "github", Reference: "main"}}, IdempotencyKey: name + "-queue", RequestDigest: testRequestDigest(name), GovernanceRecordIDs: governanceIDs(name)}
		if _, err := s.CreateExtensionWork(ctx, r); err != nil {
			t.Fatal(err)
		}
		c, err := s.ClaimExtensionWork(ctx, "worker", "source/github", domain.WorkSource, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	publish := func(c app.ExtensionWorkClaim, names ...string) app.LibraryPublishRequest {
		request := app.LibraryPublishRequest{Claim: c, Commit: strings.Repeat("a", 40)}
		for _, name := range names {
			tid := domain.TemplateID("template." + name)
			rid := domain.RevisionID(fmt.Sprintf("revision.%s.%d", name, sequence))
			create := app.CreateLabTemplateRequest{ID: tid, Name: name, ProjectID: l.ProjectID, CreatedBy: l.AuthorID, IdempotencyKey: "library-create-" + name, RequestDigest: testRequestDigest(name), GovernanceRecordIDs: governanceIDs("create-" + name)}
			revision := testRevisionRequest(rid, tid, name, name)
			revision.PublishedBy = l.AuthorID
			request.Entries = append(request.Entries, app.LibraryPublication{Key: name, Template: create, Revision: revision})
		}
		return request
	}
	finish := func(c app.ExtensionWorkClaim) {
		t.Helper()
		if err := s.FinishExtensionWork(ctx, c, domain.OperationSucceeded, []byte(`{}`), "", governanceIDs(fmt.Sprintf("finish-%d", sequence))); err != nil {
			t.Fatal(err)
		}
	}
	c := queue()
	invalid := publish(c, "first", "second")
	invalid.Entries[1].Key = "../bad"
	if err = s.PublishLibrary(ctx, invalid); err == nil {
		t.Fatal("invalid collection published")
	}
	entries, err := s.LibraryEntries(ctx, l.ID)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial collection visible")
	}
	var templates int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_lab_templates`).Scan(&templates); err != nil || templates != 0 {
		t.Fatal("partial template visible")
	}
	valid := publish(c, "first", "second")
	if err = s.PublishLibrary(ctx, valid); err != nil {
		t.Fatal(err)
	}
	// Response loss and duplicate publication are harmless.
	if err = s.PublishLibrary(ctx, valid); err != nil {
		t.Fatal(err)
	}
	finish(c)
	before, _ := s.LibraryEntries(ctx, l.ID)
	c = queue()
	if err = s.PublishLibrary(ctx, publish(c, "first", "second")); err != nil {
		t.Fatal(err)
	}
	finish(c)
	unchanged, _ := s.LibraryEntries(ctx, l.ID)
	if len(unchanged) != 2 || before[0].RevisionID != unchanged[0].RevisionID {
		t.Fatal("unchanged source created duplicate revision")
	}
	c = queue()
	if err = s.PublishLibrary(ctx, publish(c, "first")); err != nil {
		t.Fatal(err)
	}
	finish(c)
	removed, _ := s.LibraryEntries(ctx, l.ID)
	if len(removed) != 2 || removed[1].Available || removed[1].RevisionID != before[1].RevisionID {
		t.Fatal("removal lost retained revision")
	}
	local := testRevisionRequest("revision.local", "template.first", "edited", "edited")
	local.PublishedBy = l.AuthorID
	if _, err = s.PublishTemplateRevision(ctx, local); err == nil {
		t.Fatal("edited synchronized template")
	}
	// Independent manual-source work cannot bypass the library ownership check.
	c = queue()
	local.WorkFence = &app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt}
	if err = s.CancelExtensionWork(ctx, c.Work.ID, l.AuthorID, governanceIDs("cancel-library")); err != nil {
		t.Fatal(err)
	}
	if err = s.PublishLibrary(ctx, publish(c, "first")); err == nil {
		t.Fatal("cancelled publication admitted")
	}
	if err = s.FinishExtensionWork(ctx, c, domain.OperationCanceled, nil, "", governanceIDs("cancel-finish")); err != nil {
		t.Fatal(err)
	}
	// Concurrent scheduling produces one active operation.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[domain.OperationID]bool{}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("parallel-%d", i)
			work, err := s.CreateExtensionWork(ctx, app.CreateExtensionWork{Work: app.ExtensionWork{ID: domain.OperationID("operation." + name), ProjectID: l.ProjectID, ActorID: l.AuthorID, InvocationID: domain.InvocationID("invocation." + name), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkSource, LibraryID: l.ID, PluginInstance: "source", BindingID: "github", Reference: "main"}}, IdempotencyKey: name, RequestDigest: testRequestDigest(name), GovernanceRecordIDs: governanceIDs(name)})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[work.ID] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(ids) != 1 {
		t.Fatalf("active sync count %d", len(ids))
	}
	due, err := s.DueLibraries(ctx)
	if err != nil || len(due) != 0 {
		t.Fatal("active library scheduled again")
	}
	retry, err := s.ClaimExtensionWork(ctx, "retry-worker", "source/github", domain.WorkSource, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeferLibraryWork(ctx, retry, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimExtensionWork(ctx, "early-worker", "source/github", domain.WorkSource, time.Minute); err == nil {
		t.Fatal("backoff ignored")
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE labcentral_jobs SET available_at_us=1 WHERE state='queued'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE labcentral_extension_binding_leases SET lease_until_us=0`); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ClaimExtensionWork(ctx, "resumed-worker", "source/github", domain.WorkSource, time.Minute)
	if err != nil || !resumed.Recover || resumed.Work.InvocationID != retry.Work.InvocationID || resumed.Operation.FencingToken <= retry.Operation.FencingToken {
		t.Fatalf("retry lost identity/fence: %+v %v", resumed, err)
	}

}
