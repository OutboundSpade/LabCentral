package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerAccess(api huma.API, service *controlplane.Service, security *requestSecurity) {
	registerTopology(api, service, security)
	registerAccessDownload(api, service)
	registerConsoleControl(api, service)
	type createAccessInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			InstanceID      string `json:"instanceId" minLength:"1" maxLength:"128"`
			ResourceID      string `json:"resourceId" minLength:"1" maxLength:"128"`
			Method          string `json:"method" minLength:"1" maxLength:"64" pattern:"^[a-z][a-z0-9.-]*$"`
			DurationSeconds uint32 `json:"durationSeconds,omitempty" minimum:"60" maximum:"3600" default:"1800"`
		}
	}
	type createAccessOutput struct {
		Status         int
		Location       string `header:"Location"`
		CacheControl   string `header:"Cache-Control"`
		ReferrerPolicy string `header:"Referrer-Policy"`
		Body           struct {
			Session      AccessSessionDTO `json:"session"`
			LaunchURL    string           `json:"launchUrl"`
			OneTimeToken string           `json:"oneTimeToken"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "create-access-session", Method: http.MethodPost, Path: "/access-sessions", DefaultStatus: http.StatusCreated},
		func(ctx context.Context, input *createAccessInput) (*createAccessOutput, error) {
			instanceID, err := domain.NewInstanceID(input.Body.InstanceID)
			if err != nil {
				return nil, huma.Error404NotFound("Resource was not found")
			}
			resourceID, err := domain.NewLogicalResourceID(input.Body.ResourceID)
			if err != nil {
				return nil, huma.Error400BadRequest("Resource ID is invalid")
			}
			actor, err := authorizeInstanceActor(ctx, security, application.ActionAccessSessionCreate, instanceID)
			if err != nil {
				return nil, err
			}
			launch, err := service.CreateAccessSession(ctx, controlplane.AccessSessionCommand{
				SubjectID: actor.user.ID, InstanceID: instanceID, ResourceID: resourceID, Method: input.Body.Method,
				Duration: time.Duration(input.Body.DurationSeconds) * time.Second, IdempotencyKey: input.IdempotencyKey,
			})
			if err != nil {
				return nil, apiError(err)
			}
			output := &createAccessOutput{Status: http.StatusCreated, Location: "/api/v1/access-sessions/" + string(launch.Session.ID), CacheControl: "no-store", ReferrerPolicy: "no-referrer"}
			output.Body.Session = accessSessionDTO(launch.Session)
			output.Body.LaunchURL = launch.LaunchURL
			output.Body.OneTimeToken = launch.LaunchToken
			return output, nil
		})

	type accessInput struct {
		ID string `path:"id" maxLength:"128"`
	}
	huma.Get(api, "/access-sessions/{id}/presentation", func(ctx context.Context, input *accessInput) (*struct {
		Body controlplane.AccessPresentation
	}, error) {
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		id, err := domain.NewAccessSessionID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Session not found")
		}
		result, err := service.AccessPresentation(ctx, actor.user.ID, id)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct {
			Body controlplane.AccessPresentation
		}{Body: result}, nil
	})
	huma.Get(api, "/access-sessions/{id}", func(ctx context.Context, input *accessInput) (*versionedOutput[AccessSessionDTO], error) {
		id, err := domain.NewAccessSessionID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Resource was not found")
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		record, err := service.GetAccessSession(ctx, actor.user.ID, id)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[AccessSessionDTO]{ETag: resourceETag(record.ResourceVersion), Body: accessSessionDTO(record)}, nil
	})

	type revokeAccessInput struct {
		ID             string `path:"id" maxLength:"128"`
		IfMatch        string `header:"If-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Reason string `json:"reason,omitempty" maxLength:"128"`
		}
	}
	huma.Post(api, "/access-sessions/{id}/actions/revoke", func(ctx context.Context, input *revokeAccessInput) (*versionedOutput[AccessSessionDTO], error) {
		id, err := domain.NewAccessSessionID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Resource was not found")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		record, err := service.RevokeAccessSession(ctx, actor.user.ID, id, input.Body.Reason, input.IdempotencyKey, expected)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[AccessSessionDTO]{ETag: resourceETag(record.ResourceVersion), Body: accessSessionDTO(record)}, nil
	})
}

func accessSessionDTO(source controlplane.AccessSessionRecord) AccessSessionDTO {
	result := AccessSessionDTO{SharedConsole: source.SharedConsole,
		ID: string(source.ID), SubjectID: string(source.SubjectID), InstanceID: string(source.InstanceID), ResourceID: string(source.ResourceID),
		Method: source.Method, State: string(source.State), ExpiresAt: source.ExpiresAt, RevocationReason: source.RevocationReason,
		ClosureRequired: source.ClosureRequired, ResourceVersion: uint64(source.ResourceVersion), CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
	if !source.LeaseExpiresAt.IsZero() {
		value := source.LeaseExpiresAt
		result.LeaseExpiresAt = &value
	}
	if !source.RevokedAt.IsZero() {
		value := source.RevokedAt
		result.RevokedAt = &value
	}
	return result
}
