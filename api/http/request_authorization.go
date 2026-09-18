package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func authorize(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) error {
	_, err := authorizeActor(ctx, security, request)
	return err
}

func authorizeActor(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	request.SubjectID = actor.user.ID
	if err := security.options.Authorizer.Authorize(ctx, request); err != nil {
		return principal{}, authorizationError(err, false)
	}
	return actor, nil
}

func authorizeLabRequest(ctx context.Context, security *requestSecurity, request LabRequest, action application.PermissionAction, targetID domain.ProviderTargetID) (principal, domain.ProjectID, application.OwnerKind, string, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, "", "", "", huma.Error401Unauthorized("Authentication is required")
	}
	projectID := domain.ProjectID(request.ProjectID)
	ownerKind := application.OwnerKind(request.OwnerKind)
	ownerID := request.OwnerID
	if security.options.Mode == AuthModeNone {
		if projectID == "" {
			projectID = "project.local"
		}
		if ownerKind == "" {
			ownerKind = application.OwnerUser
		}
		if ownerID == "" {
			ownerID = string(controlplane.LocalAdminID)
		}
		return actor, projectID, ownerKind, ownerID, nil
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return principal{}, "", "", "", huma.Error400BadRequest("Project ID is required and must be valid")
	}
	if ownerKind == "" {
		ownerKind = application.OwnerUser
	}
	if ownerID == "" && ownerKind == application.OwnerUser {
		ownerID = string(actor.user.ID)
	}
	if !validRequestOwner(ownerKind, ownerID) {
		return principal{}, "", "", "", huma.Error400BadRequest("Instance owner is invalid")
	}
	authorizationRequest := application.AuthorizationRequest{
		SubjectID: actor.user.ID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(projectID),
		TargetID: targetID, OwnerKind: ownerKind, OwnerID: ownerID,
	}
	if err := security.options.Authorizer.Authorize(ctx, authorizationRequest); err != nil {
		return principal{}, "", "", "", authorizationError(err, false)
	}
	return actor, projectID, ownerKind, ownerID, nil
}

func authorizeLabBatchRequest(ctx context.Context, security *requestSecurity, request InstanceBatchRequest, targetID domain.ProviderTargetID) (principal, domain.ProjectID, []controlplane.InstanceOwner, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, "", nil, huma.Error401Unauthorized("Authentication is required")
	}
	projectID, err := domain.NewProjectID(request.ProjectID)
	if err != nil {
		return principal{}, "", nil, huma.Error400BadRequest("Project ID is required and must be valid")
	}
	if len(request.Owners) < 1 || len(request.Owners) > controlplane.MaximumInstanceBatchSize {
		return principal{}, "", nil, huma.Error400BadRequest(fmt.Sprintf("Batch owners must contain 1-%d entries", controlplane.MaximumInstanceBatchSize))
	}
	owners := make([]controlplane.InstanceOwner, 0, len(request.Owners))
	seen := make(map[string]bool, len(request.Owners))
	for _, requested := range request.Owners {
		owner := controlplane.InstanceOwner{Kind: application.OwnerKind(requested.OwnerKind), ID: requested.OwnerID}
		if owner.Kind != application.OwnerUser && owner.Kind != application.OwnerTeam || !validRequestOwner(owner.Kind, owner.ID) {
			return principal{}, "", nil, huma.Error400BadRequest("Batch owner is invalid")
		}
		identity := string(owner.Kind) + "\x00" + owner.ID
		if seen[identity] {
			return principal{}, "", nil, huma.Error400BadRequest("Batch owners must be unique")
		}
		seen[identity] = true
		owners = append(owners, owner)
	}
	if security.options.Mode == AuthModeNone {
		return actor, projectID, owners, nil
	}
	for _, owner := range owners {
		if err := security.options.Authorizer.Authorize(ctx, application.AuthorizationRequest{
			SubjectID: actor.user.ID, Action: application.ActionInstanceBatchCreate,
			ScopeKind: application.ScopeProject, ScopeID: string(projectID), TargetID: targetID,
			OwnerKind: owner.Kind, OwnerID: owner.ID,
		}); err != nil {
			return principal{}, "", nil, authorizationError(err, false)
		}
	}
	return actor, projectID, owners, nil
}

