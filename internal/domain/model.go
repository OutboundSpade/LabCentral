package domain

import "time"

const MaxProviderStateBytes = 256 * 1024

type DesiredLifecycle string

const (
	LifecycleRunning   DesiredLifecycle = "running"
	LifecycleStopped   DesiredLifecycle = "stopped"
	LifecyclePaused    DesiredLifecycle = "paused"
	LifecycleDestroyed DesiredLifecycle = "destroyed"
)

type Condition struct {
	Type               string
	Status             bool
	Reason             string
	Message            string
	ObservedGeneration Generation
	LastTransitionTime time.Time
}

type ResourceObservation struct {
	ObservedGeneration Generation
	Phase              ResourcePhase
	Healthy            bool
	Addresses          []string
	Drifted            bool
	ObservedAt         time.Time
	Conditions         []Condition
}

type ProviderStateEnvelope struct {
	PluginID          string
	PluginVersion     string
	SchemaVersion     uint32
	InstanceID        InstanceID
	LogicalResourceID LogicalResourceID
	OwnershipIDs      []OwnershipID
	MigrationRequired bool
	Opaque            []byte
	Digest            SHA256Digest
}

func (p ProviderStateEnvelope) ValidateFor(instanceID InstanceID, resourceID LogicalResourceID) error {
	if p.PluginID == "" || p.PluginVersion == "" || p.SchemaVersion == 0 {
		return &DomainError{Code: "provider_state_metadata_invalid", Class: ErrorStateVersionIncompatible, Message: "provider state metadata is incomplete"}
	}
	if p.InstanceID != instanceID || p.LogicalResourceID != resourceID {
		return &DomainError{Code: "provider_state_ownership_mismatch", Class: ErrorAuthorization, Message: "provider state ownership does not match the logical resource"}
	}
	if len(p.Opaque) > MaxProviderStateBytes {
		return &DomainError{Code: "provider_state_too_large", Class: ErrorStateVersionIncompatible, Message: "provider state exceeds the size limit"}
	}
	return nil
}

type LabInstance struct {
	ID               InstanceID
	RevisionID       RevisionID
	ProviderTargetID ProviderTargetID
	Generation       Generation
	ResourceVersion  ResourceVersion
	CanonicalDigest  SHA256Digest
	PolicyDigest     SHA256Digest
	DesiredLifecycle DesiredLifecycle
	Phase            InstancePhase
	ExpiresAt        *time.Time
}

type LogicalResourceRecord struct {
	InstanceID      InstanceID
	Resource        LogicalResource
	Generation      Generation
	ResourceVersion ResourceVersion
	Observation     ResourceObservation
	ProviderState   *ProviderStateEnvelope
}

type ProviderTarget struct {
	ID              ProviderTargetID
	ResourceVersion ResourceVersion
	PluginID        string
	CapabilityETag  string
	Healthy         bool
}

type TemplateRevision struct {
	ID               RevisionID
	TemplateID       TemplateID
	ResourceVersion  ResourceVersion
	BundleDigest     SHA256Digest
	AuthorAPIVersion string
	CanonicalVersion string
}

type Operation struct {
	Subject         WorkSubject
	ID              OperationID
	InstanceID      InstanceID
	State           OperationState
	Generation      Generation
	ResourceVersion ResourceVersion
	FencingToken    FencingToken
	LeaseExpiresAt  time.Time
	Attempt         uint32
}

type DomainEvent struct {
	AggregateID string
	Sequence    EventSequence
	Type        string
	OccurredAt  time.Time
	Payload     []byte
}
