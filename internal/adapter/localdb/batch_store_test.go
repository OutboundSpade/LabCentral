package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestSQLiteInstanceBatchConformance(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:instance-batch-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runInstanceBatchConformance(t, store)
}

func TestPostgreSQLInstanceBatchConformance(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "instance_batch")
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runInstanceBatchConformance(t, store)
}

func runInstanceBatchConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	setupQuotaTestTenancy(t, store)
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("batch-template"), ID: "template.batch", ProjectID: "project.alpha",
		Name: "batch-lab", CreatedBy: "user.quota", IdempotencyKey: "idem.batch-template",
		RequestDigest: testRequestDigest("batch-template"),
	}); err != nil {
		t.Fatal(err)
	}
	revisionRequest := testRevisionRequest("revision.batch-one", "template.batch", "batch-bundle", "batch-canonical")
	revisionRequest.PublishedBy = "user.quota"
	revisionRequest.IdempotencyKey = "idem.batch-revision"
	revisionRequest.RequestDigest = testRequestDigest("batch-revision")
	if _, err := store.PublishTemplateRevision(ctx, revisionRequest); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < controlplane.MaximumInstanceBatchSize; index++ {
		if err := store.PutTeam(ctx, application.TeamRecord{
			ID: domain.TeamID(fmt.Sprintf("team.batch-%03d", index)), OrganizationID: "org.quota",
			Name: fmt.Sprintf("batch-team-%03d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}

	scope := quotaTestProjectScope("project.alpha")
	limit := putQuotaTestLimit(t, store, scope, controlplane.MaximumInstanceBatchSize-1, 0, "batch-limit-99")
	request := batchTestRequest("batch.atomic", controlplane.MaximumInstanceBatchSize)
	if _, err := store.CreateInstanceBatch(ctx, request); !isQuotaExceeded(err) {
		t.Fatalf("oversized atomic batch returned %v", err)
	}
	assertTableCount(t, store, "labcentral_instance_batches", 0)
	assertTableCount(t, store, "labcentral_local_instances", 0)
	assertTableCount(t, store, "labcentral_quota_reservations", 0)
	assertQuotaUsage(t, store, scope, 0, 0, 0)

	putQuotaTestLimit(t, store, scope, controlplane.MaximumInstanceBatchSize, limit.ResourceVersion, "batch-limit-100")
	start := make(chan struct{})
	results := make(chan controlplane.InstanceBatchRecord, 2)
	errorsOut := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := store.CreateInstanceBatch(ctx, request)
			results <- result
			errorsOut <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	var created controlplane.InstanceBatchRecord
	originals, replays := 0, 0
	for result := range results {
		if result.Replay {
			replays++
		} else {
			originals++
			created = result
		}
	}
	if originals != 1 || replays != 1 {
		t.Fatalf("concurrent batch results originals=%d replays=%d", originals, replays)
	}
	if created.ID != request.ID || created.Replay || len(created.Items) != controlplane.MaximumInstanceBatchSize {
		t.Fatalf("created batch = %#v", created)
	}
	for index, item := range created.Items {
		if item.Ordinal != uint32(index) || item.InstanceID != request.Instances[index].InstanceID ||
			item.OperationID != request.Instances[index].OperationID || item.OwnerID != request.Instances[index].OwnerID {
			t.Fatalf("batch item %d = %#v", index, item)
		}
	}
	assertTableCount(t, store, "labcentral_instance_batches", 1)
	assertTableCount(t, store, "labcentral_local_instances", controlplane.MaximumInstanceBatchSize)
	assertTableCount(t, store, "labcentral_quota_reservations", controlplane.MaximumInstanceBatchSize)
	assertQuotaUsage(t, store, scope, controlplane.MaximumInstanceBatchSize, 0, 0)

	loaded, err := store.GetInstanceBatch(ctx, request.ID)
	if err != nil || loaded.ID != created.ID || len(loaded.Items) != len(created.Items) {
		t.Fatalf("loaded batch = %#v, %v", loaded, err)
	}
	replayed, err := store.CreateInstanceBatch(ctx, request)
	if err != nil || !replayed.Replay || replayed.ID != created.ID || len(replayed.Items) != len(created.Items) {
		t.Fatalf("replayed batch = %#v, %v", replayed, err)
	}
	conflict := request
	conflict.RequestDigest = testRequestDigest("batch-conflicting-body")
	if _, err := store.CreateInstanceBatch(ctx, conflict); !errors.Is(err, jobsdb.ErrIdempotencyConflict) {
		t.Fatalf("batch idempotency conflict returned %v", err)
	}

	audit, err := store.ListAuditRecords(ctx, application.AuditRecordFilter{
		Scope:         application.RecordScope{OrganizationID: "org.quota", ProjectID: "project.alpha"},
		AggregateType: "lab-instance-batch", AggregateID: string(request.ID), Limit: 10,
	})
	if err != nil || len(audit.Records) != 1 || audit.Records[0].Action != "instance.batch.create" || audit.Records[0].Outcome != application.AuditAccepted {
		t.Fatalf("batch audit = %#v, %v", audit, err)
	}
	events, err := store.ListEventRecords(ctx, application.EventRecordFilter{
		Scope:         application.RecordScope{OrganizationID: "org.quota", ProjectID: "project.alpha"},
		AggregateType: "lab-instance-batch", AggregateID: string(request.ID), Limit: 10,
	})
	if err != nil || len(events.Records) != 1 || events.Records[0].Type != "lab.instance-batch.accepted" {
		t.Fatalf("batch events = %#v, %v", events, err)
	}

	if err := store.PutUser(ctx, application.UserRecord{ID: "user.backup-admin", Username: "backup-admin", DisplayName: "Backup Admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRoleAssignment(ctx, application.RoleAssignmentRecord{
		ID: "role.backup-admin", SubjectKind: application.SubjectUser, SubjectID: "user.backup-admin",
		ScopeKind: application.ScopePlatform, ScopeID: "platform", Role: application.RolePlatformAdministrator,
		ValidFrom: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeRoleAssignment(ctx, "role.quota", application.ScopePlatform, "platform", 1, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	denied := batchTestRequest("batch.revoked", 1)
	if _, err := store.CreateInstanceBatch(ctx, denied); !errors.Is(err, controlplane.ErrAuthorityChanged) {
		t.Fatalf("revoked batch authority returned %v", err)
	}
	assertTableCount(t, store, "labcentral_instance_batches", 1)
}

func batchTestRequest(batchID string, count int) controlplane.CreateInstanceBatchRequest {
	instances := make([]controlplane.CreateInstanceRequest, 0, count)
	for index := 0; index < count; index++ {
		instance := quotaTestCreateRequest(fmt.Sprintf("inst.%s-%03d", batchID, index), "project.alpha", 3600)
		instance.RevisionID = "revision.batch-one"
		instance.OwnerKind = application.OwnerTeam
		instance.OwnerID = fmt.Sprintf("team.batch-%03d", index)
		instances = append(instances, instance)
	}
	return controlplane.CreateInstanceBatchRequest{
		GovernanceRecordIDs: governanceIDs(batchID), ID: domain.InstanceBatchID(batchID), RevisionID: "revision.batch-one",
		ProjectID: "project.alpha", CreatedBy: "user.quota", TargetID: "target.quota",
		IdempotencyKey: "idem." + batchID, RequestDigest: testRequestDigest(batchID), Instances: instances,
	}
}

func assertTableCount(t *testing.T, store *Store, table string, expected int) {
	t.Helper()
	var actual int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("%s count = %d, want %d", table, actual, expected)
	}
}
