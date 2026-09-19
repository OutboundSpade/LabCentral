package application

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

const ConsoleControlFeature = "shared-console-control-v1"
const ConsoleInputLeaseDuration = 5 * time.Second
const ConsoleControlPollInterval = time.Second
const ConsolePresenceVersion = "labcentral.console-presence/v1"

// Input leases are delivered only over the authenticated plugin callback channel.
// ValidFor is measured from the plugin's monotonic request start, not receipt.
type ConsoleInputLease struct {
	Fence    uint64
	ValidFor time.Duration
}
type ConsoleParticipant struct {
	SessionID   domain.AccessSessionID `json:"sessionId"`
	UserID      domain.UserID          `json:"userId"`
	DisplayName string                 `json:"displayName"`
}
type ConsolePresence struct {
	APIVersion         string                   `json:"apiVersion"`
	SessionID          domain.AccessSessionID   `json:"sessionId"`
	InstanceID         domain.InstanceID        `json:"instanceId"`
	ResourceID         domain.LogicalResourceID `json:"resourceId"`
	Generation         domain.Generation        `json:"generation"`
	Participants       []ConsoleParticipant     `json:"participants"`
	DriverSessionID    domain.AccessSessionID   `json:"driverSessionId,omitempty"`
	RequestedSessionID domain.AccessSessionID   `json:"requestedSessionId,omitempty"`
	Fence              uint64                   `json:"fence"`
}
type ChangeConsoleControl struct {
	ActorID        domain.UserID
	SessionID      domain.AccessSessionID
	Take           bool
	IdempotencyKey string
	RequestDigest  domain.SHA256Digest
}
type ConsoleControlStore interface {
	ConsolePresence(context.Context, domain.UserID, domain.AccessSessionID) (ConsolePresence, error)
	ChangeConsoleControl(context.Context, ChangeConsoleControl) (ConsolePresence, error)
}
