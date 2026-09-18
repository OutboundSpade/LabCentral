package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

type libraryListOutput struct {
	Body struct {
		Items []app.TemplateLibrary `json:"items"`
	}
}
type libraryEntriesOutput struct {
	Body struct {
		Items []app.LibraryEntry `json:"items"`
	}
}

func registerLibraries(api huma.API, s *controlplane.Service, security *requestSecurity) {
	type listInput struct {
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	type libraryInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/template-libraries", func(ctx context.Context, in *listInput) (*libraryListOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, err := s.ListLibraries(ctx, p.user.ID, domain.ProjectID(in.ProjectID))
		if err != nil {
			return nil, apiError(err)
		}
		out := &libraryListOutput{}
		out.Body.Items = items
		return out, nil
	})
	type createInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID       string `json:"projectId" minLength:"1" maxLength:"128"`
			Binding         string `json:"binding" minLength:"1" maxLength:"257"`
			Reference       string `json:"reference" minLength:"1" maxLength:"1024"`
			IntervalSeconds uint32 `json:"intervalSeconds,omitempty" default:"900" minimum:"60" maximum:"86400"`
		}
	}
	huma.Post(api, "/template-libraries", func(ctx context.Context, in *createInput) (*struct{ Body app.TemplateLibrary }, error) {
		p, err := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTemplateRevisionPublish, ScopeKind: app.ScopeProject, ScopeID: in.Body.ProjectID})
		if err != nil {
			return nil, err
		}
		library, err := s.CreateLibrary(ctx, p.user.ID, domain.ProjectID(in.Body.ProjectID), in.Body.Binding, in.Body.Reference, in.IdempotencyKey, in.Body.IntervalSeconds)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body app.TemplateLibrary }{Body: library}, nil
	})
	huma.Get(api, "/template-libraries/{id}/entries", func(ctx context.Context, in *libraryInput) (*libraryEntriesOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, err := s.LibraryEntries(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.ID)
		if err != nil {
			return nil, apiError(err)
		}
		out := &libraryEntriesOutput{}
		out.Body.Items = items
		return out, nil
	})
	huma.Get(api, "/template-libraries/{id}/history", func(ctx context.Context, in *libraryInput) (*extensionWorkList, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		items, err := s.LibraryHistory(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.ID)
		if err != nil {
			return nil, apiError(err)
		}
		out := &extensionWorkList{}
		out.Body.Items = items
		return out, nil
	})
	type syncInput struct {
		ID             string `path:"id" maxLength:"128"`
		ProjectID      string `query:"projectId" required:"true" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
	}
	huma.Post(api, "/template-libraries/{id}/sync", func(ctx context.Context, in *syncInput) (*extensionWorkOutput, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		work, err := s.SyncLibrary(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.ID, in.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &extensionWorkOutput{Body: work}, nil
	})
	type pauseInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
		Body      struct {
			Paused bool `json:"paused"`
		}
	}
	huma.Post(api, "/template-libraries/{id}/schedule", func(ctx context.Context, in *pauseInput) (*struct {
		Body struct {
			Paused bool `json:"paused"`
		}
	}, error) { p, ok := requestPrincipal(ctx); if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}; if err := s.SetLibraryPaused(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.ID, in.Body.Paused); err != nil {
		return nil, apiError(err)
	}; out := &struct {
		Body struct {
			Paused bool `json:"paused"`
		}
	}{}; out.Body.Paused = in.Body.Paused; return out, nil })
}
