package aws

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/aws/smithy-go"
	"testing"
)

func TestEC2DenialVersusAmbiguousFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		calls int
	}{
		{"authorization", &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "denied"}, 2},
		{"authentication", &smithy.GenericAPIError{Code: "AuthFailure", Message: "denied"}, 2},
		{"timeout", context.DeadlineExceeded, 1},
		{"transport", errors.New("response lost"), 1},
		{"service", &smithy.GenericAPIError{Code: "InternalError", Message: "unknown outcome"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			j := journal.Journal{Root: t.TempDir()}
			calls := 0
			create := func() (string, error) { calls++; return "", test.err }
			find := func() (string, bool, error) { return "", false, nil }
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := doEC2(context.Background(), j, "network", create, find); err == nil {
					t.Fatal("failure disappeared")
				}
			}
			if calls != test.calls {
				t.Fatalf("create called %d times, want %d", calls, test.calls)
			}
		})
	}
}
