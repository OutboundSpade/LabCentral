package application

import (
	"context"
	"time"
)

// Provider is the application-facing provider port. Persisted provider state is
// an opaque envelope; only adapters encode or decode its wire form.
type Provider interface {
	Validate(context.Context, ProviderValidationRequest) (ProviderValidation, error)
	Plan(context.Context, ProviderMutation) (ProviderPlan, error)
	Invoke(context.Context, string, ProviderMutation) (*ProviderResult, error)
	Lookup(context.Context, ProviderCall, ProviderTargetContext, string) (*ProviderResult, error)
	Discover(context.Context, ProviderTargetContext) (ProviderDiscovery, error)
	DecodeDiscovery([]byte) (ProviderDiscovery, error)
	StateDigest([]byte) ([]byte, error)
}

type ProviderCall struct {
	RequestID, OperationID, InvocationID, InstanceID, ResourceID, TargetID, IdempotencyKey string
	Generation, Fence                                                                      uint64
}
type ProviderTargetContext struct {
	ID                 string
	Config, Credential []byte
	ConfigFingerprint  string
}
type ProviderIssue struct{ Code, Message, FieldPath, ResourceID string }
type ProviderValidationRequest struct {
	Call                ProviderCall
	Target              ProviderTargetContext
	Canonical, Override []byte
}
type ProviderValidation struct {
	Plan             []byte
	PlanDigest       string
	Errors, Warnings []ProviderIssue
}
type ProviderDependency struct {
	ResourceID         string
	Plan               []byte
	PlanDigest         string
	State, Observation []byte
	Allocations        []AllocationKey
}
type ProviderMutation struct {
	Call                             ProviderCall
	Target                           ProviderTargetContext
	Canonical, Plan, PriorState      []byte
	Ownership                        []string
	Dependencies                     []ProviderDependency
	Allocations, ExcludedAllocations []AllocationKey
}
type ProviderPlan struct {
	Plan        []byte
	PlanDigest  string
	Error       *ProviderIssue
	Warnings    []ProviderIssue
	Allocations []AllocationKey
}
type ProviderResult struct {
	State, Observation                          []byte
	Error                                       *ProviderIssue
	CompensationAttempted, CompensationComplete bool
}
type ProviderCapability struct{ ID, Support, Evidence string }
type ProviderDiscovery struct {
	Capabilities             []ProviderCapability
	DiscoveredAt, ValidUntil time.Time
	JSON                     []byte
}

// ProviderAccess is optional; providers without console support need not expose it.
type ProviderAccess interface {
	DiscoverEndpoint(context.Context, ProviderCall, ProviderTargetContext, []byte, string) (ProviderEndpoint, error)
}
type ProviderSchema struct {
	Name         string
	Major, Minor uint32
}
type ProviderEndpoint struct {
	Schema                                                        ProviderSchema
	TargetID, InstanceID, ResourceID, Method, StateDigest, Digest string
	Opaque                                                        []byte
	ValidUntil                                                    time.Time
	Error                                                         *ProviderIssue
}

type AccessPlugin interface {
	Create(context.Context, AccessCreate) (AccessCreated, error)
	Revoke(context.Context, AccessRevoke) (AccessRevoked, error)
}
type AccessCreate struct {
	Call                                                                ProviderCall
	SessionID, SubjectID, Method, EndpointDigest, StateDigest, Audience string
	ExpiresAt                                                           time.Time
	Endpoint                                                            []byte
	Schema                                                              ProviderSchema
}
type AccessCreated struct {
	Schema             ProviderSchema
	State              []byte
	Digest, Token, URL string
	ValidUntil         time.Time
	Error              *ProviderIssue
}
type AccessRevoke struct {
	Call      ProviderCall
	SessionID string
	State     []byte
}
type AccessRevoked struct {
	Closed bool
	Error  *ProviderIssue
}

// ProviderUsage is an optional read port; older providers may return nil usage.
type ProviderUsage interface {
	ReadUsage(context.Context, ProviderMutation) (*ResourceUsage, error)
}
type ResourceUsage struct {
	SampledAt           time.Time `json:"sampledAt"`
	CPUFraction         *float64  `json:"cpuFraction,omitempty"`
	VirtualCPUs         *uint32   `json:"virtualCPUs,omitempty"`
	MemoryUsedBytes     *uint64   `json:"memoryUsedBytes,omitempty"`
	MemoryCapacityBytes *uint64   `json:"memoryCapacityBytes,omitempty"`
}
