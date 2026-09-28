package journal

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestUncertainCreateIsDiscoveredNotRepeated(t *testing.T) {
	j := Journal{t.TempDir()}
	calls := 0
	create := func() (string, error) { calls++; return "", errors.New("connection lost") }
	found := false
	find := func() (string, bool, error) { return "native-id", found, nil }
	if _, e := Do(context.Background(), j, "operation", create, find); e == nil {
		t.Fatal("lost reply ignored")
	}
	if _, e := Do(context.Background(), j, "operation", create, find); e == nil {
		t.Fatal("uncertainty ignored")
	}
	found = true
	r, e := Do(context.Background(), j, "operation", create, find)
	if e != nil || r != "native-id" || calls != 1 {
		t.Fatalf("unsafe replay: %s %v %d", r, e, calls)
	}
}

func TestAuthoritativeRejectionAllowsRetry(t *testing.T) {
	j := Journal{t.TempDir()}
	calls := 0
	create := func() (string, error) {
		calls++
		if calls == 1 {
			return "", Rejected(errors.New("permission denied"))
		}
		return "native-id", nil
	}
	find := func() (string, bool, error) {
		t.Fatal("rejected create must not be discovered as uncertain")
		return "", false, nil
	}
	if _, err := Do(context.Background(), j, "operation", create, find); err == nil {
		t.Fatal("rejection lost")
	}
	var id string
	if err := j.Load("operation", &id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected create cannot be cleaned as absent", err)
	}
	id, err := Do(context.Background(), j, "operation", create, find)
	if err != nil || id != "native-id" || calls != 2 {
		t.Fatal(id, err, calls)
	}
}
