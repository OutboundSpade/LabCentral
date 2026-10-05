package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAllocationClaimsRemainDistinctAcrossOwners(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	a, e := s.Reserve(ctx, "vmid", "lab-a", 100, 104, 2)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Reserve(ctx, "vmid", "lab-b", 100, 104, 2)
	if e != nil {
		t.Fatal(e)
	}
	if a[0] == b[0] || a[1] == b[1] {
		t.Fatal("overlap")
	}
	again, e := s.Reserve(ctx, "vmid", "lab-a", 100, 104, 2)
	if e != nil || again[0] != a[0] {
		t.Fatal("reservation is not stable")
	}
	if _, e = s.Reserve(ctx, "vmid", "lab-c", 100, 104, 2); e == nil {
		t.Fatal("exhaustion not enforced")
	}
	if e = s.Release(ctx, "lab-a"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reserve(ctx, "vmid", "lab-c", 100, 104, 2); e != nil {
		t.Fatal(e)
	}
}

func TestRelativeStatePathWithURICharacters(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open("state ?#plus+")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Put(context.Background(), "test", "one", "value"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = s.Get(context.Background(), "test", "one", &got); err != nil || got != "value" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestInstanceLockWaitAndCancellation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unlock, err := s.LockKey("instance:a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.LockKeyContext(context.Background(), "instance:b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = s.LockKeyContext(ctx, "instance:a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		release, err := s.LockKeyContext(context.Background(), "instance:a")
		if err == nil {
			release()
		}
		acquired <- err
	}()
	unlock()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued access did not resume")
	}
}

func TestSharedLifecycleGuardAcrossClients(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := first.LockKeySharedContext(ctx, "instance:lab")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.LockKeySharedContext(ctx, "instance:lab")
	if err != nil {
		a()
		t.Fatal("independent clients could not share the guard", err)
	}
	if unlock, err := second.LockKey("instance:lab"); err == nil {
		unlock()
		t.Error("exclusive mutation overlapped shared guards")
	}
	a()
	if unlock, err := first.LockKey("instance:lab"); err == nil {
		unlock()
		t.Error("exclusive mutation ignored remaining shared guard")
	}
	b()
	writer, err := first.LockKey("instance:lab")
	if err != nil {
		t.Fatal("shared guards did not release", err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 35*time.Millisecond)
	_, err = second.LockKeySharedContext(waitCtx, "instance:lab")
	stop()
	writer()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shared wait ignored exclusive mutation or cancellation", err)
	}
	reader, err := second.LockKeySharedContext(ctx, "instance:lab")
	if err != nil {
		t.Fatal("reader could not resume after mutation", err)
	}
	reader()
}
