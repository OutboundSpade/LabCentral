package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
)

const (
	AuthModeNone  = "none"
	AuthModeLocal = "local"
	AuthModeOIDC  = "oidc"

	sessionCookieName     = "__Host-labcentral-session"
	csrfCookieName        = "__Host-labcentral-csrf"
	oidcBindingCookieName = "__Host-labcentral-oidc"
)

type AuthenticationOptions struct {
	Mode           string
	Manager        *authentication.Manager
	OIDC           *authentication.OIDCService
	OIDCIssuer     string
	Repository     application.AuthRepository
	Authorizer     application.Authorizer
	PublicOrigin   string
	SecureCookies  bool
	TrustedProxies []*net.IPNet
}

type principal struct {
	user    application.UserRecord
	session application.SessionRecord
}

type principalContextKey struct{}

type requestSecurity struct {
	options      AuthenticationOptions
	publicOrigin *url.URL
}

func newRequestSecurity(options AuthenticationOptions) (*requestSecurity, error) {
	if options.Mode == "" {
		options.Mode = AuthModeNone
	}
	security := &requestSecurity{options: options}
	if options.Mode == AuthModeNone {
		return security, nil
	}
	if options.Mode != AuthModeLocal && options.Mode != AuthModeOIDC || options.Manager == nil || options.Repository == nil || options.Authorizer == nil || !options.SecureCookies {
		return nil, errors.New("authenticated HTTP mode requires the session manager, identity repository, authorizer, and secure cookies")
	}
	if options.Mode == AuthModeOIDC && options.OIDC == nil {
		return nil, errors.New("OIDC HTTP mode requires the OIDC service")
	}
	parsed, err := url.Parse(options.PublicOrigin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != options.PublicOrigin {
		return nil, errors.New("authenticated HTTP mode requires an exact HTTPS public origin")
	}
	security.publicOrigin = parsed
	return security, nil
}

func (s *requestSecurity) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if s.options.Mode != AuthModeNone {
			response.Header().Set("Strict-Transport-Security", "max-age=31536000")
			if strings.HasPrefix(request.URL.Path, "/api/v1/auth/") || request.URL.Path == "/api/v1/session" {
				response.Header().Set("Cache-Control", "no-store")
			}
		}
		if s.options.Mode == AuthModeNone {
			user := application.UserRecord{ID: controlplane.LocalAdminID, Username: "local-admin", DisplayName: "Local administrator"}
			next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal{user: user})))
			return
		}
		if !s.validPublicRequest(request) {
			writeProblem(response, http.StatusBadRequest, "invalid_public_origin", "The request did not arrive through the configured HTTPS origin.")
			return
		}
		public := publicAuthenticationPath(request.URL.Path)
		mutating := request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions
		if mutating && !s.validOrigin(request) {
			writeProblem(response, http.StatusForbidden, "csrf_origin_invalid", "The request origin was rejected.")
			return
		}
		cookie, err := request.Cookie(sessionCookieName)
		if err != nil {
			if public {
				next.ServeHTTP(response, request)
				return
			}
			writeProblem(response, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		credentials, err := s.options.Manager.AuthenticateSession(request.Context(), cookie.Value)
		if err != nil {
			if public {
				appendCookies(headerAppender{response}, expiredCookies())
				next.ServeHTTP(response, request)
				return
			}
			writeProblem(response, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		user, err := s.options.Repository.UserByID(request.Context(), credentials.Session.UserID)
		if err != nil || user.Disabled {
			writeProblem(response, http.StatusUnauthorized, "authentication_required", "Authentication is required.")
			return
		}
		if credentials.Rotated {
			appendCookies(headerAppender{response}, sessionCookies(credentials))
		}
		if mutating && !public {
			csrfCookie, cookieErr := request.Cookie(csrfCookieName)
			csrfHeader := request.Header.Get("X-CSRF-Token")
			if cookieErr != nil || csrfCookie.Value == "" || csrfHeader != csrfCookie.Value || s.options.Manager.ValidateCSRF(credentials.RequestSession, csrfHeader) != nil {
				writeProblem(response, http.StatusForbidden, "csrf_token_invalid", "The CSRF token was rejected.")
				return
			}
		}
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal{user: user, session: credentials.Session}))
		next.ServeHTTP(response, request)
	})
}

func (s *requestSecurity) validPublicRequest(request *http.Request) bool {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	} else if s.isTrustedProxy(request.RemoteAddr) {
		forwarded := request.Header.Values("X-Forwarded-Proto")
		if len(forwarded) == 1 && forwarded[0] == "https" {
			scheme = "https"
		}
	}
	return scheme == s.publicOrigin.Scheme && strings.EqualFold(request.Host, s.publicOrigin.Host)
}

func (s *requestSecurity) validOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin != "" && origin == s.options.PublicOrigin
}

func (s *requestSecurity) isTrustedProxy(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range s.options.TrustedProxies {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func publicAuthenticationPath(path string) bool {
	switch path {
	case "/api/v1/server-info", "/api/v1/auth/local/login", "/api/v1/auth/local/recover", "/api/v1/auth/oidc/begin", "/api/v1/auth/oidc/callback":
		return true
	default:
		return !strings.HasPrefix(path, "/api/")
	}
}

func requestPrincipal(ctx context.Context) (principal, bool) {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	return value, ok && value.user.ID != ""
}

func sessionCookies(credentials authentication.SessionCredentials) []http.Cookie {
	maxAge := int(time.Until(credentials.Session.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return []http.Cookie{{
		Name: sessionCookieName, Value: credentials.CookieToken, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: credentials.Session.ExpiresAt,
	}, {
		Name: csrfCookieName, Value: credentials.CSRFToken, Path: "/", Secure: true, HttpOnly: false,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: credentials.Session.ExpiresAt,
	}}
}

func expiredCookies() []http.Cookie {
	result := make([]http.Cookie, 0, 2)
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		result = append(result, http.Cookie{
			Name: name, Value: "", Path: "/", Secure: true, HttpOnly: name == sessionCookieName,
			SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
		})
	}
	return result
}

type headerAppender struct{ http.ResponseWriter }

func (response headerAppender) AppendHeader(name, value string) { response.Header().Add(name, value) }

func appendCookies(response interface{ AppendHeader(string, string) }, cookies []http.Cookie) {
	for index := range cookies {
		response.AppendHeader("Set-Cookie", cookies[index].String())
	}
}

func writeProblem(response http.ResponseWriter, status int, code, detail string) {
	response.Header().Set("Content-Type", "application/problem+json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "detail": detail,
	})
}
