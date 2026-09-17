package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"slices"
	"time"
)

func gatewayActor(ctx context.Context, action string) (principal, error) {
	p, ok := requestPrincipal(ctx)
	if !ok || p.gateway == nil || !slices.Contains(p.gateway.Scope.Actions, action) {
		return p, huma.Error403Forbidden("Gateway action is outside the approved scope")
	}
	return p, nil
}
func gatewayInstance(ctx context.Context, s *requestSecurity, service *controlplane.Service, p principal, id domain.InstanceID, action app.PermissionAction) error {
	access, e := s.options.Authorizer.AuthorizeInstance(ctx, p.user.ID, action, id)
	if e != nil {
		return authorizationError(e, false)
	}
	if access.ProjectID != p.gateway.Scope.ProjectID {
		return huma.Error403Forbidden("Instance outside approved project")
	}
	if slices.Contains(p.gateway.Scope.InstanceIDs, id) {
		return nil
	}
	// A template grant covers only this user's instances of an approved template,
	// on an approved target. It never grants access to another participant's lab.
	instance, _, e := service.GetInstance(ctx, id)
	if e != nil {
		return apiError(e)
	}
	if instance.OwnerKind != app.OwnerUser || instance.OwnerID != string(p.user.ID) || !slices.Contains(p.gateway.Scope.TargetIDs, instance.ProviderTargetID) {
		return huma.Error403Forbidden("Instance outside approved scope")
	}
	revision, e := service.GetTemplateRevision(ctx, instance.RevisionID)
	if e != nil || !slices.Contains(p.gateway.Scope.TemplateIDs, revision.TemplateID) {
		return huma.Error403Forbidden("Template outside approved scope")
	}
	return nil
}
func registerGatewayResources(api huma.API, service *controlplane.Service, s *requestSecurity) {
	huma.Get(api, "/gateway/templates", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []LabTemplateDTO `json:"items"`
		}
	}, error) { p, e := gatewayActor(ctx, "templates.read"); if e != nil {
		return nil, e
	}; if e = s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: p.user.ID, Action: app.ActionTemplateRead, ScopeKind: app.ScopeProject, ScopeID: string(p.gateway.Scope.ProjectID)}); e != nil {
		return nil, authorizationError(e, false)
	}; out := &struct {
		Body struct {
			Items []LabTemplateDTO `json:"items"`
		}
	}{}; out.Body.Items = []LabTemplateDTO{}; for _, id := range p.gateway.Scope.TemplateIDs {
		t, e := service.GetLabTemplate(ctx, id)
		if e != nil || t.ProjectID != p.gateway.Scope.ProjectID {
			continue
		}
		out.Body.Items = append(out.Body.Items, labTemplateDTO(t))
	}; return out, nil })
	huma.Get(api, "/gateway/templates/{id}/revisions", func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"128"`
	}) (*struct {
		Body struct {
			Items []TemplateRevisionDTO `json:"items"`
		}
	}, error) { p, e := gatewayActor(ctx, "templates.read"); if e != nil {
		return nil, e
	}; if !slices.Contains(p.gateway.Scope.TemplateIDs, domain.TemplateID(in.ID)) {
		return nil, huma.Error403Forbidden("Template outside approved scope")
	}; if e = s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: p.user.ID, Action: app.ActionTemplateRead, ScopeKind: app.ScopeProject, ScopeID: string(p.gateway.Scope.ProjectID)}); e != nil {
		return nil, authorizationError(e, false)
	}; t, e := service.GetLabTemplate(ctx, domain.TemplateID(in.ID)); if e != nil || t.ProjectID != p.gateway.Scope.ProjectID {
		return nil, huma.Error404NotFound("Template unavailable")
	}; page, e := service.ListTemplateRevisions(ctx, controlplane.TemplateRevisionListRequest{TemplateID: t.ID, Limit: 200}); if e != nil {
		return nil, apiError(e)
	}; out := &struct {
		Body struct {
			Items []TemplateRevisionDTO `json:"items"`
		}
	}{}; out.Body.Items = []TemplateRevisionDTO{}; for _, r := range page.Records {
		out.Body.Items = append(out.Body.Items, templateRevisionDTO(r))
	}; return out, nil })
	huma.Post(api, "/gateway/deployments", func(ctx context.Context, in *struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           LabRequest
	}) (*struct{ Body OperationDTO }, error) {
		p, e := gatewayActor(ctx, "deployments.create")
		if e != nil {
			return nil, e
		}
		b := in.Body
		target := domain.ProviderTargetID(b.ProviderTargetID)
		if b.Source != "" || b.RevisionID == "" || b.ProjectID != string(p.gateway.Scope.ProjectID) || !slices.Contains(p.gateway.Scope.TargetIDs, target) || (b.OwnerID != "" && b.OwnerID != string(p.user.ID)) || (b.OwnerKind != "" && b.OwnerKind != "user") {
			return nil, huma.Error403Forbidden("Deployment outside approved scope")
		}
		if e = s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: p.user.ID, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: b.ProjectID, TargetID: target, OwnerKind: app.OwnerUser, OwnerID: string(p.user.ID)}); e != nil {
			return nil, authorizationError(e, false)
		}
		if e = s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: p.user.ID, Action: app.ActionTemplateRead, ScopeKind: app.ScopeProject, ScopeID: b.ProjectID}); e != nil {
			return nil, authorizationError(e, false)
		}
		revision, source, e := service.OpenTemplateRevisionForProject(ctx, p.user.ID, p.gateway.Scope.ProjectID, domain.RevisionID(b.RevisionID))
		if e != nil {
			return nil, apiError(e)
		}
		if !slices.Contains(p.gateway.Scope.TemplateIDs, revision.TemplateID) {
			return nil, huma.Error403Forbidden("Revision outside approved templates")
		}
		options, e := decodeRenderOptions(b.Inputs)
		if e != nil {
			return nil, huma.Error400BadRequest("Invalid inputs")
		}
		result, e := service.CreateInstanceRevisionTargetForLifetime(ctx, p.user.ID, p.gateway.Scope.ProjectID, app.OwnerUser, string(p.user.ID), revision.ID, source, app.InstanceLifetimeRequest{TTLSeconds: b.TTLSeconds, Persistent: b.Persistent}, b.AcknowledgeDockerIsolationRisk, in.IdempotencyKey, target, b.ProviderOverride, options)
		if e != nil {
			return nil, apiError(e)
		}
		operation, e := service.GetOperation(ctx, result.OperationID)
		if e != nil {
			return nil, apiError(e)
		}
		return &struct{ Body OperationDTO }{Body: operationDTO(operation)}, nil
	})
	huma.Get(api, "/gateway/operations/{id}", func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"128"`
	}) (*struct{ Body OperationDTO }, error) { p, e := gatewayActor(ctx, "operations.read"); if e != nil {
		return nil, e
	}; access, e := s.options.Authorizer.AuthorizeOperation(ctx, p.user.ID, app.ActionInstanceRead, domain.OperationID(in.ID)); if e != nil {
		return nil, authorizationError(e, false)
	}; if e = gatewayInstance(ctx, s, service, p, access.InstanceID, app.ActionInstanceRead); e != nil {
		return nil, e
	}; o, e := service.GetOperation(ctx, domain.OperationID(in.ID)); if e != nil {
		return nil, apiError(e)
	}; return &struct{ Body OperationDTO }{Body: operationDTO(o)}, nil })
	type gatewayAccessOutput struct {
		CacheControl string `header:"Cache-Control"`
		Body         struct {
			Session      AccessSessionDTO                `json:"session"`
			OneTimeToken string                          `json:"oneTimeToken"`
			Presentation controlplane.AccessPresentation `json:"presentation"`
		}
	}
	huma.Post(api, "/gateway/access-sessions", func(ctx context.Context, in *struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			InstanceID string `json:"instanceId" maxLength:"128"`
			ResourceID string `json:"resourceId" maxLength:"128"`
			Method     string `json:"method" maxLength:"64"`
		}
	}) (*gatewayAccessOutput, error) { p, e := gatewayActor(ctx, "access.create"); if e != nil {
		return nil, e
	}; id := domain.InstanceID(in.Body.InstanceID); if e = gatewayInstance(ctx, s, service, p, id, app.ActionAccessSessionCreate); e != nil {
		return nil, e
	}; remaining := time.Until(p.gateway.ExpiresAt); if remaining <= 0 {
		return nil, huma.Error401Unauthorized("Grant expired")
	}; launch, e := service.CreateAccessSession(ctx, controlplane.AccessSessionCommand{GatewayGrantID: p.gateway.ID, SubjectID: p.user.ID, InstanceID: id, ResourceID: domain.LogicalResourceID(in.Body.ResourceID), Method: in.Body.Method, Duration: 15 * time.Minute, IdempotencyKey: in.IdempotencyKey}); if e != nil {
		return nil, apiError(e)
	}; presentation, e := service.AccessPresentation(ctx, p.user.ID, launch.Session.ID); if e != nil {
		return nil, apiError(e)
	}; out := &gatewayAccessOutput{CacheControl: "no-store"}; out.Body.Session = accessSessionDTO(launch.Session); out.Body.OneTimeToken = launch.LaunchToken; out.Body.Presentation = presentation; return out, nil })
}
