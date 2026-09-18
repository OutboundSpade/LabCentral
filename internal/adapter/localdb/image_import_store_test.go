package localdb

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestSQLiteImageImportReservations(t *testing.T) {
	imageImportReservations(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLImageImportReservations(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "image_import_reservations")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	imageImportReservations(t, s)
}
func imageImportReservations(t *testing.T, s *Store) {
	ctx := context.Background()
	claims := make([]app.ExtensionWorkClaim, 2)
	for i := range claims {
		id := fmt.Sprintf("import.%d", i)
		_, err := s.CreateExtensionWork(ctx, app.CreateExtensionWork{GovernanceRecordIDs: governanceIDs(id), IdempotencyKey: id, RequestDigest: testRequestDigest(id), Work: app.ExtensionWork{ID: domain.OperationID(id), ProjectID: "project.local", ActorID: app.LocalAdminID, InvocationID: domain.InvocationID("inv." + id), Request: app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkImageImport, PluginInstance: "core-import", BindingID: id, Import: &app.ImportWork{TargetID: "target.proxmox"}}}})
		if err != nil {
			t.Fatal(err)
		}
		claims[i], err = s.ClaimExtensionWork(ctx, "worker", "core-import/"+id, domain.WorkImageImport, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte(`{"vmid":123}`)
	hash := sha256.Sum256(raw)
	digest, _ := domain.ParseSHA256Digest(fmt.Sprintf("%x", hash))
	plan := app.ImageImportPlan{Opaque: raw, Digest: digest, Claims: []app.AllocationKey{{TargetID: "target.proxmox", Namespace: "proxmox.qemu.vmid", Value: "123"}}}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range claims {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = s.ReserveImageImportPlan(ctx, claims[i], plan) }(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range results {
		if err == nil {
			if winner != -1 {
				t.Fatal("two imports own same VMID")
			}
			winner = i
		} else {
			var conflict *app.AllocationConflictError
			if !errors.As(err, &conflict) {
				t.Fatal(err)
			}
		}
	}
	if winner < 0 {
		t.Fatal("no winner")
	}
	if err := s.ReserveImageImportPlan(ctx, claims[winner], plan); err != nil {
		t.Fatal("replay", err)
	}
	if _, err := s.GetImageImportPlan(ctx, claims[1-winner].Work.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatal("conflict retained plan", err)
	}
	stored, err := s.GetImageImportPlan(ctx, claims[winner].Work.ID)
	if err != nil || stored.Digest != digest {
		t.Fatal(stored, err)
	}
	occupied, err := s.ListTargetAllocationKeys(ctx, "target.proxmox")
	if err != nil || len(occupied) != 1 {
		t.Fatal(occupied, err)
	}
	stale := claims[winner]
	stale.Operation.FencingToken++
	if err := s.ReserveImageImportPlan(ctx, stale, plan); !errors.Is(err, jobsdb.ErrFencingConflict) {
		t.Fatal("accepted stale fence", err)
	}
	// An ordinary lab reservation must conflict with the same image claim.
	err = s.jobs.ReserveClaims(ctx, app.ReserveAllocationClaimsRequest{Binding: app.AllocationBinding{InstanceID: "instance.test", ResourceID: "vm.main", PlanDigest: digest}, Claims: plan.Claims})
	var conflict *app.AllocationConflictError
	if !errors.As(err, &conflict) {
		t.Fatal("lab failed to observe image reservation", err)
	}
}
