package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"strconv"
	"strings"
	"time"
)

type OperationsPageDTO struct {
	Items      []OperationDTO `json:"items"`
	Queued     uint64         `json:"queued"`
	Running    uint64         `json:"running"`
	Failed     uint64         `json:"failed"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

func registerOperations(api huma.API, service *controlplane.Service, security *requestSecurity) {
	admin := func(ctx context.Context) (principal, error) {
		return authorizeActor(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"})
	}
	huma.Get(api, "/admin/operations", func(ctx context.Context, input *struct {
		ProjectID string `query:"projectId" maxLength:"128"`
		Filter    string `query:"filter" default:"active" enum:"active,failed,all"`
		Cursor    string `query:"cursor" maxLength:"512"`
		Limit     uint32 `query:"limit" default:"100" minimum:"1" maximum:"200"`
	}) (*struct{ Body OperationsPageDTO }, error) {
		actor, err := admin(ctx)
		if err != nil {
			return nil, err
		}
		q := application.OperationQuery{Filter: input.Filter, Limit: input.Limit}
		if input.ProjectID != "" {
			id, err := domain.NewProjectID(input.ProjectID)
			if err != nil {
				return nil, huma.Error400BadRequest("Invalid project")
			}
			q.ProjectID = id
		}
		if input.Cursor != "" {
			raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
			parts := strings.SplitN(string(raw), "/", 2)
			if err != nil || len(parts) != 2 {
				return nil, huma.Error400BadRequest("Invalid cursor")
			}
			micros, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || micros <= 0 {
				return nil, huma.Error400BadRequest("Invalid cursor")
			}
			id, err := domain.NewOperationID(parts[1])
			if err != nil {
				return nil, huma.Error400BadRequest("Invalid cursor")
			}
			q.AfterTime = time.UnixMicro(micros)
			q.AfterID = id
		}
		page, err := service.ReadOperations(ctx, actor.user.ID, q)
		if err != nil {
			return nil, apiError(err)
		}
		out := OperationsPageDTO{Items: []OperationDTO{}, Queued: page.Queued, Running: page.Running, Failed: page.Failed}
		for _, r := range page.Items {
			out.Items = append(out.Items, operationDTO(r))
		}
		if page.HasMore && len(page.Items) > 0 {
			last := page.Items[len(page.Items)-1]
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d/%s", last.CreatedAt.UnixMicro(), last.ID)))
		}
		if _, err := admin(ctx); err != nil {
			return nil, err
		}
		return &struct{ Body OperationsPageDTO }{out}, nil
	})
	huma.Get(api, "/admin/lab-instances/{id}/usage", func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*struct{ Body controlplane.InstanceUsage }, error) {
		actor, err := admin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := domain.NewInstanceID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Lab not found")
		}
		out, err := service.ReadInstanceUsage(ctx, actor.user.ID, id)
		if err != nil {
			return nil, apiError(err)
		}
		if _, err := admin(ctx); err != nil {
			return nil, err
		}
		return &struct{ Body controlplane.InstanceUsage }{out}, nil
	})
}
