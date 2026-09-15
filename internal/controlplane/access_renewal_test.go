package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

type leaseTimingStore struct{ Store }

func (*leaseTimingStore) ExchangeAccessToken(_ context.Context, r AccessExchangeRequest) (AccessSessionRecord, bool, error) {
	return AccessSessionRecord{LeaseExpiresAt: time.Now().Add(r.LeaseDuration)}, true, nil
}
func (*leaseTimingStore) RecheckAccessLease(_ context.Context, r AccessLeaseRecheckRequest) (AccessSessionRecord, bool, error) {
	return AccessSessionRecord{LeaseExpiresAt: time.Now().Add(r.LeaseDuration)}, true, nil
}

func TestAccessRenewalScheduledBeforeLeaseExpiry(t *testing.T) {
	s := &Service{store: &leaseTimingStore{}, tokenHasher: accessContractHasher{}, ids: accessContractIDs{}}
	for name, call := range map[string]func(context.Context, application.AccessCallbackIdentity, string, string) (application.AccessAuthorizationDecision, error){"exchange": s.ExchangeAccessToken, "recheck": s.RecheckAccessLease} {
		t.Run(name, func(t *testing.T) {
			d, err := call(context.Background(), application.AccessCallbackIdentity{}, "access.timing", strings.Repeat("x", 43))
			if err != nil || !d.Granted {
				t.Fatalf("authorization failed: %v", err)
			}
			if d.RecheckAfter <= 0 || d.RecheckAfter >= time.Until(d.ValidUntil)-time.Second {
				t.Fatal("renewal lacks margin before lease expiry")
			}
		})
	}
}
