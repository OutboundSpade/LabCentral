package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
)

// Exercise the HTTP boundary: a valid service-level OIDC flow is unusable when
// the redirect response does not set the browser-binding cookie.
func TestOIDCRedirectSetsBrowserBindingCookie(t *testing.T) {
	_, store := localAuthenticationFixture(t, nil)
	material := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'o'}, 32))
	keyring, err := authentication.ParseKeyring([]byte(fmt.Sprintf(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"oidc","keys":[{"id":"oidc","material":%q}]}`, material)))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := authentication.NewManager(store, keyring, authentication.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issuer := ""
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			"code_challenge_methods_supported": []string{"S256"},
		})
	}))
	defer provider.Close()
	issuer = provider.URL
	oidc, err := authentication.NewOIDCService(context.Background(), store, manager, keyring, authentication.OIDCConfig{
		Issuer: issuer, ClientID: "classroom", ClientSecret: "test-secret", RedirectURL: "https://lab.example/api/v1/auth/oidc/callback", HTTPClient: provider.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(new(controlplane.Service), []byte("example"), fstest.MapFS{"index.html": {Data: []byte("LabCentral")}}, Options{Authentication: AuthenticationOptions{
		Mode: AuthModeOIDC, Manager: manager, Repository: store, Authorizer: evaluator, OIDC: oidc, OIDCIssuer: issuer, PublicOrigin: "https://lab.example", SecureCookies: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, secureRequest(http.MethodGet, "/api/v1/auth/oidc/begin", nil))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("begin status = %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one binding cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != oidcBindingCookieName || len(cookie.Value) < 32 || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatal("OIDC browser-binding cookie is missing required attributes")
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || location.Query().Get("state") == "" || location.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("invalid OIDC redirect")
	}
	for _, operation := range []string{"/auth/oidc/begin", "/auth/oidc/callback"} {
		headers := server.API.OpenAPI().Paths[operation].Get.Responses["303"].Headers
		if _, ok := headers["Set-Cookie"]; !ok {
			t.Fatalf("%s omits the canonical Set-Cookie header", operation)
		}
	}
}
