package authentication_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestOIDCAuthorizationCodePKCEAndOneUseState(t *testing.T) {
	provider := newTestOIDCProvider(t)
	store, manager, service := newOIDCTestServices(t, provider)
	if err := store.PutUser(context.Background(), application.UserRecord{ID: "user.oidc", Username: "oidc-user", DisplayName: "OIDC User"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkExternalIdentity(context.Background(), application.ExternalIdentityRecord{Issuer: provider.server.URL, Subject: "subject-1", UserID: "user.oidc"}); err != nil {
		t.Fatal(err)
	}

	begin, err := service.Begin(context.Background(), "/labs")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "https" || query.Get("response_type") != "code" || query.Get("state") == "" || query.Get("nonce") == "" ||
		query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization request is incomplete: %s", begin.AuthorizationURL)
	}
	provider.prepare(query.Get("nonce"), query.Get("code_challenge"), oidcTokenOptions{})
	result, err := service.Callback(context.Background(), query.Get("state"), "authorization-code", begin.BrowserBinding)
	if err != nil {
		t.Fatal(err)
	}
	if result.RedirectPath != "/labs" || result.Credentials.Session.UserID != "user.oidc" || result.Credentials.CookieToken == "" || result.Credentials.CSRFToken == "" {
		t.Fatalf("callback result = %#v", result)
	}
	if _, err := manager.AuthenticateSession(context.Background(), result.Credentials.CookieToken); err != nil {
		t.Fatalf("OIDC session did not authenticate: %v", err)
	}
	if _, err := service.Callback(context.Background(), query.Get("state"), "authorization-code", begin.BrowserBinding); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("OIDC state replay returned %v", err)
	}
}

func TestOIDCRefreshesJWKSAfterSigningKeyRotation(t *testing.T) {
	provider := newTestOIDCProvider(t)
	store, _, service := newOIDCTestServices(t, provider)
	ctx := context.Background()
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.oidc", Username: "oidc-user", DisplayName: "OIDC User"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkExternalIdentity(ctx, application.ExternalIdentityRecord{Issuer: provider.server.URL, Subject: "subject-1", UserID: "user.oidc"}); err != nil {
		t.Fatal(err)
	}

	login := func() {
		t.Helper()
		begin, err := service.Begin(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(begin.AuthorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		provider.prepare(query.Get("nonce"), query.Get("code_challenge"), oidcTokenOptions{})
		result, err := service.Callback(ctx, query.Get("state"), "authorization-code", begin.BrowserBinding)
		if err != nil {
			t.Fatal(err)
		}
		if result.Credentials.Session.UserID != "user.oidc" {
			t.Fatalf("OIDC session user = %q", result.Credentials.Session.UserID)
		}
	}

	login() // Populate the verifier's remote-key cache with the original key.
	provider.rotateSigningKey()
	login() // An unknown key ID must trigger a fresh JWKS fetch and verify cleanly.
}

func TestOIDCRejectsNonceAudienceIssuerSignaturePKCEAndUnlinkedIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		options      oidcTokenOptions
		linked       bool
		breakPKCE    bool
		breakBinding bool
		accepted     bool
	}{
		{name: "nonce", options: oidcTokenOptions{nonce: "attacker-nonce"}, linked: true},
		{name: "audience", options: oidcTokenOptions{audience: "other-client"}, linked: true},
		{name: "multiple audiences without authorized party", options: oidcTokenOptions{audiences: []string{"client", "other-client"}}, linked: true},
		{name: "multiple audiences with wrong authorized party", options: oidcTokenOptions{audiences: []string{"client", "other-client"}, authorizedParty: "other-client"}, linked: true},
		{name: "multiple audiences with matching authorized party", options: oidcTokenOptions{audiences: []string{"client", "other-client"}, authorizedParty: "client"}, linked: true, accepted: true},
		{name: "issuer", options: oidcTokenOptions{issuer: "https://attacker.invalid"}, linked: true},
		{name: "signature", options: oidcTokenOptions{wrongSigningKey: true}, linked: true},
		{name: "PKCE", linked: true, breakPKCE: true},
		{name: "browser binding", linked: true, breakBinding: true},
		{name: "unlinked identity"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := newTestOIDCProvider(t)
			store, _, service := newOIDCTestServices(t, provider)
			if err := store.PutUser(context.Background(), application.UserRecord{ID: "user.oidc", Username: "oidc-user", DisplayName: "OIDC User"}); err != nil {
				t.Fatal(err)
			}
			if testCase.linked {
				if err := store.LinkExternalIdentity(context.Background(), application.ExternalIdentityRecord{Issuer: provider.server.URL, Subject: "subject-1", UserID: "user.oidc"}); err != nil {
					t.Fatal(err)
				}
			}
			begin, err := service.Begin(context.Background(), "/")
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(begin.AuthorizationURL)
			query := parsed.Query()
			challenge := query.Get("code_challenge")
			if testCase.breakPKCE {
				challenge = strings.Repeat("x", len(challenge))
			}
			provider.prepare(query.Get("nonce"), challenge, testCase.options)
			binding := begin.BrowserBinding
			if testCase.breakBinding {
				binding = strings.Repeat("x", len(binding))
			}
			_, err = service.Callback(context.Background(), query.Get("state"), "authorization-code", binding)
			if testCase.accepted && err != nil {
				t.Fatalf("valid OIDC callback returned %v", err)
			}
			if !testCase.accepted && !errors.Is(err, authentication.ErrUnauthenticated) {
				t.Fatalf("invalid OIDC callback returned %v", err)
			}
		})
	}
}

