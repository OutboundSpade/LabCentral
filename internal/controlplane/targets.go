package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const maximumTargetEnvelopeBytes = 8 << 20

var providerKindPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

type TargetPolicy struct {
	MaxConcurrentDeployments uint32   `json:"maxConcurrentDeployments,omitempty"`
	TrustedLaptop            bool     `json:"trustedLaptop,omitempty"`
	MinimumMinor             uint32   `json:"minimumMinor,omitempty"`
	RequiredFeatures         []string `json:"requiredFeatures,omitempty"`
	NativeCapabilities       []string `json:"nativeCapabilities,omitempty"`
}

type TargetBinding struct {
	Policy             TargetPolicy
	ID                 domain.ProviderTargetID
	ProviderKind       string
	Provider           application.Provider
	ConfigEnvelope     []byte
	CredentialRef      *domain.SecretRef
	CredentialResolver application.SecretResolver
	ProtocolMajor      uint32
	ProtocolMinor      uint32
	Features           []string
}

type TargetRegistry struct {
	mu       sync.RWMutex
	bindings map[domain.ProviderTargetID]targetBinding
}

type RegisteredTarget struct {
	ID                domain.ProviderTargetID
	ProviderKind      string
	ConfigFingerprint string
	ProtocolMajor     uint32
	ProtocolMinor     uint32
	Features          []string
}

type targetBinding struct {
	policy             TargetPolicy
	id                 domain.ProviderTargetID
	providerKind       string
	provider           application.Provider
	configEnvelope     []byte
	configFingerprint  string
	credentialRef      *domain.SecretRef
	credentialResolver application.SecretResolver
	protocolMajor      uint32
	protocolMinor      uint32
	features           map[string]bool
}

type TargetSession struct {
	Policy        TargetPolicy
	ID            domain.ProviderTargetID
	ProviderKind  string
	Provider      application.Provider
	Context       application.ProviderTargetContext
	ProtocolMajor uint32
	ProtocolMinor uint32

	features map[string]bool
	secret   *application.EphemeralSecret
	once     sync.Once
}

func NewTargetRegistry(bindings []TargetBinding) (*TargetRegistry, error) {
	if len(bindings) > 64 {
		return nil, fmt.Errorf("target registry must contain at most 64 targets")
	}
	registry := &TargetRegistry{bindings: make(map[domain.ProviderTargetID]targetBinding, len(bindings))}
	for _, binding := range bindings {
		if _, err := domain.NewProviderTargetID(string(binding.ID)); err != nil {
			return nil, err
		}
		if !providerKindPattern.MatchString(binding.ProviderKind) || binding.Provider == nil {
			return nil, fmt.Errorf("target %s provider kind or client is invalid", binding.ID)
		}
		if len(binding.ConfigEnvelope) > maximumTargetEnvelopeBytes {
			return nil, fmt.Errorf("target %s configuration exceeds %d bytes", binding.ID, maximumTargetEnvelopeBytes)
		}
		if binding.ProtocolMajor != 1 || binding.ProtocolMinor > 1 {
			return nil, fmt.Errorf("target %s selected protocol is unsupported", binding.ID)
		}
		if (binding.CredentialRef == nil) != (binding.CredentialResolver == nil) {
			return nil, fmt.Errorf("target %s credential reference and resolver must be configured together", binding.ID)
		}
		if binding.CredentialRef != nil {
			if err := binding.CredentialRef.Validate(); err != nil {
				return nil, fmt.Errorf("target %s credential reference: %w", binding.ID, err)
			}
		}
		if _, duplicate := registry.bindings[binding.ID]; duplicate {
			return nil, fmt.Errorf("target %s is registered more than once", binding.ID)
		}
		if err := validateTargetPolicy(binding.Policy); err != nil {
			return nil, err
		}
		if len(binding.Features) > 128 {
			return nil, fmt.Errorf("too many target features")
		}
		features := make(map[string]bool, len(binding.Features))
		for _, feature := range binding.Features {
			if !providerKindPattern.MatchString(feature) || features[feature] {
				return nil, fmt.Errorf("target %s feature set is invalid", binding.ID)
			}
			features[feature] = true
		}
		digest := sha256.Sum256(binding.ConfigEnvelope)
		stored := targetBinding{
			policy: cloneTargetPolicy(binding.Policy),
			id:     binding.ID, providerKind: binding.ProviderKind, provider: binding.Provider,
			configEnvelope: append([]byte(nil), binding.ConfigEnvelope...), configFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
			credentialResolver: binding.CredentialResolver, protocolMajor: binding.ProtocolMajor, protocolMinor: binding.ProtocolMinor,
			features: features,
		}
		if binding.CredentialRef != nil {
			copy := *binding.CredentialRef
			stored.credentialRef = &copy
		}
		registry.bindings[binding.ID] = stored
	}
	return registry, nil
}

func NewDockerTargetRegistry(provider application.Provider) (*TargetRegistry, error) {
	return NewTargetRegistry([]TargetBinding{{
		ID: LocalDockerTarget, Policy: TargetPolicy{TrustedLaptop: true}, ProviderKind: "docker", Provider: provider, ProtocolMajor: 1,
	}})
}

func (registry *TargetRegistry) Acquire(ctx context.Context, targetID domain.ProviderTargetID, scope application.InvocationScope, includeCredential bool) (*TargetSession, error) {
	return registry.AcquireForSubject(ctx, targetID, LocalAdminID, scope, includeCredential)
}

