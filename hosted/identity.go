package hosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"net/http"
	"net/url"
	"time"
)

type OIDC struct {
	provider *oidc.Provider
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	http     *http.Client
}
type claims struct {
	Sub    string   `json:"sub"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
	Nonce  string   `json:"nonce"`
}

func (c claims) user() (User, error) {
	u := User{ID: c.Sub, Name: c.Name, Email: c.Email}
	allowed := false
	for _, g := range c.Groups {
		if g == "labcentral-users" {
			allowed = true
		}
		if g == "labcentral-admins" {
			allowed = true
			u.Admin = true
		}
	}
	if !allowed || u.ID == "" {
		return u, fmt.Errorf("CDC competition membership is required")
	}
	return u, nil
}
func NewOIDC(ctx context.Context, c Config) (*OIDC, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if c.BackchannelOrigin != "" {
		target, e := url.Parse(c.BackchannelOrigin)
		issuer, e2 := url.Parse(c.Issuer)
		if e != nil || e2 != nil || target.Scheme != "https" || target.Host == "" || target.Path != "" || target.RawQuery != "" || target.Fragment != "" || target.User != nil {
			return nil, fmt.Errorf("OIDC backchannel must be an explicit HTTPS origin")
		}
		client.Transport = &backchannelTransport{issuer.Host, target.Host, http.DefaultTransport}
	}
	ctx = oidc.ClientContext(ctx, client)
	p, e := oidc.NewProvider(ctx, c.Issuer)
	if e != nil {
		return nil, e
	}
	return &OIDC{p, oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: c.Origin + "/auth/callback", Scopes: []string{"openid", "profile", "email", "groups", "offline_access"}}, p.Verifier(&oidc.Config{ClientID: c.ClientID}), client}, nil
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
	u, e := c.user()
	return u, t, e
}
func (o *OIDC) Refresh(ctx context.Context, t *oauth2.Token) (User, *oauth2.Token, error) {
	ctx = oidc.ClientContext(ctx, o.http)
	source := o.oauth.TokenSource(ctx, t)
	next, e := source.Token()
	if e != nil {
		return User{}, nil, e
	}
	info, e := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(next))
	if e != nil {
		return User{}, nil, e
	}
	var c claims
	if e = info.Claims(&c); e != nil {
		return User{}, nil, e
	}
	u, e := c.user()
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
