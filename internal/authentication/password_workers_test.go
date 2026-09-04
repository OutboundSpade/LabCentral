package authentication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type dormantAuthRepository struct {
	application.AuthRepository
}

func TestPasswordWorkerLimitIsBoundedAndWaitingHonorsCancellation(t *testing.T) {
	keyring := &Keyring{
		current: sessionKey{id: "test", material: []byte("01234567890123456789012345678901")},
		keys:    []sessionKey{{id: "test", material: []byte("01234567890123456789012345678901")}},
	}
	if _, err := NewManager(dormantAuthRepository{}, keyring, ManagerOptions{PasswordWorkers: 17}); err == nil {
		t.Fatal("password worker count above its hard maximum was accepted")
	}
	manager, err := NewManager(dormantAuthRepository{}, keyring, ManagerOptions{PasswordWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if cap(manager.passwordSlots) != 1 {
		t.Fatalf("password worker semaphore capacity = %d, want 1", cap(manager.passwordSlots))
	}

	manager.passwordSlots <- struct{}{}
	defer func() { <-manager.passwordSlots }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- manager.CreateLocalUser(ctx, domain.UserID("user.waiting"), "waiting", "Waiting User", []byte("production-length-password"))
	}()
	select {
	case err := <-result:
		t.Fatalf("password operation bypassed the full worker semaphore: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled password operation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled password operation remained blocked on the worker semaphore")
	}
}
