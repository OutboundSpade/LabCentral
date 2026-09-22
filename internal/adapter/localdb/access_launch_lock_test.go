package localdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSQLiteAccessLaunchLock(t *testing.T) {
	accessLaunchLockContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLAccessLaunchLock(t *testing.T) {
	accessLaunchLockContract(t, newPostgreSQLQuotaTestStore(t, "access_launch_lock"))
}
func accessLaunchLockContract(t *testing.T, s *Store) {
	ctx := context.Background()
	unlock, err := s.LockAccessLaunch(ctx, "instance.one", "vm.desktop")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if got, err := s.LockAccessLaunch(wait, "instance.one", "vm.desktop"); err == nil {
		got()
		t.Fatal("overlapping resource launches")
	} else if !errors.Is(err, context.DeadlineExceeded) && wait.Err() == nil {
		t.Fatal(err)
	}
	// Other resources and database writes remain independent of the handshake.
	next, err := s.LockAccessLaunch(ctx, "instance.two", "vm.desktop")
	if err != nil {
		t.Fatal(err)
	}
	next()
	write, cancelWrite := context.WithTimeout(ctx, time.Second)
	defer cancelWrite()
	if _, err := s.db.ExecContext(write, "CREATE TABLE launch_lock_probe(id INTEGER)"); err != nil {
		t.Fatal("launch occupied writer", err)
	}
	unlock()
	again, err := s.LockAccessLaunch(ctx, "instance.one", "vm.desktop")
	if err != nil {
		t.Fatal(err)
	}
	again()
}
