package hosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"
)

type OIDC struct {
	provider     *oidc.Provider
	oauth        oauth2.Config
	verifier     *oidc.IDTokenVerifier
	http         *http.Client
	refreshSlots chan struct{}
	memberGroup  string
	adminGroup   string
}
type claims struct {
	Sub    string   `json:"sub"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
	Nonce  string   `json:"nonce"`
}

func (c claims) user(memberGroup, adminGroup string) (User, error) {
	u := User{ID: c.Sub, Name: c.Name, Email: c.Email}
	allowed := false
	for _, g := range c.Groups {
		if g == memberGroup && memberGroup != "" {
			allowed = true
		}
		if g == adminGroup && adminGroup != "" {
			allowed = true
			u.Admin = true
		}
	}
	if !allowed || u.ID == "" {
		return u, fmt.Errorf("Your account is not a member of this LabCentral installation.")
	}
	return u, nil
}
func NewOIDC(ctx context.Context, c Config) (*OIDC, error) {
	if err := validateIdentityGroups(c); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Keep OIDC connection health independent of provider and desktop traffic.
	transport.IdleConnTimeout = 30 * time.Second
	transport.HTTP2 = &http.HTTP2Config{SendPingTimeout: 5 * time.Second, PingTimeout: 3 * time.Second}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	if c.BackchannelOrigin != "" {
		target, e := url.Parse(c.BackchannelOrigin)
		issuer, e2 := url.Parse(c.Issuer)
		if e != nil || e2 != nil || target.Scheme != "https" || target.Host == "" || target.Path != "" || target.RawQuery != "" || target.Fragment != "" || target.User != nil {
			return nil, fmt.Errorf("OIDC backchannel must be an explicit HTTPS origin")
		}
		client.Transport = &backchannelTransport{issuer.Host, target.Host, transport}
	}
	client.Transport = identityStatusTransport{client.Transport}
	ctx = oidc.ClientContext(ctx, client)
	p, e := oidc.NewProvider(ctx, c.Issuer)
	if e != nil {
		return nil, e
	}
	return &OIDC{p, oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: c.Origin + "/auth/callback", Scopes: []string{"openid", "profile", "email", "groups", "offline_access"}}, p.Verifier(&oidc.Config{ClientID: c.ClientID}), client, make(chan struct{}, 4), c.MemberGroup, c.AdminGroup}, nil
}
func (o *OIDC) LoginURL(state, nonce, verifier string) string {
	return o.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}
func (o *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (User, *oauth2.Token, error) {
	ctx = oidc.ClientContext(ctx, o.http)
	t, e := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return User{}, nil, e
	}
	raw, ok := t.Extra("id_token").(string)
	if !ok {
		return User{}, nil, fmt.Errorf("ID token is missing")
	}
	token, e := o.verifier.Verify(ctx, raw)
	if e != nil {
		return User{}, nil, e
	}
	var c claims
	if e = token.Claims(&c); e != nil {
		return User{}, nil, e
	}
	if c.Nonce != nonce {
		return User{}, nil, fmt.Errorf("login nonce mismatch")
	}
	u, e := c.user(o.memberGroup, o.adminGroup)
	return u, t, e
}
func (o *OIDC) Refresh(ctx context.Context, t *oauth2.Token) (User, *oauth2.Token, error) {
	// A returning classroom must not flood the IDP with simultaneous token and
	// userinfo requests. Waiting remains bounded by the caller's deadline.
	if o.refreshSlots != nil {
		select {
		case o.refreshSlots <- struct{}{}:
			defer func() { <-o.refreshSlots }()
		case <-ctx.Done():
			return User{}, nil, identityFailure("queue", 0, ctx.Err())
		}
	}
	status := new(identityHTTPStatus)
	ctx = context.WithValue(ctx, identityStatusKey{}, status)
	ctx = oidc.ClientContext(ctx, o.http)
	source := o.oauth.TokenSource(ctx, t)
	next, e := source.Token()
	if e != nil {
		return User{}, nil, status.failure("token", e)
	}
	status.code = 0
	info, e := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(next))
	if e != nil {
		return User{}, next, status.failure("userinfo", e)
	}
	var c claims
	if e = info.Claims(&c); e != nil {
		return User{}, next, identityFailure("claims", status.code, e)
	}
	u, e := c.user(o.memberGroup, o.adminGroup)
	return u, next, e
}
func randomID(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// The operator may use the IDP's authenticated HTTPS origin for machine requests.
// Discovery, JWT issuer and audience validation still use the advertised issuer;
// browser authorization retains its canonical public URL.
type backchannelTransport struct {
	issuerHost, originHost string
	base                   http.RoundTripper
}

func (t *backchannelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	if r.URL.Scheme == "https" && r.URL.Host == t.issuerHost {
		target := *r.URL
		target.Host = t.originHost
		copy.URL = &target
		copy.Host = t.originHost
	}
	return t.base.RoundTrip(copy)
}

// Diagnostics contain only fixed categories and HTTP status, never token bodies,
// response text, endpoint queries, user details or authentication headers.
type identityError struct {
	Stage    string `json:"stage"`
	Status   int    `json:"status,omitempty"`
	Category string `json:"category"`
	Phase    string `json:"phase,omitempty"`
	Reused   bool   `json:"reused,omitempty"`
}

func (e *identityError) Error() string { return "identity " + e.Stage + ": " + e.Category }
func identityFailure(stage string, status int, err error) *identityError {
	category := "rejected"
	var network net.Error
	if errors.Is(err, context.Canceled) {
		category = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		category = "timeout"
	} else if status == 429 {
		category = "rate_limited"
	} else if status >= 500 {
		category = "upstream_unavailable"
	} else if status == 0 {
		category = "transport"
	}
	return &identityError{Stage: stage, Status: status, Category: category}
}

type identityStatusKey struct{}
type identityHTTPStatus struct {
	code   int
	mu     sync.Mutex
	phase  string
	reused bool
}

func (s *identityHTTPStatus) mark(phase string) { s.mu.Lock(); s.phase = phase; s.mu.Unlock() }
func (s *identityHTTPStatus) failure(stage string, err error) *identityError {
	s.mu.Lock()
	defer s.mu.Unlock()
	failure := identityFailure(stage, s.code, err)
	failure.Phase = s.phase
	failure.Reused = s.reused
	return failure
}

type identityStatusTransport struct{ base http.RoundTripper }

func (t identityStatusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if status, ok := r.Context().Value(identityStatusKey{}).(*identityHTTPStatus); ok {
		status.mark("connection")
		trace := &httptrace.ClientTrace{
			DNSStart:          func(httptrace.DNSStartInfo) { status.mark("dns") },
			ConnectStart:      func(string, string) { status.mark("connect") },
			TLSHandshakeStart: func() { status.mark("tls") },
			TLSHandshakeDone:  func(tls.ConnectionState, error) { status.mark("request") },
			GotConn: func(info httptrace.GotConnInfo) {
				status.mu.Lock()
				status.phase = "request"
				status.reused = info.Reused
				status.mu.Unlock()
			},
			WroteRequest:         func(httptrace.WroteRequestInfo) { status.mark("response") },
			GotFirstResponseByte: func() { status.mark("response_body") },
		}
		r = r.WithContext(httptrace.WithClientTrace(r.Context(), trace))
	}
	response, err := t.base.RoundTrip(r)
	if status, ok := r.Context().Value(identityStatusKey{}).(*identityHTTPStatus); ok && response != nil {
		status.code = response.StatusCode
	}
	return response, err
}
