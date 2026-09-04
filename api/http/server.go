package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

const NoAuthWarning = "Authentication is disabled. LabCentral is bound to loopback and grants every request local administrator privileges. Ordinary Docker containers are not safe for arbitrary hostile workloads, and published-port laptop labs cannot prove denied egress."

type Server struct {
	Handler http.Handler
	API     huma.API
}

type versionedOutput[T any] struct {
	ETag string `header:"ETag"`
	Body T
}

type Options struct {
	NoAuthWarning           string
	DefaultProviderTargetID string
	ProviderExamples        map[string]ProviderExample
	Authentication          AuthenticationOptions
}

type ProviderExample struct {
	Source           string          `json:"source"`
	ProviderOverride json.RawMessage `json:"providerOverride,omitempty"`
}

func New(service *controlplane.Service, exampleSource []byte, static fs.FS, options Options) (*Server, error) {
	if service == nil || len(exampleSource) == 0 || static == nil {
		return nil, fmt.Errorf("control-plane service, Example A, and static assets are required")
	}
	mux := http.NewServeMux()
	security, err := newRequestSecurity(options.Authentication)
	if err != nil {
		return nil, err
	}
	config := huma.DefaultConfig("LabCentral API", "1.0.0")
	config.Info.Description = "Canonical local LabCentral control-plane API. Mutations are durable asynchronous operations."
	api := humago.NewWithPrefix(mux, "/api/v1", config)
	if options.NoAuthWarning == "" {
		options.NoAuthWarning = NoAuthWarning
	}
	register(api, service, exampleSource, options, security)
	mux.Handle("/", staticHandler(static))
	return &Server{Handler: securityHeaders(security.wrap(mux)), API: api}, nil
}

type ServerInfo struct {
	ServerVersion           string   `json:"serverVersion"`
	APIVersion              string   `json:"apiVersion"`
	APIBase                 string   `json:"apiBase"`
	AuthMode                string   `json:"authMode"`
	AuthMethods             []string `json:"authMethods"`
	EnabledProviderKinds    []string `json:"enabledProviderKinds"`
	DefaultProviderTargetID string   `json:"defaultProviderTargetId"`
	NoAuthWarning           string   `json:"noAuthWarning"`
	UIFeatures              []string `json:"uiFeatures"`
}

type Session struct {
	SubjectID   string   `json:"subjectId"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
}

type LabRequest struct {
	Source                         string          `json:"source" minLength:"1" maxLength:"8388608"`
	AcknowledgeDockerIsolationRisk bool            `json:"acknowledgeDockerIsolationRisk"`
	ProjectID                      string          `json:"projectId,omitempty" maxLength:"128"`
	OwnerKind                      string          `json:"ownerKind,omitempty" enum:"user,team,project"`
	OwnerID                        string          `json:"ownerId,omitempty" maxLength:"128"`
	ProviderTargetID               string          `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage `json:"providerOverride,omitempty"`
}

type ProviderTargetDTO struct {
	ID                string           `json:"id"`
	ProviderKind      string           `json:"providerKind"`
	ConfigFingerprint string           `json:"configFingerprint"`
	ProtocolMajor     uint32           `json:"protocolMajor"`
	ProtocolMinor     uint32           `json:"protocolMinor"`
	Features          []string         `json:"features"`
	Status            string           `json:"status"`
	Discovery         json.RawMessage  `json:"discovery,omitempty"`
	Example           *ProviderExample `json:"example,omitempty"`
	DiscoveredAt      *time.Time       `json:"discoveredAt,omitempty"`
	ValidUntil        *time.Time       `json:"validUntil,omitempty"`
}

type ValidationBody struct {
	CanonicalDigest  string               `json:"canonicalDigest"`
	ResourceCount    int                  `json:"resourceCount"`
	ProviderTargetID string               `json:"providerTargetId"`
	ProviderKind     string               `json:"providerKind"`
	Warnings         []controlplane.Issue `json:"warnings"`
}

