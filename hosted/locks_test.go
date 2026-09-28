package hosted

import (
	"context"
	"errors"
	"fmt"
	"testing"
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
