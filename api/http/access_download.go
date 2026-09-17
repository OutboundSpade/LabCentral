package httpapi

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerAccessDownload(api huma.API, service *controlplane.Service) {
	type input struct {
		ID   string `path:"id" maxLength:"128"`
		Body struct {
			Token string `json:"token" minLength:"32" maxLength:"256"`
		}
	}
	type output struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		CacheControl       string `header:"Cache-Control"`
		ReferrerPolicy     string `header:"Referrer-Policy"`
		Body               []byte
	}
	huma.Post(api, "/access-sessions/{id}/download", func(ctx context.Context, in *input) (*output, error) {
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		id, err := domain.NewAccessSessionID(in.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Session not found")
		}
		value, err := service.DownloadAccess(ctx, actor.user.ID, id, in.Body.Token)
		if err != nil {
			return nil, apiError(err)
		}
		return &output{ContentType: value.MediaType, ContentDisposition: fmt.Sprintf("attachment; filename=%q", value.Filename), CacheControl: "no-store", ReferrerPolicy: "no-referrer", Body: value.Content}, nil
	})
}
