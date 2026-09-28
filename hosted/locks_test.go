package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"golang.org/x/oauth2"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionLockCancellationAndReclamation(t *testing.T) {
	s, _, _ := serviceFor(t)
	ctx := context.Background()
	unlock, err := s.lockSession(ctx, "busy")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.lockSession(cancelled, "busy"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	other, err := s.lockSession(ctx, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	other()
	unlock()
	for i := 0; i < 100; i++ {
		if _, err := s.checkedSession(ctx, fmt.Sprint(i)); err == nil {
			t.Fatal("unknown session accepted")
		}
	}
	if len(s.authLocks) != 0 {
		t.Fatal("request locks retained after completion")
	}
}

type rotatingIdentity struct {
	Identity
	started, release chan struct{}
}

func (i rotatingIdentity) Refresh(ctx context.Context, _ *oauth2.Token) (User, *oauth2.Token, error) {
	close(i.started)
	select {
	case <-i.release:
		return User{ID: "student"}, &oauth2.Token{AccessToken: "new", RefreshToken: "rotated"}, nil
	case <-ctx.Done():
		return User{}, nil, ctx.Err()
	}
}
func TestCancelledBrowserRequestDoesNotLoseRotatedToken(t *testing.T) {
	s, _, _ := serviceFor(t)
	identity := rotatingIdentity{started: make(chan struct{}), release: make(chan struct{})}
	s.Identity = identity
	var session Session
	if err := s.Store.Get(context.Background(), "session", digest("cookie"), &session); err != nil {
		t.Fatal(err)
	}
	session.Checked = time.Now().Add(-5 * time.Minute)
	if err := s.Store.Put(context.Background(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.checkedSession(ctx, session.ID); done <- err }()
	<-identity.started
	cancel()
	close(identity.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Get(context.Background(), "session", session.ID, &session); err != nil {
		t.Fatal(err)
	}
	if session.Token.RefreshToken != "rotated" || time.Since(session.Checked) > time.Minute {
		t.Fatal("rotated token was not committed")
	}
}

func TestLogoutWaitsForRefreshAndCannotResurrectSession(t *testing.T) {
	s, _, _ := serviceFor(t)
	identity := rotatingIdentity{started: make(chan struct{}), release: make(chan struct{})}
	s.Identity = identity
	var session Session
	ctx := context.Background()
	if err := s.Store.Get(ctx, "session", digest("cookie"), &session); err != nil {
		t.Fatal(err)
	}
	session.Checked = time.Now().Add(-5 * time.Minute)
	if err := s.Store.Put(ctx, "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { _, err := s.checkedSession(ctx, session.ID); refreshed <- err }()
	<-identity.started
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { s.logout(w, httptest.NewRequest("POST", "/auth/logout", nil), session); close(done) }()
	// Do not release the refresh until logout has actually joined its lock.
	// Otherwise a lucky scheduler could make the unsafe implementation pass.
	deadline := time.After(time.Second)
	waiting := false
	for !waiting {
		s.authMu.Lock()
		waiting = s.authLocks[session.ID] != nil && s.authLocks[session.ID].users == 2
		s.authMu.Unlock()
		if waiting {
			break
		}
		select {
		case <-done:
			close(identity.release)
			<-refreshed
			t.Fatal("logout completed while refresh could still restore the session")
		case <-deadline:
			close(identity.release)
			<-refreshed
			<-done
			t.Fatal("logout did not reach the session lock")
		case <-time.After(time.Millisecond):
		}
	}
	close(identity.release)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	<-done
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := s.Store.Get(ctx, "session", session.ID, &session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("logout session remained", err)
	}
}
