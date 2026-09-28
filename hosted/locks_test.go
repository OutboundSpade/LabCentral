package hosted

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/oauth2"
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
