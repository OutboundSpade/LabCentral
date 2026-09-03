package controlplane_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

func TestExampleAApplyAndDestroyThroughDurableWorker(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runExampleAStoreConformance(t, store, db)
}

func TestPostgreSQLControlPlaneStoreConformance(t *testing.T) {
	openStore, _, _ := postgreSQLTestSchema(t, "parity")
	store, db := openStore()
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("repeat PostgreSQL migration: %v", err)
	}
	runExampleAStoreConformance(t, store, db)
}

func TestPostgreSQLMultiReplicaFencingAndCrashRecovery(t *testing.T) {
	openStore, _, _ := postgreSQLTestSchema(t, "replicas")
	storeA, db := openStore()
	storeB, _ := openStore()
	ctx := context.Background()
	provider, _, closeProvider := testProvider(t)
	defer closeProvider()
	ids := &sequenceIDs{}
	service, err := controlplane.NewService(testRenderer(t), storeA, provider, ids)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateInstance(ctx, source, true, "multi-replica-fencing")
	if err != nil {
		t.Fatal(err)
	}

	type claimResult struct {
		claim controlplane.WorkClaim
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for index, store := range []*localdb.Store{storeA, storeB} {
		go func(index int, store *localdb.Store) {
			<-start
			claim, claimErr := store.Claim(ctx, fmt.Sprintf("worker-%d", index), time.Minute)
			results <- claimResult{claim: claim, err: claimErr}
		}(index, store)
	}
	close(start)
	var oldClaim controlplane.WorkClaim
	winners, empty := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			winners++
			oldClaim = result.claim
		case errors.Is(result.err, controlplane.ErrNoWork):
			empty++
		default:
			t.Fatalf("concurrent claim: %v", result.err)
		}
	}
	if winners != 1 || empty != 1 {
		t.Fatalf("concurrent claim winners=%d empty=%d", winners, empty)
	}
	if _, err := db.ExecContext(ctx, `UPDATE labcentral_jobs SET lease_until_us = 0 WHERE operation_id = $1`, created.OperationID); err != nil {
		t.Fatal(err)
	}
	newClaim, err := storeB.Claim(ctx, "worker-after-crash", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if newClaim.Operation.FencingToken <= oldClaim.Operation.FencingToken || newClaim.Operation.Attempt <= oldClaim.Operation.Attempt {
		t.Fatalf("replacement claim did not advance fence: old=%#v new=%#v", oldClaim.Operation, newClaim.Operation)
	}
	oldFence := fenceFromClaim(oldClaim)
	if err := storeA.MarkInstancePhase(ctx, oldFence, domain.InstanceApplying, "stale network partition"); !errors.Is(err, jobsdb.ErrFencingConflict) {
		t.Fatalf("stale partitioned worker update returned %v", err)
	}

	newFence := fenceFromClaim(newClaim)
	digest, err := domain.ParseSHA256Digest(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := storeA.PrepareInvocation(ctx, controlplane.PrepareInvocationRequest{
		Fence: newFence, InvocationID: "invocation.crash", ResourceID: "network.frontend", Action: "apply",
		IdempotencyKey: "invocation.crash", RequestDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := storeB.PrepareInvocation(ctx, controlplane.PrepareInvocationRequest{
		Fence: newFence, InvocationID: "invocation.replacement", ResourceID: "network.frontend", Action: "apply",
		IdempotencyKey: "invocation.crash", RequestDigest: digest,
	})
	if err != nil || recovered.ID != prepared.ID || recovered.State != "prepared" {
		t.Fatalf("recovered invocation = %#v, %v; original = %#v", recovered, err, prepared)
	}

	finalize := controlplane.FinalizeRequest{
		Fence: newFence, ActorID: controlplane.LocalAdminID, OperationKind: controlplane.OperationApply, State: domain.OperationSucceeded,
		InstancePhase: domain.InstanceReady, Message: "Ready", OutputsJSON: []byte(`{}`),
		AuditID: "audit.multi-replica-final", EventID: "event.multi-replica-final",
	}
	finalResults := make(chan error, 2)
	start = make(chan struct{})
	for _, store := range []*localdb.Store{storeA, storeB} {
		go func(store *localdb.Store) {
			<-start
			finalResults <- store.Finalize(ctx, finalize)
		}(store)
	}
	close(start)
	succeeded, fenced := 0, 0
	for range 2 {
		switch finalizeErr := <-finalResults; {
		case finalizeErr == nil:
			succeeded++
		case errors.Is(finalizeErr, jobsdb.ErrFencingConflict):
			fenced++
		default:
			t.Fatalf("concurrent finalization: %v", finalizeErr)
		}
	}
	if succeeded != 1 || fenced != 1 {
		t.Fatalf("terminal finalization succeeded=%d fenced=%d", succeeded, fenced)
	}
	operation, err := storeA.GetOperation(ctx, created.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("terminal operation = %#v, %v", operation, err)
	}
	for table, want := range map[string]int{"labcentral_job_events": 2, "labcentral_outbox_deliveries": 2} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
}

func TestPostgreSQLConcurrentIdempotentCreateHasOneDurableResult(t *testing.T) {
	openStore, _, _ := postgreSQLTestSchema(t, "idempotency")
	storeA, db := openStore()
	storeB, _ := openStore()
	provider, _, closeProvider := testProvider(t)
	defer closeProvider()
	serviceA, err := controlplane.NewService(testRenderer(t), storeA, provider, controlplane.CryptoIDs{})
	if err != nil {
		t.Fatal(err)
	}
	serviceB, err := controlplane.NewService(testRenderer(t), storeB, provider, controlplane.CryptoIDs{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type createResult struct {
		result controlplane.EnqueueResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	for _, service := range []*controlplane.Service{serviceA, serviceB} {
		go func(service *controlplane.Service) {
			<-start
			result, createErr := service.CreateInstance(context.Background(), source, true, "same-concurrent-key")
			results <- createResult{result: result, err: createErr}
		}(service)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent creates returned %v and %v", first.err, second.err)
	}
	if first.result.InstanceID != second.result.InstanceID || first.result.OperationID != second.result.OperationID || first.result.Replay == second.result.Replay {
		t.Fatalf("concurrent idempotent results = %#v and %#v", first.result, second.result)
	}
	for table, want := range map[string]int{"labcentral_local_instances": 1, "labcentral_jobs": 1, "labcentral_idempotency_records": 1} {
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
}

func TestPostgreSQLBackupRestoreCompatibility(t *testing.T) {
	toolsContainer := os.Getenv("LABCENTRAL_TEST_POSTGRES_TOOLS_CONTAINER")
	if toolsContainer == "" {
		if _, err := exec.LookPath("pg_dump"); err != nil {
			t.Skip("pg_dump is not installed")
		}
		if _, err := exec.LookPath("pg_restore"); err != nil {
			t.Skip("pg_restore is not installed")
		}
	} else if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	toolDSN := dsnForPostgreSQLTools(t, toolsContainer)
	if toolsContainer != "" && toolDSN == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_TOOLS_DSN is not set")
	}
	openStore, dsn, schemaName := postgreSQLTestSchema(t, "restore")
	if toolDSN == "" {
		toolDSN = dsn
	}
	store, db := openStore()
	runExampleAStoreConformance(t, store, db)
	tables := []string{
		"labcentral_schema_migrations", "labcentral_local_instances", "labcentral_local_resources",
		"labcentral_jobs", "labcentral_local_operations", "labcentral_runtime_invocations",
		"labcentral_job_aggregates", "labcentral_audit_intents", "labcentral_job_events",
		"labcentral_outbox_deliveries", "labcentral_idempotency_records", "labcentral_provider_allocation_claims",
		"labcentral_local_provider_targets",
	}
	before := tableCounts(t, db, tables)
	dumpPath := filepath.Join(t.TempDir(), "control-plane.dump")
	dump := postgresToolCommand(toolsContainer, toolDSN, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--schema="+schemaName)
	var dumpErrors bytes.Buffer
	dump.Stderr = &dumpErrors
	dumpBytes, err := dump.Output()
	if err != nil {
		t.Fatalf("pg_dump isolated control-plane schema: %v: %s", err, dumpErrors.Bytes())
	}
	if err := os.WriteFile(dumpPath, dumpBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA "`+schemaName+`" CASCADE`); err != nil {
		t.Fatal(err)
	}
	restore := postgresToolCommand(toolsContainer, toolDSN, "pg_restore", "--exit-on-error", "--no-owner", "--no-privileges")
	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	restore.Stdin = dumpFile
	var restoreErrors bytes.Buffer
	restore.Stderr = &restoreErrors
	err = restore.Run()
	closeErr := dumpFile.Close()
	if err != nil {
		t.Fatalf("pg_restore isolated control-plane schema: %v: %s", err, restoreErrors.Bytes())
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	restoredStore, restoredDB := openStore()
	after := tableCounts(t, restoredDB, tables)
	for table, want := range before {
		if after[table] != want {
			t.Fatalf("restored %s count=%d want=%d", table, after[table], want)
		}
	}
	instances, err := restoredStore.ListInstances(context.Background())
	if err != nil || len(instances) != 1 || instances[0].Phase != domain.InstanceDestroyed {
		t.Fatalf("restored instance = %#v, %v", instances, err)
	}
}

func dsnForPostgreSQLTools(t *testing.T, toolsContainer string) string {
	t.Helper()
	if toolsContainer == "" {
		return ""
	}
	return os.Getenv("LABCENTRAL_TEST_POSTGRES_TOOLS_DSN")
}

func postgresToolCommand(container, dsn, name string, arguments ...string) *exec.Cmd {
	if container != "" {
		dockerArguments := []string{"exec", "-i", container, name, "--dbname=" + dsn}
		return exec.Command("docker", append(dockerArguments, arguments...)...)
	}
	command := exec.Command(name, arguments...)
	command.Env = append(os.Environ(), "PGDATABASE="+dsn)
	return command
}

func tableCounts(t *testing.T, db *sql.DB, tables []string) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func fenceFromClaim(claim controlplane.WorkClaim) controlplane.Fence {
	return controlplane.Fence{
		OperationID: claim.Operation.ID, LeaseOwner: claim.LeaseOwner, Token: claim.Operation.FencingToken,
		Generation: claim.Operation.Generation, Attempt: claim.Operation.Attempt,
	}
}

func postgreSQLTestSchema(t *testing.T, suffix string) (func() (*localdb.Store, *sql.DB), string, string) {
	t.Helper()
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("LABCENTRAL_TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schemaName := fmt.Sprintf("labcentral_controlplane_%d_%d_%s", os.Getpid(), time.Now().UnixNano(), suffix)
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schemaName+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schemaName+`" CASCADE`) })
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	return func() (*localdb.Store, *sql.DB) {
		db, err := sql.Open("pgx", parsed.String())
		if err != nil {
			t.Fatal(err)
		}
		store, err := localdb.NewPostgreSQL(db)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return store, db
	}, dsn, schemaName
}

func runExampleAStoreConformance(t *testing.T, store *localdb.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	provider, engine, closeProvider := testProvider(t)
	defer closeProvider()
	renderer := testRenderer(t)
	ids := &sequenceIDs{}
	service, err := controlplane.NewService(renderer, store, provider, ids)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := controlplane.NewWorker(store, provider, ids, controlplane.WorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateInstance(ctx, source, true, "create-example-a")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateInstance(ctx, source, true, "create-example-a")
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replay || replayed.InstanceID != created.InstanceID || replayed.OperationID != created.OperationID {
		t.Fatalf("idempotent replay = %#v, original = %#v", replayed, created)
	}
	changedSource := append(append([]byte(nil), source...), '\n')
	_, err = service.CreateInstance(ctx, changedSource, true, "create-example-a")
	var idempotencyConflict *domain.DomainError
	if !errors.As(err, &idempotencyConflict) || idempotencyConflict.Code != "idempotency_conflict" {
		t.Fatalf("changed idempotent request returned %v", err)
	}
	for table, expected := range map[string]int{
		"labcentral_local_instances": 1, "labcentral_local_resources": 6, "labcentral_jobs": 1,
		"labcentral_local_operations": 1, "labcentral_audit_intents": 1, "labcentral_job_events": 1,
		"labcentral_outbox_deliveries": 1, "labcentral_idempotency_records": 1,
	} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != expected {
			t.Fatalf("%s count = %d, want %d", table, count, expected)
		}
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	instance, resources, err := service.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Phase != domain.InstanceReady || string(instance.OutputsJSON) != `{"web-url":"http://127.0.0.1:49152/"}` {
		t.Fatalf("ready instance = phase %q outputs %s", instance.Phase, instance.OutputsJSON)
	}
	for _, resource := range resources {
		if resource.Phase != domain.ResourceReady {
			t.Fatalf("resource %s phase = %s", resource.ID, resource.Phase)
		}
	}
	operation, err := service.GetOperation(ctx, created.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("apply operation = %#v, %v", operation, err)
	}
	if engine.objectCount() != 4 {
		t.Fatalf("Docker object count after apply = %d, want 4", engine.objectCount())
	}

	destroyed, err := service.DestroyInstance(ctx, created.InstanceID, "destroy-example-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	instance, resources, err = service.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Phase != domain.InstanceDestroyed || engine.objectCount() != 0 {
		t.Fatalf("destroyed instance = phase %q, Docker objects %d", instance.Phase, engine.objectCount())
	}
	for _, resource := range resources {
		if resource.Phase != domain.ResourceDestroyed {
			t.Fatalf("resource %s phase after destroy = %s", resource.ID, resource.Phase)
		}
	}
	operation, err = service.GetOperation(ctx, destroyed.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("destroy operation = %#v, %v", operation, err)
	}
}

func TestWorkerReauthorizesQueuedActorBeforeProviderAccess(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:worker-reauthorization?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, apply := range []func() error{
		func() error {
			return store.PutUser(ctx, application.UserRecord{ID: "user.actor", Username: "actor", DisplayName: "Actor"})
		},
		func() error {
			return store.PutOrganization(ctx, application.OrganizationRecord{ID: "org.alpha", Name: "alpha"})
		},
		func() error {
			return store.PutProject(ctx, application.ProjectRecord{ID: "project.alpha", OrganizationID: "org.alpha", Name: "classroom"})
		},
		func() error {
			return store.PutMembership(ctx, application.MembershipRecord{ID: "membership.actor", UserID: "user.actor", OrganizationID: "org.alpha", ValidFrom: now.Add(-time.Hour)})
		},
		func() error {
			return store.PutRoleAssignment(ctx, application.RoleAssignmentRecord{ID: "role.actor", SubjectKind: application.SubjectUser, SubjectID: "user.actor", ScopeKind: application.ScopeProject, ScopeID: "project.alpha", Role: application.RoleInstructor, ValidFrom: now.Add(-time.Hour)})
		},
		func() error {
			return store.PutTargetGrant(ctx, application.TargetGrantRecord{ID: "grant.actor", TargetID: controlplane.LocalDockerTarget, ScopeKind: application.ScopeProject, ScopeID: "project.alpha", ValidFrom: now.Add(-time.Hour)})
		},
	} {
		if err := apply(); err != nil {
			t.Fatal(err)
		}
	}
	engine := newTestEngine()
	dockerService, err := dockerprovider.NewService(engine)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingProvider{ProviderServiceServer: dockerService}
	provider, closeProvider := testProviderClient(t, recorder)
	defer closeProvider()
	ids := &sequenceIDs{}
	service, err := controlplane.NewService(testRenderer(t), store, provider, ids)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateInstanceTargetFor(ctx, "user.actor", "project.alpha", application.OwnerUser, "user.actor", source, true, "reauthorize-actor", controlplane.LocalDockerTarget, nil)
	if err != nil {
		t.Fatal(err)
	}
	validationsBefore := recorder.validationCount()
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.actor", Username: "actor", DisplayName: "Actor", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := controlplane.NewWorker(store, provider, ids, controlplane.WorkerOptions{Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); !errors.Is(err, authorization.ErrDenied) {
		t.Fatalf("disabled queued actor returned %v", err)
	}
	if recorder.validationCount() != validationsBefore || engine.objectCount() != 0 {
		t.Fatalf("provider was reached after revocation: validations %d -> %d, objects %d", validationsBefore, recorder.validationCount(), engine.objectCount())
	}
	instance, err := store.GetInstance(ctx, created.InstanceID)
	if err != nil || instance.Phase != domain.InstancePending {
		t.Fatalf("denied work changed instance: %#v, %v", instance, err)
	}
}

func TestExampleARejectsMissingDockerRiskAcknowledgement(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	provider, _, closeProvider := testProvider(t)
	defer closeProvider()
	service, err := controlplane.NewService(testRenderer(t), store, provider, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateInstance(ctx, source, false, "missing-ack")
	var validation *controlplane.ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) == 0 || validation.Issues[0].Code != "docker_isolation_acknowledgement_required" {
		t.Fatalf("missing acknowledgement error = %#v", err)
	}
}

func TestCreateAssignsInstanceBeforeDependencyAwarePlanning(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine()
	dockerService, err := dockerprovider.NewService(engine)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingProvider{ProviderServiceServer: dockerService}
	provider, closeProvider := testProviderClient(t, recorder)
	defer closeProvider()
	service, err := controlplane.NewService(testRenderer(t), store, provider, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateInstance(ctx, source, true, "plan-instance-first")
	if err != nil {
		t.Fatal(err)
	}
	plans := recorder.snapshot()
	if len(plans) == 0 {
		t.Fatal("provider received no resource plans")
	}
	foundDependency := false
	for _, plan := range plans {
		input := plan.GetInput()
		if input.GetContext().GetLabInstanceId() != string(created.InstanceID) {
			t.Fatalf("plan instance ID = %q, want %q", input.GetContext().GetLabInstanceId(), created.InstanceID)
		}
		if len(input.GetOwnershipTokens()) != 1 || input.GetOwnershipTokens()[0] == "" {
			t.Fatalf("plan ownership tokens = %#v", input.GetOwnershipTokens())
		}
		for _, dependency := range input.GetDependencies() {
			foundDependency = true
			if dependency.GetLogicalResourceId() == "" || len(dependency.GetProviderPlan()) == 0 || dependency.GetProviderPlanDigest() == "" {
				t.Fatalf("incomplete dependency plan = %#v", dependency)
			}
		}
	}
	if !foundDependency {
		t.Fatal("no dependency plan metadata reached the provider")
	}
}

func testRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	schema, err := os.ReadFile("../../schemas/lab-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewRenderer(schema, render.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

type sequenceIDs struct {
	mu   sync.Mutex
	next uint64
}

func (s *sequenceIDs) New(prefix string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("%s_%08d", prefix, s.next), nil
}

type testEngine struct {
	mu      sync.Mutex
	objects map[string]map[string]string
}

func newTestEngine() *testEngine { return &testEngine{objects: make(map[string]map[string]string)} }

func (*testEngine) Discover(context.Context) (dockerprovider.TargetInfo, error) {
	return dockerprovider.TargetInfo{APIVersion: "1.55", EngineVersion: "29.7.2", OSType: "linux", Architecture: "amd64", OperatingSystem: "test"}, nil
}

func (*testEngine) EnsureImage(context.Context, string) error { return nil }

func (e *testEngine) EnsureNetwork(_ context.Context, desired dockerprovider.NetworkCreate) (string, error) {
	if desired.Internal {
		return "", fmt.Errorf("published-port laptop network unexpectedly used Docker internal mode")
	}
	return e.ensure("network:"+desired.Name, desired.Labels), nil
}

func (e *testEngine) RemoveNetwork(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) EnsureVolume(_ context.Context, desired dockerprovider.VolumeCreate) (string, error) {
	return e.ensure("volume:"+desired.Name, desired.Labels), nil
}

func (e *testEngine) RemoveVolume(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) EnsureContainer(_ context.Context, desired dockerprovider.ContainerCreate) (dockerprovider.ContainerObservation, error) {
	if len(desired.Published) > 0 && desired.Published[0].HostIP != "127.0.0.1" {
		return dockerprovider.ContainerObservation{}, fmt.Errorf("published port was not loopback")
	}
	id := e.ensure("container:"+desired.Name, desired.Labels)
	ports := map[string]uint16{}
	for _, port := range desired.Published {
		ports[fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol)] = 49152
	}
	return dockerprovider.ContainerObservation{ID: id, Running: true, Ports: ports, Labels: desired.Labels}, nil
}

func (e *testEngine) InspectContainer(_ context.Context, id string) (dockerprovider.ContainerObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	labels, ok := e.objects[id]
	if !ok {
		return dockerprovider.ContainerObservation{}, fmt.Errorf("not found")
	}
	return dockerprovider.ContainerObservation{ID: id, Running: true, Ports: map[string]uint16{"8080/tcp": 49152}, Labels: labels}, nil
}

func (e *testEngine) RemoveContainer(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) ensure(id string, labels map[string]string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	copyOf := make(map[string]string, len(labels))
	for key, value := range labels {
		copyOf[key] = value
	}
	e.objects[id] = copyOf
	return id
}

func (e *testEngine) remove(id string, expected map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	actual, ok := e.objects[id]
	if !ok {
		return nil
	}
	for key, value := range expected {
		if actual[key] != value {
			return fmt.Errorf("ownership mismatch")
		}
	}
	delete(e.objects, id)
	return nil
}

func (e *testEngine) objectCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.objects)
}

func testProvider(t *testing.T) (pluginv1.ProviderServiceClient, *testEngine, func()) {
	t.Helper()
	engine := newTestEngine()
	service, err := dockerprovider.NewService(engine)
	if err != nil {
		t.Fatal(err)
	}
	client, closeProvider := testProviderClient(t, service)
	return client, engine, closeProvider
}

func testProviderClient(t *testing.T, service pluginv1.ProviderServiceServer) (pluginv1.ProviderServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginv1.RegisterProviderServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient(
		"passthrough:///controlplane-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pluginv1.NewProviderServiceClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}

type recordingProvider struct {
	pluginv1.ProviderServiceServer
	mu          sync.Mutex
	plans       []*pluginv1.PlanResourceRequest
	validations int
}

func (provider *recordingProvider) ValidateLab(ctx context.Context, request *pluginv1.ValidateLabRequest) (*pluginv1.ValidateLabResponse, error) {
	provider.mu.Lock()
	provider.validations++
	provider.mu.Unlock()
	return provider.ProviderServiceServer.ValidateLab(ctx, request)
}

func (provider *recordingProvider) PlanResource(ctx context.Context, request *pluginv1.PlanResourceRequest) (*pluginv1.PlanResourceResponse, error) {
	provider.mu.Lock()
	provider.plans = append(provider.plans, proto.Clone(request).(*pluginv1.PlanResourceRequest))
	provider.mu.Unlock()
	return provider.ProviderServiceServer.PlanResource(ctx, request)
}

func (provider *recordingProvider) snapshot() []*pluginv1.PlanResourceRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([]*pluginv1.PlanResourceRequest, len(provider.plans))
	for index, request := range provider.plans {
		result[index] = proto.Clone(request).(*pluginv1.PlanResourceRequest)
	}
	return result
}

func (provider *recordingProvider) validationCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.validations
}
