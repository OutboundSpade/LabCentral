package authentication

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Issuer                string
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	FlowLifetime          time.Duration
	HTTPClient            *http.Client
	AllowEnvironmentProxy bool
	Now                   func() time.Time
}

type OIDCService struct {
	repository application.AuthRepository
	manager    *Manager
	keyring    *Keyring
	issuer     string
	clientID   string
	verifier   *oidc.IDTokenVerifier
	oauth      oauth2.Config
	lifetime   time.Duration
	httpClient *http.Client
	now        func() time.Time
}

type OIDCCallbackResult struct {
	Credentials  SessionCredentials
	RedirectPath string
}

type OIDCBeginResult struct {
	AuthorizationURL string
	BrowserBinding   string
	ExpiresAt        time.Time
}

func NewOIDCService(ctx context.Context, repository application.AuthRepository, manager *Manager, keyring *Keyring, configuration OIDCConfig) (*OIDCService, error) {
	if repository == nil || manager == nil || keyring == nil {
		return nil, fmt.Errorf("OIDC repository, session manager, and keyring are required")
	}
	issuer, err := strictHTTPSURL(configuration.Issuer, false)
	if err != nil {
		return nil, fmt.Errorf("OIDC issuer is invalid")
	}
	redirect, err := strictHTTPSURL(configuration.RedirectURL, false)
	if err != nil {
		return nil, fmt.Errorf("OIDC redirect URL is invalid")
	}
	if configuration.ClientID == "" || len(configuration.ClientID) > 512 || configuration.ClientSecret == "" || len(configuration.ClientSecret) > 4096 {
		return nil, fmt.Errorf("OIDC client configuration is incomplete")
	}
	if configuration.FlowLifetime <= 0 {
		configuration.FlowLifetime = 5 * time.Minute
	}
	if configuration.FlowLifetime > 10*time.Minute {
		return nil, fmt.Errorf("OIDC flow lifetime must not exceed ten minutes")
	}
	if configuration.Now == nil {
		configuration.Now = time.Now
	}
	client := hardenedOIDCClient(configuration.HTTPClient, configuration.AllowEnvironmentProxy)
	providerContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(providerContext, issuer.String())
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	var metadata struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		JWKSURI                       string   `json:"jwks_uri"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	if err := provider.Claims(&metadata); err != nil || metadata.Issuer != issuer.String() {
		return nil, fmt.Errorf("OIDC discovery metadata is invalid")
	}
	for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.JWKSURI} {
		if _, err := strictHTTPSURL(endpoint, false); err != nil {
			return nil, fmt.Errorf("OIDC discovery contains an unsafe endpoint")
		}
	}
	if !containsString(metadata.CodeChallengeMethodsSupported, "S256") {
		return nil, fmt.Errorf("OIDC provider does not advertise PKCE S256")
	}
	endpoint := provider.Endpoint()
	if endpoint.AuthURL != metadata.AuthorizationEndpoint || endpoint.TokenURL != metadata.TokenEndpoint {
		return nil, fmt.Errorf("OIDC endpoint metadata is inconsistent")
	}
	return &OIDCService{
		repository: repository, manager: manager, keyring: keyring, issuer: issuer.String(), clientID: configuration.ClientID,
		verifier: provider.Verifier(&oidc.Config{ClientID: configuration.ClientID, Now: configuration.Now}),
		oauth: oauth2.Config{
			ClientID: configuration.ClientID, ClientSecret: configuration.ClientSecret, RedirectURL: redirect.String(),
			Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		lifetime: configuration.FlowLifetime, httpClient: client, now: configuration.Now,
	}, nil
}

func (s *OIDCService) Begin(ctx context.Context, redirectPath string) (OIDCBeginResult, error) {
	if !safeOIDCRedirectPath(redirectPath) {
		return OIDCBeginResult{}, ErrUnauthenticated
	}
	state, err := randomURLToken(32)
	if err != nil {
		return OIDCBeginResult{}, err
	}
	browserBinding, err := randomURLToken(32)
	if err != nil {
		return OIDCBeginResult{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return OIDCBeginResult{}, err
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return OIDCBeginResult{}, err
	}
	flowID, err := randomID("oidc")
	if err != nil {
		return OIDCBeginResult{}, err
	}
	encryptedVerifier, err := encryptFlowSecret(s.keyring.current.material, flowID, []byte(verifier))
	if err != nil {
		return OIDCBeginResult{}, err
	}
	now := s.now().UTC()
	if err := s.repository.CreateOIDCFlow(ctx, application.OIDCFlowRecord{
		ID: flowID, StateHash: keyedHash(s.keyring.current.material, "oidc-state", state),
		BrowserHash: keyedHash(s.keyring.current.material, "oidc-browser", browserBinding),
		NonceHash:   keyedHash(s.keyring.current.material, "oidc-nonce", nonce), PKCECiphertext: encryptedVerifier,
		KeyID: s.keyring.current.id, RedirectPath: redirectPath, ExpiresAt: now.Add(s.lifetime),
	}); err != nil {
		return OIDCBeginResult{}, err
	}
	return OIDCBeginResult{
		AuthorizationURL: s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)),
		BrowserBinding:   browserBinding, ExpiresAt: now.Add(s.lifetime),
	}, nil
}

func (s *OIDCService) Callback(ctx context.Context, state, code, browserBinding string) (OIDCCallbackResult, error) {
	if len(state) < 32 || len(state) > 256 || code == "" || len(code) > 4096 || len(browserBinding) < 32 || len(browserBinding) > 256 {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	ctx = oidc.ClientContext(ctx, s.httpClient)
	flow, key, err := s.consumeFlow(ctx, state, browserBinding)
	if err != nil {
		return OIDCCallbackResult{}, err
	}
	verifierBytes, err := decryptFlowSecret(key.material, flow.ID, flow.PKCECiphertext)
	if err != nil {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	defer zero(verifierBytes)
	token, err := s.oauth.Exchange(ctx, code, oauth2.VerifierOption(string(verifierBytes)))
	if err != nil {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" || len(rawIDToken) > 1<<20 {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	if idToken.AccessTokenHash != "" && idToken.VerifyAccessToken(token.AccessToken) != nil {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	var claims struct {
		Subject         string `json:"sub"`
		Nonce           string `json:"nonce"`
		AuthorizedParty string `json:"azp"`
	}
	if err := idToken.Claims(&claims); err != nil || idToken.Issuer != s.issuer || !validAudience(idToken.Audience, claims.AuthorizedParty, s.clientID) || claims.Subject == "" || len(claims.Subject) > 512 || claims.Nonce == "" ||
		!hmacEqual(flow.NonceHash, keyedHash(key.material, "oidc-nonce", claims.Nonce)) {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	user, err := s.repository.UserByExternalIdentity(ctx, s.issuer, claims.Subject)
	if err != nil || user.Disabled {
		return OIDCCallbackResult{}, ErrUnauthenticated
	}
	credentials, err := s.manager.NewSessionForUser(ctx, user.ID)
	if err != nil {
		return OIDCCallbackResult{}, err
	}
	return OIDCCallbackResult{Credentials: credentials, RedirectPath: flow.RedirectPath}, nil
}

func (s *OIDCService) consumeFlow(ctx context.Context, state, browserBinding string) (application.OIDCFlowRecord, sessionKey, error) {
	now := s.now().UTC()
	for _, key := range s.keyring.keys {
		flow, err := s.repository.ConsumeOIDCFlow(ctx, keyedHash(key.material, "oidc-state", state), keyedHash(key.material, "oidc-browser", browserBinding), key.id, now)
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			continue
		}
		if err != nil {
			return application.OIDCFlowRecord{}, sessionKey{}, err
		}
		return flow, key, nil
	}
	return application.OIDCFlowRecord{}, sessionKey{}, ErrUnauthenticated
}

func encryptFlowSecret(key []byte, flowID string, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, []byte("labcentral.oidc-flow.v1\x00"+flowID)), nil
}

func decryptFlowSecret(key []byte, flowID string, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrUnauthenticated
	}
	return aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], []byte("labcentral.oidc-flow.v1\x00"+flowID))
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	result := base64.RawURLEncoding.EncodeToString(value)
	zero(value)
	return result, nil
}

func strictHTTPSURL(value string, allowQuery bool) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || !allowQuery && parsed.RawQuery != "" || parsed.String() != value {
		return nil, fmt.Errorf("URL must be an exact HTTPS URL")
	}
	return parsed, nil
}

func safeOIDCRedirectPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n\\") && len(value) <= 2048
}

func hmacEqual(first, second string) bool {
	return len(first) == len(second) && subtle.ConstantTimeCompare([]byte(first), []byte(second)) == 1
}

func validAudience(audience []string, authorizedParty, clientID string) bool {
	if len(audience) == 1 {
		return audience[0] == clientID && (authorizedParty == "" || authorizedParty == clientID)
	}
	if len(audience) < 2 || authorizedParty != clientID {
		return false
	}
	return containsString(audience, clientID)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func hardenedOIDCClient(configured *http.Client, allowEnvironmentProxy bool) *http.Client {
	client := &http.Client{}
	if configured != nil {
		*client = *configured
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 15 * time.Second
	}
	transport := client.Transport
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		if !allowEnvironmentProxy {
			base.Proxy = nil
		}
		transport = base
	}
	client.Transport = boundedOIDCTransport{next: transport, maximumBytes: 2 << 20}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

type boundedOIDCTransport struct {
	next         http.RoundTripper
	maximumBytes int64
}

func (transport boundedOIDCTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > transport.maximumBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("OIDC response exceeds its size limit")
	}
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, transport.maximumBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(contents)) > transport.maximumBytes {
		return nil, fmt.Errorf("OIDC response exceeds its size limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(contents))
	response.ContentLength = int64(len(contents))
	return response, nil
}
