package controlplane

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"math"
	"time"
)

type VMUsage struct {
	ResourceID string                     `json:"resourceId"`
	Status     string                     `json:"status" enum:"available,unavailable"`
	Usage      *application.ResourceUsage `json:"usage,omitempty"`
}
type InstanceUsage struct {
	InstanceID string    `json:"instanceId"`
	Items      []VMUsage `json:"items"`
}
type cachedUsage struct {
	value InstanceUsage
	until time.Time
}

func (s *Service) authorizeOperations(ctx context.Context, subject domain.UserID) error {
	if s.operationsAuthorizer == nil {
		if subject == LocalAdminID {
			return nil
		}
		return ErrAuthorityChanged
	}
	return s.operationsAuthorizer.Authorize(ctx, application.AuthorizationRequest{SubjectID: subject, Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"})
}
func (s *Service) ReadOperations(ctx context.Context, subject domain.UserID, q application.OperationQuery) (application.OperationPage, error) {
	if err := s.authorizeOperations(ctx, subject); err != nil {
		return application.OperationPage{}, err
	}
	reader, ok := s.store.(application.OperationsReader)
	if !ok {
		return application.OperationPage{}, fmt.Errorf("operations unavailable")
	}
	page, err := reader.ReadOperations(ctx, q)
	if err != nil {
		return page, err
	}
	return page, s.authorizeOperations(ctx, subject)
}
func (s *Service) ReadInstanceUsage(ctx context.Context, subject domain.UserID, id domain.InstanceID) (InstanceUsage, error) {
	if err := s.authorizeOperations(ctx, subject); err != nil {
		return InstanceUsage{}, err
	}
	instance, resources, err := s.GetInstance(ctx, id)
	if err != nil {
		return InstanceUsage{}, err
	}
	if s.operationsAuthorizer != nil {
		if _, err := s.operationsAuthorizer.AuthorizeInstance(ctx, subject, application.ActionInstanceRead, id); err != nil {
			return InstanceUsage{}, err
		}
	}
	key := fmt.Sprintf("%s/%d", id, instance.ResourceVersion)
	value, err, _ := s.usageReads.Do(key, func() (any, error) {
		s.usageMu.Lock()
		cached, ok := s.usageCache[key]
		if s.usageSlots == nil {
			s.usageSlots = make(chan struct{}, 4)
		}
		slots := s.usageSlots
		s.usageMu.Unlock()
		if ok && time.Now().Before(cached.until) {
			return cached.value, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		out := InstanceUsage{InstanceID: string(id), Items: []VMUsage{}}
		for _, r := range resources {
			if !r.UsesVirtualMachine() {
				continue
			}
			item := VMUsage{ResourceID: string(r.ID), Status: "unavailable"}
			if len(r.ProviderState) > 0 && instance.DesiredLifecycle != domain.LifecycleDestroyed && r.Phase != domain.ResourceDestroyed {
				invocation, err := s.ids.New("usage")
				if err != nil {
					return nil, err
				}
				target, err := s.targets.AcquireForSubject(ctx, instance.ProviderTargetID, subject, application.InvocationScope{InvocationID: domain.InvocationID(invocation), InstanceID: id, ResourceID: r.ID, ExpiresAt: time.Now().Add(8 * time.Second)}, true)
				if err == nil {
					if reader, ok := target.Provider.(application.ProviderUsage); ok {
						u, err := reader.ReadUsage(ctx, application.ProviderMutation{Call: application.ProviderCall{RequestID: "resource-usage", InvocationID: invocation, InstanceID: string(id), ResourceID: string(r.ID), TargetID: string(instance.ProviderTargetID), Generation: uint64(instance.Generation)}, Target: target.Context, Canonical: r.CanonicalJSON, PriorState: r.ProviderState})
						if err == nil && validUsage(u, time.Now()) {
							item.Status = "available"
							item.Usage = u
						}
					}
					target.Close()
				}
			}
			out.Items = append(out.Items, item)
		}
		s.usageMu.Lock()
		if len(s.usageCache) > 1024 {
			s.usageCache = nil
		}
		if s.usageCache == nil {
			s.usageCache = make(map[string]cachedUsage)
		}
		s.usageCache[key] = cachedUsage{out, time.Now().Add(10 * time.Second)}
		s.usageMu.Unlock()
		return out, nil
	})
	if err != nil {
		return InstanceUsage{}, err
	}
	if err := s.authorizeOperations(ctx, subject); err != nil {
		return InstanceUsage{}, err
	}
	if s.operationsAuthorizer != nil {
		if _, err := s.operationsAuthorizer.AuthorizeInstance(ctx, subject, application.ActionInstanceRead, id); err != nil {
			return InstanceUsage{}, err
		}
	}
	current, _, err := s.GetInstance(ctx, id)
	if err != nil {
		return InstanceUsage{}, err
	}
	if current.ResourceVersion != instance.ResourceVersion {
		return InstanceUsage{}, ErrAuthorityChanged
	}
	return value.(InstanceUsage), nil
}
func validUsage(u *application.ResourceUsage, now time.Time) bool {
	if u == nil || u.SampledAt.IsZero() || u.SampledAt.After(now.Add(5*time.Second)) || u.SampledAt.Before(now.Add(-30*time.Second)) {
		return false
	}
	if u.CPUFraction != nil && (math.IsNaN(*u.CPUFraction) || math.IsInf(*u.CPUFraction, 0) || *u.CPUFraction < 0 || *u.CPUFraction > 1) {
		return false
	}
	if u.VirtualCPUs != nil && (*u.VirtualCPUs == 0 || *u.VirtualCPUs > 65536) {
		return false
	}
	if u.MemoryCapacityBytes != nil && *u.MemoryCapacityBytes == 0 {
		return false
	}
	return u.CPUFraction != nil || u.MemoryUsedBytes != nil
}

// SetOperationsAuthorizer binds the query policy before the HTTP server starts.
func (s *Service) SetOperationsAuthorizer(authorizer application.Authorizer) {
	s.operationsAuthorizer = authorizer
}