func validRequestOwner(kind application.OwnerKind, id string) bool {
	var err error
	switch kind {
	case application.OwnerUser:
		_, err = domain.NewUserID(id)
	case application.OwnerTeam:
		_, err = domain.NewTeamID(id)
	case application.OwnerProject:
		_, err = domain.NewProjectID(id)
	default:
		return false
	}
	return err == nil
}

func authorizeInstance(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) error {
	_, err := authorizeInstanceActor(ctx, security, action, instanceID)
	return err
}

func authorizeInstanceActor(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return principal{}, huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	if _, err := security.options.Authorizer.AuthorizeInstance(ctx, actor.user.ID, action, instanceID); err != nil {
		return principal{}, authorizationError(err, true)
	}
	return actor, nil
}

func authorizeOperation(ctx context.Context, security *requestSecurity, action application.PermissionAction, operationID domain.OperationID) error {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewOperationID(string(operationID)); err != nil {
		return huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return nil
	}
	if _, err := security.options.Authorizer.AuthorizeOperation(ctx, actor.user.ID, action, operationID); err != nil {
		return authorizationError(err, true)
	}
	return nil
}

func authorizationError(err error, hideExistence bool) error {
	if errors.Is(err, authorization.ErrDenied) {
		if hideExistence {
			return huma.Error404NotFound("Resource was not found")
		}
		return huma.Error403Forbidden("Permission was denied")
	}
	return huma.Error500InternalServerError("Authorization could not be evaluated")
}

func apiError(err error) error {
	if errors.Is(err, application.ErrTemplateManaged) {
		return huma.Error409Conflict("Make an independent copy before editing a synchronized template")
	}
	if errors.Is(err, authorization.ErrDenied) {
		return huma.Error403Forbidden("Current authority does not permit this operation")
	}
	var validation *controlplane.ValidationError
	if errors.As(err, &validation) {
		details := make([]error, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			location := issue.FieldPath
			if location == "" {
				location = issue.ResourceID
			}
			details = append(details, &huma.ErrorDetail{Message: issue.Code + ": " + issue.Message, Location: location})
		}
		return huma.Error422UnprocessableEntity("Lab validation failed", details...)
	}
	if errors.Is(err, controlplane.ErrNotFound) {
		return huma.Error404NotFound("Resource was not found")
	}
	if errors.Is(err, controlplane.ErrAlreadyExists) {
		return huma.Error409Conflict("A resource with the same identity or scoped name already exists")
	}
	if errors.Is(err, controlplane.ErrVersionConflict) {
		return huma.NewError(http.StatusPreconditionFailed, "The resource version no longer matches")
	}
	if errors.Is(err, controlplane.ErrAuthorityChanged) {
		return huma.Error404NotFound("Resource was not found")
	}
	if errors.Is(err, controlplane.ErrAccessUnsupported) {
		return huma.Error422UnprocessableEntity("The requested access method is unsupported")
	}
	if errors.Is(err, controlplane.ErrAccessUnavailable) {
		return huma.Error503ServiceUnavailable("The access service is unavailable")
	}
	var domainError *domain.DomainError
	var quotaError *application.QuotaExceededError
	if errors.As(err, &quotaError) {
		return huma.Error422UnprocessableEntity("The request would exceed an applicable quota")
	}
	if errors.As(err, &domainError) && domainError.Class == domain.ErrorQuota {
		return huma.Error422UnprocessableEntity(domainError.Message)
	}
	if errors.As(err, &domainError) && domainError.Code == "idempotency_conflict" {
		return huma.Error409Conflict("Idempotency key is already bound to a different request")
	}
	return huma.Error500InternalServerError("The control plane could not complete the request")
}
