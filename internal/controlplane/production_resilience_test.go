package controlplane_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const productionClassroomObjective = 2 * time.Minute

type classroomCompletion struct {
	kind     controlplane.OperationKind
	state    domain.OperationState
	duration time.Duration
}

func TestPostgreSQLHundredInstanceMultiReplicaResilience(t *testing.T) {
	openStore, _, _ := postgreSQLTestSchema(t, "hundred_instance_resilience")
	store, db := openStore()
	ctx, cancel := context.WithTimeout(context.Background(), 2*productionClassroomObjective)
	t.Cleanup(cancel)

	if err := store.PutUser(ctx, application.UserRecord{
		ID: controlplane.LocalAdminID, Username: "local-admin", DisplayName: "Load-test administrator",
	}); err != nil {
		t.Fatal(err)
	}
	owners := make([]controlplane.InstanceOwner, 0, controlplane.MaximumInstanceBatchSize)
	for index := range controlplane.MaximumInstanceBatchSize {
		teamID := domain.TeamID(fmt.Sprintf("team.resilience-%03d", index))
		if err := store.PutTeam(ctx, application.TeamRecord{
			ID: teamID, OrganizationID: "org.local", Name: fmt.Sprintf("resilience-%03d", index),
		}); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, controlplane.InstanceOwner{Kind: application.OwnerTeam, ID: string(teamID)})
	}

	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactfs.New(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	provider, engine, closeProvider := testProvider(t)
	t.Cleanup(closeProvider)
	ids := &sequenceIDs{}
	service, err := controlplane.NewService(testRenderer(t), store, provider, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetArtifactStore(artifacts); err != nil {
		t.Fatal(err)
	}
	// The migration default is intentionally conservative at 100 containers.
	// This two-container classroom declares its exact 100-instance capacity
	// policy before admission instead of bypassing quota enforcement.
	if _, err := service.PutQuotaLimit(ctx, controlplane.LocalAdminID,
		application.QuotaScope{Kind: application.QuotaScopePlatform, ID: "platform"},
		application.QuotaContainers, 2*controlplane.MaximumInstanceBatchSize, 1,
		"resilience-container-capacity"); err != nil {
		t.Fatal(err)
	}
	template, err := service.CreateLabTemplate(ctx, controlplane.LocalAdminID, "project.local",
		"resilience-lab", "100-instance production control-plane rehearsal", "resilience-template-create")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := service.PublishTemplateRevision(ctx, controlplane.LocalAdminID, template.ID, source, "resilience-template-publish")
	if err != nil {
		t.Fatal(err)
	}

	admissionStarted := time.Now()
	batch, err := service.CreateInstanceBatchRevisionTargetForLifetime(
		ctx, controlplane.LocalAdminID, "project.local", owners, revision.ID, source,
		application.InstanceLifetimeRequest{TTLSeconds: 3600}, true, "resilience-classroom-create",
		controlplane.LocalDockerTarget, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != controlplane.MaximumInstanceBatchSize || batch.Replay {
		t.Fatalf("classroom batch = %#v", batch)
	}
	instanceIDs := make([]domain.InstanceID, 0, len(batch.Items))
	wantOwners := make(map[domain.InstanceID]string, len(batch.Items))
	for _, item := range batch.Items {
		instanceIDs = append(instanceIDs, item.InstanceID)
		wantOwners[item.InstanceID] = item.OwnerID
	}

	// Simulate one replica dying after it has durably claimed work. No worker is
	// allowed to process it until another database pool observes lease expiry.
	abandoned, err := store.Claim(ctx, "replica-abandoned", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE labcentral_jobs SET lease_until_us = 0 WHERE operation_id = $1`, abandoned.Operation.ID); err != nil {
		t.Fatal(err)
	}

	var completionMu sync.Mutex
	completions := make([]classroomCompletion, 0, 2*controlplane.MaximumInstanceBatchSize)
	workerErrors := make(chan error, 16)
	workerContext, stopWorkers := context.WithCancel(ctx)
	t.Cleanup(stopWorkers)
	var workers sync.WaitGroup
	for index := 0; index < 4; index++ {
		workerStore, _ := openStore()
		worker, err := controlplane.NewWorker(workerStore, provider, ids, controlplane.WorkerOptions{
			Owner: fmt.Sprintf("replica-%d", index), LeaseDuration: 3 * time.Second, PollInterval: 2 * time.Millisecond,
			OnError: func(workerErr error) {
				select {
				case workerErrors <- workerErr:
				default:
				}
			},
			OnComplete: func(kind controlplane.OperationKind, state domain.OperationState, duration time.Duration) {
				completionMu.Lock()
				defer completionMu.Unlock()
				completions = append(completions, classroomCompletion{kind: kind, state: state, duration: duration})
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			if runErr := worker.Run(workerContext); runErr != nil {
				select {
				case workerErrors <- runErr:
				default:
				}
			}
		}()
	}

	ready := waitForClassroomPhase(t, ctx, service, instanceIDs, domain.InstanceReady, workerErrors)
	readyElapsed := time.Since(admissionStarted)
	if readyElapsed > productionClassroomObjective {
		t.Fatalf("100-instance classroom reached ready in %s, objective %s", readyElapsed, productionClassroomObjective)
	}
	for _, instance := range ready {
		if instance.OwnerKind != application.OwnerTeam || instance.OwnerID != wantOwners[instance.ID] {
			t.Fatalf("instance %s crossed owner binding: kind=%q owner=%q want=%q", instance.ID, instance.OwnerKind, instance.OwnerID, wantOwners[instance.ID])
		}
	}
	assertClassroomProviderOwnership(t, engine, instanceIDs)

	destroyStarted := time.Now()
	for index, instanceID := range instanceIDs {
		if _, err := service.DestroyInstanceAs(ctx, controlplane.LocalAdminID, instanceID,
			fmt.Sprintf("resilience-destroy-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	waitForClassroomPhase(t, ctx, service, instanceIDs, domain.InstanceDestroyed, workerErrors)
	destroyElapsed := time.Since(destroyStarted)
	if destroyElapsed > productionClassroomObjective {
		t.Fatalf("100-instance classroom destroyed in %s, objective %s", destroyElapsed, productionClassroomObjective)
	}
	stopWorkers()
	workers.Wait()
	if engine.objectCount() != 0 {
		t.Fatalf("provider objects after classroom destroy = %d, want 0", engine.objectCount())
	}

	recovered, err := store.GetOperation(ctx, abandoned.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var recoveredFence uint64
	if err := db.QueryRowContext(ctx, `SELECT fencing_token FROM labcentral_jobs WHERE operation_id = $1`, abandoned.Operation.ID).Scan(&recoveredFence); err != nil {
		t.Fatal(err)
	}
	if recovered.Attempt < 2 || domain.FencingToken(recoveredFence) <= abandoned.Operation.FencingToken || recovered.State != domain.OperationSucceeded {
		t.Fatalf("abandoned claim was not taken over safely: old=%#v recovered=%#v", abandoned.Operation, recovered)
	}
	assertClassroomTerminalState(t, ctx, store, db)
	assertClassroomCompletions(t, completions)
	t.Logf("100-instance rehearsal passed: admission-to-ready=%s destroy=%s replicas=4 abandoned_attempt=%d", readyElapsed, destroyElapsed, recovered.Attempt)
}

func waitForClassroomPhase(
	t *testing.T,
	ctx context.Context,
	service *controlplane.Service,
	ids []domain.InstanceID,
	want domain.InstancePhase,
	workerErrors <-chan error,
) []controlplane.InstanceRecord {
	t.Helper()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		instances, err := service.ListInstancesByID(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		matched := len(instances) == len(ids)
		for _, instance := range instances {
			if instance.Phase == domain.InstanceFailed || instance.Phase == domain.InstanceDegraded {
				t.Fatalf("instance %s entered terminal failure phase %s", instance.ID, instance.Phase)
			}
			matched = matched && instance.Phase == want
		}
		if matched {
			return instances
		}
		select {
		case workerErr := <-workerErrors:
			t.Fatalf("classroom worker: %v", workerErr)
		case <-ctx.Done():
			t.Fatalf("wait for all instances in phase %s: %v", want, ctx.Err())
		case <-poll.C:
		}
	}
}

func assertClassroomProviderOwnership(t *testing.T, engine *testEngine, ids []domain.InstanceID) {
	t.Helper()
	wantInstances := make(map[string]bool, len(ids))
	for _, id := range ids {
		wantInstances[string(id)] = true
	}
	counts := make(map[string]int, len(ids))
	ownershipTokens := make(map[string]bool, 4*len(ids))
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for objectID, labels := range engine.objects {
		instanceID := labels["io.labcentral.instance"]
		ownership := labels["io.labcentral.ownership"]
		if labels["io.labcentral.managed"] != "true" || !wantInstances[instanceID] || ownership == "" {
			t.Fatalf("provider object %s has invalid bounded ownership labels %#v", objectID, labels)
		}
		if ownershipTokens[ownership] {
			t.Fatalf("provider ownership token %q was reused", ownership)
		}
		ownershipTokens[ownership] = true
		counts[instanceID]++
	}
	if len(engine.objects) != 4*len(ids) || len(counts) != len(ids) {
		t.Fatalf("provider ownership distribution: objects=%d instances=%d, want %d and %d", len(engine.objects), len(counts), 4*len(ids), len(ids))
	}
	for instanceID, count := range counts {
		if count != 4 {
			t.Fatalf("instance %s owns %d provider objects, want 4", instanceID, count)
		}
	}
}

func assertClassroomTerminalState(t *testing.T, ctx context.Context, store *localdb.Store, db *sql.DB) {
	t.Helper()
	snapshot, err := store.ReadOperationalSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Jobs.Queued != 0 || snapshot.Jobs.Running != 0 || snapshot.Jobs.RecoveryRequired != 0 ||
		snapshot.Jobs.OldestQueuedAge != 0 || snapshot.Jobs.OldestExpiredLeaseAge != 0 {
		t.Fatalf("durable work remained after classroom destroy: %#v", snapshot.Jobs)
	}
	if snapshot.Cleanup.UnknownResources != 0 || snapshot.Cleanup.FailedResources != 0 || snapshot.Cleanup.OrphanedAllocations != 0 {
		t.Fatalf("cleanup telemetry after classroom destroy: %#v", snapshot.Cleanup)
	}
	if countOperationalState(snapshot.Operations, domain.OperationSucceeded) != 2*controlplane.MaximumInstanceBatchSize {
		t.Fatalf("operation terminal counts = %#v", snapshot.Operations)
	}
	if countResourcePhase(snapshot.Resources, domain.ResourceDestroyed) != 6*controlplane.MaximumInstanceBatchSize {
		t.Fatalf("resource terminal counts = %#v", snapshot.Resources)
	}
	for queryName, query := range map[string]string{
		"quota reservations": `SELECT COUNT(*) FROM labcentral_quota_reservations WHERE state <> 'released' OR reconciliation_required <> 0`,
		"quota usage":        `SELECT COUNT(*) FROM labcentral_quota_usage WHERE reserved_value <> 0 OR committed_value <> 0 OR retained_value <> 0`,
		"allocation claims":  `SELECT COUNT(*) FROM labcentral_provider_allocation_claims WHERE state <> 'released'`,
	} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("read %s: %v", queryName, err)
		}
		if count != 0 {
			t.Fatalf("active %s after classroom destroy = %d", queryName, count)
		}
	}
}

func assertClassroomCompletions(t *testing.T, completions []classroomCompletion) {
	t.Helper()
	if len(completions) != 2*controlplane.MaximumInstanceBatchSize {
		t.Fatalf("worker completion observations = %d, want %d", len(completions), 2*controlplane.MaximumInstanceBatchSize)
	}
	counts := map[controlplane.OperationKind]int{}
	for _, completed := range completions {
		if completed.state != domain.OperationSucceeded || completed.duration <= 0 || completed.duration > productionClassroomObjective {
			t.Fatalf("invalid worker completion observation %#v", completed)
		}
		counts[completed.kind]++
	}
	if counts[controlplane.OperationApply] != controlplane.MaximumInstanceBatchSize || counts[controlplane.OperationDestroy] != controlplane.MaximumInstanceBatchSize {
		t.Fatalf("worker completion counts = %#v", counts)
	}
}

func countOperationalState(counts []application.OperationStateCount, state domain.OperationState) uint64 {
	var total uint64
	for _, count := range counts {
		if count.State == state {
			total += count.Count
		}
	}
	return total
}

func countResourcePhase(counts []application.ResourcePhaseCount, phase domain.ResourcePhase) uint64 {
	var total uint64
	for _, count := range counts {
		if count.Phase == phase {
			total += count.Count
		}
	}
	return total
}
