package journal

import (
	"context"
	"errors"
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
