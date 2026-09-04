package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/danielgtaylor/huma/v2"
)

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
