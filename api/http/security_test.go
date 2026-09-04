package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

func TestLocalAuthenticationDurablyRateLimitsByAccount(t *testing.T) {
	server := localAuthenticationServer(t, nil)
	for attempt := 1; attempt <= 6; attempt++ {
		request := secureRequest(http.MethodPost, "/api/v1/auth/local/login", []byte(`{"username":"admin","password":"wrong-production-password"}`))
		request.Header.Set("Origin", "https://lab.example")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("login attempt %d returned %d, want %d: %s", attempt, response.Code, want, response.Body.String())
		}
		if attempt == 6 && response.Header().Get("Retry-After") != "900" {
			t.Fatalf("rate-limited login Retry-After = %q", response.Header().Get("Retry-After"))
		}
	}
}

func TestTargetRefreshRequiresGrantEvenForPlatformViewer(t *testing.T) {
	server, store := localAuthenticationFixture(t, nil)
	passwordHash, err := authentication.HashPassword([]byte("a-viewer-production-password"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(context.Background(), application.UserRecord{ID: "user.viewer", Username: "viewer", DisplayName: "Viewer", PasswordHash: passwordHash}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRoleAssignment(context.Background(), application.RoleAssignmentRecord{
		ID: "role.viewer", SubjectKind: application.SubjectUser, SubjectID: "user.viewer",
		ScopeKind: application.ScopePlatform, ScopeID: "platform", Role: application.RoleViewer,
	}); err != nil {
		t.Fatal(err)
	}
	session, csrf := loginCookies(t, server, "viewer", "a-viewer-production-password")
	request := secureRequest(http.MethodPost, "/api/v1/provider-targets/target.local-docker/actions/refresh", nil)
	request.Header.Set("Origin", "https://lab.example")
	request.Header.Set("X-CSRF-Token", csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("ungranted platform viewer target refresh returned %d: %s", response.Code, response.Body.String())
	}
}

func TestBearerSessionLifecycleAndCredentialSeparation(t *testing.T) {
	server := localAuthenticationServer(t, nil)
	sessionCookie, csrfCookie := loginCookies(t, server, "admin", "a-production-length-password")

	issue := secureRequest(http.MethodPost, "/api/v1/auth/tokens", nil)
	issue.Header.Set("Origin", "https://lab.example")
	issue.Header.Set("X-CSRF-Token", csrfCookie.Value)
	issue.AddCookie(sessionCookie)
	issue.AddCookie(csrfCookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, issue)
	var issued struct {
		Token     string    `json:"token"`
		TokenType string    `json:"tokenType"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &issued) != nil || len(issued.Token) < 32 || issued.TokenType != "Bearer" || issued.ExpiresAt.IsZero() {
		t.Fatalf("bearer issue returned %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	cookieAsBearer := secureRequest(http.MethodGet, "/api/v1/session", nil)
	cookieAsBearer.Header.Set("Authorization", "Bearer "+sessionCookie.Value)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, cookieAsBearer)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("browser session replayed as bearer returned %d: %s", response.Code, response.Body.String())
	}
	bearerAsCookie := secureRequest(http.MethodGet, "/api/v1/session", nil)
	bearerAsCookie.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, bearerAsCookie)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("bearer replayed as browser session returned %d: %s", response.Code, response.Body.String())
	}

	bearerSession := secureRequest(http.MethodGet, "/api/v1/session", nil)
	bearerSession.Header.Set("Authorization", "Bearer "+issued.Token)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, bearerSession)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"subjectId":"user.admin"`) {
		t.Fatalf("bearer session returned %d: %s", response.Code, response.Body.String())
	}

	bearerMutation := secureRequest(http.MethodPost, "/api/v1/organizations", []byte(`{"id":"org.bearer","name":"bearer"}`))
	bearerMutation.Header.Set("Authorization", "Bearer "+issued.Token)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, bearerMutation)
	if response.Code != http.StatusOK {
		t.Fatalf("bearer-only mutation returned %d: %s", response.Code, response.Body.String())
	}

	mixed := secureRequest(http.MethodGet, "/api/v1/session", nil)
	mixed.Header.Set("Authorization", "Bearer "+issued.Token)
	mixed.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, mixed)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mixed authentication returned %d: %s", response.Code, response.Body.String())
	}

	malformed := secureRequest(http.MethodGet, "/api/v1/session", nil)
	malformed.Header.Set("Authorization", "Basic rejected")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, malformed)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("malformed authorization returned %d: %s", response.Code, response.Body.String())
	}

	logout := secureRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.Header.Set("Authorization", "Bearer "+issued.Token)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, logout)
	if response.Code != http.StatusNoContent {
		t.Fatalf("bearer logout returned %d: %s", response.Code, response.Body.String())
	}
	bearerSession = secureRequest(http.MethodGet, "/api/v1/session", nil)
	bearerSession.Header.Set("Authorization", "Bearer "+issued.Token)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, bearerSession)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer returned %d: %s", response.Code, response.Body.String())
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

	malformedAddress := httptest.NewRequest(http.MethodGet, "http://lab.example/api/v1/server-info", nil)
	malformedAddress.Host = "lab.example"
	malformedAddress.RemoteAddr = "10.1.2.3:1234"
	malformedAddress.Header.Set("X-Forwarded-Proto", "https")
	malformedAddress.Header.Set("X-Forwarded-For", "not-an-ip")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, malformedAddress)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed forwarded address returned %d", response.Code)
	}
}

