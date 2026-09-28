package hosted

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/oauth2"
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

type blockedIdentity struct {
	Identity
	entered chan struct{}
	release chan struct{}
}

func (i blockedIdentity) Refresh(ctx context.Context, _ *oauth2.Token) (User, *oauth2.Token, error) {
	i.entered <- struct{}{}
	select {
	case <-i.release:
		return User{}, nil, errors.New("identity unavailable")
	case <-ctx.Done():
		return User{}, nil, ctx.Err()
	}
}
func TestAllDesktopsFailClosedWithoutWaitingForGuestCleanup(t *testing.T) {
	s, _, engine := serviceFor(t)
	identity := blockedIdentity{entered: make(chan struct{}, 20), release: make(chan struct{})}
	s.Identity = identity
	ctx := context.Background()
	var desktops []*desktopSession
	for i := 0; i < 20; i++ {
		id := fmt.Sprint(i)
		session := Session{ID: id, User: User{ID: id}, Expires: time.Now().Add(time.Hour), Checked: time.Now().Add(-5 * time.Minute)}
		if err := s.Store.Put(ctx, "session", id, session); err != nil {
			t.Fatal(err)
		}
		dctx, cancel := context.WithCancel(ctx)
		defer cancel()
		record := accessRecord{ID: id, UserID: id, SessionID: id, Expires: time.Now().Add(time.Hour)}
		d := &desktopSession{record: record, ctx: dctx, cancel: cancel}
		s.desktops[id] = d
		desktops = append(desktops, d)
		if err := s.Store.Put(ctx, "desktop", id, record); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { s.revalidateDesktops(ctx); close(done) }()
	defer close(identity.release)
	for i := 0; i < 20; i++ {
		select {
		case <-identity.entered:
		case <-time.After(time.Second):
			t.Fatal("identity refresh serialized across unrelated users")
		}
	}
	for i := 0; i < 20; i++ {
		identity.release <- struct{}{}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revocation waited for infrastructure cleanup")
	}
	for _, d := range desktops {
		if d.ctx.Err() == nil {
			t.Fatal("unauthorized transport remains active")
		}
	}
	if engine.closed != 0 {
		t.Fatal("authorization loop performed blocking guest cleanup")
	}
	job, err := s.Store.NextJob(ctx)
	if err != nil || job == nil || job.Kind != "access-cleanup" {
		t.Fatal(job, err)
	}
	if err := s.recoverAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if engine.closed != 20 || len(s.desktops) != 0 {
		t.Fatal("durable cleanup did not close revoked sessions", engine.closed)
	}
}
