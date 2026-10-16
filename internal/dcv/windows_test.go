package dcv

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"testing"
	"time"
)

func TestWindowsConsoleReadinessRequiresOwnershipAndRunningState(t *testing.T) {
	for _, tc := range []struct {
		output      string
		ready, fail bool
	}{
		{`[]`, false, false}, {``, false, true}, {`invalid`, false, true},
		{`[{"id":"console","owner":"lab","type":"console","status":"running"}]`, true, false},
		{`[{"id":"console","owner":"lab","type":"console","status":"starting"}]`, false, false},
		{`[{"id":"console","owner":"Administrator","type":"console","status":"running"}]`, false, true},
	} {
		ready, err := WindowsReady(context.Background(), func(context.Context, string) ([]byte, error) { return []byte(tc.output), nil })
		if ready != tc.ready || (err != nil) != tc.fail {
			t.Fatalf("%s: %v %v", tc.output, ready, err)
		}
	}
}
func TestWindowsConsoleUnknownStateNeverCreatesAnotherSession(t *testing.T) {
	calls := 0
	err := StartWindows(context.Background(), func(context.Context, string) ([]byte, error) { calls++; return nil, errors.New("connection lost") })
	if err == nil || calls != 1 {
		t.Fatal("unknown state replayed a mutation", err, calls)
	}
	calls = 0
	err = StartWindows(context.Background(), func(context.Context, string) ([]byte, error) {
		calls++
		return []byte(`[{"id":"console","owner":"lab","type":"console","status":"running"}]`), nil
	})
	if err != nil || calls != 1 {
		t.Fatal("existing console was recreated", err, calls)
	}
}

func TestWindowsSlowReadinessDoesNotReplaceUnknownConsole(t *testing.T) {
	creates, reads := 0, 0
	err := startWindows(context.Background(), func(ctx context.Context, command string) ([]byte, error) {
		if command == windows.Command(WindowsCLI+`create-session --type console --owner lab console; if ($LASTEXITCODE -ne 0) {exit $LASTEXITCODE}`) {
			creates++
			return nil, nil
		}
		reads++
		if reads == 1 {
			return []byte(`[]`), nil
		}
		if reads < 5 {
			return nil, nil
		} // Empty status after create is unknown, not absent.
		return []byte(`[{"id":"console","owner":"lab","type":"console","status":"running"}]`), nil
	}, time.Second, time.Millisecond)
	if err != nil || creates != 1 || reads != 5 {
		t.Fatal(err, creates, reads)
	}
	creates = 0
	err = startWindows(context.Background(), func(ctx context.Context, command string) ([]byte, error) {
		if command == windows.Command(WindowsCLI+`create-session --type console --owner lab console; if ($LASTEXITCODE -ne 0) {exit $LASTEXITCODE}`) {
			creates++
		}
		return []byte(`[{"id":"console","owner":"lab","type":"console","status":"starting"}]`), nil
	}, 5*time.Millisecond, time.Millisecond)
	if err == nil || creates != 0 {
		t.Fatal("timeout replaced existing console", err, creates)
	}
}
