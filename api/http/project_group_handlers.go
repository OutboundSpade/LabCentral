package httpapi

import (
	"context"
	"errors"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func projectGroupError(err error) error {
	if errors.Is(err, app.ErrGroupHasLabs) {
		return huma.Error409Conflict("End all active group labs before deleting the group")
	}
	if errors.Is(err, app.ErrGroupEnrollmentClosed) {
		return huma.Error409Conflict("Group enrollment is closed")
	}
	if errors.Is(err, app.ErrGroupMembershipConflict) {
		return huma.Error409Conflict("Leave your current project group before joining another")
	}
	if errors.Is(err, app.ErrAuthVersionConflict) {
		return huma.NewError(412, "The group version no longer matches")
	}
	return apiError(err)
}
func registerProjectGroups(api huma.API, s *controlplane.Service, security *requestSecurity) {
	type listInput struct {
		ProjectID string `path:"projectId" minLength:"1" maxLength:"128"`
		After     string `query:"after" maxLength:"128"`
		Limit     uint32 `query:"limit" default:"100" minimum:"1" maximum:"200"`
	}
	huma.Get(api, "/projects/{projectId}/groups", func(ctx context.Context, in *listInput) (*struct{ Body app.ProjectGroupPage }, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		result, err := s.ListProjectGroups(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.After, in.Limit)
		if err != nil {
			return nil, projectGroupError(err)
		}
		return &struct{ Body app.ProjectGroupPage }{result}, nil
	})

	type rosterInput struct {
		ProjectID string `path:"projectId" minLength:"1" maxLength:"128"`
		ID        string `path:"id" minLength:"1" maxLength:"128"`
		After     string `query:"after" maxLength:"128"`
		Limit     uint32 `query:"limit" default:"100" minimum:"1" maximum:"200"`
	}
	huma.Get(api, "/projects/{projectId}/groups/{id}/members", func(ctx context.Context, in *rosterInput) (*struct{ Body app.ProjectGroupMemberPage }, error) {
		p, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		result, err := s.ListProjectGroupMembers(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.TeamID(in.ID), in.After, in.Limit)
		if err != nil {
			return nil, projectGroupError(err)
		}
		return &struct{ Body app.ProjectGroupMemberPage }{result}, nil
	})
	type createInput struct {
		ProjectID      string `path:"projectId" minLength:"1" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Names          []string `json:"names" minItems:"1" maxItems:"100"`
			EnrollmentOpen bool     `json:"enrollmentOpen"`
		}
	}
	huma.Post(api, "/projects/{projectId}/groups", func(ctx context.Context, in *createInput) (*struct{ Body app.ProjectGroupPage }, error) {
		p, err := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTeamManage, ScopeKind: app.ScopeProject, ScopeID: in.ProjectID})
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, name := range in.Body.Names {
			if !app.ValidWorkspaceName(name) || seen[name] {
				return nil, huma.Error400BadRequest("Group names must be distinct and contain 1-128 printable characters")
			}
			seen[name] = true
		}
		result, err := s.CreateProjectGroups(ctx, p.user.ID, domain.ProjectID(in.ProjectID), in.Body.Names, in.Body.EnrollmentOpen, in.IdempotencyKey)
		if err != nil {
			return nil, projectGroupError(err)
		}
		return &struct{ Body app.ProjectGroupPage }{app.ProjectGroupPage{Items: result}}, nil
	})
	type enrollmentInput struct {
		ProjectID      string `path:"projectId" minLength:"1" maxLength:"128"`
		ID             string `path:"id" minLength:"1" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		IfMatch        string `header:"If-Match"`
		Body           struct {
			Open bool `json:"open"`
		}
	}
	huma.Put(api, "/projects/{projectId}/groups/{id}/enrollment", func(ctx context.Context, in *enrollmentInput) (*versionedOutput[app.ProjectGroup], error) {
		p, err := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTeamManage, ScopeKind: app.ScopeProject, ScopeID: in.ProjectID})
		if err != nil {
			return nil, err
		}
		version, err := expectedResourceVersion(in.IfMatch)
		if err != nil {
			return nil, err
		}
		result, err := s.ChangeProjectGroupEnrollment(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.TeamID(in.ID), in.Body.Open, version, in.IdempotencyKey)
		if err != nil {
			return nil, projectGroupError(err)
		}
		return &versionedOutput[app.ProjectGroup]{ETag: resourceETag(result.ResourceVersion), Body: result}, nil
	})
	type deleteInput struct {
		ProjectID      string `path:"projectId" minLength:"1" maxLength:"128"`
		ID             string `path:"id" minLength:"1" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		IfMatch        string `header:"If-Match"`
	}
	huma.Delete(api, "/projects/{projectId}/groups/{id}", func(ctx context.Context, in *deleteInput) (*struct{}, error) {
		p, err := authorizeActor(ctx, security, app.AuthorizationRequest{Action: app.ActionTeamManage, ScopeKind: app.ScopeProject, ScopeID: in.ProjectID})
		if err != nil {
			return nil, err
		}
		version, err := expectedResourceVersion(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if err = s.DeleteProjectGroup(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.TeamID(in.ID), version, in.IdempotencyKey); err != nil {
			return nil, projectGroupError(err)
		}
		return &struct{}{}, nil
	})
	type memberInput struct {
		ProjectID      string `path:"projectId" minLength:"1" maxLength:"128"`
		ID             string `path:"id" minLength:"1" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
	}
	for _, join := range []bool{true, false} {
		action := "leave"
		if join {
			action = "join"
		}
		huma.Post(api, "/projects/{projectId}/groups/{id}/"+action, func(ctx context.Context, in *memberInput) (*versionedOutput[app.ProjectGroup], error) {
			p, ok := requestPrincipal(ctx)
			if !ok {
				return nil, huma.Error401Unauthorized("Authentication required")
			}
			result, err := s.ChangeProjectGroupMembership(ctx, p.user.ID, domain.ProjectID(in.ProjectID), domain.TeamID(in.ID), join, in.IdempotencyKey)
			if err != nil {
				return nil, projectGroupError(err)
			}
			return &versionedOutput[app.ProjectGroup]{ETag: resourceETag(result.ResourceVersion), Body: result}, nil
		})
	}
}
