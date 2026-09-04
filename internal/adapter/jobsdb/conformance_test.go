package jobsdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type conformanceCase func(*testing.T, *Store)

func TestJobContract001StateChangeAndOutboxAreAtomic(t *testing.T) {
	runSQLiteCase(t, contract001AtomicOutbox)
}

func TestJobContract002ExpiredLeaseIsFenced(t *testing.T) {
	runSQLiteCase(t, contract002Fencing)
}

func TestJobContract003UnknownInvocationIsQueriedBeforeRetry(t *testing.T) {
	runSQLiteCase(t, contract003UnknownInvocation)
}

func TestJobContract004IdempotencyKeyBindsRequestDigest(t *testing.T) {
	runSQLiteCase(t, contract004Idempotency)
}

func TestJobContract005QuotaReservationSurvivesUnknownOutcome(t *testing.T) {
	runSQLiteCase(t, contract005QuotaRetention)
}

func TestJobContract006DestroyCannotSucceedWithLeftovers(t *testing.T) {
	runSQLiteCase(t, contract006DestroyLeftovers)
}

func TestJobContract007ConcurrentClaimsHaveOneWinner(t *testing.T) {
	runSQLiteCase(t, contract007ConcurrentClaims)
}

func TestJobContract008OutboxDeliveryIsAtLeastOnceAndDeduplicable(t *testing.T) {
	runSQLiteCase(t, contract008AtLeastOnceOutbox)
}

func TestPostgreSQLJobConformance(t *testing.T) {
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	tests := []struct {
		name string
		run  conformanceCase
	}{
		{"TEST-JOB-001", contract001AtomicOutbox},
		{"TEST-JOB-002", contract002Fencing},
		{"TEST-JOB-003", contract003UnknownInvocation},
		{"TEST-JOB-004", contract004Idempotency},
		{"TEST-JOB-005", contract005QuotaRetention},
		{"TEST-JOB-006", contract006DestroyLeftovers},
		{"TEST-JOB-007", contract007ConcurrentClaims},
		{"TEST-JOB-008", contract008AtLeastOnceOutbox},
		{"TEST-ARCH-011", contractClaimFilter},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := newPostgreSQLTestStore(t, dsn, index)
			testCase.run(t, store)
		})
	}
}

func runSQLiteCase(t *testing.T, testCase conformanceCase) {
	t.Helper()
	store := newSQLiteTestStore(t)
	testCase(t, store)
}

func newSQLiteTestStore(t *testing.T) *Store {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_defensive=1", name, time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate SQLite: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat SQLite migration: %v", err)
	}
	return store
}

func newPostgreSQLTestStore(t *testing.T, dsn string, index int) *Store {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("LABCENTRAL_TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("labcentral_jobs_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), index)
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(12)
	store, err := New(db, DialectPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate PostgreSQL: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat PostgreSQL migration: %v", err)
	}
	return store
}

