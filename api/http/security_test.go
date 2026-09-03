package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
)

func TestLocalAuthenticationCookiesOriginCSRFAndRevocation(t *testing.T) {
	server := localAuthenticationServer(t, nil)
	loginBody := []byte(`{"username":"admin","password":"a-production-length-password"}`)

	wrongOrigin := secureRequest(http.MethodPost, "/api/v1/auth/local/login", loginBody)
	wrongOrigin.Header.Set("Origin", "https://attacker.invalid")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, wrongOrigin)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login returned %d: %s", response.Code, response.Body.String())
	}

	login := secureRequest(http.MethodPost, "/api/v1/auth/local/login", loginBody)
	login.Header.Set("Origin", "https://lab.example")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, login)
	if response.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || !csrfCookie.Secure || csrfCookie.HttpOnly || sessionCookie.Path != "/" || csrfCookie.Path != "/" {
		t.Fatalf("unsafe session cookies: %#v", cookies)
	}

	sessionRequest := secureRequest(http.MethodGet, "/api/v1/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, sessionRequest)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"subjectId":"user.admin"`) {
		t.Fatalf("session returned %d: %s", response.Code, response.Body.String())
	}

	logout := secureRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.Header.Set("Origin", "https://lab.example")
	logout.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, logout)
	if response.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF returned %d: %s", response.Code, response.Body.String())
	}

	logout = secureRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.Header.Set("Origin", "https://lab.example")
	logout.Header.Set("X-CSRF-Token", csrfCookie.Value)
	logout.AddCookie(sessionCookie)
	logout.AddCookie(csrfCookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, logout)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 2 {
		t.Fatalf("CSRF-protected logout returned %d cookies=%#v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}

	sessionRequest = secureRequest(http.MethodGet, "/api/v1/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, sessionRequest)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie returned %d: %s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedOriginRejectsUntrustedForwardedTLS(t *testing.T) {
	_, trustedNetwork, _ := net.ParseCIDR("10.0.0.0/8")
	server := localAuthenticationServer(t, []*net.IPNet{trustedNetwork})

	untrusted := httptest.NewRequest(http.MethodGet, "http://lab.example/api/v1/server-info", nil)
	untrusted.Host = "lab.example"
	untrusted.RemoteAddr = "192.0.2.1:1234"
	untrusted.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, untrusted)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrusted forwarded TLS returned %d", response.Code)
	}

	trusted := httptest.NewRequest(http.MethodGet, "http://lab.example/api/v1/server-info", nil)
	trusted.Host = "lab.example"
	trusted.RemoteAddr = "10.1.2.3:1234"
	trusted.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, trusted)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted forwarded TLS returned %d: %s", response.Code, response.Body.String())
	}

	confused := httptest.NewRequest(http.MethodGet, "http://lab.example/api/v1/server-info", nil)
	confused.Host = "lab.example"
	confused.RemoteAddr = "10.1.2.3:1234"
	confused.Header.Add("X-Forwarded-Proto", "https")
	confused.Header.Add("X-Forwarded-Proto", "http")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, confused)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous forwarded proto returned %d", response.Code)
	}
}

func TestFreshAuthenticatedInstallationCanCreateTenancyAndGrants(t *testing.T) {
	server, store := localAuthenticationFixture(t, nil)
	sessionCookie, csrfCookie := loginCookies(t, server, "admin", "a-production-length-password")
	post := func(path, body string, want int) {
		t.Helper()
		request := secureRequest(http.MethodPost, path, []byte(body))
		request.Header.Set("Origin", "https://lab.example")
		request.Header.Set("X-CSRF-Token", csrfCookie.Value)
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("POST %s returned %d: %s", path, response.Code, response.Body.String())
		}
	}
	post("/api/v1/organizations", `{"id":"org.alpha","name":"alpha"}`, http.StatusOK)
	post("/api/v1/projects", `{"id":"project.alpha","organizationId":"org.alpha","name":"classroom"}`, http.StatusOK)
	post("/api/v1/memberships", `{"id":"membership.admin.alpha","userId":"user.admin","organizationId":"org.alpha"}`, http.StatusOK)
	post("/api/v1/provider-target-grants", `{"id":"grant.alpha","targetId":"target.local-docker","scopeKind":"project","scopeId":"project.alpha"}`, http.StatusOK)
	post("/api/v1/users", `{"id":"user.student","username":"student","displayName":"Student","password":"a-student-production-password"}`, http.StatusOK)
	post("/api/v1/memberships", `{"id":"membership.student.alpha","userId":"user.student","organizationId":"org.alpha"}`, http.StatusOK)
	post("/api/v1/role-assignments", `{"id":"role.student.alpha","subjectKind":"user","subjectId":"user.student","scopeKind":"project","scopeId":"project.alpha","role":"participant"}`, http.StatusOK)

	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Authorize(context.Background(), application.AuthorizationRequest{
		SubjectID: "user.student", Action: application.ActionInstanceCreate, ScopeKind: application.ScopeProject,
		ScopeID: "project.alpha", TargetID: "target.local-docker", OwnerKind: application.OwnerUser, OwnerID: "user.student",
	}); err != nil {
		t.Fatalf("freshly configured participant could not create a lab: %v", err)
	}

	studentSession, studentCSRF := loginCookies(t, server, "student", "a-student-production-password")
	denied := secureRequest(http.MethodPost, "/api/v1/organizations", []byte(`{"id":"org.forbidden","name":"forbidden"}`))
	denied.Header.Set("Origin", "https://lab.example")
	denied.Header.Set("X-CSRF-Token", studentCSRF.Value)
	denied.AddCookie(studentSession)
	denied.AddCookie(studentCSRF)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, denied)
	if response.Code != http.StatusForbidden {
		t.Fatalf("participant organization mutation returned %d: %s", response.Code, response.Body.String())
	}
}

func localAuthenticationServer(t *testing.T, trusted []*net.IPNet) *Server {
	server, _ := localAuthenticationFixture(t, trusted)
	return server
}

func localAuthenticationFixture(t *testing.T, trusted []*net.IPNet) (*Server, *localdb.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:http-auth-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-")))
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	material := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'k'}, 32))
	keyring, err := authentication.ParseKeyring([]byte(fmt.Sprintf(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"current","keys":[{"id":"current","material":%q}]}`, material)))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := authentication.NewManager(store, keyring, authentication.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BootstrapLocalAdministrator(context.Background(), "user.admin", "admin", "Administrator", []byte("a-production-length-password")); err != nil {
		t.Fatal(err)
	}
	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	server, err := New(new(controlplane.Service), []byte("example"), static, Options{Authentication: AuthenticationOptions{
		Mode: AuthModeLocal, Manager: manager, Repository: store, Authorizer: evaluator, PublicOrigin: "https://lab.example",
		SecureCookies: true, TrustedProxies: trusted,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func loginCookies(t *testing.T, server *Server, username, password string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	request := secureRequest(http.MethodPost, "/api/v1/auth/local/login", []byte(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
	request.Header.Set("Origin", "https://lab.example")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login did not return both cookies: %#v", response.Result().Cookies())
	}
	return sessionCookie, csrfCookie
}

func secureRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, "https://lab.example"+path, bytes.NewReader(body))
	request.Host = "lab.example"
	request.TLS = &tls.ConnectionState{}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}
