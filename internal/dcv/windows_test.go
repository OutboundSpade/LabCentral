package dcv

import (
	"context"
	"errors"
	"testing"
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
