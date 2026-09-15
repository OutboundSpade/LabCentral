package controlplane

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"math"
	"testing"
	"time"
)

func TestMeasurementValidity(t *testing.T) {
	now := time.Now()
	zero := 0.0
	nan := math.NaN()
	negative := -0.1
	tooHigh := 1.5
	memory := uint64(42)
	for _, test := range []struct {
		name  string
		u     *application.ResourceUsage
		valid bool
	}{
		{"missing", nil, false}, {"empty", &application.ResourceUsage{SampledAt: now}, false},
		{"real zero", &application.ResourceUsage{SampledAt: now, CPUFraction: &zero}, true},
		{"memory only", &application.ResourceUsage{SampledAt: now, MemoryUsedBytes: &memory}, true},
		{"stale", &application.ResourceUsage{SampledAt: now.Add(-31 * time.Second), CPUFraction: &zero}, false},
		{"future", &application.ResourceUsage{SampledAt: now.Add(time.Minute), CPUFraction: &zero}, false},
		{"nan", &application.ResourceUsage{SampledAt: now, CPUFraction: &nan}, false},
		{"negative", &application.ResourceUsage{SampledAt: now, CPUFraction: &negative}, false},
		{"over capacity", &application.ResourceUsage{SampledAt: now, CPUFraction: &tooHigh}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validUsage(test.u, now) != test.valid {
				t.Fatal("invalid measurement decision")
			}
		})
	}
}

type usageStore struct {
	Store
	instance  application.InstanceRecord
	resources []application.ResourceRecord
}

func (s *usageStore) GetInstance(context.Context, domain.InstanceID) (application.InstanceRecord, error) {
	return s.instance, nil
}
func (s *usageStore) ListResources(context.Context, domain.InstanceID) ([]application.ResourceRecord, error) {
	return s.resources, nil
}

type usagePolicy struct {
	application.Authorizer
	denied bool
}

func (p *usagePolicy) Authorize(context.Context, application.AuthorizationRequest) error {
	if p.denied {
		return ErrAuthorityChanged
	}
	return nil
}
func (p *usagePolicy) AuthorizeInstance(context.Context, domain.UserID, application.PermissionAction, domain.InstanceID) (application.InstanceAccessRecord, error) {
	if p.denied {
		return application.InstanceAccessRecord{}, ErrAuthorityChanged
	}
	return application.InstanceAccessRecord{}, nil
}

type usageProvider struct {
	application.Provider
	calls int
	after func()
}

func (p *usageProvider) ReadUsage(context.Context, application.ProviderMutation) (*application.ResourceUsage, error) {
	p.calls++
	if p.after != nil {
		p.after()
	}
	cpu := 0.2
	return &application.ResourceUsage{SampledAt: time.Now(), CPUFraction: &cpu}, nil
}
func TestUsageRechecksAuthorityAndCachesWithoutPersistence(t *testing.T) {
	policy := &usagePolicy{}
	provider := &usageProvider{}
	targets, err := NewTargetRegistry([]TargetBinding{{ID: "target.test", ProviderKind: "test", Provider: provider, ProtocolMajor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	store := &usageStore{instance: application.InstanceRecord{ID: "instance.test", ProviderTargetID: "target.test", ResourceVersion: 1}, resources: []application.ResourceRecord{{ID: "vm.test", Kind: domain.ResourceVM, Phase: domain.ResourceReady, ProviderState: []byte("opaque")}}}
	s := &Service{store: store, targets: targets, ids: CryptoIDs{}, operationsAuthorizer: policy}
	for range 2 {
		out, err := s.ReadInstanceUsage(context.Background(), "user.admin", "instance.test")
		if err != nil || len(out.Items) != 1 || out.Items[0].Status != "available" {
			t.Fatalf("usage: %+v %v", out, err)
		}
	}
	if provider.calls != 1 {
		t.Fatal("repeated query did not use bounded cache")
	}
	policy.denied = true
	if _, err := s.ReadInstanceUsage(context.Background(), "user.admin", "instance.test"); err == nil || provider.calls != 1 {
		t.Fatal("cached data bypassed authorization")
	}
	policy.denied = false
	store.instance.ResourceVersion++
	provider.after = func() { policy.denied = true }
	if _, err := s.ReadInstanceUsage(context.Background(), "user.admin", "instance.test"); err == nil {
		t.Fatal("in-flight policy revocation leaked usage")
	}
	if string(store.resources[0].ProviderState) != "opaque" {
		t.Fatal("read overwrote provider state")
	}
}
