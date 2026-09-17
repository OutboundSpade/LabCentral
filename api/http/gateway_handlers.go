package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/delegation"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"io"
	"net/url"
	"slices"
	"strings"
)

type deviceFormInput struct {
	Authorization string `header:"Authorization" maxLength:"1024"`
	ContentType   string `header:"Content-Type"`
	RawBody       []byte
}
type deviceWireOutput[T any] struct {
	Status       int
	CacheControl string `header:"Cache-Control"`
	Pragma       string `header:"Pragma"`
	Body         T
}
type deviceErrorBody struct {
	Error string `json:"error"`
}
type deviceResponseBody struct {
	DeviceCode      string `json:"device_code,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	Interval        int    `json:"interval,omitempty"`
	Error           string `json:"error,omitempty"`
}
type deviceTokenBody struct {
	AccessToken string `json:"access_token,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	Error       string `json:"error,omitempty"`
}

func deviceError(err error) string {
	for _, known := range []error{app.ErrGatewayInvalid, app.ErrGatewayExpired, app.ErrGatewayDenied, app.ErrGatewayPending, app.ErrGatewaySlowDown, app.ErrGatewayConsumed} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "server_error"
}
func clientForm(ctx context.Context, s *requestSecurity, in *deviceFormInput, allowed []string) (app.GatewayClient, url.Values, error) {
	if s.gateway == nil {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	if len(in.RawBody) > 32768 || in.ContentType != "application/x-www-form-urlencoded" || !strings.HasPrefix(in.Authorization, "Basic ") {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	credentials, e := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(in.Authorization, "Basic "))
	if e != nil {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	id, secret, ok := strings.Cut(string(credentials), ":")
	clear(credentials)
	if !ok {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	id, e = url.QueryUnescape(id)
	if e != nil {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	secret, e = url.QueryUnescape(secret)
	if e != nil {
		return app.GatewayClient{}, nil, app.ErrGatewayInvalid
	}
	c, e := s.gateway.AuthenticateClient(ctx, id, secret)
	if e != nil {
		return c, nil, e
	}
	form, e := url.ParseQuery(string(in.RawBody))
	if e != nil {
		return c, nil, app.ErrGatewayDenied
	}
	for k, v := range form {
		if !slices.Contains(allowed, k) || len(v) != 1 {
			return c, nil, app.ErrGatewayDenied
		}
	}
	if form.Get("client_id") != "" && form.Get("client_id") != id {
		return c, nil, app.ErrGatewayInvalid
	}
	return c, form, nil
}
func registerGateway(api huma.API, service *controlplane.Service, s *requestSecurity) {
	formSchema := func(properties map[string]*huma.Schema) *huma.RequestBody {
		return &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/x-www-form-urlencoded": {Schema: &huma.Schema{Type: "object", Properties: properties, AdditionalProperties: false}}}}
	}
	textSchema := &huma.Schema{Type: "string"}
	huma.Register(api, huma.Operation{OperationID: "request-device-authorization", Method: "POST", Path: "/device/authorization", MaxBodyBytes: 32768, SkipValidateBody: true, RequestBody: formSchema(map[string]*huma.Schema{"client_id": textSchema, "scope": textSchema})}, func(ctx context.Context, in *deviceFormInput) (*deviceWireOutput[deviceResponseBody], error) {
		out := &deviceWireOutput[deviceResponseBody]{Status: 400, CacheControl: "no-store", Pragma: "no-cache"}
		client, form, e := clientForm(ctx, s, in, []string{"client_id", "scope"})
		if e != nil {
			out.Body.Error = deviceError(e)
			return out, nil
		}
		var scope app.GatewayScope
		decoder := json.NewDecoder(strings.NewReader(form.Get("scope")))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&scope); e != nil {
			out.Body.Error = "invalid_scope"
			return out, nil
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			out.Body.Error = "invalid_scope"
			return out, nil
		}
		r, e := s.gateway.Request(ctx, client, scope, s.options.PublicOrigin)
		if e != nil {
			out.Body.Error = deviceError(e)
			return out, nil
		}
		out.Status = 200
		out.Body = deviceResponseBody{DeviceCode: r.DeviceCode, UserCode: r.UserCode, VerificationURI: r.VerificationURI, ExpiresIn: r.ExpiresIn, Interval: r.Interval}
		return out, nil
	})
	huma.Register(api, huma.Operation{OperationID: "poll-device-token", Method: "POST", Path: "/device/token", MaxBodyBytes: 32768, SkipValidateBody: true, RequestBody: formSchema(map[string]*huma.Schema{"client_id": textSchema, "device_code": textSchema, "grant_type": textSchema})}, func(ctx context.Context, in *deviceFormInput) (*deviceWireOutput[deviceTokenBody], error) {
		out := &deviceWireOutput[deviceTokenBody]{Status: 400, CacheControl: "no-store", Pragma: "no-cache"}
		client, form, e := clientForm(ctx, s, in, []string{"client_id", "device_code", "grant_type"})
		if e != nil {
			out.Body.Error = deviceError(e)
			return out, nil
		}
		if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			out.Body.Error = "unsupported_grant_type"
			return out, nil
		}
		r, e := s.gateway.Poll(ctx, client, form.Get("device_code"))
		if e != nil {
			out.Body.Error = deviceError(e)
			return out, nil
		}
		out.Status = 200
		out.Body = deviceTokenBody{AccessToken: r.AccessToken, TokenType: r.TokenType, ExpiresIn: r.ExpiresIn}
		return out, nil
	})
	type clientOutput struct {
		CacheControl string `header:"Cache-Control"`
		Body         struct {
			Client       app.GatewayClient `json:"client"`
			ClientSecret string            `json:"clientSecret"`
		}
	}
	huma.Post(api, "/admin/gateway-clients", func(ctx context.Context, in *struct {
		Body struct {
			Name string `json:"name" minLength:"1" maxLength:"128"`
		}
	}) (*clientOutput, error) {
		actor, e := authorizeActor(ctx, s, app.AuthorizationRequest{Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform"})
		if e != nil {
			return nil, e
		}
		if s.gateway == nil {
			return nil, huma.Error503ServiceUnavailable("Gateway authorization requires authenticated mode")
		}
		c, secret, e := s.gateway.CreateClient(ctx, actor.user.ID, in.Body.Name)
		if e != nil {
			return nil, apiError(e)
		}
		out := &clientOutput{CacheControl: "no-store"}
		out.Body.Client = c
		out.Body.ClientSecret = secret
		return out, nil
	})
	huma.Get(api, "/admin/gateway-clients", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items []app.GatewayClient `json:"items"`
		}
	}, error) {
		if e := authorize(ctx, s, app.AuthorizationRequest{Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform"}); e != nil {
			return nil, e
		}
		out := &struct {
			Body struct {
				Items []app.GatewayClient `json:"items"`
			}
		}{}
		out.Body.Items = []app.GatewayClient{}
		if s.gateway == nil {
			return out, nil
		}
		items, e := s.gateway.Store.ListGatewayClients(ctx)
		out.Body.Items = items
		return out, apiErrorOrNil(e)
	})
	huma.Post(api, "/admin/gateway-clients/{id}/status", func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"128"`
		Body struct {
			Disabled bool `json:"disabled"`
		}
	}) (*struct{}, error) {
		actor, e := authorizeActor(ctx, s, app.AuthorizationRequest{Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform"})
		if e != nil {
			return nil, e
		}
		if s.gateway == nil {
			return nil, huma.Error503ServiceUnavailable("Gateway unavailable")
		}
		if e = s.gateway.DisableClient(ctx, in.ID, in.Body.Disabled, actor.user.ID); e != nil {
			return nil, apiError(e)
		}
		return &struct{}{}, nil
	})
	browser := func(ctx context.Context) (principal, error) {
		p, ok := requestPrincipal(ctx)
		if !ok || p.bearer || p.session.ID == "" || s.gateway == nil {
			return p, huma.Error403Forbidden("Use a signed-in browser session")
		}
		return p, nil
	}
	type codeInput struct {
		Body struct {
			UserCode string `json:"userCode" minLength:"8" maxLength:"16"`
		}
	}
	type deviceView struct {
		CacheControl string `header:"Cache-Control"`
		Body         app.GatewayDevice
	}
	huma.Post(api, "/device/inspect", func(ctx context.Context, in *codeInput) (*deviceView, error) {
		p, e := browser(ctx)
		if e != nil {
			return nil, e
		}
		d, e := s.gateway.Inspect(ctx, p.user.ID, in.Body.UserCode)
		if e != nil {
			return nil, huma.Error400BadRequest("Code unavailable or request limit exceeded")
		}
		return &deviceView{CacheControl: "no-store", Body: d}, nil
	})
	huma.Post(api, "/device/approve", func(ctx context.Context, in *struct {
		Body struct {
			UserCode string `json:"userCode" minLength:"8" maxLength:"16"`
			Approve  bool   `json:"approve"`
		}
	}) (*struct{}, error) {
		p, e := browser(ctx)
		if e != nil {
			return nil, e
		}
		d, e := s.gateway.Inspect(ctx, p.user.ID, in.Body.UserCode)
		if e != nil {
			return nil, huma.Error400BadRequest("Code unavailable")
		}
		if in.Body.Approve {
			if e = validateGatewayApproval(ctx, s, service, p.user.ID, d.Scope); e != nil {
				return nil, e
			}
		}
		if e = s.gateway.Approve(ctx, p.user.ID, in.Body.UserCode, in.Body.Approve); e != nil {
			return nil, huma.Error400BadRequest("Approval failed")
		}
		return &struct{}{}, nil
	})
	huma.Get(api, "/session/gateway-grants", func(ctx context.Context, _ *struct{}) (*struct {
		CacheControl string `header:"Cache-Control"`
		Body         struct {
			Items []app.GatewayGrant `json:"items"`
		}
	}, error) {
		p, e := browser(ctx)
		if e != nil {
			return nil, e
		}
		items, e := s.gateway.Store.ListGatewayGrants(ctx, p.user.ID)
		if e != nil {
			return nil, apiError(e)
		}
		out := &struct {
			CacheControl string `header:"Cache-Control"`
			Body         struct {
				Items []app.GatewayGrant `json:"items"`
			}
		}{CacheControl: "no-store"}
		out.Body.Items = items
		return out, nil
	})
	huma.Post(api, "/session/gateway-grants/{id}/revoke", func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"128"`
	}) (*struct{}, error) {
		p, e := browser(ctx)
		if e != nil {
			return nil, e
		}
		if e = s.gateway.Revoke(ctx, in.ID, p.user.ID); e != nil {
			return nil, apiError(e)
		}
		return &struct{}{}, nil
	})
	registerGatewayResources(api, service, s)
}
func apiErrorOrNil(e error) error {
	if e == nil {
		return nil
	}
	return apiError(e)
}
func validateGatewayApproval(ctx context.Context, s *requestSecurity, service *controlplane.Service, subject domain.UserID, scope app.GatewayScope) error {
	if e := delegation.ValidateScope(scope); e != nil {
		return huma.Error400BadRequest("Invalid scope")
	}
	if e := s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: subject, Action: app.ActionTemplateRead, ScopeKind: app.ScopeProject, ScopeID: string(scope.ProjectID)}); e != nil {
		return authorizationError(e, false)
	}
	for _, id := range scope.TemplateIDs {
		t, e := service.GetLabTemplate(ctx, id)
		if e != nil || t.ProjectID != scope.ProjectID {
			return huma.Error403Forbidden("Template outside approved project")
		}
	}
	for _, id := range scope.InstanceIDs {
		record, e := s.options.Authorizer.AuthorizeInstance(ctx, subject, app.ActionInstanceRead, id)
		if e != nil || record.ProjectID != scope.ProjectID {
			return huma.Error403Forbidden("Instance outside authorized scope")
		}
		if slices.Contains(scope.Actions, "access.create") {
			if _, e = s.options.Authorizer.AuthorizeInstance(ctx, subject, app.ActionAccessSessionCreate, id); e != nil {
				return authorizationError(e, false)
			}
		}
	}
	if slices.Contains(scope.Actions, "deployments.create") {
		for _, id := range scope.TargetIDs {
			if e := s.options.Authorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: subject, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: string(scope.ProjectID), TargetID: id, OwnerKind: app.OwnerUser, OwnerID: string(subject)}); e != nil {
				return authorizationError(e, false)
			}
		}
	}
	return nil
}
