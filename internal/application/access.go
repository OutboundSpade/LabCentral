package application

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

// KeyedTokenHash identifies a domain-separated HMAC without exposing its key.
// Access launch and lease tokens are reduced to this form before persistence.
type KeyedTokenHash struct {
	KeyID  string
	Digest string
}

type KeyedTokenHasher interface {
	HashCurrent(purpose, value string) KeyedTokenHash
	HashCandidates(purpose, value string) []KeyedTokenHash
}

// AccessCallbackIdentity is fixed by the verified host launch. An access
// plugin cannot choose or override these values in an authorization request.
type AccessCallbackIdentity struct {
	PluginID         string
	PluginInstanceID string
	Audience         string
	ProtocolMajor    uint32
	ProtocolMinor    uint32
}

type AccessAuthorizationDecision struct {
	SharedConsole bool
	InputLease    *ConsoleInputLease
	Granted       bool
	Lease         string
	ValidUntil    time.Time
	RecheckAfter  time.Duration
	Code          string
	SafeMessage   string
}

type AccessAuthorizationHandler interface {
	ExchangeAccessToken(context.Context, AccessCallbackIdentity, string, string) (AccessAuthorizationDecision, error)
	RecheckAccessLease(context.Context, AccessCallbackIdentity, string, string) (AccessAuthorizationDecision, error)
	EndAccessLease(context.Context, AccessCallbackIdentity, string, string, string) (bool, error)
}

type AccessDeliveryStore interface {
	ClaimAccessDelivery(context.Context, domain.AccessSessionID, domain.UserID, []KeyedTokenHash, GovernanceRecordIDs) (AccessSessionRecord, error)
}
