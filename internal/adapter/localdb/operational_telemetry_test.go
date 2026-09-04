package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestSQLiteOperationalTelemetrySnapshot(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:operational-telemetry-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	runOperationalTelemetrySnapshotConformance(t, store)
}

func TestPostgreSQLOperationalTelemetrySnapshot(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "operational_telemetry")
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOperationalTelemetrySnapshotConformance(t, store)
}

func runOperationalTelemetrySnapshotConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	setupQuotaTestTenancy(t, store)

	accessInstance, providerDigest := createReadyAccessTestInstance(t, store, "inst.telemetry-access")
	beginAccessTestSession(t, store, accessInstance, providerDigest, "access.telemetry-pending", "telemetry-pending")
	ready := beginAccessTestSession(t, store, accessInstance, providerDigest, "access.telemetry-ready", "telemetry-ready")
	finalizeAccessTestSession(t, store, ready, providerDigest, accessTestHash("telemetry-ready"), "telemetry-ready")

	queued, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.telemetry-queued", "project.beta", 3600))
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.telemetry-running", "project.gamma", 3600))
	if err != nil {
		t.Fatal(err)
	}
	var nowMicros int64
	if err := store.db.QueryRowContext(ctx, `SELECT `+store.nowMicros()).Scan(&nowMicros); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET available_at_us = ? WHERE operation_id = ?`), nowMicros-(2*time.Minute).Microseconds(), queued.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET state = ?, recovery_required = 1, lease_owner = 'worker.telemetry', lease_until_us = ? WHERE operation_id = ?`),
		domain.OperationRunning, nowMicros-time.Minute.Microseconds(), running.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_resources SET phase = ? WHERE instance_id = ?`), domain.ResourceCleanupFailed, running.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_provider_allocation_claims
		(target_id, namespace, claim_value, instance_id, resource_id, plan_digest, state, created_at_us, updated_at_us)
		VALUES ('target.quota', 'telemetry', 'leftover', ?, 'container.test', ?, ?, ?, ?)`),
		running.InstanceID, fmt.Sprintf("%064d", 1), application.AllocationOrphaned, nowMicros, nowMicros); err != nil {
		t.Fatal(err)
	}
	for index, target := range []struct {
		id           string
		discoveredAt int64
		validUntil   int64
	}{
		{"target.telemetry-stale", nowMicros - (3 * time.Minute).Microseconds(), nowMicros - time.Minute.Microseconds()},
		{"target.telemetry-fresh", nowMicros - time.Minute.Microseconds(), nowMicros + time.Minute.Microseconds()},
	} {
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_local_provider_targets
			(target_id, provider_kind, config_fingerprint, protocol_major, protocol_minor, features_json,
			 discovery_json, discovered_at_us, valid_until_us, updated_at_us)
			VALUES (?, 'telemetry', ?, 1, 2, ?, ?, ?, ?, ?)`), target.id, fmt.Sprintf("sha256:%064d", index+1),
			[]byte(`[]`), []byte(`{}`), target.discoveredAt, target.validUntil, nowMicros); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := store.ReadOperationalSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Jobs.Queued != 1 || snapshot.Jobs.Running != 1 || snapshot.Jobs.RecoveryRequired != 1 {
		t.Fatalf("durable jobs = %#v", snapshot.Jobs)
	}
	if snapshot.Jobs.OldestQueuedAge < 2*time.Minute || snapshot.Jobs.OldestExpiredLeaseAge < time.Minute {
		t.Fatalf("database-time job ages = %#v", snapshot.Jobs)
	}
	if snapshot.Outbox.JobPending != 3 || snapshot.Outbox.EventPending == 0 {
		t.Fatalf("outbox state = %#v", snapshot.Outbox)
	}
	if operationalStateCount(snapshot.Operations, domain.OperationSucceeded) != 1 ||
		operationalStateCount(snapshot.Operations, domain.OperationQueued) != 1 ||
		operationalStateCount(snapshot.Operations, domain.OperationRunning) != 1 {
		t.Fatalf("operation states = %#v", snapshot.Operations)
	}
	if accessStateCount(snapshot.Access, domain.AccessSessionPending) != 1 || accessStateCount(snapshot.Access, domain.AccessSessionReady) != 1 {
		t.Fatalf("access states = %#v", snapshot.Access)
	}
	if resourcePhaseCount(snapshot.Resources, domain.ResourceReady) != 1 ||
		resourcePhaseCount(snapshot.Resources, domain.ResourcePending) != 1 ||
		resourcePhaseCount(snapshot.Resources, domain.ResourceCleanupFailed) != 1 {
		t.Fatalf("resource states = %#v", snapshot.Resources)
	}
	if snapshot.Cleanup.UnknownResources != 0 || snapshot.Cleanup.FailedResources != 1 || snapshot.Cleanup.OrphanedAllocations != 1 {
		t.Fatalf("cleanup state = %#v", snapshot.Cleanup)
	}
	if snapshot.Targets.Total != 2 || snapshot.Targets.Stale != 1 || snapshot.Targets.OldestAge < 3*time.Minute {
		t.Fatalf("target capability state = %#v", snapshot.Targets)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET state = 'operation-secret-canary' WHERE operation_id = ?`), queued.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadOperationalSnapshot(ctx); err == nil {
		t.Fatal("corrupt durable operation state was accepted into operational telemetry")
	}
}

func operationalStateCount(counts []application.OperationStateCount, state domain.OperationState) uint64 {
	var result uint64
	for _, count := range counts {
		if count.State == state {
			result += count.Count
		}
	}
	return result
}

func accessStateCount(counts []application.AccessStateCount, state domain.AccessSessionState) uint64 {
	var result uint64
	for _, count := range counts {
		if count.State == state {
			result += count.Count
		}
	}
	return result
}

func resourcePhaseCount(counts []application.ResourcePhaseCount, phase domain.ResourcePhase) uint64 {
	var result uint64
	for _, count := range counts {
		if count.Phase == phase {
			result += count.Count
		}
	}
	return result
}
