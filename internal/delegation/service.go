// Package delegation implements browser-approved grants; it never creates a
// user identity or replaces the control plane's ordinary authorization checks.
package delegation

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"slices"
	"strings"
)

type Service struct {
	Store  app.GatewayStore
	Hasher app.KeyedTokenHasher
}

func secret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func ids() (app.GovernanceRecordIDs, error) {
	a, e := newID("audit")
	if e != nil {
		return app.GovernanceRecordIDs{}, e
	}
	v, e := newID("event")
	return app.GovernanceRecordIDs{AuditID: a, EventID: v}, e
}
func (s *Service) AuthenticateClient(ctx context.Context, id, value string) (app.GatewayClient, error) {
	if s == nil || s.Store == nil || s.Hasher == nil || len(value) > 256 {
		return app.GatewayClient{}, app.ErrGatewayInvalid
	}
	c, e := s.Store.GatewayClient(ctx, id)
	if e != nil || c.Disabled {
		return c, app.ErrGatewayInvalid
	}
	for _, h := range s.Hasher.HashCandidates("gateway.client", value) {
		if h.KeyID == c.SecretHash.KeyID && subtle.ConstantTimeCompare([]byte(h.Digest), []byte(c.SecretHash.Digest)) == 1 {
			return c, nil
		}
	}
	return c, app.ErrGatewayInvalid
}
func (s *Service) CreateClient(ctx context.Context, actor domain.UserID, name string) (app.GatewayClient, string, error) {
	id, e := newID("gateway")
	if e != nil {
		return app.GatewayClient{}, "", e
	}
	value, e := secret("lgc_")
	if e != nil {
		return app.GatewayClient{}, "", e
	}
	records, e := ids()
	if e != nil {
		return app.GatewayClient{}, "", e
	}
	c := app.GatewayClient{ID: id, Name: name, SecretHash: s.Hasher.HashCurrent("gateway.client", value)}
	e = s.Store.CreateGatewayClient(ctx, c, actor, records)
	return c, value, e
}
func ValidateScope(scope app.GatewayScope) error {
	if _, e := domain.NewProjectID(string(scope.ProjectID)); e != nil {
		return app.ErrGatewayDenied
	}
	if len(scope.Actions) == 0 || len(scope.Actions) > 4 || len(scope.TemplateIDs) > 64 || len(scope.InstanceIDs) > 64 || len(scope.TargetIDs) > 16 {
		return app.ErrGatewayDenied
	}
	for _, a := range scope.Actions {
		if !slices.Contains([]string{"templates.read", "deployments.create", "operations.read", "access.create"}, a) {
			return app.ErrGatewayDenied
		}
	}
	if slices.Contains(scope.Actions, "deployments.create") && (len(scope.TemplateIDs) == 0 || len(scope.TargetIDs) == 0) {
		return app.ErrGatewayDenied
	}
	if len(scope.TemplateIDs)+len(scope.InstanceIDs) == 0 {
		return app.ErrGatewayDenied
	}
	for _, id := range scope.TemplateIDs {
		if _, e := domain.NewTemplateID(string(id)); e != nil {
			return app.ErrGatewayDenied
		}
	}
	for _, id := range scope.InstanceIDs {
		if _, e := domain.NewInstanceID(string(id)); e != nil {
			return app.ErrGatewayDenied
		}
	}
	for _, id := range scope.TargetIDs {
		if _, e := domain.NewProviderTargetID(string(id)); e != nil {
			return app.ErrGatewayDenied
		}
	}
	return nil
}

type DeviceResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func (s *Service) Request(ctx context.Context, client app.GatewayClient, scope app.GatewayScope, origin string) (DeviceResponse, error) {
	if e := ValidateScope(scope); e != nil {
		return DeviceResponse{}, e
	}
	id, e := newID("device")
	if e != nil {
		return DeviceResponse{}, e
	}
	device, e := secret("lcd_")
	if e != nil {
		return DeviceResponse{}, e
	}
	b := make([]byte, 5)
	if _, e = rand.Read(b); e != nil {
		return DeviceResponse{}, e
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	_, e = s.Store.CreateGatewayDevice(ctx, app.GatewayDevice{ID: id, ClientID: client.ID, Scope: scope, DeviceHash: s.Hasher.HashCurrent("gateway.device", device), UserCodeHash: s.Hasher.HashCurrent("gateway.user-code", code)})
	if e != nil {
		return DeviceResponse{}, e
	}
	return DeviceResponse{DeviceCode: device, UserCode: code[:4] + "-" + code[4:], VerificationURI: origin + "/device", ExpiresIn: 300, Interval: 5}, nil
}
func code(value string) (string, error) {
	value = strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(value)), "-", "")
	if len(value) != 8 {
		return "", app.ErrGatewayExpired
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= '2' && r <= '7') {
			return "", app.ErrGatewayExpired
		}
	}
	return value, nil
}
func (s *Service) Inspect(ctx context.Context, actor domain.UserID, value string) (app.GatewayDevice, error) {
	c, e := code(value)
	if e != nil {
		return app.GatewayDevice{}, e
	}
	return s.Store.InspectGatewayDevice(ctx, s.Hasher.HashCandidates("gateway.user-code", c), actor)
}
func (s *Service) Approve(ctx context.Context, actor domain.UserID, value string, approve bool) error {
	c, e := code(value)
	if e != nil {
		return e
	}
	r, e := ids()
	if e != nil {
		return e
	}
	return s.Store.ApproveGatewayDevice(ctx, s.Hasher.HashCandidates("gateway.user-code", c), actor, approve, r)
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (s *Service) Poll(ctx context.Context, client app.GatewayClient, device string) (TokenResponse, error) {
	if len(device) > 256 || !strings.HasPrefix(device, "lcd_") {
		return TokenResponse{}, app.ErrGatewayExpired
	}
	id, e := newID("grant")
	if e != nil {
		return TokenResponse{}, e
	}
	token, e := secret("lcg_")
	if e != nil {
		return TokenResponse{}, e
	}
	records, e := ids()
	if e != nil {
		return TokenResponse{}, e
	}
	_, e = s.Store.PollGatewayDevice(ctx, client.ID, s.Hasher.HashCandidates("gateway.device", device), app.GatewayGrant{ID: id, TokenHash: s.Hasher.HashCurrent("gateway.grant", token)}, records)
	if e != nil {
		return TokenResponse{}, e
	}
	return TokenResponse{AccessToken: token, TokenType: "Bearer", ExpiresIn: 900}, nil
}
func (s *Service) Authenticate(ctx context.Context, client, clientSecret, token string) (app.GatewayGrant, error) {
	if _, e := s.AuthenticateClient(ctx, client, clientSecret); e != nil {
		return app.GatewayGrant{}, e
	}
	if len(token) > 256 || !strings.HasPrefix(token, "lcg_") {
		return app.GatewayGrant{}, app.ErrGatewayExpired
	}
	return s.Store.AuthenticateGatewayGrant(ctx, client, s.Hasher.HashCandidates("gateway.grant", token))
}
func (s *Service) Revoke(ctx context.Context, id string, actor domain.UserID) error {
	r, e := ids()
	if e != nil {
		return e
	}
	return s.Store.RevokeGatewayGrant(ctx, id, actor, r)
}
func (s *Service) DisableClient(ctx context.Context, id string, disabled bool, actor domain.UserID) error {
	r, e := ids()
	if e != nil {
		return e
	}
	return s.Store.SetGatewayClientDisabled(ctx, id, disabled, actor, r)
}

func (s *Service) DeleteClient(ctx context.Context, id string, actor domain.UserID) error {
	r, err := ids()
	if err != nil {
		return err
	}
	return s.Store.DeleteGatewayClient(ctx, id, actor, r)
}

func newID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + "." + hex.EncodeToString(b), nil
}
