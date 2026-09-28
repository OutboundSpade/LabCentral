package hosted

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestQueueIncludesOldJobsAndPrioritizesDestruction(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	first, e := st.Enqueue(ctx, "build", "old")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 60; i++ {
		if _, e = st.Enqueue(ctx, "refresh", fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
	j, e := st.NextJob(ctx)
	if e != nil || j.ID != first.ID {
		t.Fatal(j, e)
	}
	end, e := st.Enqueue(ctx, "end", "meeting")
	if e != nil {
		t.Fatal(e)
	}
	j, e = st.NextJob(ctx)
	if e != nil || j.ID != end.ID {
		t.Fatal(j, e)
	}
}
func TestOverdueMeetingCancelsProvisioningAndQueuesCleanup(t *testing.T) {
	s, _, _ := serviceFor(t)
	setupMeeting(t, s.Store)
	ctx := context.Background()
	m, _ := s.Store.Meeting(ctx)
	m.Expires = time.Now().Add(-time.Minute)
	_ = s.Store.Put(ctx, "meeting", "current", m)
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.jobCancel = cancel
	s.jobKind = "prepare"
	s.enforceDeadline(ctx)
	if jobCtx.Err() == nil {
		t.Fatal("provisioning survived expiry")
	}
	m, _ = s.Store.Meeting(ctx)
	j, e := s.Store.NextJob(ctx)
	if e != nil || m.Status != "ending" || j.Kind != "end" {
		t.Fatal(m, j, e)
	}
	s.enforceDeadline(ctx)
	jobs, _ := s.Store.Jobs(ctx)
	if len(jobs) != 1 {
		t.Fatal("duplicate cleanup after reconciliation")
	}
}
func TestDesktopRecoveryRetainsUnknownOutcomes(t *testing.T) {
	s, v, engine := serviceFor(t)
	ctx := context.Background()
	record := accessRecord{ID: "unresolved", UserID: v.User.ID}
	_ = s.Store.Put(ctx, "desktop", record.ID, record)
	engine.closeError = errors.New("response lost")
	if s.recoverAccess(ctx) == nil {
		t.Fatal("reported unknown cleanup complete")
	}
	if s.Store.Get(ctx, "desktop", record.ID, new(accessRecord)) != nil {
		t.Fatal("lost cleanup ownership")
	}
	engine.closeError = nil
	if e := s.recoverAccess(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.recoverAccess(ctx); e != nil {
		t.Fatal(e)
	}
	if engine.closed != 2 {
		t.Fatal("repeated completed cleanup", engine.closed)
	}
}
