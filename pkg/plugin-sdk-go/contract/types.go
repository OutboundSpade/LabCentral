package contract

import (
	"errors"
	"time"
)

var ErrOutcomeUnknown = errors.New("provider outcome is unknown")

type Fault string

const (
	FaultNone                 Fault = ""
	FaultCrashAfterCreate     Fault = "crash_after_create_before_response"
	FaultDropFinalResponse    Fault = "drop_final_response"
	FaultFailBootstrap        Fault = "fail_step_bootstrap"
	FaultFailDeleteDisk       Fault = "fail_delete_disk"
	FaultFailDeleteVMAttacker Fault = "fail_delete_vm_attacker"
	FaultPluginProcessExit    Fault = "plugin_process_exit"
)

type InvocationPhase string

const (
	PhaseBeforeProviderRequest InvocationPhase = "provider_request_not_sent"
	PhaseSideEffectCommitted   InvocationPhase = "provider_request_committed"
)

type Scenario struct {
	Fault                   Fault
	ResourceGeneration      uint64
	FencingToken            uint64
	CollisionDisplayName    string
	CollisionOwnershipToken string
	PhysicalSteps           []string
	CreatedPrimitives       []Primitive
	PriorLogicalResources   []string
	ReadableStateMajors     []uint32
	WritableStateMajor      uint32
	CurrentCapabilityDigest string
	InvocationPhase         InvocationPhase
	InFlightInvocationID    string
	VirtualNow              time.Time
}

type CallContext struct {
	OperationID       string
	InvocationID      string
	ResourceID        string
	IdempotencyKey    string
	DesiredGeneration uint64
	FencingToken      uint64
}

type Primitive struct {
	ID             string
	Kind           string
	DisplayName    string
	OwnershipToken string
}

type ProviderState struct {
	SchemaMajor   uint32
	PluginID      string
	PluginVersion string
	ResourceID    string
	Generation    uint64
	Ownership     []Primitive
	Opaque        []byte
	Digest        string
}

type StructuredError struct {
	Code                 string
	Class                string
	Message              string
	Retryable            bool
	RevalidationRequired bool
}

func (e *StructuredError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

type Progress struct {
	Sequence uint64
	Phase    string
	Message  string
	At       time.Time
}

type ApplyRequest struct {
	Context                 CallContext
	DisplayName             string
	PriorState              *ProviderState
	PlanCapabilityDigest    string
	CurrentCapabilityDigest string
	TargetCredential        []byte
	GuestSecret             []byte
}

type MutationResult struct {
	State                 *ProviderState
	Error                 *StructuredError
	ResourceState         string
	OperationState        string
	InstancePhase         string
	CompensationAttempted bool
	CompensationComplete  bool
	CompensationOrder     []string
	Leftovers             []string
	Progress              []Progress
}

type InvocationResult struct {
	Known    bool
	Complete bool
	Result   MutationResult
}

type CancelOutcome string

const (
	CancelRequested CancelOutcome = "requested"
	CancelTooLate   CancelOutcome = "too_late"
)

type CancelResult struct {
	Outcome        CancelOutcome
	OperationState string
	QueryRequired  bool
}

type MigrationResult struct {
	Possible        bool
	SourceUnchanged bool
	State           *ProviderState
	Error           *StructuredError
}

type PlanRequest struct {
	CanonicalDigest  string
	CapabilityDigest string
	PolicyDigest     string
}

type PlanResult struct {
	Bytes  []byte
	Digest string
}

type GraphNode struct {
	ID           string
	Dependencies []string
	Ownership    Primitive
}

type DestroySummary struct {
	Attempted        []string
	Leftovers        []string
	OperationState   string
	InstancePhase    string
	DestroyedClaimed bool
}

type Snapshot struct {
	CreateCount        int
	DeleteCount        int
	MutationCount      int
	DuplicateMutations int
	AttemptedDeletes   []string
	LogicalResources   []string
	Progress           []Progress
	SafeSinks          [][]byte
	AuditAlerts        []string
}

// Harness is implemented by provider test adapters. Production providers can
// run the same suite through an RPC-backed harness without importing core
// internals or exposing their SDK models to LabCentral.
type Harness interface {
	Apply(ApplyRequest) (MutationResult, error)
	GetInvocation(string) InvocationResult
	Destroy(CallContext, ProviderState, string) MutationResult
	DestroyGraph([]GraphNode) DestroySummary
	MigrateState(ProviderState, uint32, bool) MigrationResult
	ValidatePlan(string, string) *StructuredError
	Cancel(string) CancelResult
	Plan(PlanRequest) PlanResult
	AdvanceClock(time.Duration)
	Restart() Harness
	Snapshot() Snapshot
}

type Factory func(Scenario) Harness
