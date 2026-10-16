package aws

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"reflect"
)

// Only explicit authorization/validation rejection or throttling permits a create
// to be retried. An empty
// discovery result after a transport failure remains uncertain.
func doEC2[T any](ctx context.Context, j journal.Journal, key string, create func() (T, error), find func() (T, bool, error)) (T, error) {
	// A durable success is an identity checkpoint, not an ownership lease. Recheck
	// it before replaying later mutations that use that resource.
	var saved T
	if err := j.Load(key, &saved); err == nil {
		live, found, err := find()
		if err != nil {
			return saved, err
		}
		if !found || !reflect.DeepEqual(saved, live) {
			return saved, fmt.Errorf("recovery_required: recorded resource for %s is absent or ownership changed; investigate before continuing", key)
		}
		return saved, nil
	}
	return journal.Do(ctx, j, key, func() (T, error) {
		value, err := create()
		if rejectedEC2Error(err) {
			err = journal.Rejected(err)
		}
		return value, err
	}, find)
}
