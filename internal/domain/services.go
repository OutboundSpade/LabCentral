package domain

import "context"

type RevisionContent struct {
	Revision       TemplateRevision
	VerifiedBundle []byte
}

type InputKind string

const (
	InputString     InputKind = "string"
	InputInteger    InputKind = "integer"
	InputNumber     InputKind = "number"
	InputBoolean    InputKind = "boolean"
	InputStringList InputKind = "stringList"
	InputCIDR       InputKind = "cidr"
	InputIP         InputKind = "ip"
	InputDuration   InputKind = "duration"
	InputSecretRef  InputKind = "secretRef"
)

type TypedInput struct {
	Name       Name
	Kind       InputKind
	String     string
	Integer    int64
	Number     float64
	Boolean    bool
	StringList []string
	SecretRef  *SecretRef
}

type TypedInputs []TypedInput

type RenderPolicy struct {
	MaxBundleBytes    uint64
	MaxResources      uint32
	MaxCanonicalBytes uint64
	MaxExpressionCost uint64
}

type CanonicalLab struct {
	APIVersion      string
	RendererVersion string
	CanonicalBytes  []byte
	Digest          SHA256Digest
	Resources       []LogicalResource
}

type ReportIssue struct {
	Code       string
	Message    string
	ResourceID LogicalResourceID
	FieldPath  string
}

type RenderReport struct {
	Warnings []ReportIssue
}

type ResourceGraph struct {
	Resources    []LogicalResource
	ApplyOrder   []LogicalResourceID
	DestroyOrder []LogicalResourceID
}

type Policy struct {
	Digest            SHA256Digest
	RequiredIsolation IsolationClass
	RequiredEgress    EgressMode
}

type Capability struct {
	ID      CapabilityID
	Version string
	Enabled bool
}

type CapabilitySnapshot struct {
	TargetID     ProviderTargetID
	ETag         string
	Capabilities []Capability
}

type MatchReport struct {
	Eligible bool
	Missing  []CapabilityID
	Warnings []ReportIssue
}

type SchedulingRequest struct {
	InstanceID InstanceID
	Graph      ResourceGraph
	Policy     Policy
}

type EligibleTarget struct {
	Target ProviderTarget
	Match  MatchReport
}

type SchedulingDecision struct {
	TargetID ProviderTargetID
	Reason   string
}

type AuthorizationRequest struct {
	SubjectID UserID
	Action    string
	ScopeID   string
	ObjectID  string
}

type AuthorizationDecision struct {
	Allowed bool
	Reason  string
}

type AggregateState interface {
	AggregateIdentity() string
}

type DomainCommand interface {
	CommandType() string
}

type Observations struct {
	Resources []ResourceObservation
}

type LabRenderer interface {
	Render(context.Context, RevisionContent, TypedInputs, RenderPolicy) (CanonicalLab, RenderReport, error)
}

type GraphBuilder interface {
	Build(CanonicalLab) (ResourceGraph, error)
}

type CapabilityMatcher interface {
	Match(ResourceGraph, Policy, CapabilitySnapshot) MatchReport
}

type Scheduler interface {
	Select(SchedulingRequest, []EligibleTarget) (SchedulingDecision, error)
}

type Authorizer interface {
	Check(AuthorizationRequest) AuthorizationDecision
}

type StateReducer interface {
	Reduce(AggregateState, DomainCommand, Observations) (AggregateState, []DomainEvent, error)
}