func TestOIDCRejectsUnsafeConfigurationAndRedirects(t *testing.T) {
	provider := newTestOIDCProvider(t)
	store, manager, _ := newOIDCTestServices(t, provider)
	keyring := parseKeyring(t, "current", map[string]byte{"current": 'o'})
	for _, configuration := range []authentication.OIDCConfig{
		{Issuer: "http://issuer.invalid", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://lab.example/callback"},
		{Issuer: provider.server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://lab.example/callback?confused=true", HTTPClient: provider.server.Client()},
		{Issuer: provider.server.URL, ClientID: "client", RedirectURL: "https://lab.example/callback", HTTPClient: provider.server.Client()},
	} {
		if _, err := authentication.NewOIDCService(context.Background(), store, manager, keyring, configuration); err == nil {
			t.Fatalf("unsafe OIDC configuration was accepted: %#v", configuration)
		}
	}
	service, err := authentication.NewOIDCService(context.Background(), store, manager, keyring, authentication.OIDCConfig{
		Issuer: provider.server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://lab.example/callback", HTTPClient: provider.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, redirect := range []string{"https://attacker.invalid", "//attacker.invalid", "/safe\\evil", "/safe\nheader"} {
		if _, err := service.Begin(context.Background(), redirect); !errors.Is(err, authentication.ErrUnauthenticated) {
			t.Fatalf("unsafe post-login redirect %q returned %v", redirect, err)
		}
	}
}

func TestOIDCRejectsRedirectedOversizedAndInsecureDiscovery(t *testing.T) {
	provider := newTestOIDCProvider(t)
	store, manager, _ := newOIDCTestServices(t, provider)
	keyring := parseKeyring(t, "current", map[string]byte{"current": 'o'})
	for _, mode := range []string{"redirect", "oversized", "insecure-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if mode == "redirect" && request.URL.Path == "/.well-known/openid-configuration" {
					http.Redirect(response, request, server.URL+"/metadata", http.StatusFound)
					return
				}
				if mode == "oversized" {
					response.Header().Set("Content-Type", "application/json")
					_, _ = response.Write([]byte(strings.Repeat("x", (2<<20)+1)))
					return
				}
				endpointBase := server.URL
				if mode == "insecure-endpoint" {
					endpointBase = "http://issuer.invalid"
				}
				writeJSON(response, map[string]any{
					"issuer": server.URL, "authorization_endpoint": endpointBase + "/authorize", "token_endpoint": endpointBase + "/token",
					"jwks_uri": endpointBase + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
					"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
				})
			}))
			defer server.Close()
			_, err := authentication.NewOIDCService(context.Background(), store, manager, keyring, authentication.OIDCConfig{
				Issuer: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://lab.example/callback", HTTPClient: server.Client(),
			})
			if err == nil {
				t.Fatal("unsafe OIDC discovery was accepted")
			}
		})
	}
}