type OperationDTO struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instanceId"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Message    string    `json:"message"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	Attempt    uint32    `json:"attempt"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type ResourceDTO struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type InstanceDTO struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"projectId"`
	OwnerKind        string         `json:"ownerKind"`
	OwnerID          string         `json:"ownerId"`
	CreatedBy        string         `json:"createdBy"`
	ProviderTargetID string         `json:"providerTargetId"`
	Phase            string         `json:"phase"`
	DesiredLifecycle string         `json:"desiredLifecycle"`
	CanonicalDigest  string         `json:"canonicalDigest"`
	ResourceVersion  uint64         `json:"resourceVersion"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []ResourceDTO  `json:"resources,omitempty"`
	ErrorCode        string         `json:"errorCode,omitempty"`
	ErrorMessage     string         `json:"errorMessage,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

func register(api huma.API, service *controlplane.Service, exampleSource []byte, options Options, security *requestSecurity) {
	huma.Get(api, "/server-info", func(context.Context, *struct{}) (*struct{ Body ServerInfo }, error) {
		warning := ""
		if security.options.Mode == AuthModeNone {
			warning = options.NoAuthWarning
		}
		return &struct{ Body ServerInfo }{Body: ServerInfo{
			ServerVersion: "0.1.0", APIVersion: "v1", APIBase: "/api/v1", AuthMode: security.options.Mode, AuthMethods: authMethods(security.options.Mode),
			EnabledProviderKinds: service.ProviderKinds(), DefaultProviderTargetID: options.DefaultProviderTargetID, NoAuthWarning: warning,
			UIFeatures: []string{"docker-example-a", "provider-targets", "validate", "plan", "apply", "destroy"},
		}}, nil
	})
	registerAuthentication(api, security)
	registerAdministration(api, security)

	type providerTargetsInput struct {
		ProjectID string `query:"projectId" maxLength:"128"`
	}
	huma.Get(api, "/provider-targets", func(ctx context.Context, input *providerTargetsInput) (*struct {
		Body struct {
			Items []ProviderTargetDTO `json:"items"`
		}
	}, error) {
		authorizationRequest := application.AuthorizationRequest{
			Action: application.ActionProviderTargetHealthRead, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}
		var allowedTargetIDs []domain.ProviderTargetID
		if security.options.Mode != AuthModeNone {
			projectID, err := domain.NewProjectID(input.ProjectID)
			if err != nil {
				return nil, huma.Error400BadRequest("Project ID is required and must be valid")
			}
			authorizationRequest.ScopeKind, authorizationRequest.ScopeID = application.ScopeProject, string(projectID)
		}
		if err := authorize(ctx, security, authorizationRequest); err != nil {
			return nil, err
		}
		if security.options.Mode != AuthModeNone {
			actor, _ := requestPrincipal(ctx)
			for _, targetID := range service.ProviderTargetIDs() {
				request := authorizationRequest
				request.SubjectID, request.TargetID = actor.user.ID, targetID
				if err := security.options.Authorizer.Authorize(ctx, request); err == nil {
					allowedTargetIDs = append(allowedTargetIDs, targetID)
				} else if !errors.Is(err, authorization.ErrDenied) {
					return nil, authorizationError(err, false)
				}
			}
		}
		var targets []controlplane.ProviderTargetStatus
		var err error
		if security.options.Mode == AuthModeNone {
			targets, err = service.ListProviderTargets(ctx)
		} else {
			targets, err = service.ListProviderTargetsByID(ctx, allowedTargetIDs)
		}
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items []ProviderTargetDTO `json:"items"`
			}
		}{}
		output.Body.Items = make([]ProviderTargetDTO, 0, len(targets))
		for _, target := range targets {
			item := ProviderTargetDTO{
				ID: string(target.ID), ProviderKind: target.ProviderKind, ConfigFingerprint: target.ConfigFingerprint,
				ProtocolMajor: target.ProtocolMajor, ProtocolMinor: target.ProtocolMinor,
				Features: append([]string(nil), target.Features...), Status: target.Status,
			}
			item.Example = providerExample(options, string(target.ID))
			if len(target.DiscoveryJSON) > 0 {
				item.Discovery = append(json.RawMessage(nil), target.DiscoveryJSON...)
				discoveredAt, validUntil := target.DiscoveredAt, target.ValidUntil
				item.DiscoveredAt, item.ValidUntil = &discoveredAt, &validUntil
			}
			output.Body.Items = append(output.Body.Items, item)
		}
		return output, nil
	})

	type RefreshTargetInput struct {
		ID string `path:"id"`
	}
	huma.Post(api, "/provider-targets/{id}/actions/refresh", func(ctx context.Context, input *RefreshTargetInput) (*struct{ Body ProviderTargetDTO }, error) {
		id, err := domain.NewProviderTargetID(input.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("Provider target ID is invalid")
		}
		actor, err := authorizeActor(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProviderTargetHealthRead, ScopeKind: application.ScopePlatform, ScopeID: "platform", TargetID: id,
		})
		if err != nil {
			return nil, err
		}
		if err := service.RefreshTargetAs(ctx, actor.user.ID, id); err != nil {
			return nil, huma.Error503ServiceUnavailable("Provider target discovery failed")
		}
		targets, err := service.ListProviderTargetsByID(ctx, []domain.ProviderTargetID{id})
		if err != nil {
			return nil, apiError(err)
		}
		for _, target := range targets {
			if target.ID == id {
				discoveredAt, validUntil := target.DiscoveredAt, target.ValidUntil
				return &struct{ Body ProviderTargetDTO }{Body: ProviderTargetDTO{
					ID: string(target.ID), ProviderKind: target.ProviderKind, ConfigFingerprint: target.ConfigFingerprint,
					ProtocolMajor: target.ProtocolMajor, ProtocolMinor: target.ProtocolMinor,
					Features: target.Features, Status: target.Status, Discovery: append(json.RawMessage(nil), target.DiscoveryJSON...),
					Example: providerExample(options, string(target.ID)), DiscoveredAt: &discoveredAt, ValidUntil: &validUntil,
				}}, nil
			}
		}
		return nil, huma.Error404NotFound("Provider target was not found")
	})

	huma.Get(api, "/session", func(ctx context.Context, _ *struct{}) (*struct{ Body Session }, error) {
		session, err := sessionForPrincipal(ctx, security)
		if err != nil {
			return nil, err
		}
		return &struct{ Body Session }{Body: session}, nil
	})

	type exampleInput struct {
		ProjectID string `query:"projectId" maxLength:"128"`
	}
	huma.Get(api, "/examples/docker-laptop", func(ctx context.Context, input *exampleInput) (*struct {
		Body struct {
			Source string `json:"source"`
		}
	}, error) {
		request := LabRequest{ProjectID: input.ProjectID}
		if _, _, _, _, err := authorizeLabRequest(ctx, security, request, application.ActionInstanceValidate, controlplane.LocalDockerTarget); err != nil {
			return nil, err
		}
		output := &struct {
			Body struct {
				Source string `json:"source"`
			}
		}{}
		output.Body.Source = string(exampleSource)
		return output, nil
	})

	huma.Post(api, "/lab-validations", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body ValidationBody }, error) {
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstanceValidate, targetID); err != nil {
			return nil, err
		}
		result, err := service.ValidateTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body ValidationBody }{Body: ValidationBody{
			CanonicalDigest: result.Canonical.Digest.String(), ResourceCount: len(result.Canonical.Resources),
			ProviderTargetID: string(result.TargetID), ProviderKind: result.ProviderKind, Warnings: result.Warnings,
		}}, nil
	})

	huma.Post(api, "/lab-plans", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body controlplane.PlanResult }, error) {
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstancePlan, targetID); err != nil {
			return nil, err
		}
		result, err := service.PlanTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body controlplane.PlanResult }{Body: result}, nil
	})

	type CreateInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           LabRequest
	}
	type AcceptedOutput struct {
		Status   int
		Location string `header:"Location"`
		Body     struct {
			Operation OperationDTO `json:"operation"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "create-lab-instance", Method: http.MethodPost, Path: "/lab-instances", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *CreateInput) (*AcceptedOutput, error) {
			targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
			actor, projectID, ownerKind, ownerID, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstanceCreate, targetID)
			if err != nil {
				return nil, err
			}
			result, err := service.CreateInstanceTargetFor(ctx, actor.user.ID, projectID, ownerKind, ownerID, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride)
			if err != nil {
				return nil, apiError(err)
			}
			operation, err := service.GetOperation(ctx, result.OperationID)
			if err != nil {
				return nil, apiError(err)
			}
			output := &AcceptedOutput{Status: http.StatusAccepted, Location: "/api/v1/operations/" + string(result.OperationID)}
			output.Body.Operation = operationDTO(operation)
			return output, nil
		})

	huma.Get(api, "/lab-instances", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items      []InstanceDTO `json:"items"`
			NextCursor string        `json:"nextCursor,omitempty"`
		}
	}, error) {
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		var instances []controlplane.InstanceRecord
		var err error
		if security.options.Mode == AuthModeNone {
			instances, err = service.ListInstances(ctx)
		} else {
			ids, authorizationErr := security.options.Authorizer.AuthorizedInstanceIDs(ctx, actor.user.ID, application.ActionInstanceRead)
			if authorizationErr != nil {
				return nil, authorizationError(authorizationErr, false)
			}
			instances, err = service.ListInstancesByID(ctx, ids)
		}
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []InstanceDTO `json:"items"`
				NextCursor string        `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]InstanceDTO, 0, len(instances))
		for _, instance := range instances {
			_, resources, err := service.GetInstance(ctx, instance.ID)
			if err != nil {
				return nil, apiError(err)
			}
			output.Body.Items = append(output.Body.Items, instanceDTO(instance, resources))
		}
		return output, nil
	})

	type InstanceInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/lab-instances/{id}", func(ctx context.Context, input *InstanceInput) (*struct {
		ETag string `header:"ETag"`
		Body InstanceDTO
	}, error) {
		if err := authorizeInstance(ctx, security, application.ActionInstanceRead, domain.InstanceID(input.ID)); err != nil {
			return nil, err
		}
		instance, resources, err := service.GetInstance(ctx, domain.InstanceID(input.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &struct {
			ETag string `header:"ETag"`
			Body InstanceDTO
		}{ETag: fmt.Sprintf(`"%d"`, instance.ResourceVersion), Body: instanceDTO(instance, resources)}, nil
	})

	type DestroyInput struct {
		ID             string `path:"id"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct{}
	}
	huma.Register(api, huma.Operation{OperationID: "destroy-lab-instance", Method: http.MethodPost, Path: "/lab-instances/{id}/actions/destroy", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *DestroyInput) (*AcceptedOutput, error) {
			actor, err := authorizeInstanceActor(ctx, security, application.ActionInstanceDestroy, domain.InstanceID(input.ID))
			if err != nil {
				return nil, err
			}
			result, err := service.DestroyInstanceAs(ctx, actor.user.ID, domain.InstanceID(input.ID), input.IdempotencyKey)
			if err != nil {
				return nil, apiError(err)
			}
			operation, err := service.GetOperation(ctx, result.OperationID)
			if err != nil {
				return nil, apiError(err)
			}
			output := &AcceptedOutput{Status: http.StatusAccepted, Location: "/api/v1/operations/" + string(result.OperationID)}
			output.Body.Operation = operationDTO(operation)
			return output, nil
		})

	type OperationInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/operations/{id}", func(ctx context.Context, input *OperationInput) (*struct{ Body OperationDTO }, error) {
		if err := authorizeOperation(ctx, security, application.ActionInstanceRead, domain.OperationID(input.ID)); err != nil {
			return nil, err
		}
		operation, err := service.GetOperation(ctx, domain.OperationID(input.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body OperationDTO }{Body: operationDTO(operation)}, nil
	})
}

type authenticationOutput struct {
	Cookies []http.Cookie `header:"Set-Cookie"`
	Body    Session
}

func registerAuthentication(api huma.API, security *requestSecurity) {
	consumeIPLimit := func(ctx context.Context, dimension string, window time.Duration, limit uint32) error {
		address, ok := requestClientAddress(ctx)
		if !ok {
			return huma.Error500InternalServerError("Authentication request context is unavailable")
		}
		allowed, err := security.options.Manager.ConsumeAuthenticationAttempt(ctx, dimension, address, window, limit)
		if err != nil {
			return huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return authenticationRateLimitError(window)
		}
		return nil
	}
	type localLoginInput struct {
		Body struct {
			Username string `json:"username" minLength:"1" maxLength:"63"`
			Password string `json:"password" minLength:"14" maxLength:"1024"`
		}
	}
	huma.Post(api, "/auth/local/login", func(ctx context.Context, input *localLoginInput) (*authenticationOutput, error) {
		if security.options.Mode != AuthModeLocal {
			return nil, huma.Error404NotFound("Authentication method is not enabled")
		}
		allowed, err := security.options.Manager.ConsumeAuthenticationAttempt(ctx, "login-global", "installation", time.Minute, 300)
		if err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return nil, authenticationRateLimitError(time.Minute)
		}
		if err := consumeIPLimit(ctx, "login-ip", time.Minute, 300); err != nil {
			return nil, err
		}
		allowed, err = security.options.Manager.ConsumeAuthenticationAttempt(ctx, "login-user", input.Body.Username, 15*time.Minute, 5)
		if err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return nil, authenticationRateLimitError(15 * time.Minute)
		}
		password := []byte(input.Body.Password)
		defer clear(password)
		credentials, err := security.options.Manager.AuthenticateLocal(ctx, input.Body.Username, password)
		if err != nil {
			return nil, huma.Error401Unauthorized("Credentials were rejected")
		}
		if err := security.options.Manager.ClearAuthenticationAttempts(ctx, "login-user", input.Body.Username); err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		user, err := security.options.Repository.UserByID(ctx, credentials.Session.UserID)
		if err != nil {
			return nil, huma.Error401Unauthorized("Credentials were rejected")
		}
		body, err := sessionForUser(ctx, security, user)
		if err != nil {
			return nil, err
		}
		return &authenticationOutput{Cookies: sessionCookies(credentials), Body: body}, nil
	})

	type localRecoveryInput struct {
		Body struct {
			Token       string `json:"token" minLength:"32" maxLength:"256"`
			NewPassword string `json:"newPassword" minLength:"14" maxLength:"1024"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "recover-local-password", Method: http.MethodPost, Path: "/auth/local/recover", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, input *localRecoveryInput) (*struct{ Status int }, error) {
			if security.options.Mode != AuthModeLocal {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "recovery-ip", 15*time.Minute, 10); err != nil {
				return nil, err
			}
			password := []byte(input.Body.NewPassword)
			defer clear(password)
			if err := security.options.Manager.RecoverLocalPassword(ctx, input.Body.Token, password); err != nil {
				return nil, huma.Error401Unauthorized("Recovery token was rejected")
			}
			return &struct{ Status int }{Status: http.StatusNoContent}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*struct {
			Status  int
			Cookies []http.Cookie `header:"Set-Cookie"`
		}, error) {
			if security.options.Mode == AuthModeNone {
				return nil, huma.Error404NotFound("Authentication is not enabled")
			}
			actor, ok := requestPrincipal(ctx)
			if !ok || actor.session.ID == "" {
				return nil, huma.Error401Unauthorized("Authentication is required")
			}
			if err := security.options.Manager.Revoke(ctx, actor.session.ID); err != nil {
				return nil, huma.Error500InternalServerError("Session revocation failed")
			}
			return &struct {
				Status  int
				Cookies []http.Cookie `header:"Set-Cookie"`
			}{Status: http.StatusNoContent, Cookies: expiredCookies()}, nil
		})

	type bearerTokenOutput struct {
		Body struct {
			Token     string    `json:"token"`
			TokenType string    `json:"tokenType"`
			ExpiresAt time.Time `json:"expiresAt"`
		}
	}
	huma.Post(api, "/auth/tokens", func(ctx context.Context, _ *struct{}) (*bearerTokenOutput, error) {
		if security.options.Mode == AuthModeNone {
			return nil, huma.Error404NotFound("Authentication is not enabled")
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		credentials, err := security.options.Manager.NewBearerForUser(ctx, actor.user.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("Bearer credential could not be issued")
		}
		output := &bearerTokenOutput{}
		output.Body.Token = credentials.CookieToken
		output.Body.TokenType = "Bearer"
		output.Body.ExpiresAt = credentials.Session.ExpiresAt
		return output, nil
	})

	type oidcBeginInput struct {
		Redirect string `query:"redirect" default:"/" maxLength:"2048"`
	}
	type redirectOutput struct {
		Status   int
		Location string        `header:"Location"`
		Cookies  []http.Cookie `header:"Set-Cookie,omitempty"`
	}
	huma.Register(api, huma.Operation{OperationID: "begin-oidc-login", Method: http.MethodGet, Path: "/auth/oidc/begin", DefaultStatus: http.StatusSeeOther},
		func(ctx context.Context, input *oidcBeginInput) (*redirectOutput, error) {
			if security.options.Mode != AuthModeOIDC {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "oidc-begin-ip", 10*time.Minute, 300); err != nil {
				return nil, err
			}
			result, err := security.options.OIDC.Begin(ctx, input.Redirect)
			if err != nil {
				return nil, huma.Error400BadRequest("OIDC login request was rejected")
			}
			maxAge := int(time.Until(result.ExpiresAt).Seconds())
			if maxAge < 1 {
				maxAge = 1
			}
			return &redirectOutput{Status: http.StatusSeeOther, Location: result.AuthorizationURL, Cookies: []http.Cookie{{
				Name: oidcBindingCookieName, Value: result.BrowserBinding, Path: "/", Secure: true, HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: result.ExpiresAt,
			}}}, nil
		})

	type oidcCallbackInput struct {
		State          string      `query:"state" required:"true" maxLength:"256"`
		Code           string      `query:"code" required:"true" maxLength:"4096"`
		BrowserBinding http.Cookie `cookie:"__Host-labcentral-oidc"`
	}
	huma.Register(api, huma.Operation{OperationID: "complete-oidc-login", Method: http.MethodGet, Path: "/auth/oidc/callback", DefaultStatus: http.StatusSeeOther},
		func(ctx context.Context, input *oidcCallbackInput) (*redirectOutput, error) {
			if security.options.Mode != AuthModeOIDC {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "oidc-callback-ip", 10*time.Minute, 300); err != nil {
				return nil, err
			}
			result, err := security.options.OIDC.Callback(ctx, input.State, input.Code, input.BrowserBinding.Value)
			if err != nil {
				return nil, huma.Error401Unauthorized("OIDC login response was rejected")
			}
			cookies := sessionCookies(result.Credentials)
			cookies = append(cookies, http.Cookie{
				Name: oidcBindingCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
			})
			return &redirectOutput{Status: http.StatusSeeOther, Location: result.RedirectPath, Cookies: cookies}, nil
		})
}

