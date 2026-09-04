package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestSQLiteQuotaExpirationConformance(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:quota-expiration-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	runQuotaExpirationConformance(t, store)
}

func TestPostgreSQLQuotaExpirationConformance(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "quota")
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runQuotaExpirationConformance(t, store)
}

func newPostgreSQLQuotaTestStore(t *testing.T, prefix string) *Store {
	t.Helper()
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("LABCENTRAL_TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("labcentral_%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`) })
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgreSQL(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteLegacyQuotaUpgradeConformance(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:quota-upgrade-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runLegacyQuotaUpgradeConformance(t, store)
}

func TestPostgreSQLLegacyQuotaUpgradeConformance(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "quota_upgrade")
	runLegacyQuotaUpgradeConformance(t, store)
}

func runLegacyQuotaUpgradeConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.jobs.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, migration := range []struct {
		id         string
		statements []string
	}{
		{migrationID, store.migrationStatements()},
		{targetMigrationID, store.targetMigrationStatements()},
		{authMigrationID, store.authMigrationStatements()},
		{authHardeningMigrationID, authenticationHardeningMigrationStatements(store.integerType())},
		{authLifecycleMigrationID, authenticationLifecycleMigrationStatements(store.integerType())},
		{templateMigrationID, store.templateMigrationStatements()},
		{auditMigrationID, auditMigrationStatements(store.integerType(), store.blobType())},
	} {
		if err := store.applyMigration(ctx, migration.id, migration.statements); err != nil {
			t.Fatalf("migrate through %s: %v", migration.id, err)
		}
	}
	now := time.Now().UTC().UnixMicro()
	type legacyFixture struct {
		id            string
		operationID   string
		instancePhase domain.InstancePhase
		operation     domain.OperationState
		resourcePhase domain.ResourcePhase
		claim         bool
	}
	fixtures := []legacyFixture{
		{id: "inst.legacy-ready", operationID: "operation.legacy-ready", instancePhase: domain.InstanceReady, operation: domain.OperationSucceeded, resourcePhase: domain.ResourceReady},
		{id: "inst.legacy-uncertain", operationID: "operation.legacy-uncertain", instancePhase: domain.InstanceFailed, operation: domain.OperationPartial, resourcePhase: domain.ResourceUnknown, claim: true},
	}
	canonical := []byte(`{"id":"container.test","kind":"Container","spec":{"compute":{"cpuMillis":250,"memoryBytes":134217728}}}`)
	plan := []byte(`{"planned":true}`)
	planDigest := sha256.Sum256(plan)
	for index, fixture := range fixtures {
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_job_aggregates
			(aggregate_id, phase, resource_version, operation_state, instance_phase)
			VALUES (?, 'complete', 1, ?, ?)`), fixture.id, fixture.operation, fixture.instancePhase); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_jobs
			(id, operation_id, instance_id, aggregate_id, state, desired_generation, payload_version, payload, available_at_us)
			VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)`), "job."+fixture.id, fixture.operationID, fixture.id, fixture.id,
			fixture.operation, []byte(`{"schemaVersion":1}`), now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_local_instances
			(instance_id, canonical_digest, canonical_lab_json, source_yaml, phase, desired_lifecycle, generation,
			 resource_version, isolation_warning_acknowledged, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, 1, 1, 1, ?, ?)`), fixture.id, fmt.Sprintf("%064d", index+1),
			[]byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","resources":[]}`), []byte("apiVersion: v1\n"),
			fixture.instancePhase, domain.LifecycleRunning, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_local_resources
			(instance_id, resource_id, kind, ordinal, canonical_resource_json, provider_plan, phase, ownership_token, updated_at_us)
			VALUES (?, 'container.test', ?, 0, ?, ?, ?, ?, ?)`), fixture.id, domain.ResourceContainer, canonical, plan,
			fixture.resourcePhase, "ownership."+fixture.id, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_local_operations
			(operation_id, instance_id, kind, message, created_at_us, updated_at_us)
			VALUES (?, ?, 'apply', 'legacy fixture', ?, ?)`), fixture.operationID, fixture.id, now, now); err != nil {
			t.Fatal(err)
		}
		if fixture.claim {
			if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_provider_allocation_claims
				(target_id, namespace, claim_value, instance_id, resource_id, plan_digest, state, created_at_us, updated_at_us)
				VALUES ('target.local-docker', 'legacy-id', 'uncertain-1', ?, 'container.test', ?, 'committed', ?, ?)`),
				fixture.id, fmt.Sprintf("%x", planDigest), now, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
	for _, expected := range []struct {
		instanceID             string
		state                  string
		reconciliationRequired int
	}{
		{"inst.legacy-ready", "committed", 0},
		{"inst.legacy-uncertain", "retained", 1},
	} {
		var state string
		var reconciliation int
		if err := store.db.QueryRowContext(ctx, store.bind(`SELECT state, reconciliation_required
			FROM labcentral_quota_reservations WHERE instance_id = ?`), expected.instanceID).Scan(&state, &reconciliation); err != nil {
			t.Fatal(err)
		}
		if state != expected.state || reconciliation != expected.reconciliationRequired {
			t.Fatalf("legacy %s quota state=%s reconciliation=%d", expected.instanceID, state, reconciliation)
		}
	}
	usage, err := store.ListQuotaUsage(ctx, application.QuotaScope{Kind: application.QuotaScopePlatform, ID: "platform"})
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range []application.QuotaDimension{application.QuotaInstances, application.QuotaContainers} {
		found := false
		for _, record := range usage {
			if record.Dimension == dimension {
				found = true
				if record.Committed != 1 || record.Retained != 1 || record.Reserved != 0 {
					t.Fatalf("legacy %s usage = %#v", dimension, record)
				}
			}
		}
		if !found {
			t.Fatalf("legacy %s usage is missing", dimension)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("repeat upgraded migration: %v", err)
	}
	var reservations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_quota_reservations`).Scan(&reservations); err != nil || reservations != 2 {
		t.Fatalf("idempotent legacy reservations=%d, %v", reservations, err)
	}
}

func runQuotaExpirationConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	setupQuotaTestTenancy(t, store)
	platformScope := application.QuotaScope{Kind: application.QuotaScopePlatform, ID: "platform"}
	defaultContainers, err := store.GetQuotaLimit(ctx, platformScope, application.QuotaContainers)
	if err != nil {
		t.Fatal(err)
	}
	updatedContainers, err := store.PutQuotaLimit(ctx, application.PutQuotaLimitRequest{
		GovernanceRecordIDs: governanceIDs("platform-containers-update"), Scope: platformScope,
		Dimension: application.QuotaContainers, Limit: defaultContainers.Limit + 1,
		ExpectedResourceVersion: defaultContainers.ResourceVersion, UpdatedBy: "user.quota",
		IdempotencyKey: "idem.platform-containers-update", RequestDigest: testRequestDigest("platform-containers-update"),
	})
	if err != nil {
		t.Fatalf("update unused migration-created platform quota: %v", err)
	}
	if updatedContainers.ResourceVersion != defaultContainers.ResourceVersion+1 {
		t.Fatalf("updated platform quota version = %d, want %d", updatedContainers.ResourceVersion, defaultContainers.ResourceVersion+1)
	}
	usage, err := store.ListQuotaUsage(ctx, platformScope)
	if err != nil {
		t.Fatal(err)
	}
	foundContainers := false
	for _, item := range usage {
		if item.Dimension == application.QuotaContainers {
			foundContainers = item.Reserved == 0 && item.Committed == 0 && item.Retained == 0
		}
	}
	if !foundContainers {
		t.Fatalf("updated platform container quota has no zero-valued usage ledger: %#v", usage)
	}

	alphaScope := quotaTestProjectScope("project.alpha")
	alphaLimit := putQuotaTestLimit(t, store, alphaScope, 1, 0, "alpha-limit")
	if alphaLimit.ResourceVersion != 1 {
		t.Fatalf("new quota version = %d", alphaLimit.ResourceVersion)
	}
	first, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.alpha-one", "project.alpha", 0))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := store.GetInstance(ctx, first.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.ExpiresAt.Before(time.Now().Add(7*time.Hour)) || instance.MaximumExpiresAt.Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("default database-time lifetime = expires %s maximum %s", instance.ExpiresAt, instance.MaximumExpiresAt)
	}
	if _, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.alpha-two", "project.alpha", 0)); !isQuotaExceeded(err) {
		t.Fatalf("second project instance returned %v", err)
	}
	if _, err := store.GetInstance(ctx, "inst.alpha-two"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("over-quota create left an instance: %v", err)
	}
	assertQuotaUsage(t, store, alphaScope, 1, 0, 0)

	// Installing a scoped quota after admission must account for the existing
	// reservation instead of presenting a false zero baseline.
	if _, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.beta-one", "project.beta", 0)); err != nil {
		t.Fatal(err)
	}
	betaScope := quotaTestProjectScope("project.beta")
	if _, err := store.PutQuotaLimit(ctx, quotaTestLimitRequest(betaScope, 0, 0, "beta-too-low")); !isQuotaExceeded(err) {
		t.Fatalf("new under-sized quota returned %v", err)
	}
	if _, err := store.GetQuotaLimit(ctx, betaScope, application.QuotaInstances); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("failed quota creation persisted a limit: %v", err)
	}
	putQuotaTestLimit(t, store, betaScope, 1, 0, "beta-limit")
	assertQuotaUsage(t, store, betaScope, 1, 0, 0)

	gammaScope := quotaTestProjectScope("project.gamma")
	putQuotaTestLimit(t, store, gammaScope, 1, 0, "gamma-limit")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, id := range []string{"inst.gamma-one", "inst.gamma-two"} {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, createErr := store.CreateInstance(ctx, quotaTestCreateRequest(id, "project.gamma", 0))
			results <- createErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, exceeded := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if isQuotaExceeded(result) {
			exceeded++
		} else {
			t.Fatalf("concurrent quota create returned %v", result)
		}
	}
	if successes != 1 || exceeded != 1 {
		t.Fatalf("concurrent quota results successes=%d exceeded=%d", successes, exceeded)
	}

	deltaScope := quotaTestProjectScope("project.delta")
	policy := putQuotaTestPolicy(t, store, deltaScope, 30, 60, 30, 10, 0, 0, "delta-policy")
	created, err := store.CreateInstance(ctx, quotaTestCreateRequest("inst.delta-one", "project.delta", 30))
	if err != nil {
		t.Fatal(err)
	}
	delta, err := store.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	renewal := application.RenewInstanceLifetimeRequest{
		GovernanceRecordIDs: governanceIDs("delta-renew"), InstanceID: delta.ID, TTLSeconds: 45,
		ExpectedResourceVersion: delta.ResourceVersion, ActorID: "user.quota", IdempotencyKey: "idem.delta-renew",
		RequestDigest: testRequestDigest("delta-renew"),
	}
	renewed, err := store.RenewInstanceLifetime(ctx, renewal)
	if err != nil || renewed.ResourceVersion != delta.ResourceVersion+1 || !renewed.ExpiresAt.After(delta.ExpiresAt) {
		t.Fatalf("renewed lifetime = %#v, %v", renewed, err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances
		SET resource_version = resource_version + 1, updated_at_us = `+store.nowMicros()+` WHERE instance_id = ?`), delta.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.RenewInstanceLifetime(ctx, renewal)
	if err != nil || replayed.ResourceVersion != renewed.ResourceVersion || !replayed.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal replay did not return original snapshot: %#v, %v", replayed, err)
	}
	currentDelta, err := store.GetInstance(ctx, delta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeTargetGrant(ctx, "grant.quota", 1, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	revokedRenewal := renewal
	revokedRenewal.TTLSeconds = 50
	revokedRenewal.ExpectedResourceVersion = currentDelta.ResourceVersion
	revokedRenewal.IdempotencyKey = "idem.delta-renew-revoked"
	revokedRenewal.RequestDigest = testRequestDigest("delta-renew-revoked")
	revokedRenewal.GovernanceRecordIDs = governanceIDs("delta-renew-revoked")
	if _, err := store.RenewInstanceLifetime(ctx, revokedRenewal); !errors.Is(err, application.ErrAuthorityChanged) {
		t.Fatalf("renewal after target-grant revocation returned %v", err)
	}
	updatedPolicy := policy
	updatedPolicy.MaximumTTLSeconds = 50
	updatedPolicy.DefaultTTLSeconds = 25
	policyRequest := quotaTestPolicyRequest(updatedPolicy, policy.ResourceVersion, "delta-policy-update")
	if _, err := store.PutLifetimePolicy(ctx, policyRequest); err != nil {
		t.Fatal(err)
	}
	originalPolicy, err := store.PutLifetimePolicy(ctx, quotaTestPolicyRequest(application.LifetimePolicyRecord{
		Scope: deltaScope, DefaultTTLSeconds: 30, MaximumTTLSeconds: 60, RenewalWindowSeconds: 30,
		WarningBeforeSeconds: 10, RenewalAllowed: true,
	}, 0, "delta-policy"))
	if err != nil || originalPolicy.ResourceVersion != 1 {
		t.Fatalf("policy replay after update = %#v, %v", originalPolicy, err)
	}

	// Warning writes are one-shot and stale candidates cannot win after a
	// lifetime change because the exact expiry is part of the predicate.
	nowMicros := time.Now().UTC().UnixMicro()
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances SET
		expires_at_us = ?, expiration_warning_at_us = ?, expiration_warning_emitted_at_us = 0,
		expiration_grace_seconds = 0 WHERE instance_id = ?`), nowMicros+3_600_000_000, nowMicros-1_000_000, delta.ID); err != nil {
		t.Fatal(err)
	}
	warnings, err := store.ListExpirationWarnings(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var warning application.ExpirationCandidate
	for _, candidate := range warnings {
		if candidate.InstanceID == delta.ID {
			warning = candidate
		}
	}
	if warning.InstanceID == "" {
		t.Fatal("warning candidate was not listed")
	}
	marked, err := store.MarkExpirationWarning(ctx, application.MarkExpirationWarningRequest{GovernanceRecordIDs: governanceIDs("delta-warning"), Candidate: warning})
	if err != nil || !marked {
		t.Fatalf("mark warning = %t, %v", marked, err)
	}
	marked, err = store.MarkExpirationWarning(ctx, application.MarkExpirationWarningRequest{GovernanceRecordIDs: governanceIDs("delta-warning-replay"), Candidate: warning})
	if err != nil || marked {
		t.Fatalf("repeat warning = %t, %v", marked, err)
	}

	// A queued reservation that exceeded its hold without ever creating an
	// invocation is failed and released atomically, so it can never apply later.
	zetaScope := quotaTestProjectScope("project.zeta")
	putQuotaTestLimit(t, store, zetaScope, 1, 0, "zeta-limit")
	zetaRequest := quotaTestCreateRequest("inst.zeta-one", "project.zeta", 0)
	zetaRequest.Resources[0].Allocations = []application.AllocationKey{{TargetID: "target.quota", Namespace: "vmid", Value: "901001"}}
	zeta, err := store.CreateInstance(ctx, zetaRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_quota_reservations
		SET expires_at_us = `+store.nowMicros()+` - 1000000 WHERE instance_id = ?`), zeta.InstanceID); err != nil {
		t.Fatal(err)
	}
	expiredCount, err := store.ExpireStaleQuotaReservations(ctx, 10)
	if err != nil || expiredCount != 1 {
		t.Fatalf("expired reservations = %d, %v", expiredCount, err)
	}
	operation, err := store.GetOperation(ctx, zeta.OperationID)
	if err != nil || operation.State != domain.OperationTimedOut {
		t.Fatalf("expired operation = %#v, %v", operation, err)
	}
	zetaInstance, err := store.GetInstance(ctx, zeta.InstanceID)
	if err != nil || zetaInstance.Phase != domain.InstanceFailed {
		t.Fatalf("expired reservation instance = %#v, %v", zetaInstance, err)
	}
	assertQuotaUsage(t, store, zetaScope, 0, 0, 0)
	claims, err := store.ListClaims(ctx, zeta.InstanceID)
	if err != nil || len(claims) != 1 || claims[0].State != application.AllocationReleased {
		t.Fatalf("expired reservation claims = %#v, %v", claims, err)
	}

	// A DB-time TTL expiry and a worker reclaim racing for an expired running
	// lease have one winner. If reclaim wins, the next sweep still safely
	// terminalizes the invocation-free operation and releases its claims.
	etaScope := quotaTestProjectScope("project.eta")
	putQuotaTestLimit(t, store, etaScope, 1, 0, "eta-limit")
	etaRequest := quotaTestCreateRequest("inst.eta-one", "project.eta", 0)
	etaRequest.Resources[0].Allocations = []application.AllocationKey{{TargetID: "target.quota", Namespace: "vmid", Value: "901002"}}
	eta, err := store.CreateInstance(ctx, etaRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET state = ?, lease_owner = 'expired-worker',
		lease_until_us = `+store.nowMicros()+` - 1000000, available_at_us = 0 WHERE operation_id = ?`),
		domain.OperationRunning, eta.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET available_at_us = `+store.nowMicros()+` + 3600000000
		WHERE operation_id <> ? AND state = ?`), eta.OperationID, domain.OperationQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances SET phase = ?, expires_at_us = `+store.nowMicros()+` - 1000000
		WHERE instance_id = ?`), domain.InstanceValidating, eta.InstanceID); err != nil {
		t.Fatal(err)
	}
	type sweepResult struct {
		count uint32
		err   error
	}
	startRace := make(chan struct{})
	sweepResults := make(chan sweepResult, 1)
	claimResults := make(chan error, 1)
	go func() {
		<-startRace
		count, sweepErr := store.ExpireStaleQuotaReservations(ctx, 1)
		sweepResults <- sweepResult{count: count, err: sweepErr}
	}()
	go func() {
		<-startRace
		_, claimErr := store.Claim(ctx, "replacement-worker", time.Minute)
		claimResults <- claimErr
	}()
	close(startRace)
	sweep := <-sweepResults
	claimErr := <-claimResults
	if sweep.err != nil || (claimErr != nil && !errors.Is(claimErr, application.ErrNoWork)) {
		t.Fatalf("sweeper/reclaim race: count=%d sweep=%v claim=%v", sweep.count, sweep.err, claimErr)
	}
	if sweep.count == 1 && claimErr == nil {
		t.Fatal("sweeper and worker both won the expired lease")
	}
	if claimErr == nil {
		if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET lease_until_us = 0 WHERE operation_id = ?`), eta.OperationID); err != nil {
			t.Fatal(err)
		}
		count, err := store.ExpireStaleQuotaReservations(ctx, 1)
		if err != nil || count != 1 {
			t.Fatalf("post-reclaim expiration = %d, %v", count, err)
		}
	}
	etaOperation, err := store.GetOperation(ctx, eta.OperationID)
	if err != nil || etaOperation.State != domain.OperationTimedOut {
		t.Fatalf("TTL-expired running operation = %#v, %v", etaOperation, err)
	}
	assertQuotaUsage(t, store, etaScope, 0, 0, 0)
	claims, err = store.ListClaims(ctx, eta.InstanceID)
	if err != nil || len(claims) != 1 || claims[0].State != application.AllocationReleased {
		t.Fatalf("TTL-expired running claims = %#v, %v", claims, err)
	}

	// Distinct callers and keys converge on the one active destroy operation.
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances SET
		expires_at_us = `+store.nowMicros()+` - 1000000, expiration_grace_seconds = 0,
		desired_lifecycle = ? WHERE instance_id = ?`), domain.LifecycleRunning, delta.ID); err != nil {
		t.Fatal(err)
	}
	delta, err = store.GetInstance(ctx, delta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueDestroy(ctx, quotaTestDestroyRequest(delta.ID, "operation.delta-blocked-expire", application.SystemExpirationActorID, "idem.delta-blocked", delta.ExpiresAt)); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("destroy raced active apply: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET state = ? WHERE operation_id = ?`), domain.OperationSucceeded, created.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances SET phase = ? WHERE instance_id = ?`), domain.InstanceReady, delta.ID); err != nil {
		t.Fatal(err)
	}
	firstDestroy, err := store.QueueDestroy(ctx, quotaTestDestroyRequest(delta.ID, "operation.delta-expire", application.SystemExpirationActorID, "idem.delta-expire", delta.ExpiresAt))
	if err != nil {
		t.Fatal(err)
	}
	secondDestroy, err := store.QueueDestroy(ctx, quotaTestDestroyRequest(delta.ID, "operation.delta-manual", "user.quota", "idem.delta-manual", time.Time{}))
	if err != nil || !secondDestroy.Replay || secondDestroy.OperationID != firstDestroy.OperationID {
		t.Fatalf("converged destroy = %#v, %v; first=%#v", secondDestroy, err, firstDestroy)
	}
}

