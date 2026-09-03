package proxmox

import (
	"context"
	"fmt"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

// MutationAuthorizationInput contains the live, non-secret facts that a
// production authorizer must bind to reviewed qualification evidence. The
// credential envelope is deliberately not exposed to the authorizer.
type MutationAuthorizationInput struct {
	Target                  *pluginv1.TargetContext
	Config                  TargetConfig
	Inventory               Inventory
	MutationInventoryDigest string
}

type MutationAuthorization struct {
	Evidence string
}

// MutationAuthorizer is the production write boundary. Implementations must
// fail closed unless reviewed evidence is authentic, current, and bound to the
// exact target configuration, provider build, and live inventory.
type MutationAuthorizer interface {
	AuthorizeMutation(context.Context, MutationAuthorizationInput) (MutationAuthorization, error)
}

// NewProductionService is the only exported constructor that can enable the
// mutation implementation. Supplying target configuration alone is never
// sufficient: every mutation is re-authorized through authorizer.
func NewProductionService(authorizer MutationAuthorizer) (*Service, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("Proxmox production mutation authorizer is required")
	}
	service := NewService()
	service.allowT0Mutation = true
	service.mutationAuthorizer = authorizer
	return service, nil
}

func (service *Service) authorizeMutation(ctx context.Context, target *pluginv1.TargetContext, config TargetConfig, inventory Inventory, mutationDigest string) (string, error) {
	if !service.allowT0Mutation {
		return "", fmt.Errorf("mutation execution is disabled")
	}
	// The package-private T0 constructor exercises the complete mutation state
	// machine without creating a production bypass.
	if service.mutationAuthorizer == nil {
		return "T0-only mutation authorization", nil
	}
	authorization, err := service.mutationAuthorizer.AuthorizeMutation(ctx, MutationAuthorizationInput{
		Target: target, Config: config, Inventory: inventory, MutationInventoryDigest: mutationDigest,
	})
	if err != nil || authorization.Evidence == "" || len(authorization.Evidence) > 2048 {
		return "", fmt.Errorf("production qualification authorization failed")
	}
	return authorization.Evidence, nil
}