func authenticationRateLimitError(window time.Duration) error {
	retrySeconds := int64((window + time.Second - 1) / time.Second)
	return huma.ErrorWithHeaders(huma.Error429TooManyRequests("Too many authentication attempts"), http.Header{
		"Retry-After": []string{strconv.FormatInt(retrySeconds, 10)},
	})
}

func registerAdministration(api huma.API, security *requestSecurity) {
	if security.options.Mode == AuthModeNone {
		return
	}
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

func sessionForPrincipal(ctx context.Context, security *requestSecurity) (Session, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return Session{}, huma.Error401Unauthorized("Authentication is required")
	}
	if security.options.Mode == AuthModeNone {
		return Session{SubjectID: string(actor.user.ID), DisplayName: actor.user.DisplayName, Roles: []string{string(application.RolePlatformAdministrator)}}, nil
	}
	return sessionForUser(ctx, security, actor.user)
}

func sessionForUser(ctx context.Context, security *requestSecurity, user application.UserRecord) (Session, error) {
	snapshot, err := security.options.Repository.AuthorizationSnapshot(ctx, user.ID, time.Now().UTC())
	if err != nil {
		return Session{}, huma.Error401Unauthorized("Authentication is required")
	}
	seen := make(map[string]bool, len(snapshot.Assignments))
	roles := make([]string, 0, len(snapshot.Assignments))
	for _, assignment := range snapshot.Assignments {
		role := string(assignment.Role)
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	return Session{SubjectID: string(user.ID), DisplayName: user.DisplayName, Roles: roles}, nil
}

func authMethods(mode string) []string {
	switch mode {
	case AuthModeLocal:
		return []string{"password"}
	case AuthModeOIDC:
		return []string{"oidc"}
	default:
		return []string{"none"}
	}
}

func authorize(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) error {
	_, err := authorizeActor(ctx, security, request)
	return err
}

func authorizeActor(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	request.SubjectID = actor.user.ID
	if err := security.options.Authorizer.Authorize(ctx, request); err != nil {
		return principal{}, authorizationError(err, false)
	}
	return actor, nil
}

func authorizeLabRequest(ctx context.Context, security *requestSecurity, request LabRequest, action application.PermissionAction, targetID domain.ProviderTargetID) (principal, domain.ProjectID, application.OwnerKind, string, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, "", "", "", huma.Error401Unauthorized("Authentication is required")
	}
	projectID := domain.ProjectID(request.ProjectID)
	ownerKind := application.OwnerKind(request.OwnerKind)
	ownerID := request.OwnerID
	if security.options.Mode == AuthModeNone {
		if projectID == "" {
			projectID = "project.local"
		}
		if ownerKind == "" {
			ownerKind = application.OwnerUser
		}
		if ownerID == "" {
			ownerID = string(controlplane.LocalAdminID)
		}
		return actor, projectID, ownerKind, ownerID, nil
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return principal{}, "", "", "", huma.Error400BadRequest("Project ID is required and must be valid")
	}
	if ownerKind == "" {
		ownerKind = application.OwnerUser
	}
	if ownerID == "" && ownerKind == application.OwnerUser {
		ownerID = string(actor.user.ID)
	}
	if !validRequestOwner(ownerKind, ownerID) {
		return principal{}, "", "", "", huma.Error400BadRequest("Instance owner is invalid")
	}
	authorizationRequest := application.AuthorizationRequest{
		SubjectID: actor.user.ID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(projectID),
		TargetID: targetID, OwnerKind: ownerKind, OwnerID: ownerID,
	}
	if err := security.options.Authorizer.Authorize(ctx, authorizationRequest); err != nil {
		return principal{}, "", "", "", authorizationError(err, false)
	}
	return actor, projectID, ownerKind, ownerID, nil
}

