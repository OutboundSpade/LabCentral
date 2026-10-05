// Package dcv checks native virtual-session readiness before publishing access.
package dcv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

type Run func(context.Context, string) ([]byte, error)

var errEmptyStatus = errors.New("DCV returned an empty session status; readiness is unconfirmed")

type session struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Display string `json:"x11-display"`
}

func state(ctx context.Context, run Run, id string) (bool, bool, error) {
	output, err := run(ctx, "sudo dcv list-sessions -j")
	if err != nil {
		return false, false, err
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, false, errEmptyStatus
	}
	var sessions []session
	if err = json.Unmarshal(output, &sessions); err != nil {
		return false, false, fmt.Errorf("invalid DCV session status: %w", err)
	}
	for _, s := range sessions {
		if s.ID == id {
			return true, s.Status == "running" && s.Display != "", nil
		}
	}
	return false, false, nil
}

// Ready reports an observed running X display; transport errors remain unknown.
func Ready(ctx context.Context, run Run, id string) (bool, error) {
	_, ready, err := state(ctx, run, id)
	return ready, err
}

// Start waits for an X display. The caller owns the named session and holds its
// machine lock. A failed asynchronous launch is retried only after confirmed absence.
func Start(ctx context.Context, run Run, user, id string) error {
	return start(ctx, run, user, id, 25*time.Second, 250*time.Millisecond)
}
func start(ctx context.Context, run Run, user, id string, timeout, interval time.Duration) error {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	if !valid.MatchString(user) || !valid.MatchString(id) {
		return fmt.Errorf("invalid DCV session identity")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := run(ctx, "sudo dcv create-session --type virtual --user "+user+" --owner "+user+" "+id); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, timeout)
		err := waitReady(wait, run, id, interval)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		exists, ready, check := state(ctx, run, id)
		if check != nil {
			return check
		}
		if ready {
			return nil
		}
		if exists {
			return fmt.Errorf("DCV session is still starting; readiness is unconfirmed")
		}
	}
	return fmt.Errorf("DCV session exited before its X display became ready")
}
func waitReady(ctx context.Context, run Run, id string, interval time.Duration) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		_, ready, err := state(ctx, run, id)
		if err != nil && !errors.Is(err, errEmptyStatus) {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
