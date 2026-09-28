package aws

import (
	"context"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

// Only explicit authorization rejection permits a create to be retried. An empty
// discovery result after a transport failure remains uncertain.
func doEC2[T any](ctx context.Context, j journal.Journal, key string, create func() (T, error), find func() (T, bool, error)) (T, error) {
	return journal.Do(ctx, j, key, func() (T, error) {
		value, err := create()
		if apiCode(err, "UnauthorizedOperation") || apiCode(err, "AuthFailure") {
			err = journal.Rejected(err)
		}
		return value, err
	}, find)
}
