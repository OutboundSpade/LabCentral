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
	"sync/atomic"
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
	groups := []string{"lab_members"}
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
	o, e := NewOIDC(context.Background(), Config{MemberGroup: "lab_members", AdminGroup: "lab_admins", Issuer: issuer, BackchannelOrigin: server.URL, ClientID: "client", ClientSecret: "secret", Origin: "https://labs.invalid"})
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
		{"groups", func() { groups = []string{"other"} }, func() { groups = []string{"lab_members"} }},
		{"near-match", func() { groups = []string{"lab_members-extra"} }, func() { groups = []string{"lab_members"} }},
		{"subject", func() { subject = "" }, func() { subject = "student" }},
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
	groups = []string{"lab_admins"}
	if u, _, e := o.Exchange(context.Background(), "code", "verifier", "nonce"); e != nil || !u.Admin {
		t.Fatal("administrator group alone must admit administrators", u, e)
	}
	if u, _, e := o.Refresh(context.Background(), token); e != nil || !u.Admin {
		t.Fatal(u, e)
	}
	groups = []string{"lab_members"}
	if u, _, e := o.Refresh(context.Background(), token); e != nil || u.Admin {
		t.Fatal("removed administrator group retained privilege", u, e)
	}
}

func TestIdentityGroupsAreRequiredBeforeDiscovery(t *testing.T) {
	base := Config{MemberGroup: "team_members", AdminGroup: "team_admins", Origin: "https://labs.invalid", DesktopOrigin: "https://desktop.invalid", Issuer: "https://issuer.invalid"}
	if err := ValidateConfig(base); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"memberGroup", "adminGroup"} {
		for _, value := range []string{"", " ", " members", "members ", "two groups", "members\t", "members\n", "members\r", "members\u00a0", "members\u2003"} {
			t.Run(field+"/"+fmt.Sprintf("%q", value), func(t *testing.T) {
				c := base
				if field == "memberGroup" {
					c.MemberGroup = value
				} else {
					c.AdminGroup = value
				}
				for name, validate := range map[string]func() error{
					"config":    func() error { return ValidateConfig(c) },
					"startup":   func() error { _, err := New(context.Background(), c, nil); return err },
					"discovery": func() error { _, err := NewOIDC(context.Background(), c); return err },
				} {
					if err := validate(); err == nil || !strings.Contains(err.Error(), field) {
						t.Fatal(name, "invalid group reached identity or state initialization", err)
					}
				}
			})
		}
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

func TestOIDCRefreshBoundsConcurrentUpstreamWorkAndCancelsWaiters(t *testing.T) {
	var active, maximum, calls atomic.Int32
	entered := make(chan struct{}, 20)
	release := make(chan struct{})
	issuer := "https://issuer.example.invalid"
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(w, 200, claims{Sub: "student", Groups: []string{"lab_members"}})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	old := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = old }()
	o, err := NewOIDC(context.Background(), Config{MemberGroup: "lab_members", AdminGroup: "lab_admins", Issuer: issuer, BackchannelOrigin: server.URL, ClientID: "client", Origin: "https://labs.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	token := &oauth2.Token{AccessToken: "access", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	done := make(chan error, 20)
	for range 4 {
		go func() { _, _, err := o.Refresh(context.Background(), token); done <- err }()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("refresh did not reach upstream")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = o.Refresh(ctx, token); err == nil {
		t.Fatal("cancelled waiter accepted", err)
	}
	if calls.Load() != 4 {
		t.Fatal("cancelled waiter reached upstream")
	}
	for range 16 {
		go func() { _, _, err := o.Refresh(context.Background(), token); done <- err }()
	}
	close(release)
	for range 20 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() > 4 || calls.Load() != 20 {
		t.Fatalf("upstream maximum %d, calls %d", maximum.Load(), calls.Load())
	}
}

func TestIdentityTimeoutReportsPhaseWithoutRequestSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	status := new(identityHTTPStatus)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), identityStatusKey{}, status), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/?secret=must-not-log", nil)
	req.Header.Set("Authorization", "Bearer must-not-log")
	client := http.Client{Transport: identityStatusTransport{http.DefaultTransport}}
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected timeout")
	}
	failure := status.failure("userinfo", err)
	data, _ := json.Marshal(failure)
	if failure.Phase != "response" || failure.Category != "timeout" || strings.Contains(string(data), "must-not-log") {
		t.Fatal(string(data))
	}
}
