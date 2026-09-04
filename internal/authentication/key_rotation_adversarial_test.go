package authentication_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
)

func TestAuthenticationRateLimitSurvivesRollingKeyRotation(t *testing.T) {
	ctx := context.Background()
	provider := newTestOIDCProvider(t)
	store, oldManager, _ := newOIDCTestServices(t, provider)
	for attempt := 0; attempt < 2; attempt++ {
		allowed, err := oldManager.ConsumeAuthenticationAttempt(ctx, "login-user", "rotation-user", 15*time.Minute, 2)
		if err != nil || !allowed {
			t.Fatalf("old-key attempt %d: allowed=%v err=%v", attempt+1, allowed, err)
		}
	}
	retainedManager, _ := newOIDCServicesForKeyring(t, provider, store, parseKeyring(t, "new", map[string]byte{"new": 'n', "current": 'o'}))
	allowed, err := retainedManager.ConsumeAuthenticationAttempt(ctx, "login-user", "rotation-user", 15*time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("rolling key change reset the old-key authentication bucket")
	}
	if err := retainedManager.ClearAuthenticationAttempts(ctx, "login-user", "rotation-user"); err != nil {
		t.Fatal(err)
	}
	allowed, err = oldManager.ConsumeAuthenticationAttempt(ctx, "login-user", "rotation-user", 15*time.Minute, 2)
	if err != nil || !allowed {
		t.Fatalf("cross-key clear did not clear retained bucket: allowed=%v err=%v", allowed, err)
	}
}

func TestRetainedOldKeyAuthenticatesAndRetiredKeyRejectsExistingArtifacts(t *testing.T) {
	ctx := context.Background()
	provider := newTestOIDCProvider(t)
	store, oldManager, oldOIDC := newOIDCTestServices(t, provider)
	password := []byte("old-key-production-password")
	if err := oldManager.CreateLocalUser(ctx, "user.rotation", "rotation-user", "Rotation User", password); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkExternalIdentity(ctx, application.ExternalIdentityRecord{
		Issuer: provider.server.URL, Subject: "subject-1", UserID: "user.rotation",
	}); err != nil {
		t.Fatal(err)
	}

	oldSession, err := oldManager.AuthenticateLocal(ctx, "rotation-user", password)
	if err != nil {
		t.Fatal(err)
	}
	oldRecovery, err := oldManager.IssuePasswordRecovery(ctx, "user.rotation")
	if err != nil {
		t.Fatal(err)
	}
	oldFlow, err := oldOIDC.Begin(ctx, "/labs")
	if err != nil {
		t.Fatal(err)
	}
	oldFlowQuery := mustAuthorizationQuery(t, oldFlow.AuthorizationURL)
	provider.prepare(oldFlowQuery.Get("nonce"), oldFlowQuery.Get("code_challenge"), oidcTokenOptions{})

	retainedKeys := parseKeyring(t, "new", map[string]byte{"new": 'n', "current": 'o'})
	retainedManager, retainedOIDC := newOIDCServicesForKeyring(t, provider, store, retainedKeys)
	retiredKeys := parseKeyring(t, "new", map[string]byte{"new": 'n'})
	retiredManager, retiredOIDC := newOIDCServicesForKeyring(t, provider, store, retiredKeys)

	if _, err := retiredManager.AuthenticateSession(ctx, oldSession.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("session signed by a retired key returned %v", err)
	}
	if err := retiredManager.RecoverLocalPassword(ctx, oldRecovery, []byte("retired-key-password-change")); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("recovery token signed by a retired key returned %v", err)
	}
	if _, err := retiredOIDC.Callback(ctx, oldFlowQuery.Get("state"), "authorization-code", oldFlow.BrowserBinding); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("OIDC flow signed by a retired key returned %v", err)
	}

	rotated, err := retainedManager.AuthenticateSession(ctx, oldSession.CookieToken)
	if err != nil {
		t.Fatalf("retained old key did not authenticate its session: %v", err)
	}
	if !rotated.Rotated || rotated.Session.KeyID != "new" {
		t.Fatalf("old-key session was not rotated to the current key: %#v", rotated)
	}
	if err := retainedManager.RecoverLocalPassword(ctx, oldRecovery, []byte("retained-key-password-change")); err != nil {
		t.Fatalf("retained old key did not consume its recovery token: %v", err)
	}
	result, err := retainedOIDC.Callback(ctx, oldFlowQuery.Get("state"), "authorization-code", oldFlow.BrowserBinding)
	if err != nil {
		t.Fatalf("retained old key did not complete its OIDC flow: %v", err)
	}
	if result.Credentials.Session.KeyID != "new" || result.RedirectPath != "/labs" {
		t.Fatalf("OIDC completion did not issue current-key credentials: %#v", result)
	}
}

func TestOIDCParallelFlowsRemainBoundToTheirOwnBrowserSecret(t *testing.T) {
	ctx := context.Background()
	provider := newTestOIDCProvider(t)
	store, _, service := newOIDCTestServices(t, provider)
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.oidc-tabs", Username: "oidc-tabs", DisplayName: "OIDC Tabs"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkExternalIdentity(ctx, application.ExternalIdentityRecord{
		Issuer: provider.server.URL, Subject: "subject-1", UserID: "user.oidc-tabs",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := service.Begin(ctx, "/first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Begin(ctx, "/second")
	if err != nil {
		t.Fatal(err)
	}
	firstQuery := mustAuthorizationQuery(t, first.AuthorizationURL)
	secondQuery := mustAuthorizationQuery(t, second.AuthorizationURL)
	if first.BrowserBinding == second.BrowserBinding || firstQuery.Get("state") == secondQuery.Get("state") {
		t.Fatal("parallel OIDC flows reused state or browser binding material")
	}

	if _, err := service.Callback(ctx, firstQuery.Get("state"), "authorization-code", second.BrowserBinding); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("first state accepted the second flow's browser binding: %v", err)
	}
	if _, err := service.Callback(ctx, secondQuery.Get("state"), "authorization-code", first.BrowserBinding); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("second state accepted the first flow's browser binding: %v", err)
	}

	provider.prepare(firstQuery.Get("nonce"), firstQuery.Get("code_challenge"), oidcTokenOptions{})
	firstResult, err := service.Callback(ctx, firstQuery.Get("state"), "authorization-code", first.BrowserBinding)
	if err != nil {
		t.Fatalf("binding mismatch consumed the first flow: %v", err)
	}
	provider.prepare(secondQuery.Get("nonce"), secondQuery.Get("code_challenge"), oidcTokenOptions{})
	secondResult, err := service.Callback(ctx, secondQuery.Get("state"), "authorization-code", second.BrowserBinding)
	if err != nil {
		t.Fatalf("binding mismatch consumed the second flow: %v", err)
	}
	if firstResult.RedirectPath != "/first" || secondResult.RedirectPath != "/second" {
		t.Fatalf("parallel flow redirects crossed: first=%q second=%q", firstResult.RedirectPath, secondResult.RedirectPath)
	}
}

func newOIDCServicesForKeyring(t *testing.T, provider *testOIDCProvider, store *localdb.Store, keyring *authentication.Keyring) (*authentication.Manager, *authentication.OIDCService) {
	t.Helper()
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
	return manager, service
}

func mustAuthorizationQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}