func TestUnknownPayloadVersionIsNotClaimed(t *testing.T) {
	store := newSQLiteTestStore(t)
	operation := testOperation("future-payload")
	if err := store.EnqueueVersioned(context.Background(), operation, []byte("future"), CurrentPayloadVersion+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimJob(context.Background(), "old-worker", time.Minute); !errors.Is(err, ErrNoClaimableJob) {
		t.Fatalf("worker claimed an unknown payload version: %v", err)
	}
}

func contract001AtomicOutbox(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAggregate(ctx, "instance-atomic", "queued"); err != nil {
		t.Fatal(err)
	}
	mutation := AtomicMutation{
		AggregateID:             "instance-atomic",
		ExpectedResourceVersion: 0,
		NewPhase:                "applying",
		AuditID:                 "audit-atomic",
		ActorID:                 "user-atomic",
		Action:                  "instance.apply",
		EventID:                 "event-atomic",
		EventType:               "lab.instance.applying",
		EventPayload:            []byte(`{"safe":"payload"}`),
		OutboxSink:              "events",
	}
	for _, fault := range []faultPoint{faultBeforeCommit, faultDuringCommit} {
		if err := store.applyAtomic(ctx, mutation, fault); !errors.Is(err, errInjectedTransactionFault) {
			t.Fatalf("fault %s returned %v", fault, err)
		}
		assertAtomicCounts(t, store, "instance-atomic", "queued", 0, 0, 0, 0)
	}
	if err := store.ApplyAtomic(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	assertAtomicCounts(t, store, "instance-atomic", "applying", 1, 1, 1, 1)
}

func assertAtomicCounts(t *testing.T, store *Store, aggregateID, phase string, version, audits, events, outbox int) {
	t.Helper()
	ctx := context.Background()
	var gotPhase string
	var gotVersion int
	if err := store.db.QueryRowContext(ctx, store.bind(`SELECT phase, resource_version FROM labcentral_job_aggregates WHERE aggregate_id = ?`), aggregateID).Scan(&gotPhase, &gotVersion); err != nil {
		t.Fatal(err)
	}
	if gotPhase != phase || gotVersion != version {
		t.Fatalf("aggregate = (%s,%d), want (%s,%d)", gotPhase, gotVersion, phase, version)
	}
	for table, want := range map[string]int{
		"labcentral_audit_intents":     audits,
		"labcentral_job_events":        events,
		"labcentral_outbox_deliveries": outbox,
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
}

func contract002Fencing(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	operation := testOperation("fence")
	if err := store.Enqueue(ctx, operation, `{"version":1}`); err != nil {
		t.Fatal(err)
	}
	oldClaim, err := store.ClaimJob(ctx, "worker-old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expireJobLease(t, store, operation.ID)
	newClaim, err := store.ClaimJob(ctx, "worker-new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if newClaim.Operation.FencingToken <= oldClaim.Operation.FencingToken {
		t.Fatalf("new fence %d did not exceed old fence %d", newClaim.Operation.FencingToken, oldClaim.Operation.FencingToken)
	}
	oldCheckpoint := application.OperationCheckpoint{
		OperationID: operation.ID,
		LeaseOwner:  "worker-old",
		Token:       oldClaim.Operation.FencingToken,
		Generation:  operation.Generation,
		State:       domain.OperationRunning,
		Attempt:     oldClaim.Operation.Attempt,
		Payload:     []byte("stale"),
	}
	if err := store.Checkpoint(ctx, oldCheckpoint); !errors.Is(err, ErrFencingConflict) {
		t.Fatalf("old worker checkpoint returned %v", err)
	}
	newCheckpoint := oldCheckpoint
	newCheckpoint.LeaseOwner = "worker-new"
	newCheckpoint.Token = newClaim.Operation.FencingToken
	newCheckpoint.Attempt = newClaim.Operation.Attempt
	newCheckpoint.Payload = []byte("current")
	if err := store.Checkpoint(ctx, newCheckpoint); err != nil {
		t.Fatalf("new worker checkpoint: %v", err)
	}
}

func contract003UnknownInvocation(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	operation := testOperation("unknown")
	if err := store.Enqueue(ctx, operation, "payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimJob(ctx, "worker-old", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInvocation(ctx, InvocationRecord{
		ID:             "invocation-unknown",
		OperationID:    operation.ID,
		IdempotencyKey: "operation-unknown-resource-1",
		RequestDigest:  digest("a"),
		State:          InvocationUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	expireJobLease(t, store, operation.ID)
	claim, err := store.ClaimJob(ctx, "worker-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Action != application.ClaimGetInvocation {
		t.Fatalf("claim action = %q, want %q", claim.Action, application.ClaimGetInvocation)
	}
}

func contract004Idempotency(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	binding := IdempotencyBinding{
		SubjectID:     "user-idempotency",
		RouteAction:   "POST:/instances",
		Key:           "same-key",
		RequestDigest: digest("a"),
		ResultID:      "operation-original",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}
	first, err := store.BindIdempotency(ctx, binding)
	if err != nil || first.Replay || first.ResultID != binding.ResultID {
		t.Fatalf("first binding = %#v, %v", first, err)
	}
	replayBinding := binding
	replayBinding.ResultID = "operation-ignored"
	replay, err := store.BindIdempotency(ctx, replayBinding)
	if err != nil || !replay.Replay || replay.ResultID != binding.ResultID {
		t.Fatalf("replay binding = %#v, %v", replay, err)
	}
	conflict := binding
	conflict.RequestDigest = digest("b")
	if _, err := store.BindIdempotency(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting binding returned %v", err)
	}
}

func contract005QuotaRetention(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateQuotaReservation(ctx, application.QuotaReservation{
		ID:          "quota-unknown",
		ProjectID:   "project-quota",
		OperationID: "operation-quota",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	resolution, err := store.ResolveQuota(ctx, "quota-unknown", QuotaOutcomeUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.ReleaseAllowed || !resolution.ReconciliationRequired {
		t.Fatalf("unknown quota resolution = %#v", resolution)
	}
	var state string
	var reconciliation int
	if err := store.db.QueryRowContext(ctx, `SELECT state, reconciliation_required FROM labcentral_quota_reservations WHERE reservation_id = 'quota-unknown'`).Scan(&state, &reconciliation); err != nil {
		t.Fatal(err)
	}
	if state != "active" || reconciliation != 1 {
		t.Fatalf("quota row = (%s,%d), want (active,1)", state, reconciliation)
	}
}

func contract006DestroyLeftovers(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAggregate(ctx, "instance-leftovers", "destroying"); err != nil {
		t.Fatal(err)
	}
	result, err := store.FinalizeDestroy(ctx, "instance-leftovers", []domain.OwnershipID{"disk-456"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationState != domain.OperationPartial || result.InstancePhase != domain.InstanceDegraded {
		t.Fatalf("destroy result = %#v", result)
	}
	var operationState, instancePhase string
	if err := store.db.QueryRowContext(ctx, `SELECT operation_state, instance_phase FROM labcentral_job_aggregates WHERE aggregate_id = 'instance-leftovers'`).Scan(&operationState, &instancePhase); err != nil {
		t.Fatal(err)
	}
	if operationState != string(domain.OperationPartial) || instancePhase != string(domain.InstanceDegraded) {
		t.Fatalf("persisted destroy state = (%s,%s)", operationState, instancePhase)
	}
}

func contract007ConcurrentClaims(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	operation := testOperation("concurrent")
	if err := store.Enqueue(ctx, operation, "payload"); err != nil {
		t.Fatal(err)
	}
	first := concurrentClaims(t, store, 8)
	if len(first) != 1 {
		t.Fatalf("first claim winners = %d, want 1", len(first))
	}
	expireJobLease(t, store, operation.ID)
	second := concurrentClaims(t, store, 8)
	if len(second) != 1 {
		t.Fatalf("second claim winners = %d, want 1", len(second))
	}
	if second[0].Operation.FencingToken <= first[0].Operation.FencingToken {
		t.Fatalf("fencing tokens are not monotonic: first=%d second=%d", first[0].Operation.FencingToken, second[0].Operation.FencingToken)
	}
}

func concurrentClaims(t *testing.T, store *Store, count int) []ClaimedJob {
	t.Helper()
	start := make(chan struct{})
	claims := make(chan ClaimedJob, count)
	errorsSeen := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			claim, err := store.ClaimJob(context.Background(), fmt.Sprintf("worker-%d", worker), time.Minute)
			switch {
			case err == nil:
				claims <- claim
			case errors.Is(err, ErrNoClaimableJob):
			default:
				errorsSeen <- err
			}
		}(index)
	}
	close(start)
	workers.Wait()
	close(claims)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent claim: %v", err)
	}
	result := make([]ClaimedJob, 0, len(claims))
	for claim := range claims {
		result = append(result, claim)
	}
	return result
}

func contract008AtLeastOnceOutbox(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAggregate(ctx, "instance-outbox", "queued"); err != nil {
		t.Fatal(err)
	}
	mutation := AtomicMutation{
		AggregateID:             "instance-outbox",
		ExpectedResourceVersion: 0,
		NewPhase:                "running",
		AuditID:                 "audit-outbox",
		ActorID:                 "user-outbox",
		Action:                  "instance.run",
		EventID:                 "event-stable",
		EventType:               "lab.instance.running",
		EventPayload:            []byte(`{"phase":"running"}`),
		OutboxSink:              "events",
	}
	if err := store.ApplyAtomic(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimOutbox(ctx, "delivery-old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempts != 1 {
		t.Fatalf("first delivery attempts = %d", first.Attempts)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_outbox_deliveries SET lease_until_us = 0 WHERE event_id = ? AND sink = ?`), first.EventID, first.Sink); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimOutbox(ctx, "delivery-new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempts != 2 || second.EventID != first.EventID || second.AggregateSequence != first.AggregateSequence {
		t.Fatalf("redelivery identity changed: first=%#v second=%#v", first, second)
	}
	if err := store.AckOutbox(ctx, second.EventID, second.Sink, second.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimOutbox(ctx, "delivery-third", time.Minute); !errors.Is(err, ErrNoOutboxDelivery) {
		t.Fatalf("delivered outbox row was claimed again: %v", err)
	}
}

func testOperation(suffix string) domain.Operation {
	return domain.Operation{
		ID:         domain.OperationID("operation-" + suffix),
		InstanceID: domain.InstanceID("instance-" + suffix),
		State:      domain.OperationQueued,
		Generation: 1,
	}
}

func expireJobLease(t *testing.T, store *Store, operationID domain.OperationID) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_jobs SET lease_until_us = 0 WHERE operation_id = ?`), operationID); err != nil {
		t.Fatal(err)
	}
}

func digest(character string) domain.SHA256Digest {
	return domain.SHA256Digest(strings.Repeat(character, 64))
}

func TestClaimFilterLeavesUnavailableInstanceQueued(t *testing.T) {
	runSQLiteCase(t, contractClaimFilter)
}
func contractClaimFilter(t *testing.T, store *Store) {
	ctx := context.Background()
	down := testOperation("down")
	up := testOperation("up")
	for _, op := range []domain.Operation{down, up} {
		if err := store.Enqueue(ctx, op, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := store.ClaimJobForInstances(ctx, "worker.up", time.Minute, []string{string(up.InstanceID)})
	if err != nil || claim.Operation.ID != up.ID {
		t.Fatal("available instance was not claimed", err)
	}
	claim, err = store.ClaimJobForInstances(ctx, "worker.recovered", time.Minute, []string{string(down.InstanceID)})
	if err != nil || claim.Operation.ID != down.ID || claim.Operation.Attempt != 1 {
		t.Fatal("unavailable instance was lost or consumed", err)
	}
}
