package hosted

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func dispatchCleanup(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	var workers sync.WaitGroup
	done := make(chan error, 16)
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("cleanup did not run")
	}
}

func TestAccessCleanupBusySweepDefersToPolicy(t *testing.T) {
	s, v, team, e := desktopIntentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	opened := make(chan struct{})
	go func() {
		defer close(opened)
		s.launchDesktop(w, httptest.NewRequest("POST", "/api/access", nil), v, team, v.Expires, "workstation")
	}()
	<-e.entered
	defer func() {
		select {
		case <-opened:
		default:
			close(e.release)
			<-opened
		}
	}()
	// A revoke fences the opening generation but cannot clean it until the
	// in-flight native grant returns. Independent records must still be visited.
	if err := s.revokeUser(ctx, v.User.ID); !errors.Is(err, errDesktopBusy) {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "desktop", "independent", accessRecord{ID: "independent", UserID: "other"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Store.Enqueue(ctx, "access-cleanup", "pending")
	if err != nil {
		t.Fatal(err)
	}
	dispatchCleanup(t, s, ctx)
	jobs, err := s.Store.Jobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Status != "complete" || jobs[0].Error != "" {
		t.Fatal("busy cleanup failed or immediately re-enqueued", jobs, err)
	}
	if e.closes.Load() != 1 {
		t.Fatal("busy record closed or independent record skipped", e.closes.Load())
	}
	if err = s.Store.Get(ctx, "desktop", e.info.ID, new(accessRecord)); err != nil {
		t.Fatal("busy intent lost", err)
	}
	if err = s.Store.Get(ctx, "desktop", "independent", new(accessRecord)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("independent intent retained", err)
	}
	// The scheduler's immediate completion wake must not create a retry loop.
	var workers sync.WaitGroup
	if err = s.dispatchJobs(ctx, &workers, make(chan error, 1)); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if queued, err := s.Store.QueuedJobs(ctx); err != nil || len(queued) != 0 {
		t.Fatal("hot retry", queued, err)
	}
	// Policy checks retain just one pending sweep, even while the native call
	// remains busy. In production these checks occur every 20 seconds.
	s.revalidateDesktops(ctx)
	s.revalidateDesktops(ctx)
	queued, err := s.Store.QueuedJobs(ctx)
	if err != nil || len(queued) != 1 {
		t.Fatal("policy retry missing or duplicated", queued, err)
	}
	close(e.release)
	<-opened
	if w.Code != 503 {
		t.Fatal("revoked grant published", w.Code)
	}
	dispatchCleanup(t, s, ctx)
	if e.closes.Load() != 2 {
		t.Fatal("deferred record not cleaned", e.closes.Load())
	}
	if err = s.Store.Get(ctx, "desktop", e.info.ID, new(accessRecord)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("confirmed cleanup retained", err)
	}
	jobs, err = s.Store.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Status != "complete" || j.Error != "" {
			t.Fatal("busy cleanup needs attention", j)
		}
	}
	s.revalidateDesktops(ctx)
	if queued, err = s.Store.QueuedJobs(ctx); err != nil || len(queued) != 0 {
		t.Fatal("confirmed absence retried", queued, err)
	}
}

func TestAccessCleanupUnknownStillFailsJobAndPersists(t *testing.T) {
	for _, nativeErr := range []error{errors.New("native outcome unknown"), errors.Join(errDesktopBusy, errors.New("native outcome unknown"))} {
		t.Run(nativeErr.Error(), func(t *testing.T) {
			s, v, e := serviceFor(t)
			ctx := context.Background()
			e.closeError = nativeErr
			if err := s.Store.Put(ctx, "desktop", "unknown", accessRecord{ID: "unknown", UserID: v.User.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Store.Enqueue(ctx, "access-cleanup", "pending"); err != nil {
				t.Fatal(err)
			}
			dispatchCleanup(t, s, ctx)
			jobs, err := s.Store.Jobs(ctx)
			if err != nil || len(jobs) != 1 || jobs[0].Status != "failed" || jobs[0].Error != e.closeError.Error() {
				t.Fatal("unknown outcome hidden", jobs, err)
			}
			reopened, err := OpenStore(s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err = reopened.Get(ctx, "desktop", "unknown", new(accessRecord)); err != nil {
				t.Fatal("unknown intent lost", err)
			}
			var op desktopOperation
			if err = reopened.Get(ctx, "desktop-operation", "unknown", &op); err != nil || op.Status != "closing" {
				t.Fatal("closing intent lost", op, err)
			}
		})
	}
}

func TestAccessCleanupPolicyRetriesClosingIntentBesideActiveRelay(t *testing.T) {
	s, v, team, e := desktopIntentFixture(t)
	ctx := context.Background()
	// A reconnect can retain its old healthy relay while the new native grant
	// settles. Its enqueue may have coalesced with an already-running sweep.
	record := accessRecord{ID: e.info.ID, UserID: v.User.ID, SessionID: v.ID, TeamID: team.ID, InstanceID: team.InstanceID, Expires: v.Expires}
	relayCtx, stop := context.WithCancel(ctx)
	defer stop()
	s.desktops[record.ID] = &desktopSession{record: record, ctx: relayCtx, cancel: stop}
	if err := s.Store.Put(ctx, "desktop", record.ID, record); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "desktop-operation", record.ID, desktopOperation{Generation: "grant", Status: "closing"}); err != nil {
		t.Fatal(err)
	}
	s.revalidateDesktops(ctx)
	queued, err := s.Store.QueuedJobs(ctx)
	if err != nil || len(queued) != 1 {
		t.Fatal("active relay hid closing intent", queued, err)
	}
	dispatchCleanup(t, s, ctx)
	if relayCtx.Err() == nil || e.closes.Load() != 1 {
		t.Fatal("closing intent was not reconciled")
	}
}
