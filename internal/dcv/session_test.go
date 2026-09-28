package dcv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionMustHaveRunningDisplay(t *testing.T) {
	creates, reads := 0, 0
	run := func(ctx context.Context, command string) ([]byte, error) {
		if strings.Contains(command, "create-session") {
			creates++
			return nil, nil
		}
		reads++
		if reads == 1 {
			return []byte(`[{"id":"student","status":"running","x11-display":""}]`), nil
		}
		return []byte(`[{"id":"student","status":"running","x11-display":":1"}]`), nil
	}
	if err := start(context.Background(), run, "student", "student", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || reads != 2 {
		t.Fatal(creates, reads)
	}
}
func TestFailedLaunchRetriesOnlyConfirmedAbsence(t *testing.T) {
	for _, outcome := range []string{"absent", "unknown", "starting"} {
		t.Run(outcome, func(t *testing.T) {
			creates := 0
			run := func(ctx context.Context, command string) ([]byte, error) {
				if strings.Contains(command, "create-session") {
					creates++
					return nil, nil
				}
				if creates == 2 {
					return []byte(`[{"id":"student","status":"running","x11-display":":1"}]`), nil
				}
				if outcome == "unknown" {
					return nil, errors.New("response lost")
				}
				if outcome == "starting" {
					return []byte(`[{"id":"student","status":"starting"}]`), nil
				}
				return []byte(`[]`), nil
			}
			err := start(context.Background(), run, "student", "student", 5*time.Millisecond, time.Millisecond)
			if outcome == "absent" {
				if err != nil || creates != 2 {
					t.Fatal(err, creates)
				}
			} else if err == nil || creates != 1 {
				t.Fatal(err, creates)
			}
		})
	}
}
func TestCancellationDoesNotRestartSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	creates := 0
	run := func(context.Context, string) ([]byte, error) { creates++; cancel(); return nil, context.Canceled }
	if err := start(ctx, run, "student", "student", time.Second, time.Millisecond); !errors.Is(err, context.Canceled) || creates != 1 {
		t.Fatal(err, creates)
	}
}
