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

// Xdcv loads Mesa and LLVM lazily while initializing GLX, even with DCV-GL off.
// On a freshly restored image those reads can exceed the launcher's display-fd
// deadline. Read the executable, CPU graphics modules and their dependencies
// before starting that clock. This also works with already captured images and
// does not change the server configuration or another user's session.
const warmGraphics = `sudo bash -c 'set -euo pipefail
for file in /usr/bin/Xdcv /usr/lib/xorg/modules/extensions/libglx.so /usr/lib/*/libGLX_mesa.so.* /usr/lib/*/libgallium*.so /usr/lib/*/dri/swrast_dri.so; do
  if test -f "$file"; then
    cat "$file" >/dev/null
    ldd "$file"
  fi
done | awk '\''/=> \// {print $3} /^\// {print $1}'\'' | sort -u | while IFS= read -r file; do
  cat "$file" >/dev/null
done'`

type session struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Status  string `json:"status"`
	Display string `json:"x11-display"`
}

func state(ctx context.Context, run Run, id string, owners ...string) (bool, bool, error) {
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
			if len(owners) > 0 && (owners[0] == "" || s.Owner != owners[0]) {
				return true, false, fmt.Errorf("ownership_mismatch: DCV session owner differs; session retained")
			}
			return true, s.Status == "running" && s.Display != "", nil
		}
	}
	return false, false, nil
}

// Ready reports an observed running X display; transport errors remain unknown.
func Ready(ctx context.Context, run Run, id string) (bool, error) {
	_, ready, err := state(ctx, run, id, id)
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
	if _, err := run(ctx, warmGraphics); err != nil {
		return fmt.Errorf("prepare DCV graphics libraries: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		// CPU-backed virtual desktops must not initialize DCV-GL: it can leave
		// Xdcv starting without a usable display on hosts with no GPU.
		if _, err := run(ctx, "sudo dcv create-session --type virtual --gl off --user "+user+" --owner "+user+" "+id); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, timeout)
		err := waitReady(wait, run, id, user, interval)
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
		exists, ready, check := state(ctx, run, id, user)
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
func waitReady(ctx context.Context, run Run, id, user string, interval time.Duration) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		_, ready, err := state(ctx, run, id, user)
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

// Close verifies the native owner before closing a session, then confirms absence.
// Empty or lost status responses retain cleanup intent; a matching name alone is
// never evidence that a session belongs to this account.
func Close(ctx context.Context, run Run, user, id string) error {
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(id) {
		return fmt.Errorf("invalid DCV session identity")
	}
	exists, _, err := state(ctx, run, id, user)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err = run(ctx, "sudo dcv close-session "+id); err != nil {
		return err
	}
	exists, _, err = state(ctx, run, id, user)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("DCV session removal unconfirmed; retry revocation")
	}
	return nil
}