func setupQuotaTestTenancy(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.quota", Username: "quota", DisplayName: "Quota User"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOrganization(ctx, application.OrganizationRecord{ID: "org.quota", Name: "quota-org"}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []application.ProjectRecord{
		{ID: "project.alpha", OrganizationID: "org.quota", Name: "quota-alpha"},
		{ID: "project.beta", OrganizationID: "org.quota", Name: "quota-beta"},
		{ID: "project.gamma", OrganizationID: "org.quota", Name: "quota-gamma"},
		{ID: "project.delta", OrganizationID: "org.quota", Name: "quota-delta"},
		{ID: "project.zeta", OrganizationID: "org.quota", Name: "quota-zeta"},
		{ID: "project.eta", OrganizationID: "org.quota", Name: "quota-eta"},
	} {
		if err := store.PutProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutMembership(ctx, application.MembershipRecord{
		ID: "membership.quota", UserID: "user.quota", OrganizationID: "org.quota", ValidFrom: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRoleAssignment(ctx, application.RoleAssignmentRecord{
		ID: "role.quota", SubjectKind: application.SubjectUser, SubjectID: "user.quota",
		ScopeKind: application.ScopePlatform, ScopeID: "platform", Role: application.RolePlatformAdministrator,
		ValidFrom: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTargetGrant(ctx, application.TargetGrantRecord{
		ID: "grant.quota", TargetID: "target.quota", ScopeKind: application.ScopePlatform,
		ScopeID: "platform", ValidFrom: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
}

func quotaTestProjectScope(projectID domain.ProjectID) application.QuotaScope {
	return application.QuotaScope{Kind: application.QuotaScopeProject, ID: string(projectID), OrganizationID: "org.quota"}
}

func quotaTestCreateRequest(instanceID string, projectID domain.ProjectID, ttl uint64) application.CreateInstanceRequest {
	canonical := []byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","resources":[]}`)
	plan := []byte(`{"planned":true}`)
	canonicalDigest, planDigest, requestDigest := sha256.Sum256(canonical), sha256.Sum256(plan), sha256.Sum256([]byte(instanceID))
	return application.CreateInstanceRequest{
		InstanceID: domain.InstanceID(instanceID), ProjectID: projectID, OwnerKind: application.OwnerUser,
		OwnerID: "user.quota", CreatedBy: "user.quota", ProviderTargetID: "target.quota",
		OperationID: domain.OperationID("operation." + instanceID), CanonicalDigest: domain.SHA256Digest(fmt.Sprintf("%x", canonicalDigest)),
		CanonicalJSON: canonical, SourceYAML: []byte("apiVersion: labcentral.io/v1alpha1\n"),
		Resources: []application.ResourceSeed{{
			ID: "container.test", Kind: domain.ResourceContainer,
			CanonicalJSON: []byte(`{"id":"container.test","kind":"Container","spec":{"compute":{"cpuMillis":250,"memoryBytes":134217728}}}`),
			ProviderPlan:  plan, ProviderPlanDigest: domain.SHA256Digest(fmt.Sprintf("%x", planDigest)), OwnershipToken: "ownership." + instanceID,
		}},
		Acknowledged: true, SubjectID: "user.quota", IdempotencyKey: "idempotency." + instanceID,
		RequestDigest: domain.SHA256Digest(fmt.Sprintf("%x", requestDigest)), Payload: []byte(`{"schemaVersion":1}`),
		AuditID: "audit." + instanceID, EventID: "event." + instanceID,
		Quota: application.InstanceQuotaRequest{
			ReservationID: domain.QuotaReservationID("quota." + instanceID),
			Amounts:       []application.QuotaAmount{{Dimension: application.QuotaInstances, Value: 1}},
			Lifetime:      application.InstanceLifetimeRequest{TTLSeconds: ttl},
		},
	}
}

func quotaTestLimitRequest(scope application.QuotaScope, limit uint64, expected domain.ResourceVersion, suffix string) application.PutQuotaLimitRequest {
	return application.PutQuotaLimitRequest{
		GovernanceRecordIDs: governanceIDs(suffix), Scope: scope, Dimension: application.QuotaInstances,
		Limit: limit, ExpectedResourceVersion: expected, UpdatedBy: "user.quota",
		IdempotencyKey: "idem." + suffix, RequestDigest: testRequestDigest(suffix),
	}
}

func putQuotaTestLimit(t *testing.T, store *Store, scope application.QuotaScope, limit uint64, expected domain.ResourceVersion, suffix string) application.QuotaLimitRecord {
	t.Helper()
	record, err := store.PutQuotaLimit(context.Background(), quotaTestLimitRequest(scope, limit, expected, suffix))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func quotaTestPolicyRequest(policy application.LifetimePolicyRecord, expected domain.ResourceVersion, suffix string) application.PutLifetimePolicyRequest {
	return application.PutLifetimePolicyRequest{
		GovernanceRecordIDs: governanceIDs(suffix), Policy: policy, ExpectedResourceVersion: expected,
		UpdatedBy: "user.quota", IdempotencyKey: "idem." + suffix, RequestDigest: testRequestDigest(suffix),
	}
}

func putQuotaTestPolicy(t *testing.T, store *Store, scope application.QuotaScope, defaultTTL, maximumTTL, renewalWindow, warning, grace uint64, expected domain.ResourceVersion, suffix string) application.LifetimePolicyRecord {
	t.Helper()
	policy := application.LifetimePolicyRecord{
		Scope: scope, DefaultTTLSeconds: defaultTTL, MaximumTTLSeconds: maximumTTL,
		RenewalWindowSeconds: renewalWindow, WarningBeforeSeconds: warning, DestructionGraceSecs: grace,
		RenewalAllowed: true,
	}
	record, err := store.PutLifetimePolicy(context.Background(), quotaTestPolicyRequest(policy, expected, suffix))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertQuotaUsage(t *testing.T, store *Store, scope application.QuotaScope, reserved, committed, retained uint64) {
	t.Helper()
	records, err := store.ListQuotaUsage(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Dimension == application.QuotaInstances {
			if record.Reserved != reserved || record.Committed != committed || record.Retained != retained || record.UpdatedAt.IsZero() {
				t.Fatalf("quota usage = %#v, want reserved=%d committed=%d retained=%d", record, reserved, committed, retained)
			}
			return
		}
	}
	t.Fatal("instances quota usage was not found")
}

func isQuotaExceeded(err error) bool {
	var exceeded *application.QuotaExceededError
	return errors.As(err, &exceeded)
}

func quotaTestDestroyRequest(instanceID domain.InstanceID, operationID domain.OperationID, actor domain.UserID, key string, due time.Time) application.QueueDestroyRequest {
	return application.QueueDestroyRequest{
		InstanceID: instanceID, OperationID: operationID, SubjectID: actor, IdempotencyKey: key,
		RequestDigest: testRequestDigest(key), Payload: []byte(`{"schemaVersion":1}`),
		AuditID: "audit." + string(operationID), EventID: "event." + string(operationID), ExpirationDueAt: due,
	}
}
