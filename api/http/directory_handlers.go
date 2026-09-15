package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

type DirectoryQuery struct {
	After string `query:"after" maxLength:"128"`
	Limit uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type ProjectChoiceDTO struct {
	application.ProjectRecord
	AllowedActions []application.PermissionAction `json:"allowedActions"`
}
type ProjectDirectoryDTO struct {
	Items      []ProjectChoiceDTO `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}
type UserDirectoryDTO struct {
	Items      []application.UserRecord `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

type OrganizationDirectoryDTO struct {
	Items      []application.OrganizationRecord `json:"items"`
	NextCursor string                           `json:"nextCursor,omitempty"`
}
type TargetGrantDirectoryDTO struct {
	Items      []application.TargetGrantRecord `json:"items"`
	NextCursor string                          `json:"nextCursor,omitempty"`
}

func registerDirectory(api huma.API, security *requestSecurity) {
	huma.Get(api, "/organizations", func(ctx context.Context, input *DirectoryQuery) (*struct{ Body OrganizationDirectoryDTO }, error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionOrganizationManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		items, err := security.options.Repository.ListOrganizations(ctx, input.After, input.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("Organizations could not be loaded")
		}
		output := &struct{ Body OrganizationDirectoryDTO }{Body: OrganizationDirectoryDTO{Items: items}}
		if len(items) == int(input.Limit) {
			output.Body.NextCursor = string(items[len(items)-1].ID)
		}
		return output, nil
	})
	huma.Get(api, "/provider-target-grants", func(ctx context.Context, input *DirectoryQuery) (*struct{ Body TargetGrantDirectoryDTO }, error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		items, err := security.options.Repository.ListTargetGrants(ctx, input.After, input.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("Target access could not be loaded")
		}
		output := &struct{ Body TargetGrantDirectoryDTO }{Body: TargetGrantDirectoryDTO{Items: items}}
		if len(items) == int(input.Limit) {
			output.Body.NextCursor = items[len(items)-1].ID
		}
		return output, nil
	})

	type membershipQuery struct {
		DirectoryQuery
		OrganizationID string `query:"organizationId" required:"true" maxLength:"128"`
	}
	type membershipPage struct {
		Items      []application.MembershipRecord `json:"items"`
		NextCursor string                         `json:"nextCursor,omitempty"`
	}
	huma.Get(api, "/memberships", func(ctx context.Context, input *membershipQuery) (*struct{ Body membershipPage }, error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: input.OrganizationID}); err != nil {
			return nil, err
		}
		items, err := security.options.Repository.ListMemberships(ctx, domain.OrganizationID(input.OrganizationID), input.After, input.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("Memberships could not be loaded")
		}
		output := &struct{ Body membershipPage }{Body: membershipPage{Items: items}}
		if len(items) == int(input.Limit) {
			output.Body.NextCursor = items[len(items)-1].ID
		}
		return output, nil
	})
	huma.Get(api, "/projects", func(ctx context.Context, input *DirectoryQuery) (*struct{ Body ProjectDirectoryDTO }, error) {
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Sign in required")
		}
		snapshot, err := security.options.Repository.AuthorizationSnapshot(ctx, actor.user.ID, time.Now().UTC())
		if err != nil {
			return nil, huma.Error500InternalServerError("Project permissions could not be loaded")
		}
		output := &struct{ Body ProjectDirectoryDTO }{Body: ProjectDirectoryDTO{Items: []ProjectChoiceDTO{}}}
		after := input.After
		for {
			candidates, err := security.options.Repository.ListProjects(ctx, after, 200)
			if err != nil {
				return nil, huma.Error500InternalServerError("Projects could not be loaded")
			}
			for _, project := range candidates {
				request := application.AuthorizationRequest{SubjectID: actor.user.ID, ScopeKind: application.ScopeProject, ScopeID: string(project.ID), Action: application.ActionTemplateRead}
				if err := security.options.Authorizer.Authorize(ctx, request); err != nil {
					if errors.Is(err, authorization.ErrDenied) {
						continue
					}
					return nil, authorizationError(err, false)
				}
				choice := ProjectChoiceDTO{ProjectRecord: project, AllowedActions: []application.PermissionAction{application.ActionTemplateRead}}
				for _, action := range []application.PermissionAction{application.ActionTemplateCreate, application.ActionQuotaManage, application.ActionProjectManage} {
					request.Action = action
					if err := security.options.Authorizer.Authorize(ctx, request); err == nil {
						choice.AllowedActions = append(choice.AllowedActions, action)
					} else if !errors.Is(err, authorization.ErrDenied) {
						return nil, authorizationError(err, false)
					}
				}
				request.Action = application.ActionInstanceBatchCreate
				for _, grant := range snapshot.Grants {
					request.TargetID = grant.TargetID
					if err := security.options.Authorizer.Authorize(ctx, request); err == nil {
						choice.AllowedActions = append(choice.AllowedActions, request.Action)
						break
					} else if !errors.Is(err, authorization.ErrDenied) {
						return nil, authorizationError(err, false)
					}
				}
				output.Body.Items = append(output.Body.Items, choice)
				if len(output.Body.Items) == int(input.Limit) {
					output.Body.NextCursor = string(choice.ID)
					return output, nil
				}
			}
			if len(candidates) < 200 {
				break
			}
			after = string(candidates[len(candidates)-1].ID)
		}
		return output, nil
	})
	huma.Get(api, "/users", func(ctx context.Context, input *DirectoryQuery) (*struct{ Body UserDirectoryDTO }, error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		items, err := security.options.Repository.ListDirectoryUsers(ctx, "", input.After, input.Limit, time.Now().UTC())
		if err != nil {
			return nil, huma.Error500InternalServerError("People could not be loaded")
		}
		output := &struct{ Body UserDirectoryDTO }{Body: UserDirectoryDTO{Items: items}}
		if len(items) == int(input.Limit) {
			output.Body.NextCursor = string(items[len(items)-1].ID)
		}
		return output, nil
	})
	type classroomQuery struct {
		DirectoryQuery
		ID       string `path:"id" maxLength:"128"`
		TargetID string `query:"targetId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/projects/{id}/members", func(ctx context.Context, input *classroomQuery) (*struct{ Body UserDirectoryDTO }, error) {
		target, err := domain.NewProviderTargetID(input.TargetID)
		if err != nil {
			return nil, huma.Error400BadRequest("Target is invalid")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionInstanceBatchCreate, ScopeKind: application.ScopeProject, ScopeID: input.ID, TargetID: target}); err != nil {
			return nil, err
		}
		scope, err := security.options.Repository.ResolveScope(ctx, application.ScopeProject, input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Project was not found")
		}
		output := &struct{ Body UserDirectoryDTO }{Body: UserDirectoryDTO{Items: []application.UserRecord{}}}
		after := input.After
		for {
			candidates, err := security.options.Repository.ListDirectoryUsers(ctx, scope.OrganizationID, after, 200, time.Now().UTC())
			if err != nil {
				return nil, huma.Error500InternalServerError("Classroom could not be loaded")
			}
			for _, user := range candidates {
				err := security.options.Authorizer.Authorize(ctx, application.AuthorizationRequest{SubjectID: user.ID, Action: application.ActionInstanceCreate, ScopeKind: application.ScopeProject, ScopeID: input.ID, TargetID: target, OwnerKind: application.OwnerUser, OwnerID: string(user.ID)})
				if err == nil {
					output.Body.Items = append(output.Body.Items, user)
					if len(output.Body.Items) == int(input.Limit) {
						output.Body.NextCursor = string(user.ID)
						return output, nil
					}
				} else if !errors.Is(err, authorization.ErrDenied) {
					return nil, authorizationError(err, false)
				}
			}
			if len(candidates) < 200 {
				break
			}
			after = string(candidates[len(candidates)-1].ID)
		}
		return output, nil
	})
}
