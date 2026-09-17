package httpapi

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerTopology(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type input struct {
		ID string `path:"id" maxLength:"128"`
	}
	huma.Get(api, "/lab-instances/{id}/topology", func(ctx context.Context, in *input) (*struct{ Body controlplane.Topology }, error) {
		id, err := domain.NewInstanceID(in.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Lab not found")
		}
		actor, err := authorizeInstanceActor(ctx, security, application.ActionInstanceRead, id)
		if err != nil {
			return nil, err
		}
		value, err := service.Topology(ctx, actor.user.ID, id)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body controlplane.Topology }{Body: value}, nil
	})
}