func newOIDCTestServices(t *testing.T, provider *testOIDCProvider) (*localdb.Store, *authentication.Manager, *authentication.OIDCService) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:oidc-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	keyring := parseKeyring(t, "current", map[string]byte{"current": 'o'})
	manager, err := authentication.NewManager(store, keyring, authentication.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := authentication.NewOIDCService(context.Background(), store, manager, keyring, authentication.OIDCConfig{
		Issuer: provider.server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://lab.example/api/v1/auth/oidc/callback",
		HTTPClient: provider.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, manager, service
}

type oidcTokenOptions struct {
	nonce           string
	issuer          string
	audience        string
	audiences       []string
	authorizedParty string
	wrongSigningKey bool
}

type testOIDCProvider struct {
	t             *testing.T
	server        *httptest.Server
	key           *rsa.PrivateKey
	keyID         string
	wrongKey      *rsa.PrivateKey
	mu            sync.Mutex
	nonce         string
	codeChallenge string
	options       oidcTokenOptions
}

func newTestOIDCProvider(t *testing.T) *testOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &testOIDCProvider{t: t, key: key, keyID: "test-key", wrongKey: wrongKey}
	provider.server = httptest.NewTLSServer(http.HandlerFunc(provider.serveHTTP))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *testOIDCProvider) rotateSigningKey() {
	p.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.key, p.keyID = key, "rotated-key"
	p.mu.Unlock()
}

func (p *testOIDCProvider) prepare(nonce, codeChallenge string, options oidcTokenOptions) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce, p.codeChallenge, p.options = nonce, codeChallenge, options
}

func (p *testOIDCProvider) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(response, map[string]any{
			"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token",
			"jwks_uri": p.server.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	case "/keys":
		p.mu.Lock()
		key, keyID := p.key, p.keyID
		p.mu.Unlock()
		writeJSON(response, map[string]any{"keys": []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: keyID, Algorithm: "RS256", Use: "sig"}}})
	case "/token":
		p.token(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (p *testOIDCProvider) token(response http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		http.Error(response, "invalid form", http.StatusBadRequest)
		return
	}
	username, password, basic := request.BasicAuth()
	p.mu.Lock()
	nonce, challenge, options, key, keyID := p.nonce, p.codeChallenge, p.options, p.key, p.keyID
	p.mu.Unlock()
	verifierDigest := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
	if !basic || username != "client" || password != "secret" || request.Form.Get("grant_type") != "authorization_code" ||
		request.Form.Get("code") != "authorization-code" || base64.RawURLEncoding.EncodeToString(verifierDigest[:]) != challenge {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	issuer, audience, claimNonce := p.server.URL, jwt.Audience{"client"}, nonce
	if options.issuer != "" {
		issuer = options.issuer
	}
	if options.audience != "" {
		audience = jwt.Audience{options.audience}
	}
	if len(options.audiences) > 0 {
		audience = append(jwt.Audience(nil), options.audiences...)
	}
	if options.nonce != "" {
		claimNonce = options.nonce
	}
	if options.wrongSigningKey {
		key = p.wrongKey
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		p.t.Errorf("create signer: %v", err)
		http.Error(response, "signer", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: "subject-1", Audience: audience, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Claims(struct {
		Nonce           string `json:"nonce"`
		AuthorizedParty string `json:"azp,omitempty"`
	}{Nonce: claimNonce, AuthorizedParty: options.authorizedParty}).Serialize()
	if err != nil {
		p.t.Errorf("sign ID token: %v", err)
		http.Error(response, "token", http.StatusInternalServerError)
		return
	}
	writeJSON(response, map[string]any{"access_token": "access-token", "token_type": "Bearer", "expires_in": 300, "id_token": raw})
}

func writeJSON(response http.ResponseWriter, value any) {
	writeJSONStatus(response, http.StatusOK, value)
}

func writeJSONStatus(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