func validRequestOwner(kind application.OwnerKind, id string) bool {
	var err error
	switch kind {
	case application.OwnerUser:
		_, err = domain.NewUserID(id)
	case application.OwnerTeam:
		_, err = domain.NewTeamID(id)
	case application.OwnerProject:
		_, err = domain.NewProjectID(id)
	default:
		return false
	}
	return err == nil
}

func authorizeInstance(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) error {
	_, err := authorizeInstanceActor(ctx, security, action, instanceID)
	return err
}

func authorizeInstanceActor(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return principal{}, huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	if _, err := security.options.Authorizer.AuthorizeInstance(ctx, actor.user.ID, action, instanceID); err != nil {
		return principal{}, authorizationError(err, true)
	}
	return actor, nil
}

func authorizeOperation(ctx context.Context, security *requestSecurity, action application.PermissionAction, operationID domain.OperationID) error {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewOperationID(string(operationID)); err != nil {
		return huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return nil
	}
	if _, err := security.options.Authorizer.AuthorizeOperation(ctx, actor.user.ID, action, operationID); err != nil {
		return authorizationError(err, true)
	}
	return nil
}

func authorizationError(err error, hideExistence bool) error {
	if errors.Is(err, authorization.ErrDenied) {
		if hideExistence {
			return huma.Error404NotFound("Resource was not found")
		}
		return huma.Error403Forbidden("Permission was denied")
	}
	return huma.Error500InternalServerError("Authorization could not be evaluated")
}

