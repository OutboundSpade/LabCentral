package hosted

import "context"

// A session lock exists only while requests hold or wait for it. Unknown cookies
// cannot accumulate permanent entries, and waiting respects request cancellation.
type sessionLock struct {
	users int
	busy  chan struct{}
}

func (s *Service) lockSession(ctx context.Context, id string) (func(), error) {
	s.authMu.Lock()
	if s.authLocks == nil {
		s.authLocks = make(map[string]*sessionLock)
	}
	lock := s.authLocks[id]
	if lock == nil {
		lock = &sessionLock{busy: make(chan struct{}, 1)}
		s.authLocks[id] = lock
	}
	lock.users++
	s.authMu.Unlock()
	release := func() {
		s.authMu.Lock()
		defer s.authMu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(s.authLocks, id)
		}
	}
	select {
	case lock.busy <- struct{}{}:
		return func() { <-lock.busy; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
