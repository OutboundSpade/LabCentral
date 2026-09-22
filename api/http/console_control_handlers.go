package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerConsoleControl(api huma.API, s *controlplane.Service) {
	type input struct {
		ID string `path:"id" minLength:"1" maxLength:"128"`
	}
	type output struct {
		CacheControl string `header:"Cache-Control"`
		Body         app.ConsolePresence
	}
	huma.Get(api, "/access-sessions/{id}/console", func(ctx context.Context, in *input) (*output, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		out, err := s.ConsolePresence(ctx, p.user.ID, domain.AccessSessionID(in.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &output{CacheControl: "no-store", Body: out}, nil
	})
	type changeInput struct {
		OnlyIfAlone    bool   `query:"onlyIfAlone" default:"false"`
		ID             string `path:"id" minLength:"1" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
	}
	for _, take := range []bool{true, false} {
		action := "release-control"
		if take {
			action = "take-control"
		}
		huma.Post(api, "/access-sessions/{id}/console/"+action, func(ctx context.Context, in *changeInput) (*output, error) {
			p, ok := requestPrincipal(ctx)
			if !ok {
				return nil, huma.Error401Unauthorized("Authentication required")
			}
			out, err := s.ChangeConsoleControlConditional(ctx, p.user.ID, domain.AccessSessionID(in.ID), take, in.OnlyIfAlone, in.IdempotencyKey)
			if err != nil {
				return nil, apiError(err)
			}
			return &output{CacheControl: "no-store", Body: out}, nil
		})
	}
}
