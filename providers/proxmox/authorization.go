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

// CleanupAuthorizer grants only an explicitly named, already-owned deletion.
type CleanupAuthorizationInput struct {
	MutationAuthorizationInput
	InstanceID, ResourceID, OwnershipDigest string
}
type CleanupAuthorizer interface {
	AuthorizeCleanup(context.Context, CleanupAuthorizationInput) (MutationAuthorization, error)
}

func (service *Service) authorizeExecution(ctx context.Context, input *pluginv1.ResourceMutationInput, config TargetConfig, inventory Inventory, digest, action string) (string, error) {
	evidence, err := service.authorizeMutation(ctx, input.GetTarget(), config, inventory, digest)
	if err == nil {
		return evidence, nil
	}
	authorizer, ok := service.mutationAuthorizer.(CleanupAuthorizer)
	if !ok || action != "destroy" || len(input.GetOwnershipTokens()) != 1 || input.GetOwnershipTokens()[0] == "" || input.GetPriorState() == nil {
		return "", err
	}
	state, stateErr := decodeQEMUState(input.GetPriorState())
	if stateErr != nil || state.Ownership == nil || state.Ownership.InstanceID != input.GetContext().GetLabInstanceId() || state.LogicalResourceID != input.GetContext().GetLogicalResourceId() || state.TargetID != input.GetTarget().GetTargetId() || state.Ownership.Token != input.GetOwnershipTokens()[0] {
		return "", err
	}
	grant, e := authorizer.AuthorizeCleanup(ctx, CleanupAuthorizationInput{MutationAuthorizationInput: MutationAuthorizationInput{Target: input.GetTarget(), Config: config, Inventory: inventory, MutationInventoryDigest: digest}, InstanceID: state.Ownership.InstanceID, ResourceID: state.LogicalResourceID, OwnershipDigest: digestDocument([]byte(state.Ownership.Token))})
	if e != nil || grant.Evidence == "" || len(grant.Evidence) > 2048 {
		return "", err
	}
	return grant.Evidence, nil
}
