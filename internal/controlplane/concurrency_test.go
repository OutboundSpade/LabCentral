package controlplane_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSQLiteTargetAdmission(t *testing.T) {
	store, err := localdb.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	testTargetAdmission(t, []*localdb.Store{store, store}, nil)
}

func TestPostgreSQLTargetAdmissionAcrossReplicas(t *testing.T) {
	open, _, _ := postgreSQLTestSchema(t, "admission")
	a, db := open()
	b, _ := open()
	testTargetAdmission(t, []*localdb.Store{a, b}, db)
}

func testTargetAdmission(t *testing.T, stores []*localdb.Store, db *sql.DB) {
	ctx := context.Background()
	provider, _, closeProvider := testProvider(t)
	defer closeProvider()
	service, err := controlplane.NewService(testRenderer(t), stores[0], providergrpc.New(provider), &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := service.CreateInstance(ctx, source, true, fmt.Sprintf("concurrency-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Persisted target settings override the startup default on every replica.
	settings, err := stores[0].PutDeploymentSettings(ctx, controlplane.LocalDockerTarget, application.DeploymentSettings{MaxConcurrentDeployments: 2})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := stores[1].GetDeploymentSettings(ctx, controlplane.LocalDockerTarget)
	if err != nil || loaded != settings {
		t.Fatalf("replica settings: %+v %v", loaded, err)
	}
	if _, err := stores[1].PutDeploymentSettings(ctx, controlplane.LocalDockerTarget, application.DeploymentSettings{MaxConcurrentDeployments: 16}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale write: %v", err)
	}
	limits := []application.TargetExecutionLimit{{TargetID: controlplane.LocalDockerTarget, Maximum: 1}}
	var mu sync.Mutex
	var claims []application.WorkClaim
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := stores[i%2].ClaimTargets(ctx, fmt.Sprintf("worker-%d", i), time.Minute, limits)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				claims = append(claims, claim)
			} else if !errors.Is(err, application.ErrNoWork) {
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(claims) != 2 {
		t.Fatalf("capacity admitted %d operations, want 2", len(claims))
	}
	// Expired work keeps its slot; recovery reuses it and advances the fence.
	for _, claim := range claims {
		if err := stores[0].Renew(ctx, fenceFromClaim(claim), time.Microsecond); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(2 * time.Millisecond)
	recovered, err := stores[1].ClaimTargets(ctx, "recovery", time.Minute, limits)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, claim := range claims {
		if recovered.Operation.ID == claim.Operation.ID {
			found = true
			if recovered.Operation.FencingToken <= claim.Operation.FencingToken {
				t.Fatal("recovery failed to advance fence")
			}
		}
	}
	if !found {
		t.Fatal("new work displaced expired operation")
	}
	// Reducing capacity does not strand recovery or permit new work.
	settings.MaxConcurrentDeployments = 1
	if _, err := stores[1].PutDeploymentSettings(ctx, controlplane.LocalDockerTarget, settings); err != nil {
		t.Fatal(err)
	}
	limits[0].Maximum = 16 // a stale local flag cannot bypass the stored limit.
	recovered2, err := stores[0].ClaimTargets(ctx, "recovery-two", time.Minute, limits)
	if err != nil {
		t.Fatal(err)
	}
	if recovered2.Operation.ID == recovered.Operation.ID {
		t.Fatal("claimed active lease")
	}
	if _, err := stores[0].ClaimTargets(ctx, "excess", time.Minute, limits); !errors.Is(err, application.ErrNoWork) {
		t.Fatalf("exceeded reduced capacity: %v", err)
	}
}
