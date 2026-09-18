package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

type extensionWorkOutput struct{ Body app.ExtensionWork }
type extensionWorkList struct {
	Body struct {
		Items []app.ExtensionWork `json:"items"`
	}
}

func registerSourceOperations(api huma.API, s *controlplane.Service, security *requestSecurity) {
	registerBuildOperations(api, s, security)
	registerLibraries(api, s, security)
	type resolveInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID  string `json:"projectId" minLength:"1" maxLength:"128"`
			TemplateID string `json:"templateId" minLength:"1" maxLength:"128"`
			Binding    string `json:"binding" minLength:"1" maxLength:"257"`
			Reference  string `json:"reference" minLength:"1" maxLength:"1024"`
		}
	}
	huma.Post(api, "/source-resolutions", func(ctx context.Context, in *resolveInput) (*extensionWorkOutput, error) {
		actor, err := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTemplateRevisionPublish, ScopeKind: app.ScopeProject, ScopeID: in.Body.ProjectID})
		if err != nil {
			return nil, err
		}
		w, err := s.ResolveSource(ctx, actor.user.ID, domain.ProjectID(in.Body.ProjectID), domain.TemplateID(in.Body.TemplateID), in.Body.Binding, in.Body.Reference, in.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
	type listInput struct {
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/source-bindings", func(ctx context.Context, in *listInput) (*struct {
		Body struct {
			Items []controlplane.SourceBindingView `json:"items"`
		}
	}, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, e := s.SourceBindings(ctx, p.user.ID, domain.ProjectID(in.ProjectID))
		if e != nil {
			return nil, apiError(e)
		}
		out := &struct {
			Body struct {
				Items []controlplane.SourceBindingView `json:"items"`
			}
		}{}
		out.Body.Items = items
		return out, nil
	})
	huma.Get(api, "/extension-operations", func(ctx context.Context, in *listInput) (*extensionWorkList, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, e := s.ListExtensionWork(ctx, p.user.ID, domain.ProjectID(in.ProjectID))
		if e != nil {
			return nil, apiError(e)
		}
		out := &extensionWorkList{}
		out.Body.Items = items
		return out, nil
	})
	type workInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/extension-operations/{id}", func(ctx context.Context, in *workInput) (*extensionWorkOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		w, e := s.GetExtensionWork(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.OperationID(in.ID))
		if e != nil {
			return nil, apiError(e)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
	huma.Post(api, "/extension-operations/{id}/cancel", func(ctx context.Context, in *workInput) (*extensionWorkOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		if e := s.CancelExtensionWork(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.OperationID(in.ID)); e != nil {
			return nil, apiError(e)
		}
		w, e := s.GetExtensionWork(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.OperationID(in.ID))
		if e != nil {
			return nil, apiError(e)
		}
		return &extensionWorkOutput{Body: w}, nil
	})
}
