package controlplane

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Service) GetDeploymentSettings(ctx context.Context, id domain.ProviderTargetID) (application.DeploymentSettings, error) {
	fallback := uint32(0)
	s.targets.mu.RLock()
	binding, ok := s.targets.bindings[id]
	if ok {
		fallback = binding.policy.MaxConcurrentDeployments
	}
	s.targets.mu.RUnlock()
	if !ok {
		return application.DeploymentSettings{}, ErrNotFound
	}
	if fallback == 0 {
		fallback = 1
	}
	result, err := s.store.GetDeploymentSettings(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return application.DeploymentSettings{MaxConcurrentDeployments: fallback}, nil
	}
	return result, err
}
func (s *Service) PutDeploymentSettings(ctx context.Context, id domain.ProviderTargetID, input application.DeploymentSettings) (application.DeploymentSettings, error) {
	if _, err := s.GetDeploymentSettings(ctx, id); err != nil {
		return application.DeploymentSettings{}, err
	}
	return s.store.PutDeploymentSettings(ctx, id, input)
}