func TestTrustedProxyClientAddressUsesNearestUntrustedHop(t *testing.T) {
	_, trustedNetwork, _ := net.ParseCIDR("10.0.0.0/8")
	security := &requestSecurity{options: AuthenticationOptions{TrustedProxies: []*net.IPNet{trustedNetwork}}}
	request := httptest.NewRequest(http.MethodGet, "https://lab.example/", nil)
	request.RemoteAddr = "10.1.2.3:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20, 10.9.8.7")
	address, err := security.clientAddress(request)
	if err != nil || address != "198.51.100.20" {
		t.Fatalf("resolved client address = %q, %v", address, err)
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
	recoveryRequest := secureRequest(http.MethodPost, "/api/v1/users/user.student/actions/issue-password-recovery", nil)
	recoveryRequest.Header.Set("Origin", "https://lab.example")
	recoveryRequest.Header.Set("X-CSRF-Token", csrfCookie.Value)
	recoveryRequest.AddCookie(sessionCookie)
	recoveryRequest.AddCookie(csrfCookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, recoveryRequest)
	var recovery struct {
		Token string `json:"token"`
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &recovery) != nil || recovery.Token == "" {
		t.Fatalf("password recovery issue returned %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	recoverPassword := secureRequest(http.MethodPost, "/api/v1/auth/local/recover", []byte(fmt.Sprintf(`{"token":%q,"newPassword":"a-new-production-password"}`, recovery.Token)))
	recoverPassword.Header.Set("Origin", "https://lab.example")
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, recoverPassword)
	if response.Code != http.StatusNoContent {
		t.Fatalf("password recovery returned %d: %s", response.Code, response.Body.String())
	}

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

	studentSession, studentCSRF := loginCookies(t, server, "student", "a-new-production-password")
	targets := secureRequest(http.MethodGet, "/api/v1/provider-targets?projectId=project.alpha", nil)
	targets.AddCookie(studentSession)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, targets)
	if response.Code != http.StatusOK {
		t.Fatalf("project-scoped target list returned %d: %s", response.Code, response.Body.String())
	}
	denied := secureRequest(http.MethodPost, "/api/v1/organizations", []byte(`{"id":"org.forbidden","name":"forbidden"}`))
	denied.Header.Set("Origin", "https://lab.example")
	denied.Header.Set("X-CSRF-Token", studentCSRF.Value)
	denied.AddCookie(studentSession)
	denied.AddCookie(studentCSRF)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, denied)
	if response.Code != http.StatusForbidden {
		t.Fatalf("participant organization mutation returned %d: %s", response.Code, response.Body.String())
	}
}

