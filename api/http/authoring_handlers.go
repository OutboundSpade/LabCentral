package httpapi

import (
	"context"
	"encoding/json"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"github.com/OutboundSpade/LabCentral/schemas"
	"github.com/danielgtaylor/huma/v2"
)

func registerAuthoring(api huma.API, service *controlplane.Service, security *requestSecurity) {
	huma.Get(api, "/lab-authoring-schema", func(ctx context.Context, _ *struct{}) (*struct{ Body json.RawMessage }, error) {
		if _, ok := requestPrincipal(ctx); !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		return &struct{ Body json.RawMessage }{Body: schemas.LabV1Alpha1()}, nil
	})
	huma.Post(api, "/lab-authoring-documents", func(ctx context.Context, input *struct {
		Body struct {
			Source string `json:"source" maxLength:"1048576"`
		}
	}) (*struct{ Body json.RawMessage }, error) {
		if _, ok := requestPrincipal(ctx); !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		value, err := render.ParseYAML([]byte(input.Body.Source), render.DefaultParseLimits())
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("Document could not be parsed", err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("Document could not be represented")
		}
		return &struct{ Body json.RawMessage }{Body: data}, nil
	})
	authorizeTarget := func(ctx context.Context, id string) (domain.ProviderTargetID, error) {
		target, err := domain.NewProviderTargetID(id)
		if err != nil {
			return "", huma.Error400BadRequest("Invalid target")
		}
		return target, authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"})
	}
	huma.Get(api, "/admin/provider-targets/{id}/deployment-settings", func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*versionedOutput[application.DeploymentSettings], error) { id, err := authorizeTarget(ctx, input.ID); if err != nil {
		return nil, err
	}; result, err := service.GetDeploymentSettings(ctx, id); if err != nil {
		return nil, apiError(err)
	}; return &versionedOutput[application.DeploymentSettings]{ETag: resourceETag(result.ResourceVersion), Body: result}, nil })
	huma.Put(api, "/admin/provider-targets/{id}/deployment-settings", func(ctx context.Context, input *struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    struct {
			Maximum uint32 `json:"maxConcurrentDeployments" minimum:"1" maximum:"16"`
		}
	}) (*versionedOutput[application.DeploymentSettings], error) {
		id, err := authorizeTarget(ctx, input.ID)
		if err != nil {
			return nil, err
		}
		// Version zero identifies the startup default before the first stored override.
		version, err := expectedResourceVersion(input.IfMatch)
		if input.IfMatch == `"0"` {
			version, err = 0, nil
		}
		if err != nil {
			return nil, err
		}
		result, err := service.PutDeploymentSettings(ctx, id, application.DeploymentSettings{MaxConcurrentDeployments: input.Body.Maximum, ResourceVersion: version})
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[application.DeploymentSettings]{ETag: resourceETag(result.ResourceVersion), Body: result}, nil
	})
}
