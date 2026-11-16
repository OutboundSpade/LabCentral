package aws

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

const ec2MutationMaxAttempts = 6

// retryEC2Mutation retries only an explicit throttle: none of the preceding
// attempts executed. The caller must journal intent before entering this helper
// and disable SDK mutation retries. The closure retains its native client token.
// Keep the wait injectable without global state so failure tests need no sleeps.
func retryEC2Mutation[T any](ctx context.Context, create func() (T, error), wait func(context.Context, time.Duration) error) (T, error) {
	var zero T
	var throttle error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, journal.Rejected(fmt.Errorf("EC2 mutation not submitted; retry the operation: %w", errors.Join(err, throttle)))
		}
		value, err := create()
		if !throttledEC2Error(err) {
			return value, err
		}
		throttle = err
		if attempt == ec2MutationMaxAttempts-1 {
			return zero, journal.Rejected(fmt.Errorf("EC2 mutation throttled after %d attempts; no mutation executed; retry the operation: %w", ec2MutationMaxAttempts, err))
		}
		// Full jitter in [0, min(1s * 2^attempt, 16s)). Five backoffs
		// total less than 31 seconds, further bounded by the caller's context.
		ceiling := min(time.Second<<attempt, 16*time.Second)
		if err := wait(ctx, time.Duration(rand.Int64N(int64(ceiling)))); err != nil {
			return zero, journal.Rejected(fmt.Errorf("EC2 throttle backoff interrupted; no mutation executed; retry the operation: %w", errors.Join(err, throttle)))
		}
	}
}

// Only explicit authorization/validation rejection or throttling permits a create
// to be retried. Empty discovery after a transport failure remains uncertain.
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
		value, err := retryEC2Mutation(ctx, create, waitRouteRetry)
		if rejectedEC2Error(err) {
			err = journal.Rejected(err)
		}
		return value, err
	}, find)
}
