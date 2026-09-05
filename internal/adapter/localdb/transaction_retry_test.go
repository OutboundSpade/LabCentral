package localdb

import (
	"context"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestPostgreSQLDestroyRetriesAuditContentionAtomically(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "destroy_retry")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	setupQuotaTestTenancy(t, store)
	created, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.retry", "project.alpha", 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE labcentral_jobs SET state = $1 WHERE operation_id = $2`, domain.OperationSucceeded, created.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE labcentral_local_instances SET phase = $1 WHERE instance_id = $2`, domain.InstanceReady, created.InstanceID); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	// A sequence survives transaction rollback, so the first eight transactions
	// abort at audit insertion, after the job and instance writes. This models a
	// contention burst without relying on machine speed or goroutine scheduling.
	for _, statement := range []string{
		`CREATE SEQUENCE test_destroy_conflicts`,
		`CREATE FUNCTION test_destroy_conflict() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.operation_id = 'operation.retry-destroy' AND nextval('test_destroy_conflicts') <= 8 THEN
		    RAISE EXCEPTION 'simulated audit contention' USING ERRCODE = '40001';
		  END IF;
		  RETURN NEW;
		END $$`,
		`CREATE TRIGGER test_destroy_conflict BEFORE INSERT ON labcentral_audit_records
		 FOR EACH ROW EXECUTE FUNCTION test_destroy_conflict()`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	request := quotaTestDestroyRequest(created.InstanceID, "operation.retry-destroy", "user.quota", "idem.retry-destroy", time.Time{})
	result, err := store.QueueDestroy(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID != request.OperationID || result.Replay {
		t.Fatalf("destroy result = %#v", result)
	}
	replayed, err := store.QueueDestroy(ctx, request)
	if err != nil || !replayed.Replay || replayed.OperationID != result.OperationID {
		t.Fatalf("destroy replay = %#v, %v", replayed, err)
	}
	after, err := store.GetInstance(ctx, created.InstanceID)
	if err != nil || after.ResourceVersion != before.ResourceVersion+1 || after.DesiredLifecycle != domain.LifecycleDestroyed {
		t.Fatalf("instance after retries = %#v, %v", after, err)
	}
	for _, table := range []string{"labcentral_jobs", "labcentral_local_operations", "labcentral_audit_records", "labcentral_events"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE operation_id = $1`, request.OperationID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s has %d destroy records after retries and replay, want 1", table, count)
		}
	}
}
