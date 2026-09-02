package jobsdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	_ "modernc.org/sqlite"
)

type allocationConformanceCase func(*testing.T, *Store)

func TestAllocationContract001AtomicReservation(t *testing.T) {
	runSQLiteCase(t, allocationContract001AtomicReservation)
}

func TestAllocationContract002IdempotentReplay(t *testing.T) {
	runSQLiteCase(t, allocationContract002IdempotentReplay)
}

func TestAllocationContract003TargetIsolation(t *testing.T) {
	runSQLiteCase(t, allocationContract003TargetIsolation)
}

func TestAllocationContract004ReleasedReuse(t *testing.T) {
	runSQLiteCase(t, allocationContract004ReleasedReuse)
}

func TestAllocationContract005Commit(t *testing.T) {
	runSQLiteCase(t, allocationContract005Commit)
}

func TestAllocationContract006UnknownRetention(t *testing.T) {
	runSQLiteCase(t, allocationContract006UnknownRetention)
}

func TestAllocationContract007OrphanRetention(t *testing.T) {
	runSQLiteCase(t, allocationContract007OrphanRetention)
}

func TestAllocationContract008AbsenceGatedRelease(t *testing.T) {
	runSQLiteCase(t, allocationContract008AbsenceGatedRelease)
}

func TestAllocationContract009RestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allocations.db")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rwc&_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_defensive=1"}).String()
	open := func() (*sql.DB, *Store) {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		store, err := New(db, DialectSQLite)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		return db, store
	}
	db, store := open()
	request := allocationRequest("restart", allocationKey("target-a", "proxmox.qemu.vmid", "430"))
	if err := store.ReserveClaims(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaims(context.Background(), mutateRequest(request)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, store = open()
	t.Cleanup(func() { _ = db.Close() })
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationCommitted)
}

func TestAllocationContract010ConcurrentWinner(t *testing.T) {
	runSQLiteCase(t, allocationContract010ConcurrentWinner)
}

func TestAllocationContract011InputBounds(t *testing.T) {
	runSQLiteCase(t, allocationContract011InputBounds)
}

func TestAllocationContract012StaleBinding(t *testing.T) {
	runSQLiteCase(t, allocationContract012StaleBinding)
}

func TestAllocationContract014StaleFence(t *testing.T) {
	runSQLiteCase(t, allocationContract014StaleFence)
}

func TestPostgreSQLAllocationConformance(t *testing.T) {
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	tests := []struct {
		name string
		run  allocationConformanceCase
	}{
		{"TEST-ALLOC-001", allocationContract001AtomicReservation},
		{"TEST-ALLOC-002", allocationContract002IdempotentReplay},
		{"TEST-ALLOC-003", allocationContract003TargetIsolation},
		{"TEST-ALLOC-004", allocationContract004ReleasedReuse},
		{"TEST-ALLOC-005", allocationContract005Commit},
		{"TEST-ALLOC-006", allocationContract006UnknownRetention},
		{"TEST-ALLOC-007", allocationContract007OrphanRetention},
		{"TEST-ALLOC-008", allocationContract008AbsenceGatedRelease},
		{"TEST-ALLOC-010", allocationContract010ConcurrentWinner},
		{"TEST-ALLOC-011", allocationContract011InputBounds},
		{"TEST-ALLOC-012", allocationContract012StaleBinding},
		{"TEST-ALLOC-014", allocationContract014StaleFence},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := newPostgreSQLTestStore(t, dsn, 100+index)
			testCase.run(t, store)
		})
	}
}

func allocationContract001AtomicReservation(t *testing.T, store *Store) {
	ctx := context.Background()
	occupied := allocationRequest("occupied", allocationKey("target-a", "proxmox.network.vlan", "200"))
	if err := store.ReserveClaims(ctx, occupied); err != nil {
		t.Fatal(err)
	}
	candidate := allocationRequest("candidate",
		allocationKey("target-a", "proxmox.network.vlan", "199"),
		allocationKey("target-a", "proxmox.network.vlan", "200"),
	)
	var conflict *application.AllocationConflictError
	if err := store.ReserveClaims(ctx, candidate); !errors.As(err, &conflict) || len(conflict.Conflicts) != 1 {
		t.Fatalf("reservation conflict = %#v, %v", conflict, err)
	}
	claims, err := store.ListClaims(ctx, candidate.Binding.InstanceID)
	if err != nil || len(claims) != 0 {
		t.Fatalf("partial claims = %#v, %v", claims, err)
	}
}

func allocationContract002IdempotentReplay(t *testing.T, store *Store) {
	request := allocationRequest("replay",
		allocationKey("target-a", "proxmox.qemu.vmid", "401"),
		allocationKey("target-a", "proxmox.network.vlan", "201"),
	)
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.ReserveClaims(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationHeld, application.AllocationHeld)
}

