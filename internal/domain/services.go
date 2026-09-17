package domain

import (
	"context"
	"fmt"
)

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
	InputEnum       InputKind = "enum"
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
	Enum       *EnumValue
	SecretRef  *SecretRef
}

// EnumValue preserves whether an enum input is a string or an integer. Exactly
// one member must be set, including when the selected value is the zero value.
type EnumValue struct {
	String  *string
	Integer *int64
}

func (v EnumValue) Value() (any, error) {
	switch {
	case v.String != nil && v.Integer == nil:
		return *v.String, nil
	case v.String == nil && v.Integer != nil:
		return *v.Integer, nil
	default:
		return nil, fmt.Errorf("enum value must set exactly one of string or integer")
	}
}

type TypedInputs []TypedInput

type ResolvedBuildImage struct {
	RecipeID string
	ImageRef ImageRef
}

type RenderPolicy struct {
	BuildImages       []ResolvedBuildImage
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
