package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Two independent Store handles on one root model two CLI processes. Concurrent
// read-then-write reservations must queue rather than fail with SQLITE_BUSY.
func TestIndependentHandlesQueueConcurrentReservations(t *testing.T) {
	root := t.TempDir()
	a, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for n := 0; n < 40; n++ {
		s := a
		if n%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func(n int, s *Store) {
			defer wg.Done()
			if _, err := s.Reserve(context.Background(), "scope", fmt.Sprintf("owner-%d", n), 1, 1000, 1); err != nil {
				errs <- err
			}
		}(n, s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