func providerExample(options Options, targetID string) *ProviderExample {
	example, exists := options.ProviderExamples[targetID]
	if !exists || example.Source == "" {
		return nil
	}
	result := &ProviderExample{Source: example.Source}
	if len(example.ProviderOverride) > 0 {
		result.ProviderOverride = append(json.RawMessage(nil), example.ProviderOverride...)
	}
	return result
}

func operationDTO(source controlplane.OperationRecord) OperationDTO {
	return OperationDTO{ID: string(source.ID), InstanceID: string(source.InstanceID), Kind: string(source.Kind), State: string(source.State), Message: source.Message, ErrorCode: source.ErrorCode, Attempt: source.Attempt, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt}
}

func instanceDTO(source controlplane.InstanceRecord, resources []controlplane.ResourceRecord) InstanceDTO {
	outputs := make(map[string]any)
	_ = json.Unmarshal(source.OutputsJSON, &outputs)
	result := InstanceDTO{
		ID: string(source.ID), ProjectID: string(source.ProjectID), OwnerKind: string(source.OwnerKind), OwnerID: source.OwnerID, CreatedBy: string(source.CreatedBy),
		ProviderTargetID: string(source.ProviderTargetID), Phase: string(source.Phase), DesiredLifecycle: string(source.DesiredLifecycle),
		CanonicalDigest: source.CanonicalDigest.String(), ResourceVersion: uint64(source.ResourceVersion), Outputs: outputs,
		ErrorCode: source.ErrorCode, ErrorMessage: source.ErrorMessage, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
	for _, resource := range resources {
		result.Resources = append(result.Resources, ResourceDTO{ID: string(resource.ID), Kind: string(resource.Kind), Phase: string(resource.Phase), ErrorCode: resource.ErrorCode})
	}
	return result
}

func requestTargetID(request LabRequest, configuredDefault string) domain.ProviderTargetID {
	if request.ProviderTargetID == "" {
		if configuredDefault != "" {
			return domain.ProviderTargetID(configuredDefault)
		}
		return controlplane.LocalDockerTarget
	}
	return domain.ProviderTargetID(request.ProviderTargetID)
}

func apiError(err error) error {
	var validation *controlplane.ValidationError
	if errors.As(err, &validation) {
		details := make([]error, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			location := issue.FieldPath
			if location == "" {
				location = issue.ResourceID
			}
			details = append(details, &huma.ErrorDetail{Message: issue.Code + ": " + issue.Message, Location: location})
		}
		return huma.Error422UnprocessableEntity("Lab validation failed", details...)
	}
	if errors.Is(err, controlplane.ErrNotFound) {
		return huma.Error404NotFound("Resource was not found")
	}
	var domainError *domain.DomainError
	if errors.As(err, &domainError) && domainError.Code == "idempotency_conflict" {
		return huma.Error409Conflict("Idempotency key is already bound to a different request")
	}
	return huma.Error500InternalServerError("The control plane could not complete the request")
}

func staticHandler(content fs.FS) http.Handler {
	files := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			http.NotFound(response, request)
			return
		}
		clean := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if clean == "." || clean == "" {
			files.ServeHTTP(response, request)
			return
		}
		if _, err := fs.Stat(content, clean); err != nil {
			http.NotFound(response, request)
			return
		}
		files.ServeHTTP(response, request)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}
