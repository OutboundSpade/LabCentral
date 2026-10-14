package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriterTransactionDoesNotBlockReadPool(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.Put(ctx, "probe", "value", "before"); err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE objects SET body='"after"' WHERE kind='probe'`); err != nil {
		t.Fatal(err)
	}
	read, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	var value string
	if err = st.Get(read, "probe", "value", &value); err != nil || value != "before" {
		t.Fatalf("reader blocked or observed uncommitted write: %q %v", value, err)
	}
	if _, err = st.DB.read.ExecContext(ctx, `DELETE FROM objects`); err == nil {
		t.Fatal("read pool permits mutation")
	}
	// Every writing transaction reserves the single writer immediately; cancellation
	// cannot leave a waiter consuming a connection or retry a stale snapshot.
	waiting, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if _, err = st.DB.BeginTx(waiting, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = st.Get(ctx, "probe", "value", &value); err != nil || value != "after" {
		t.Fatal(value, err)
	}
	var syncLevel int
	var mode string
	if err = st.DB.DB.QueryRow(`PRAGMA synchronous`).Scan(&syncLevel); err != nil || syncLevel != 2 {
		t.Fatal(syncLevel, err)
	}
	if err = st.DB.read.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatal(mode, err)
	}
}
func TestUserWaitCancellationReleasesReferences(t *testing.T) {
	s, _, _ := serviceFor(t)
	release, err := s.lockUser(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.lockUser(ctx, "same"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	s.userMu.Lock()
	remaining := len(s.userLocks)
	s.userMu.Unlock()
	if remaining != 0 {
		t.Fatalf("retained %d unused user locks", remaining)
	}
}

type blockedDesktopEngine struct {
	Engine
	entered, release chan struct{}
	info             lc.AccessInfo
	closes           atomic.Int32
}

func (e *blockedDesktopEngine) OpenDesktopWithOptions(ctx context.Context, _, _ string, _ lc.AccessOptions) (lc.AccessInfo, error) {
	close(e.entered)
	select {
	case <-ctx.Done():
		return lc.AccessInfo{}, ctx.Err()
	case <-e.release:
		return e.info, nil
	}
}
func (e *blockedDesktopEngine) CloseAccess(context.Context, string) error {
	e.closes.Add(1)
	return nil
}
func desktopIntentFixture(t *testing.T) (*Service, Session, Team, *blockedDesktopEngine) {
	s, v, _ := serviceFor(t)
	ctx := context.Background()
	teams := setupMeeting(t, s.Store)
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready',instance_id='instance'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "relay", Value: "test"})
		w.WriteHeader(303)
	}))
	t.Cleanup(relay.Close)
	engine := &blockedDesktopEngine{Engine: s.Engine, entered: make(chan struct{}), release: make(chan struct{}), info: lc.AccessInfo{ID: digest("instance\x00workstation\x00" + v.User.ID)[:24], URL: relay.URL + "/launch", Expires: v.Expires}}
	s.Engine = engine
	return s, v, Team{ID: teams[0].ID, InstanceID: "instance"}, engine
}
func TestCleanupSnapshotCannotRevokeFreshOpen(t *testing.T) {
	s, v, team, e := desktopIntentFixture(t)
	ctx := context.Background()
	// An old expired record has the exact identity of the new launch.
	old := accessRecord{e.info.ID, v.User.ID, v.ID, team.ID, team.InstanceID, time.Now().Add(-time.Minute)}
	if err := s.Store.Put(ctx, "desktop", old.ID, old); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.launchDesktop(w, httptest.NewRequest("POST", "/api/access", nil), v, team, v.Expires, "workstation")
		close(done)
	}()
	<-e.entered
	lockCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	release, err := s.lockUser(lockCtx, v.User.ID)
	if err != nil {
		t.Fatal("native open held user lock", err)
	}
	release()
	if err = s.recoverAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if e.closes.Load() != 0 {
		t.Fatal("expiry cleanup raced a fresh open")
	}
	close(e.release)
	<-done
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.desktopMu.Lock()
	d := s.desktops[old.ID]
	s.desktopMu.Unlock()
	if d == nil || !d.record.Expires.After(time.Now()) || d.ctx.Err() != nil {
		t.Fatal("fresh relay was lost")
	}
	d.cancel()
}
func TestRevocationDuringOpenRetainsCleanupUntilConfirmation(t *testing.T) {
	s, v, team, e := desktopIntentFixture(t)
	ctx := context.Background()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.launchDesktop(w, httptest.NewRequest("POST", "/api/access", nil), v, team, v.Expires, "workstation")
		close(done)
	}()
	<-e.entered
	if err := s.Store.BeginSwitch(ctx, v.User.ID, "", "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.revokeUser(ctx, v.User.ID); !errors.Is(err, errDesktopBusy) {
		t.Fatal(err)
	}
	close(e.release)
	<-done
	if w.Code != 503 {
		t.Fatal("revoked open published a launch ticket", w.Code)
	}
	var record accessRecord
	if err := s.Store.Get(ctx, "desktop", e.info.ID, &record); err != nil {
		t.Fatal("cleanup intent disappeared", err)
	}
	if err := s.recoverAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if e.closes.Load() != 1 {
		t.Fatal(e.closes.Load())
	}
	if err := s.Store.Get(ctx, "desktop", e.info.ID, &record); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestLearningActivityBackoffAndImmediateDeduplication(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	old := time.Now().Add(-6 * time.Minute)
	if err := s.nextLearningCheck(ctx, v.User.ID, p.ID, run.ID, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	var due time.Time
	if err := s.Store.Get(ctx, "learning-check-due", run.ID, &due); err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(due); delay < 53*time.Second || delay > 67*time.Second {
		t.Fatal("idle interval", delay)
	}
	if err := s.touchLearning(ctx, v.User.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.nextLearningCheck(ctx, v.User.ID, p.ID, run.ID, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Get(ctx, "learning-check-due", run.ID, &due); err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(due); delay < 17*time.Second || delay > 23*time.Second {
		t.Fatal("active interval", delay)
	}
	// Manual enqueue ignores the future periodic due time, but remains deduplicated.
	var id string
	for n := range 2 {
		check, err := s.queueLearningCheck(ctx, v.User.ID, p.ID, "restore", run.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			id = check.ID
		} else if check.ID != id {
			t.Fatal("check now duplicated pending work")
		}
	}
	a := learningInterval("run-a", true)
	b := learningInterval("run-b", true)
	if a == b {
		t.Fatal("cohort checks aligned")
	}
}

func TestAggregateNativeAndSSHLimitsAndCancelledWaiters(t *testing.T) {
	s, _, _ := serviceFor(t)
	s.Config.Aggregate = AggregateOptions{MaxNativeOperations: 2, MaxSSHSessions: 2}
	p := &loadProvider{latency: 10 * time.Millisecond}
	wrapped, err := NewBudgetedProvider(p, s.Config.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	s.sshSlots = wrapped.sshSlots
	var ssh loadCounter
	var wg sync.WaitGroup
	for n := range 20 {
		wg.Go(func() {
			_, err := wrapped.Execute(context.Background(), lc.Command{Action: "machine.create", Owner: fmt.Sprint(n), Machine: lc.PlannedMachine{Name: "server"}}, nil)
			if err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			release, err := s.acquireSSH(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			done := ssh.enter()
			time.Sleep(time.Millisecond)
			done()
			release()
		})
	}
	wg.Wait()
	if p.native.maximum.Load() != 2 || ssh.maximum.Load() > 2 {
		t.Fatal(p.native.maximum.Load(), ssh.maximum.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.acquireSSH(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = wrapped.Execute(ctx, lc.Command{Action: "machine.create"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, o := range []AggregateOptions{{MaxNativeOperations: -1}, {MaxNativeOperations: 65}, {MaxSSHSessions: -1}, {MaxSSHSessions: 65}} {
		if _, err = NewBudgetedProvider(p, o); err == nil {
			t.Fatal("accepted invalid budget", o)
		}
	}
}

func TestMachineClaimsKeepWholeTeamMutationExclusion(t *testing.T) {
	a := appendMachineClaims([]jobClaim{{"team:one", false}}, "instance", []GuestPlaybook{{Machine: "web"}, {Machine: "web"}})
	b := appendMachineClaims([]jobClaim{{"team:one", false}}, "instance", []GuestPlaybook{{Machine: "db"}})
	if len(a) != 2 || claimsConflict(a, b) {
		t.Fatal("unrelated machines were serialized", a, b)
	}
	if !claimsConflict(a, []jobClaim{{"team:one", true}}) || !claimsConflict(a, a) {
		t.Fatal("mutation exclusion was weakened")
	}
}
func TestDispatcherWakesOnEnqueueAndCompletion(t *testing.T) {
	s, teams := exerciseFixture(t)
	s.Config.Worker.MaxGuestJobs = 1
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	s.exerciseRunner = func(ctx context.Context, team Team, _ BreakRun, _ lc.Source) (string, error) {
		started <- team.ID
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "verified", nil
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	// Drain startup activity and then enqueue while the 5 s fallback is sleeping.
	for {
		jobs, err := s.Store.Jobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		settled := false
		for _, j := range jobs {
			if j.Kind == "access-cleanup" && j.Status == "complete" {
				settled = true
			}
		}
		if settled {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("startup did not settle")
		case <-time.After(time.Millisecond):
		}
	}
	start := time.Now()
	queueExercise(t, s, teams[0].ID, "reset")
	queueExercise(t, s, teams[1].ID, "reset")
	select {
	case <-started:
		if time.Since(start) > time.Second {
			t.Fatal("enqueue waited for fallback tick")
		}
	case <-ctx.Done():
		t.Fatal("dispatcher did not wake")
	}
	close(release)
	select {
	case <-started:
		if time.Since(start) > time.Second {
			t.Fatal("completion waited for fallback tick")
		}
	case <-ctx.Done():
		t.Fatal("completion did not wake dispatcher")
	}
}

func TestInterruptedObservationalCheckCanResume(t *testing.T) {
	s, teams := checkFixture(t)
	ctx := context.Background()
	j := failedCheck(t, s, teams[0])
	var run CheckRun
	if err := s.Store.Get(ctx, "check", j.Target, &run); err != nil {
		t.Fatal(err)
	}
	run.Status = "checking"
	if err := s.Store.Put(ctx, "check", run.ID, run); err != nil {
		t.Fatal(err)
	}
	engine := practiceEngineFor(t, s)
	if err := s.runCheck(ctx, j); err != nil {
		t.Fatal("observation could not resume", err)
	}
	if err := s.Store.Get(ctx, "check", run.ID, &run); err != nil || run.Status != "complete" || engine.checks != 1 {
		t.Fatal(run, engine.checks, err)
	}
}

func TestDesktopSSHLeaseIsSharedWithoutDoubleAdmission(t *testing.T) {
	s, _, _ := serviceFor(t)
	s.Config.Aggregate = AggregateOptions{MaxNativeOperations: 1, MaxSSHSessions: 1}
	native := &loadProvider{}
	provider, err := NewBudgetedProvider(native, s.Config.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	s.sshSlots = provider.sshSlots
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, release, err := s.sshContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Native access already owns a lease before taking the library machine guard;
	// the wrapper must recognize it rather than wait on its own occupied slot.
	if _, err = provider.Execute(lease, lc.Command{Action: "access.close"}, nil); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if len(provider.sshSlots) != 0 {
		t.Fatal("SSH lease leaked")
	}
	bad := AggregateOptions{MaxNativeOperations: 2, MaxSSHSessions: 2}
	c := Config{MemberGroup: "lab_members", AdminGroup: "lab_admins", Origin: "https://labs.invalid", DesktopOrigin: "https://desktop.invalid", Issuer: "https://oidc.invalid", Aggregate: bad}
	if _, err = New(ctx, c, nil, provider); err == nil {
		t.Fatal("mismatched aggregate provider budget accepted")
	}
}