func TestAdministrativeAuthorityLifecycleIsVersionedRevocableAndRenewable(t *testing.T) {
	server, store := localAuthenticationFixture(t, nil)
	adminSession, adminCSRF := loginCookies(t, server, "admin", "a-production-length-password")
	request := func(method, path string, body any, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		httpRequest := secureRequest(method, path, encoded)
		httpRequest.Header.Set("Origin", "https://lab.example")
		httpRequest.Header.Set("X-CSRF-Token", adminCSRF.Value)
		if ifMatch != "" {
			httpRequest.Header.Set("If-Match", ifMatch)
		}
		httpRequest.AddCookie(adminSession)
		httpRequest.AddCookie(adminCSRF)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httpRequest)
		return response
	}
	decode := func(response *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %d response: %v body=%s", response.Code, err, response.Body.String())
		}
	}
	assertOK := func(response *httptest.ResponseRecorder) {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("administrative request returned %d: %s", response.Code, response.Body.String())
		}
	}

	assertOK(request(http.MethodPost, "/api/v1/organizations", application.OrganizationRecord{ID: "org.lifecycle", Name: "lifecycle"}, ""))
	assertOK(request(http.MethodPost, "/api/v1/projects", application.ProjectRecord{ID: "project.lifecycle", OrganizationID: "org.lifecycle", Name: "classroom"}, ""))
	userResponse := request(http.MethodPost, "/api/v1/users", map[string]any{
		"id": "user.lifecycle", "username": "lifecycle-user", "displayName": "Lifecycle User", "password": "a-lifecycle-production-password",
	}, "")
	assertOK(userResponse)
	var user application.UserRecord
	decode(userResponse, &user)
	if user.ResourceVersion != 1 || userResponse.Header().Get("ETag") != `"1"` {
		t.Fatalf("created user version=%d ETag=%q", user.ResourceVersion, userResponse.Header().Get("ETag"))
	}

	membershipResponse := request(http.MethodPost, "/api/v1/memberships", application.MembershipRecord{
		ID: "membership.lifecycle", UserID: user.ID, OrganizationID: "org.lifecycle",
	}, "")
	assertOK(membershipResponse)
	var membership application.MembershipRecord
	decode(membershipResponse, &membership)
	roleResponse := request(http.MethodPost, "/api/v1/role-assignments", application.RoleAssignmentRecord{
		ID: "role.lifecycle", SubjectKind: application.SubjectUser, SubjectID: string(user.ID), ScopeKind: application.ScopeProject,
		ScopeID: "project.lifecycle", Role: application.RoleParticipant,
	}, "")
	assertOK(roleResponse)
	var role application.RoleAssignmentRecord
	decode(roleResponse, &role)
	grantResponse := request(http.MethodPost, "/api/v1/provider-target-grants", application.TargetGrantRecord{
		ID: "grant.lifecycle", TargetID: controlplane.LocalDockerTarget, ScopeKind: application.ScopeProject, ScopeID: "project.lifecycle",
	}, "")
	assertOK(grantResponse)
	var grant application.TargetGrantRecord
	decode(grantResponse, &grant)

	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	authorizationRequest := application.AuthorizationRequest{
		SubjectID: user.ID, Action: application.ActionInstanceCreate, ScopeKind: application.ScopeProject, ScopeID: "project.lifecycle",
		TargetID: controlplane.LocalDockerTarget, OwnerKind: application.OwnerUser, OwnerID: string(user.ID),
	}
	assertAuthorization := func(want bool) {
		t.Helper()
		err := evaluator.Authorize(context.Background(), authorizationRequest)
		if (err == nil) != want {
			t.Fatalf("authorization allowed=%t want=%t err=%v", err == nil, want, err)
		}
	}
	assertAuthorization(true)

	missingPrecondition := request(http.MethodPut, "/api/v1/provider-target-grants/"+grant.ID, grant, "")
	if missingPrecondition.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match returned %d: %s", missingPrecondition.Code, missingPrecondition.Body.String())
	}
	grantRevoke := request(http.MethodPost, "/api/v1/provider-target-grants/"+grant.ID+"/actions/revoke", nil, resourceETag(grant.ResourceVersion))
	assertOK(grantRevoke)
	decode(grantRevoke, &grant)
	assertAuthorization(false)
	grant.ValidFrom, grant.ValidUntil, grant.ResourceVersion = time.Now().UTC().Add(-time.Minute), time.Time{}, 0
	grantRenew := request(http.MethodPut, "/api/v1/provider-target-grants/"+grant.ID, grant, `"2"`)
	assertOK(grantRenew)
	decode(grantRenew, &grant)
	assertAuthorization(true)
	staleGrant := request(http.MethodPut, "/api/v1/provider-target-grants/"+grant.ID, grant, `"2"`)
	if staleGrant.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale provider grant update returned %d: %s", staleGrant.Code, staleGrant.Body.String())
	}

	membershipRevoke := request(http.MethodPost, "/api/v1/memberships/"+membership.ID+"/actions/revoke?organizationId=org.lifecycle", nil, resourceETag(membership.ResourceVersion))
	assertOK(membershipRevoke)
	decode(membershipRevoke, &membership)
	assertAuthorization(false)
	membership.ValidFrom, membership.ValidUntil, membership.ResourceVersion = time.Now().UTC().Add(-time.Minute), time.Time{}, 0
	membershipRenew := request(http.MethodPut, "/api/v1/memberships/"+membership.ID, membership, `"2"`)
	assertOK(membershipRenew)
	decode(membershipRenew, &membership)
	assertAuthorization(true)

	roleRevoke := request(http.MethodPost, "/api/v1/role-assignments/"+role.ID+"/actions/revoke?scopeKind=project&scopeId=project.lifecycle", nil, resourceETag(role.ResourceVersion))
	assertOK(roleRevoke)
	decode(roleRevoke, &role)
	assertAuthorization(false)
	role.ValidUntil, role.ResourceVersion = time.Time{}, 0
	roleRenew := request(http.MethodPut, "/api/v1/role-assignments/"+role.ID, role, `"2"`)
	assertOK(roleRenew)
	assertAuthorization(true)

	studentSession, _ := loginCookies(t, server, "lifecycle-user", "a-lifecycle-production-password")
	disable := request(http.MethodPost, "/api/v1/users/"+string(user.ID)+"/actions/disable", nil, resourceETag(user.ResourceVersion))
	assertOK(disable)
	decode(disable, &user)
	oldSession := secureRequest(http.MethodGet, "/api/v1/session", nil)
	oldSession.AddCookie(studentSession)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, oldSession)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user's old session returned %d", response.Code)
	}
	enable := request(http.MethodPost, "/api/v1/users/"+string(user.ID)+"/actions/enable", nil, resourceETag(user.ResourceVersion))
	assertOK(enable)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, oldSession)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("re-enabled user's revoked session returned %d", response.Code)
	}
	_, _ = loginCookies(t, server, "lifecycle-user", "a-lifecycle-production-password")

	adminSnapshot, err := store.AuthorizationSnapshot(context.Background(), "user.admin", time.Now().UTC())
	if err != nil || len(adminSnapshot.Assignments) != 1 {
		t.Fatalf("load bootstrap administrator assignment: assignments=%d err=%v", len(adminSnapshot.Assignments), err)
	}
	adminRole := adminSnapshot.Assignments[0]
	shiftedAdminRole := adminRole
	shiftedAdminRole.ValidFrom = time.Now().UTC().Add(time.Hour)
	shiftLastAdmin := request(http.MethodPut, "/api/v1/role-assignments/"+adminRole.ID, shiftedAdminRole, resourceETag(adminRole.ResourceVersion))
	if shiftLastAdmin.Code != http.StatusPreconditionFailed {
		t.Fatalf("last platform administrator validity shift returned %d: %s", shiftLastAdmin.Code, shiftLastAdmin.Body.String())
	}
	lastAdmin := request(http.MethodPost, "/api/v1/role-assignments/"+adminRole.ID+"/actions/revoke?scopeKind=platform&scopeId=platform", nil, resourceETag(adminRole.ResourceVersion))
	if lastAdmin.Code != http.StatusConflict {
		t.Fatalf("last platform administrator revoke returned %d: %s", lastAdmin.Code, lastAdmin.Body.String())
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
