package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerAdministration(api huma.API, security *requestSecurity) {
	if security.options.Mode == AuthModeNone {
		return
	}
	registerDirectory(api, security)
	type userInput struct {
		Body struct {
			ID              string `json:"id" minLength:"1" maxLength:"128"`
			Username        string `json:"username" minLength:"1" maxLength:"63"`
			DisplayName     string `json:"displayName" minLength:"1" maxLength:"128"`
			Password        string `json:"password,omitempty" maxLength:"1024"`
			ExternalSubject string `json:"externalSubject,omitempty" maxLength:"512"`
		}
	}
	huma.Post(api, "/users", func(ctx context.Context, input *userInput) (*versionedOutput[application.UserRecord], error) {
		if security.options.Mode == AuthModeNone {
			return nil, huma.Error404NotFound("User administration is not enabled")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		userID, err := domain.NewUserID(input.Body.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("User ID is invalid")
		}
		switch security.options.Mode {
		case AuthModeLocal:
			if input.Body.ExternalSubject != "" {
				return nil, huma.Error400BadRequest("External subject is not accepted in local mode")
			}
			password := []byte(input.Body.Password)
			defer clear(password)
			if err := security.options.Manager.CreateLocalUser(ctx, userID, input.Body.Username, input.Body.DisplayName, password); err != nil {
				return nil, huma.Error400BadRequest("User could not be created")
			}
		case AuthModeOIDC:
			if input.Body.Password != "" || input.Body.ExternalSubject == "" {
				return nil, huma.Error400BadRequest("An external subject is required in OIDC mode")
			}
			if err := security.options.Manager.CreateOIDCUser(ctx, userID, input.Body.Username, input.Body.DisplayName, security.options.OIDCIssuer, input.Body.ExternalSubject); err != nil {
				return nil, huma.Error400BadRequest("User could not be created")
			}
		}
		user, err := security.options.Repository.UserByID(ctx, userID)
		if err != nil {
			return nil, huma.Error500InternalServerError("User could not be loaded")
		}
		user.PasswordHash = ""
		return &versionedOutput[application.UserRecord]{ETag: resourceETag(user.ResourceVersion), Body: user}, nil
	})
	type userStateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
	}
	setUserDisabled := func(disabled bool) func(context.Context, *userStateInput) (*versionedOutput[application.UserRecord], error) {
		return func(ctx context.Context, input *userStateInput) (*versionedOutput[application.UserRecord], error) {
			if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
				return nil, err
			}
			userID, err := domain.NewUserID(input.ID)
			if err != nil {
				return nil, huma.Error400BadRequest("User ID is invalid")
			}
			expected, err := expectedResourceVersion(input.IfMatch)
			if err != nil {
				return nil, err
			}
			user, err := security.options.Repository.SetUserDisabled(ctx, userID, disabled, expected, time.Now().UTC())
			if err != nil {
				return nil, administrationWriteError(err, "User state could not be changed")
			}
			user.PasswordHash = ""
			return &versionedOutput[application.UserRecord]{ETag: resourceETag(user.ResourceVersion), Body: user}, nil
		}
	}
	huma.Post(api, "/users/{id}/actions/disable", setUserDisabled(true))
	huma.Post(api, "/users/{id}/actions/enable", setUserDisabled(false))
	type issueRecoveryInput struct {
		ID string `path:"id"`
	}
	huma.Post(api, "/users/{id}/actions/issue-password-recovery", func(ctx context.Context, input *issueRecoveryInput) (*struct {
		Body struct {
			Token string `json:"token"`
		}
	}, error) {
		if security.options.Mode != AuthModeLocal {
			return nil, huma.Error404NotFound("Local password recovery is not enabled")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		userID, err := domain.NewUserID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("User was not found")
		}
		token, err := security.options.Manager.IssuePasswordRecovery(ctx, userID)
		if err != nil {
			return nil, huma.Error404NotFound("User was not found")
		}
		output := &struct {
			Body struct {
				Token string `json:"token"`
			}
		}{}
		output.Body.Token = token
		return output, nil
	})

	type organizationInput struct {
		Body application.OrganizationRecord
	}
	huma.Post(api, "/organizations", func(ctx context.Context, input *organizationInput) (*versionedOutput[application.OrganizationRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionOrganizationManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateOrganization(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Organization could not be created")
		}
		return &versionedOutput[application.OrganizationRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type organizationUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.OrganizationRecord
	}
	huma.Put(api, "/organizations/{id}", func(ctx context.Context, input *organizationUpdateInput) (*versionedOutput[application.OrganizationRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionOrganizationManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Organization path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateOrganization(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Organization could not be updated")
		}
		return &versionedOutput[application.OrganizationRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type projectInput struct {
		Body application.ProjectRecord
	}
	huma.Post(api, "/projects", func(ctx context.Context, input *projectInput) (*versionedOutput[application.ProjectRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProjectManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateProject(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Project could not be created")
		}
		return &versionedOutput[application.ProjectRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type projectUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.ProjectRecord
	}
	huma.Put(api, "/projects/{id}", func(ctx context.Context, input *projectUpdateInput) (*versionedOutput[application.ProjectRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProjectManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Project path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateProject(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Project could not be updated")
		}
		return &versionedOutput[application.ProjectRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type teamInput struct {
		Body application.TeamRecord
	}
	huma.Post(api, "/teams", func(ctx context.Context, input *teamInput) (*versionedOutput[application.TeamRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionTeamManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateTeam(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Team could not be created")
		}
		return &versionedOutput[application.TeamRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type teamUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.TeamRecord
	}
	huma.Put(api, "/teams/{id}", func(ctx context.Context, input *teamUpdateInput) (*versionedOutput[application.TeamRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTeamManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Team path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateTeam(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Team could not be updated")
		}
		return &versionedOutput[application.TeamRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type membershipInput struct {
		Body application.MembershipRecord
	}
	huma.Post(api, "/memberships", func(ctx context.Context, input *membershipInput) (*versionedOutput[application.MembershipRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateMembership(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be created")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type membershipUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.MembershipRecord
	}
	huma.Put(api, "/memberships/{id}", func(ctx context.Context, input *membershipUpdateInput) (*versionedOutput[application.MembershipRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID {
			return nil, huma.Error400BadRequest("Membership path and body IDs must match")
		}
		if input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Membership renewal requires validFrom")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateMembership(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be updated")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type membershipRevokeInput struct {
		ID             string `path:"id"`
		OrganizationID string `query:"organizationId" required:"true"`
		IfMatch        string `header:"If-Match"`
	}
	huma.Post(api, "/memberships/{id}/actions/revoke", func(ctx context.Context, input *membershipRevokeInput) (*versionedOutput[application.MembershipRecord], error) {
		organizationID, err := domain.NewOrganizationID(input.OrganizationID)
		if err != nil {
			return nil, huma.Error400BadRequest("Organization ID is invalid")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(organizationID)}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeMembership(ctx, input.ID, organizationID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be revoked")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type roleAssignmentInput struct {
		Body application.RoleAssignmentRecord
	}
	huma.Post(api, "/role-assignments", func(ctx context.Context, input *roleAssignmentInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionRoleAssignmentManage, ScopeKind: input.Body.ScopeKind, ScopeID: input.Body.ScopeID,
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateRoleAssignment(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be created")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type roleAssignmentUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.RoleAssignmentRecord
	}
	huma.Put(api, "/role-assignments/{id}", func(ctx context.Context, input *roleAssignmentUpdateInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionRoleAssignmentManage, ScopeKind: input.Body.ScopeKind, ScopeID: input.Body.ScopeID}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID || input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Role assignment path/body or validity is invalid")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateRoleAssignment(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be updated")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type roleAssignmentRevokeInput struct {
		ID        string                `path:"id"`
		ScopeKind application.ScopeKind `query:"scopeKind" required:"true"`
		ScopeID   string                `query:"scopeId" required:"true"`
		IfMatch   string                `header:"If-Match"`
	}
	huma.Post(api, "/role-assignments/{id}/actions/revoke", func(ctx context.Context, input *roleAssignmentRevokeInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionRoleAssignmentManage, ScopeKind: input.ScopeKind, ScopeID: input.ScopeID}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeRoleAssignment(ctx, input.ID, input.ScopeKind, input.ScopeID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be revoked")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type targetGrantInput struct {
		Body application.TargetGrantRecord
	}
	huma.Post(api, "/provider-target-grants", func(ctx context.Context, input *targetGrantInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateTargetGrant(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be created")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type targetGrantUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.TargetGrantRecord
	}
	huma.Put(api, "/provider-target-grants/{id}", func(ctx context.Context, input *targetGrantUpdateInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID || input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Provider target grant path/body or validity is invalid")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateTargetGrant(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be updated")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type targetGrantRevokeInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
	}
	huma.Post(api, "/provider-target-grants/{id}/actions/revoke", func(ctx context.Context, input *targetGrantRevokeInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeTargetGrant(ctx, input.ID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be revoked")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
}

func resourceETag(version domain.ResourceVersion) string {
	return fmt.Sprintf(`"%d"`, version)
}

func expectedResourceVersion(value string) (domain.ResourceVersion, error) {
	if value == "" {
		return 0, huma.NewError(http.StatusPreconditionRequired, "If-Match is required")
	}
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, huma.Error400BadRequest("If-Match must contain one quoted resource version")
	}
	parsed, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	if err != nil || parsed == 0 {
		return 0, huma.Error400BadRequest("If-Match resource version is invalid")
	}
	return domain.ResourceVersion(parsed), nil
}

func expectedUpsertResourceVersion(ifMatch, ifNoneMatch string) (domain.ResourceVersion, error) {
	if ifMatch != "" && ifNoneMatch != "" {
		return 0, huma.Error400BadRequest("If-Match and If-None-Match cannot be combined")
	}
	if ifNoneMatch != "" {
		if ifNoneMatch != "*" {
			return 0, huma.Error400BadRequest("If-None-Match must be *")
		}
		return 0, nil
	}
	if ifMatch == "" {
		return 0, huma.NewError(http.StatusPreconditionRequired, "If-Match or If-None-Match is required")
	}
	if len(ifMatch) < 3 || ifMatch[0] != '"' || ifMatch[len(ifMatch)-1] != '"' || strings.Contains(ifMatch[1:len(ifMatch)-1], `"`) {
		return 0, huma.Error400BadRequest("If-Match must contain one quoted resource version")
	}
	parsed, err := strconv.ParseUint(ifMatch[1:len(ifMatch)-1], 10, 64)
	if err != nil || parsed == 0 {
		return 0, huma.Error400BadRequest("If-Match resource version is invalid")
	}
	return domain.ResourceVersion(parsed), nil
}

func administrationWriteError(err error, message string) error {
	switch {
	case errors.Is(err, application.ErrAuthVersionConflict):
		return huma.NewError(http.StatusPreconditionFailed, "The resource version no longer matches")
	case errors.Is(err, application.ErrLastPlatformAdministrator):
		return huma.Error409Conflict("At least one active platform administrator must remain")
	case errors.Is(err, application.ErrAuthRecordNotFound):
		return huma.Error404NotFound("The requested administrative resource was not found")
	default:
		return huma.Error400BadRequest(message)
	}
}
