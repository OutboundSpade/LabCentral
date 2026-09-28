package hosted

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestOIDCValidatesSignedClaimsAndPKCE(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if e != nil {
		t.Fatal(e)
	}
	issuer := "https://issuer.example.invalid"
	tokenIssuer := issuer
	var audience = "client"
	var nonce = "nonce"
	var subject = "student"
	groups := []string{"labcentral-users"}
	expiry := time.Now().Add(time.Hour).Unix()
	var pkce string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pkce = r.Form.Get("code_verifier")
		claims := map[string]any{"iss": tokenIssuer, "aud": audience, "sub": subject, "exp": expiry, "iat": time.Now().Unix(), "nonce": nonce, "groups": groups, "name": "Student"}
		b, _ := json.Marshal(claims)
		signed, _ := signer.Sign(b)
		token, _ := signed.CompactSerialize()
		writeJSON(w, 200, map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 300, "id_token": token})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, 200, claims{Sub: subject, Groups: groups})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()
	o, e := NewOIDC(context.Background(), Config{Issuer: issuer, BackchannelOrigin: server.URL, ClientID: "client", ClientSecret: "secret", Origin: "https://labs.invalid"})
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(o.LoginURL("state", "nonce", "verifier"))
	if u.Host != "issuer.example.invalid" {
		t.Fatal("browser authorization origin changed")
	}
	q := u.Query()
	if q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier("verifier") || q.Get("state") != "state" || q.Get("nonce") != "nonce" {
		t.Fatal("missing PKCE, state or nonce")
	}
	user, token, e := o.Exchange(context.Background(), "code", "verifier", "nonce")
	if e != nil || user.ID != "student" || pkce != "verifier" {
		t.Fatal(user, e, pkce)
	}
	for _, test := range []struct {
		name   string
		modify func()
		reset  func()
	}{
		{"issuer", func() { tokenIssuer = server.URL }, func() { tokenIssuer = issuer }},
		{"audience", func() { audience = "other-client" }, func() { audience = "client" }},
		{"nonce", func() { nonce = "other-nonce" }, func() { nonce = "nonce" }},
		{"expiry", func() { expiry = time.Now().Add(-time.Hour).Unix() }, func() { expiry = time.Now().Add(time.Hour).Unix() }},
		{"groups", func() { groups = []string{"other"} }, func() { groups = []string{"labcentral-users"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.modify()
			defer test.reset()
			if _, _, e := o.Exchange(context.Background(), "code", "verifier", "nonce"); e == nil {
				t.Fatal("invalid signed claims accepted")
			}
		})
	}
	groups = nil
	if _, _, e = o.Refresh(context.Background(), token); e == nil {
		t.Fatal("revoked group accepted by userinfo refresh")
	}
	groups = []string{"labcentral-admins"}
	if u, _, e := o.Refresh(context.Background(), token); e != nil || !u.Admin {
		t.Fatal(u, e)
	}
}

func TestIdentityDiagnosticsNeverIncludeUpstreamSecrets(t *testing.T) {
	for _, code := range []int{400, 401, 429, 500} {
		failure := identityFailure("token", code, fmt.Errorf("secret-token response body"))
		data, _ := json.Marshal(failure)
		if strings.Contains(string(data), "secret") || failure.Status != code {
			t.Fatal(string(data))
		}
	}
}
