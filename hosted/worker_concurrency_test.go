package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
	"testing"
	"time"
)

type isolatedCleanupEngine struct {
	Engine
	fail  bool
	calls []string
}

func (e *isolatedCleanupEngine) CloseAccess(_ context.Context, id string) error {
	e.calls = append(e.calls, id)
	if e.fail && id == "a-unknown" {
		return errors.New("guest revocation outcome unknown")
	}
	return nil
}

func TestAccessRecoveryFailureDoesNotStrandOtherUsers(t *testing.T) {
	s, _, _ := serviceFor(t)
	ctx := context.Background()
	e := &isolatedCleanupEngine{fail: true}
	s.Engine = e
	for _, id := range []string{"a-unknown", "b-independent"} {
		if err := s.Store.Put(ctx, "desktop", id, accessRecord{ID: id, UserID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.recoverAccess(ctx); err == nil {
		t.Fatal("unknown guest cleanup reported complete")
	}
	if len(e.calls) != 2 {
		t.Fatal("one unknown outcome stranded another user", e.calls)
	}
	if err := s.Store.Get(ctx, "desktop", "a-unknown", new(accessRecord)); err != nil {
		t.Fatal("unknown cleanup intent lost", err)
	}
	if err := s.Store.Get(ctx, "desktop", "b-independent", new(accessRecord)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("confirmed cleanup was not persisted", err)
	}
	// Reopen the store to verify recovery retains only unresolved ownership.
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered := &Service{Store: reopened, Engine: e, desktops: map[string]*desktopSession{}}
	e.fail = false
	if err = recovered.recoverAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 3 || e.calls[2] != "a-unknown" {
		t.Fatal("recovery repeated confirmed cleanup", e.calls)
	}
}

type blockedPracticeEngine struct {
	Engine
	started chan string
	release chan struct{}
}

func (e *blockedPracticeEngine) Inspect(context.Context, string) (lc.Instance, error) {
	return lc.Instance{}, sql.ErrNoRows
}

func (e *blockedPracticeEngine) DeployWithID(ctx context.Context, p lc.Plan, id string) (lc.Instance, error) {
	e.started <- id
	select {
	case <-e.release:
		return lc.Instance{ID: id, Plan: p, Status: "ready"}, nil
	case <-ctx.Done():
		return lc.Instance{}, ctx.Err()
	}
}

func TestPracticePreparationStopsAtAllocationDeadline(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(100 * time.Millisecond)
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE practice_runs SET expires=? WHERE id=?`, expires.UTC().Format(time.RFC3339Nano), run.ID); err != nil {
		t.Fatal(err)
	}
	before, err := s.Store.practice(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Engine = &blockedPracticeEngine{started: make(chan string, 1), release: make(chan struct{})}
	start := time.Now()
	if err = s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("preparation did not observe allocation expiry", err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("expired preparation waited for outer job timeout")
	}
	after, err := s.Store.practice(ctx, run.ID)
	if err != nil || after.Status != "ending" || after.InstanceID != before.InstanceID || after.EndedAt != nil {
		t.Fatal("expiry lost pending native cleanup identity", after, err)
	}
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := 0
	for _, job := range jobs {
		if job.Kind == "practice-end" && job.Target == run.ID {
			cleanup++
		}
	}
	if cleanup != 1 {
		t.Fatal("expired preparation did not persist one cleanup intent", cleanup)
	}
	quota, err := s.Store.quota(ctx, v.User.ID, 180, time.Now().UTC())
	if err != nil || quota.UsedSeconds < 1 {
		t.Fatal("pending cleanup stopped quota accounting", quota, err)
	}
}

func TestPersonalPreparationsOverlapWithoutBlockingStudentProgress(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := &blockedPracticeEngine{started: make(chan string, 4), release: make(chan struct{})}
	s.Engine = e
	for n := 0; n < 4; n++ {
		user := fmt.Sprint(n)
		if n == 0 {
			user = v.User.ID
		}
		if _, err := s.Store.reservePractice(ctx, user, p.ID, 180, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	done := make(chan error, 6)
	defer func() { cancel(); workers.Wait() }()
	start := time.Now()
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		select {
		case <-e.started:
		case <-ctx.Done():
			t.Fatal("independent personal provisioning serialized")
		}
	}
	t.Logf("three independent native preparations entered in %s", time.Since(start))
	var queued int
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='practice-prepare' AND status='queued'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("personal preparation bypassed bulk capacity", queued, err)
	}
	// Authentication, private-data filtering and durable answer progress remain
	// available while all three native calls are deliberately blocked.
	responsive := make(chan error, 1)
	go func() {
		w := request(s, "GET", "/api/learning/linux", "", "", "")
		if w.Code != 200 {
			responsive <- fmt.Errorf("learning view status %d: %s", w.Code, w.Body.String())
			return
		}
		w = request(s, "POST", "/api/learning/linux/answer", `{"stepID":"identify","answer":"PRIVATE-ANSWER-SENTINEL"}`, s.Config.Origin, v.CSRF)
		if w.Code != 200 {
			responsive <- fmt.Errorf("answer status %d: %s", w.Code, w.Body.String())
			return
		}
		responsive <- nil
	}()
	select {
	case err := <-responsive:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("blocked provisioning held shared request locks")
	}
	progress, err := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if err != nil || progress.CompletedSteps != 1 {
		t.Fatal("concurrent native work lost answer persistence", progress, err)
	}
	close(e.release)
	for n := 0; n < 3; n++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("native preparations did not finish")
		}
	}
}

func TestDesktopRevalidationDoesNotHoldRelayMutexWhileWaitingForStore(t *testing.T) {
	s, v, _ := serviceFor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.Put(ctx, "desktop", "pending", accessRecord{ID: "pending", UserID: v.User.ID}); err != nil {
		t.Fatal(err)
	}
	s.tickets["expired"] = launchTicket{Expires: time.Now().Add(-time.Minute)}
	s.tickets["valid"] = launchTicket{Expires: time.Now().Add(time.Hour)}
	// Occupy the configured sole SQL connection rather than approximating a
	// slow query. WaitCount below proves the policy loop reached the blocked read.
	conn, err := s.Store.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Store.DB.Stats().WaitCount
	done := make(chan struct{})
	go func() { s.revalidateDesktops(ctx); close(done) }()
	defer func() {
		_ = conn.Close()
		cancel()
		<-done
	}()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.Store.DB.Stats().WaitCount == before {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("revalidation did not reach the blocked SQL read")
		}
	}
	if !s.desktopMu.TryLock() {
		t.Fatal("blocked SQL read held unrelated desktop/ticket mutex")
	}
	s.tickets["unrelated-launch"] = launchTicket{Expires: time.Now().Add(time.Hour)}
	s.desktopMu.Unlock()
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revalidation did not resume after SQL connection release")
	}
	s.desktopMu.Lock()
	_, expired := s.tickets["expired"]
	_, valid := s.tickets["valid"]
	_, unrelated := s.tickets["unrelated-launch"]
	s.desktopMu.Unlock()
	if expired || !valid || !unrelated {
		t.Fatal("ticket expiry lost concurrent valid tickets")
	}
	job, err := s.Store.NextJob(ctx)
	if err != nil || job == nil || job.Kind != "access-cleanup" || job.Target != "pending" {
		t.Fatal("pending cleanup signal lost after lock separation", job, err)
	}
}