func allocationContract003TargetIsolation(t *testing.T, store *Store) {
	first := allocationRequest("target-one", allocationKey("target-a", "proxmox.qemu.vmid", "402"))
	second := allocationRequest("target-two", allocationKey("target-b", "proxmox.qemu.vmid", "402"))
	if err := store.ReserveClaims(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveClaims(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func allocationContract004ReleasedReuse(t *testing.T, store *Store) {
	ctx := context.Background()
	key := allocationKey("target-a", "proxmox.qemu.vmid", "403")
	first := allocationRequest("reuse-old", key)
	if err := store.ReserveClaims(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{
		MutateAllocationClaimsRequest: mutateRequest(first), ProviderAbsenceConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	var replayConflict *application.AllocationConflictError
	if err := store.ReserveClaims(ctx, first); !errors.As(err, &replayConflict) {
		t.Fatalf("released old plan replay returned %v", err)
	}
	second := allocationRequest("reuse-new", key)
	if err := store.ReserveClaims(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertClaimStates(t, store, second.Binding.InstanceID, application.AllocationHeld)
}

func allocationContract005Commit(t *testing.T, store *Store) {
	request := allocationRequest("commit",
		allocationKey("target-a", "proxmox.qemu.vmid", "404"),
		allocationKey("target-a", "proxmox.network.vlan", "204"),
	)
	if err := store.ReserveClaims(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaims(context.Background(), mutateRequest(request)); err != nil {
		t.Fatal(err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationCommitted, application.AllocationCommitted)
}

func allocationContract006UnknownRetention(t *testing.T, store *Store) {
	ctx := context.Background()
	key := allocationKey("target-a", "proxmox.qemu.vmid", "405")
	request := allocationRequest("unknown", key)
	if err := store.ReserveClaims(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaims(ctx, mutateRequest(request)); err != nil {
		t.Fatal(err)
	}
	contender := allocationRequest("unknown-contender", key)
	var conflict *application.AllocationConflictError
	if err := store.ReserveClaims(ctx, contender); !errors.As(err, &conflict) {
		t.Fatalf("unknown claim became available: %v", err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationCommitted)
}

func allocationContract007OrphanRetention(t *testing.T, store *Store) {
	ctx := context.Background()
	key := allocationKey("target-a", "proxmox.qemu.vmid", "406")
	request := allocationRequest("orphan", key)
	if err := store.ReserveClaims(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.OrphanClaims(ctx, mutateRequest(request)); err != nil {
		t.Fatal(err)
	}
	contender := allocationRequest("orphan-contender", key)
	var conflict *application.AllocationConflictError
	if err := store.ReserveClaims(ctx, contender); !errors.As(err, &conflict) {
		t.Fatalf("orphan claim became available: %v", err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationOrphaned)
}

func allocationContract008AbsenceGatedRelease(t *testing.T, store *Store) {
	ctx := context.Background()
	request := allocationRequest("release", allocationKey("target-a", "proxmox.qemu.vmid", "407"))
	if err := store.ReserveClaims(ctx, request); err != nil {
		t.Fatal(err)
	}
	mutation := mutateRequest(request)
	if err := store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{MutateAllocationClaimsRequest: mutation}); !errors.Is(err, ErrProviderAbsenceRequired) {
		t.Fatalf("release without absence returned %v", err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationHeld)
	if err := store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{MutateAllocationClaimsRequest: mutation, ProviderAbsenceConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationReleased)
}

func allocationContract010ConcurrentWinner(t *testing.T, store *Store) {
	key := allocationKey("target-a", "proxmox.network.vlan", "208")
	requests := []application.ReserveAllocationClaimsRequest{
		allocationRequest("concurrent-a", key), allocationRequest("concurrent-b", key),
	}
	start := make(chan struct{})
	var successes atomic.Int32
	var conflicts atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for _, request := range requests {
		request := request
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := store.ReserveClaims(context.Background(), request)
			if err == nil {
				successes.Add(1)
				return
			}
			var conflict *application.AllocationConflictError
			if errors.As(err, &conflict) {
				conflicts.Add(1)
				return
			}
			unexpected.Store(err)
		}()
	}
	close(start)
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected concurrent reservation error: %v", value)
	}
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("concurrent results = %d successes, %d conflicts", successes.Load(), conflicts.Load())
	}
}

func allocationContract011InputBounds(t *testing.T, store *Store) {
	ctx := context.Background()
	valid := allocationRequest("bounds", allocationKey("target-a", "proxmox.qemu.vmid", "409"))
	cases := []application.ReserveAllocationClaimsRequest{
		{Binding: valid.Binding},
		{Binding: valid.Binding, Claims: []application.AllocationKey{allocationKey("target-a", "UPPER", "1")}},
		{Binding: valid.Binding, Claims: []application.AllocationKey{allocationKey("target-a", "proxmox.qemu.vmid", "unsafe value")}},
		{Binding: valid.Binding, Claims: []application.AllocationKey{allocationKey("target-a", "proxmox.qemu.vmid", "1"), allocationKey("target-b", "proxmox.qemu.vmid", "2")}},
	}
	for index, request := range cases {
		if err := store.ReserveClaims(ctx, request); err == nil {
			t.Fatalf("invalid allocation case %d was accepted", index)
		}
	}
	claims, err := store.ListClaims(ctx, valid.Binding.InstanceID)
	if err != nil || len(claims) != 0 {
		t.Fatalf("invalid allocation requests mutated state: %#v, %v", claims, err)
	}
}

func allocationContract012StaleBinding(t *testing.T, store *Store) {
	ctx := context.Background()
	request := allocationRequest("stale", allocationKey("target-a", "proxmox.qemu.vmid", "410"))
	if err := store.ReserveClaims(ctx, request); err != nil {
		t.Fatal(err)
	}
	stale := mutateRequest(request)
	stale.Binding.PlanDigest = allocationDigest("different-plan")
	if err := store.CommitClaims(ctx, stale); !errors.Is(err, ErrAllocationBinding) {
		t.Fatalf("stale binding transition returned %v", err)
	}
	assertClaimStates(t, store, request.Binding.InstanceID, application.AllocationHeld)
}

func allocationContract014StaleFence(t *testing.T, store *Store) {
	ctx := context.Background()
	request := allocationRequest("fence", allocationKey("target-a", "proxmox.qemu.vmid", "411"))
	if err := store.ReserveClaims(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitClaims(ctx, mutateRequest(request)); err != nil {
		t.Fatal(err)
	}
	current := mutateRequest(request)
	current.FencingToken = 5
	if err := store.RetainClaims(ctx, current); err != nil {
		t.Fatal(err)
	}
	stale := mutateRequest(request)
	stale.FencingToken = 4
	if err := store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{
		MutateAllocationClaimsRequest: stale, ProviderAbsenceConfirmed: true,
	}); !errors.Is(err, ErrAllocationFence) {
		t.Fatalf("stale allocation fence returned %v", err)
	}
	claims, err := store.ListClaims(ctx, request.Binding.InstanceID)
	if err != nil || len(claims) != 1 || claims[0].State != application.AllocationCommitted || claims[0].FencingToken != 5 {
		t.Fatalf("claim after stale fence = %#v, %v", claims, err)
	}
	fresh := mutateRequest(request)
	fresh.FencingToken = 6
	if err := store.ReleaseClaims(ctx, application.ReleaseAllocationClaimsRequest{
		MutateAllocationClaimsRequest: fresh, ProviderAbsenceConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func allocationRequest(suffix string, claims ...application.AllocationKey) application.ReserveAllocationClaimsRequest {
	return application.ReserveAllocationClaimsRequest{
		Binding: application.AllocationBinding{
			InstanceID: domain.InstanceID("instance-" + suffix),
			ResourceID: domain.LogicalResourceID("vm." + suffix),
			PlanDigest: allocationDigest("plan-" + suffix),
		},
		Claims: claims,
	}
}

func allocationDigest(value string) domain.SHA256Digest {
	sum := sha256.Sum256([]byte(value))
	return domain.SHA256Digest(hex.EncodeToString(sum[:]))
}

func mutateRequest(request application.ReserveAllocationClaimsRequest) application.MutateAllocationClaimsRequest {
	return application.MutateAllocationClaimsRequest{Binding: request.Binding, Claims: request.Claims}
}

func allocationKey(target, namespace, value string) application.AllocationKey {
	return application.AllocationKey{TargetID: domain.ProviderTargetID(target), Namespace: namespace, Value: value}
}

func assertClaimStates(t *testing.T, store *Store, instanceID domain.InstanceID, expected ...application.AllocationClaimState) {
	t.Helper()
	claims, err := store.ListClaims(context.Background(), instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != len(expected) {
		t.Fatalf("claim count = %d, want %d: %#v", len(claims), len(expected), claims)
	}
	for index, state := range expected {
		if claims[index].State != state {
			t.Fatalf("claim %d state = %q, want %q", index, claims[index].State, state)
		}
	}
}

func TestAllocationMigrationChecksumRemainsStable(t *testing.T) {
	store := newSQLiteTestStore(t)
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM labcentral_schema_migrations
		WHERE id IN (?, ?)`, migrationID, allocationMigrationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
	}
	if got := len(store.allocationMigrationStatements()); got != 2 {
		t.Fatalf("allocation migration statements = %d", got)
	}
}
