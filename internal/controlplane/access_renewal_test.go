package controlplane

import (
	"context"
	"crypto/sha256"
	"fmt"
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

type retainedLeaseStore struct{ Store }

func (*retainedLeaseStore) RecheckAccessLease(_ context.Context, r AccessLeaseRecheckRequest) (AccessSessionRecord, bool, error) {
	return AccessSessionRecord{SharedConsole: true, LeaseRetained: true, LeaseExpiresAt: time.Now().Add(10 * time.Second)}, true, nil
}
func TestSpectatorRecheckReturnsExistingToken(t *testing.T) {
	s := &Service{store: &retainedLeaseStore{}, tokenHasher: accessContractHasher{}, ids: accessContractIDs{}}
	lease := strings.Repeat("x", 43)
	d, err := s.RecheckAccessLease(context.Background(), application.AccessCallbackIdentity{}, "access.viewer", lease)
	if err != nil || !d.Granted || d.Lease != lease || d.InputLease != nil || d.RecheckAfter != application.ConsoleControlPollInterval {
		t.Fatalf("invalid retained decision: %v", err)
	}
}

type lostRenewalStore struct {
	Store
	current application.KeyedTokenHash
	calls   int
}

type retryLeaseHasher struct{}

func (retryLeaseHasher) HashCurrent(purpose, value string) application.KeyedTokenHash {
	return application.KeyedTokenHash{KeyID: "test", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(purpose+"/"+value)))}
}
func (h retryLeaseHasher) HashCandidates(purpose, value string) []application.KeyedTokenHash {
	return []application.KeyedTokenHash{h.HashCurrent(purpose, value)}
}

func (s *lostRenewalStore) RecheckAccessLease(_ context.Context, r AccessLeaseRecheckRequest) (AccessSessionRecord, bool, error) {
	matched := false
	for _, h := range r.LeaseHashes {
		if h == s.current {
			matched = true
		}
	}
	if !matched {
		return AccessSessionRecord{}, false, nil
	}
	s.current = r.NewLease // durable commit happened even if its response is lost
	s.calls++
	if s.calls == 1 {
		return AccessSessionRecord{}, false, context.DeadlineExceeded
	}
	return AccessSessionRecord{LeaseExpiresAt: time.Now().Add(r.LeaseDuration)}, true, nil
}
func TestAccessRenewalRecoversAfterCommittedResponseIsLost(t *testing.T) {
	lease := strings.Repeat("x", 43)
	hasher := retryLeaseHasher{}
	store := &lostRenewalStore{current: hasher.HashCurrent("access-lease", lease)}
	s := &Service{store: store, tokenHasher: hasher, ids: accessContractIDs{}}
	if _, err := s.RecheckAccessLease(context.Background(), application.AccessCallbackIdentity{}, "access.retry", lease); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	d, err := s.RecheckAccessLease(context.Background(), application.AccessCallbackIdentity{}, "access.retry", lease)
	if err != nil || !d.Granted || d.Lease != lease {
		t.Fatalf("lost renewal response destroyed active credential: %+v %v", d, err)
	}
}