func (registry *TargetRegistry) AcquireForSubject(ctx context.Context, targetID domain.ProviderTargetID, subjectID domain.UserID, scope application.InvocationScope, includeCredential bool) (*TargetSession, error) {
	if registry == nil {
		return nil, fmt.Errorf("target registry is unavailable")
	}
	registry.mu.RLock()
	binding, exists := registry.bindings[targetID]
	registry.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("provider target %s is not registered", targetID)
	}
	if live, ok := binding.provider.(interface{ Available() bool }); ok && !live.Available() {
		return nil, fmt.Errorf("provider target unavailable")
	}
	session := &TargetSession{
		Policy: cloneTargetPolicy(binding.policy),
		ID:     binding.id, ProviderKind: binding.providerKind, Provider: binding.provider,
		Context: application.ProviderTargetContext{
			ID: string(binding.id), Config: append([]byte(nil), binding.configEnvelope...), ConfigFingerprint: binding.configFingerprint,
		},
		ProtocolMajor: binding.protocolMajor, ProtocolMinor: binding.protocolMinor, features: binding.features,
	}
	if !includeCredential {
		return session, nil
	}
	if _, err := domain.NewUserID(string(subjectID)); err != nil {
		return nil, fmt.Errorf("provider target credential subject is invalid")
	}
	if binding.credentialResolver == nil || binding.credentialRef == nil {
		return session, nil
	}
	if scope.InvocationID == "" || scope.InstanceID == "" || scope.ResourceID == "" || scope.ExpiresAt.IsZero() || !scope.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("provider target %s credential scope is invalid", targetID)
	}
	secret, err := binding.credentialResolver.Resolve(ctx, application.AuthorizedSecretRef{Reference: *binding.credentialRef, SubjectID: subjectID}, scope)
	if err != nil {
		return nil, fmt.Errorf("resolve provider target %s credential", targetID)
	}
	credential := secret.Bytes()
	if len(credential) == 0 || len(credential) > maximumTargetEnvelopeBytes {
		zeroBytes(credential)
		secret.Destroy()
		return nil, fmt.Errorf("provider target %s credential envelope is invalid", targetID)
	}
	session.secret = secret
	session.Context.Credential = credential
	return session, nil
}

func (registry *TargetRegistry) IDs() []domain.ProviderTargetID {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]domain.ProviderTargetID, 0, len(registry.bindings))
	for id := range registry.bindings {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (registry *TargetRegistry) ProviderKinds() []string {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	seen := make(map[string]bool, len(registry.bindings))
	for _, binding := range registry.bindings {
		seen[binding.providerKind] = true
	}
	result := make([]string, 0, len(seen))
	for kind := range seen {
		result = append(result, kind)
	}
	sort.Strings(result)
	return result
}

func (registry *TargetRegistry) RegisteredTargets() []RegisteredTarget {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]RegisteredTarget, 0, len(registry.bindings))
	for _, binding := range registry.bindings {
		features := make([]string, 0, len(binding.features))
		for feature := range binding.features {
			features = append(features, feature)
		}
		sort.Strings(features)
		result = append(result, RegisteredTarget{
			ID: binding.id, ProviderKind: binding.providerKind, ConfigFingerprint: binding.configFingerprint,
			ProtocolMajor: binding.protocolMajor, ProtocolMinor: binding.protocolMinor, Features: features,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (session *TargetSession) Supports(minor uint32, features ...string) bool {
	if session == nil || session.ProtocolMajor != 1 || session.ProtocolMinor < minor {
		return false
	}
	for _, feature := range features {
		if !session.features[feature] {
			return false
		}
	}
	return true
}

func (session *TargetSession) Close() {
	if session == nil {
		return
	}
	session.once.Do(func() {
		zeroBytes(session.Context.Credential)
		session.Context.Credential = nil
		if session.secret != nil {
			session.secret.Destroy()
		}
	})
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (registry *TargetRegistry) AvailableIDs() []domain.ProviderTargetID {
	ids := registry.IDs()
	result := make([]domain.ProviderTargetID, 0, len(ids))
	for _, id := range ids {
		registry.mu.RLock()
		p := registry.bindings[id].provider
		registry.mu.RUnlock()
		if live, ok := p.(interface{ Available() bool }); ok && !live.Available() {
			continue
		}
		result = append(result, id)
	}
	return result
}

func cloneTargetPolicy(p TargetPolicy) TargetPolicy {
	p.RequiredFeatures = append([]string(nil), p.RequiredFeatures...)
	p.NativeCapabilities = append([]string(nil), p.NativeCapabilities...)
	return p
}
func validateTargetPolicy(p TargetPolicy) error {
	if p.MaxConcurrentDeployments > 16 {
		return fmt.Errorf("maxConcurrentDeployments must be between 1 and 16")
	}
	if p.MinimumMinor > 1 {
		return fmt.Errorf("unsupported target policy protocol")
	}
	for _, values := range [][]string{p.RequiredFeatures, p.NativeCapabilities} {
		if len(values) > 128 {
			return fmt.Errorf("target policy exceeds limit")
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !providerKindPattern.MatchString(v) || seen[v] {
				return fmt.Errorf("invalid target policy set")
			}
			seen[v] = true
		}
	}
	return nil
}

func (r *TargetRegistry) ExecutionLimits() []application.TargetExecutionLimit {
	ids := r.AvailableIDs()
	r.mu.RLock()
	defer r.mu.RUnlock()
	limits := make([]application.TargetExecutionLimit, 0, len(ids))
	for _, id := range ids {
		n := r.bindings[id].policy.MaxConcurrentDeployments
		if n == 0 {
			n = 1
		}
		limits = append(limits, application.TargetExecutionLimit{TargetID: id, Maximum: n})
	}
	return limits
}

// ExecutionCapacity reserves workers for runtime limit changes and recovered
// targets. Database admission decides how many may actually execute.
func (r *TargetRegistry) ExecutionCapacity() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.bindings) * 16
}
