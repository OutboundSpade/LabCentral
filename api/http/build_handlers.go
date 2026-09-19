package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"reflect"
)

func registerBuildOperations(api huma.API, s *controlplane.Service, security *requestSecurity) {
	// The established preparation representation is domain-shaped. Its unused
	// pointer alternatives marshal as null; Huma object refs are nonnullable by
	// default. Widen those two schema alternatives without changing stored bytes,
	// field names or the accepted representation of existing clients.
	typedInput := api.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(domain.TypedInput{}), false, "")
	for _, name := range []string{"Enum", "SecretRef"} {
		typedInput.Properties[name] = &huma.Schema{AnyOf: []*huma.Schema{typedInput.Properties[name], {Type: "null"}}}
	}
	typedInput.PrecomputeMessages()

	type buildInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID  string `json:"projectId" minLength:"1" maxLength:"128"`
			RevisionID string `json:"revisionId" minLength:"1" maxLength:"128"`
			RecipeID   string `json:"recipeId" minLength:"1" maxLength:"64"`
		}
	}
	huma.Post(api, "/builds", func(ctx context.Context, in *buildInput) (*extensionWorkOutput, error) {
		p, e := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTemplateRevisionPublish, ScopeKind: app.ScopeProject, ScopeID: in.Body.ProjectID})
		if e != nil {
			return nil, e
		}
		w, e := s.RequestBuild(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), domain.RevisionID(in.Body.RevisionID), in.Body.RecipeID, in.IdempotencyKey)
		if e != nil {
			return nil, apiError(e)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
	type importInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID string `json:"projectId" minLength:"1" maxLength:"128"`
			BuildID   string `json:"buildId" minLength:"1" maxLength:"128"`
			TargetID  string `json:"targetId" minLength:"1" maxLength:"128"`
		}
	}
	huma.Post(api, "/image-imports", func(ctx context.Context, in *importInput) (*extensionWorkOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		w, e := s.RequestImport(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), domain.OperationID(in.Body.BuildID), domain.ProviderTargetID(in.Body.TargetID), in.IdempotencyKey)
		if e != nil {
			return nil, apiError(e)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
	type preparationListInput struct {
		ProjectID string `query:"projectId" required:"true" minLength:"1" maxLength:"128"`
	}
	huma.Get(api, "/deployment-preparations", func(ctx context.Context, in *preparationListInput) (*extensionWorkList, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, err := s.ListOwnPreparations(ctx, p.user.ID, domain.ProjectID(in.ProjectID))
		if err != nil {
			return nil, apiError(err)
		}
		out := &extensionWorkList{}
		out.Body.Items = items
		return out, nil
	})
	type prepareInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID   string              `json:"projectId" minLength:"1" maxLength:"128"`
			Preparation app.PreparationWork `json:"preparation"`
		}
	}
	huma.Post(api, "/deployment-preparations", func(ctx context.Context, in *prepareInput) (*extensionWorkOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		w, e := s.RequestPreparation(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), in.Body.Preparation, in.IdempotencyKey)
		if e != nil {
			return nil, apiError(e)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
	type imageInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	type imageOutput struct{ Body app.StoredImageBinding }
	huma.Get(api, "/image-bindings/{id}", func(ctx context.Context, in *imageInput) (*imageOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		b, e := s.ImageBinding(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.ID)
		if e != nil {
			return nil, apiError(e)
		}
		return &imageOutput{Body: b}, nil
	})
	type imagePolicyInput struct {
		ID   string `path:"id" maxLength:"128"`
		Body struct {
			ProjectID string `json:"projectId" minLength:"1" maxLength:"128"`
			Approved  bool   `json:"approved"`
			Retired   bool   `json:"retired"`
		}
	}
	huma.Post(api, "/image-bindings/{id}/policy", func(ctx context.Context, in *imagePolicyInput) (*imageOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		e := s.SetImageApproval(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), in.ID, in.Body.Approved, in.Body.Retired)
		if e != nil {
			return nil, apiError(e)
		}
		b, e := s.ImageBinding(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), in.ID)
		if e != nil {
			return nil, apiError(e)
		}
		return &imageOutput{Body: b}, nil
	})
}
