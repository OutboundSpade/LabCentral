package application

import (
	"context"
	"errors"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

type GatewayScope struct {
	ProjectID   domain.ProjectID          `json:"projectId"`
	Actions     []string                  `json:"actions"`
	TemplateIDs []domain.TemplateID       `json:"templateIds"`
	InstanceIDs []domain.InstanceID       `json:"instanceIds"`
	TargetIDs   []domain.ProviderTargetID `json:"targetIds"`
}
type GatewayClient struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Disabled   bool           `json:"disabled"`
	SecretHash KeyedTokenHash `json:"-"`
}
type GatewayDevice struct {
	ID                       string         `json:"id"`
	ClientID                 string         `json:"clientId"`
	ClientName               string         `json:"clientName"`
	Scope                    GatewayScope   `json:"scope"`
	ExpiresAt                time.Time      `json:"expiresAt"`
	State                    string         `json:"state"`
	SubjectID                domain.UserID  `json:"-"`
	DeviceHash, UserCodeHash KeyedTokenHash `json:"-"`
}
type GatewayGrant struct {
	ID         string         `json:"id"`
	ClientID   string         `json:"clientId"`
	ClientName string         `json:"clientName"`
	SubjectID  domain.UserID  `json:"subjectId"`
	Scope      GatewayScope   `json:"scope"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Revoked    bool           `json:"revoked"`
	TokenHash  KeyedTokenHash `json:"-"`
}

var ErrGatewayInvalid = errors.New("invalid_client")
var ErrGatewayExpired = errors.New("expired_token")
var ErrGatewayPending = errors.New("authorization_pending")
var ErrGatewaySlowDown = errors.New("slow_down")
var ErrGatewayDenied = errors.New("access_denied")
var ErrGatewayConsumed = errors.New("invalid_grant")

type GatewayStore interface {
	CreateGatewayClient(context.Context, GatewayClient, domain.UserID, GovernanceRecordIDs) error
	ListGatewayClients(context.Context) ([]GatewayClient, error)
	SetGatewayClientDisabled(context.Context, string, bool, domain.UserID, GovernanceRecordIDs) error
	DeleteGatewayClient(context.Context, string, domain.UserID, GovernanceRecordIDs) error
	GatewayClient(context.Context, string) (GatewayClient, error)
	CreateGatewayDevice(context.Context, GatewayDevice) (GatewayDevice, error)
	InspectGatewayDevice(context.Context, []KeyedTokenHash, domain.UserID) (GatewayDevice, error)
	ApproveGatewayDevice(context.Context, []KeyedTokenHash, domain.UserID, bool, GovernanceRecordIDs) error
	PollGatewayDevice(context.Context, string, []KeyedTokenHash, GatewayGrant, GovernanceRecordIDs) (GatewayGrant, error)
	AuthenticateGatewayGrant(context.Context, string, []KeyedTokenHash) (GatewayGrant, error)
	ListGatewayGrants(context.Context, domain.UserID) ([]GatewayGrant, error)
	RevokeGatewayGrant(context.Context, string, domain.UserID, GovernanceRecordIDs) error
}
