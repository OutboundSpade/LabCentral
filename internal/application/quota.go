package application

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type QuotaScopeKind string

const (
	QuotaScopePlatform     QuotaScopeKind = "platform"
	QuotaScopeOrganization QuotaScopeKind = "organization"
	QuotaScopeProject      QuotaScopeKind = "project"
	QuotaScopeTeam         QuotaScopeKind = "team"
	QuotaScopeUser         QuotaScopeKind = "user"
	QuotaScopeTarget       QuotaScopeKind = "target"
)

type QuotaDimension string

const (
	QuotaInstances        QuotaDimension = "instances"
	QuotaVMs              QuotaDimension = "vms"
	QuotaContainers       QuotaDimension = "containers"
	QuotaCPUMillis        QuotaDimension = "cpu_millis"
	QuotaMemoryBytes      QuotaDimension = "memory_bytes"
	QuotaStorageBytes     QuotaDimension = "storage_bytes"
	QuotaPublicIPs        QuotaDimension = "public_ips"
	QuotaPublishedPorts   QuotaDimension = "published_ports"
	QuotaSnapshots        QuotaDimension = "snapshots"
	MaxQuotaValue                        = uint64(math.MaxInt64)
	MaxInstanceTTLSeconds                = uint64((366 * 24 * time.Hour) / time.Second)
)

type QuotaScope struct {
	Kind           QuotaScopeKind        `json:"kind"`
	ID             string                `json:"id"`
	OrganizationID domain.OrganizationID `json:"organizationId,omitempty"`
}

type QuotaAmount struct {
	Dimension QuotaDimension `json:"dimension"`
	Value     uint64         `json:"value"`
}

type QuotaLimitRecord struct {
	Scope           QuotaScope             `json:"scope"`
	Dimension       QuotaDimension         `json:"dimension"`
	Limit           uint64                 `json:"limit"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	UpdatedBy       domain.UserID          `json:"updatedBy"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
}

type QuotaUsageRecord struct {
	Scope     QuotaScope     `json:"scope"`
	Dimension QuotaDimension `json:"dimension"`
	Reserved  uint64         `json:"reserved"`
	Committed uint64         `json:"committed"`
	Retained  uint64         `json:"retained"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

func (u QuotaUsageRecord) Total() uint64 { return u.Reserved + u.Committed + u.Retained }

type LifetimePolicyRecord struct {
	Scope                QuotaScope             `json:"scope"`
	DefaultTTLSeconds    uint64                 `json:"defaultTtlSeconds"`
	MaximumTTLSeconds    uint64                 `json:"maximumTtlSeconds"`
	RenewalWindowSeconds uint64                 `json:"renewalWindowSeconds"`
	WarningBeforeSeconds uint64                 `json:"warningBeforeSeconds"`
	DestructionGraceSecs uint64                 `json:"destructionGraceSeconds"`
	RenewalAllowed       bool                   `json:"renewalAllowed"`
	PersistentAllowed    bool                   `json:"persistentAllowed"`
	ResourceVersion      domain.ResourceVersion `json:"resourceVersion"`
	UpdatedBy            domain.UserID          `json:"updatedBy"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
}

type InstanceLifetimeRequest struct {
	TTLSeconds uint64 `json:"ttlSeconds,omitempty"`
	Persistent bool   `json:"persistent,omitempty"`
}

type ResolvedInstanceLifetime struct {
	ExpiresAt               time.Time
	MaximumExpiresAt        time.Time
	ExpirationWarningAt     time.Time
	DestructionGraceSeconds uint64
	RenewalAllowed          bool
	RenewalWindowSeconds    uint64
	MaximumTTLSeconds       uint64
	PolicyDigest            domain.SHA256Digest
}

type InstanceQuotaRequest struct {
	ReservationID domain.QuotaReservationID
	Amounts       []QuotaAmount
	Lifetime      InstanceLifetimeRequest
}

type QuotaExceededError struct {
	Scope     QuotaScope
	Dimension QuotaDimension
	Limit     uint64
	Current   uint64
	Requested uint64
}

func (e *QuotaExceededError) Error() string {
	if e == nil {
		return "quota would be exceeded"
	}
	return fmt.Sprintf("%s quota for %s %s would be exceeded", e.Dimension, e.Scope.Kind, e.Scope.ID)
}

func (e *QuotaExceededError) Unwrap() error {
	return &domain.DomainError{Code: "quota_exceeded", Class: domain.ErrorQuota, Message: "The requested lab would exceed an applicable quota"}
}

func ValidateQuotaScope(scope QuotaScope) error {
	if scope.ID == "" || len(scope.ID) > 128 || strings.TrimSpace(scope.ID) != scope.ID || strings.ContainsRune(scope.ID, '\x00') {
		return fmt.Errorf("quota scope ID must be 1-128 trimmed bytes without NUL")
	}
	switch scope.Kind {
	case QuotaScopePlatform:
		if scope.ID != "platform" || scope.OrganizationID != "" {
			return fmt.Errorf("platform quota scope must use ID platform without an organization")
		}
	case QuotaScopeOrganization:
		organizationID, err := domain.NewOrganizationID(scope.ID)
		if err != nil || scope.OrganizationID != organizationID {
			return fmt.Errorf("organization quota scope must bind its organization ID")
		}
	case QuotaScopeProject:
		if _, err := domain.NewProjectID(scope.ID); err != nil || scope.OrganizationID == "" {
			return fmt.Errorf("project quota scope must bind a project and organization")
		}
	case QuotaScopeTeam:
		if _, err := domain.NewTeamID(scope.ID); err != nil || scope.OrganizationID == "" {
			return fmt.Errorf("team quota scope must bind a team and organization")
		}
	case QuotaScopeUser:
		if _, err := domain.NewUserID(scope.ID); err != nil || scope.OrganizationID == "" {
			return fmt.Errorf("user quota scope must bind a user and organization")
		}
	case QuotaScopeTarget:
		if _, err := domain.NewProviderTargetID(scope.ID); err != nil || scope.OrganizationID != "" {
			return fmt.Errorf("target quota scope must bind a target without an organization")
		}
	default:
		return fmt.Errorf("unsupported quota scope kind %q", scope.Kind)
	}
	return nil
}

func ValidateQuotaDimension(dimension QuotaDimension) error {
	switch dimension {
	case QuotaInstances, QuotaVMs, QuotaContainers, QuotaCPUMillis, QuotaMemoryBytes,
		QuotaStorageBytes, QuotaPublicIPs, QuotaPublishedPorts, QuotaSnapshots:
		return nil
	default:
		return fmt.Errorf("unsupported quota dimension %q", dimension)
	}
}

func NormalizeQuotaAmounts(amounts []QuotaAmount) ([]QuotaAmount, error) {
	combined := make(map[QuotaDimension]uint64, len(amounts))
	for _, amount := range amounts {
		if err := ValidateQuotaDimension(amount.Dimension); err != nil {
			return nil, err
		}
		if amount.Value == 0 {
			continue
		}
		if amount.Value > MaxQuotaValue || combined[amount.Dimension] > MaxQuotaValue-amount.Value {
			return nil, fmt.Errorf("quota amount for %s exceeds the supported range", amount.Dimension)
		}
		combined[amount.Dimension] += amount.Value
	}
	normalized := make([]QuotaAmount, 0, len(combined))
	for dimension, value := range combined {
		normalized = append(normalized, QuotaAmount{Dimension: dimension, Value: value})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Dimension < normalized[j].Dimension })
	return normalized, nil
}

func ValidateLifetimePolicy(policy LifetimePolicyRecord) error {
	if err := ValidateQuotaScope(policy.Scope); err != nil {
		return err
	}
	if policy.DefaultTTLSeconds == 0 || policy.MaximumTTLSeconds == 0 || policy.DefaultTTLSeconds > policy.MaximumTTLSeconds || policy.MaximumTTLSeconds > MaxInstanceTTLSeconds {
		return fmt.Errorf("lifetime default and maximum must be positive, ordered, and at most %d seconds", MaxInstanceTTLSeconds)
	}
	if policy.RenewalWindowSeconds > policy.MaximumTTLSeconds || policy.WarningBeforeSeconds >= policy.MaximumTTLSeconds || policy.DestructionGraceSecs > 24*60*60 {
		return fmt.Errorf("lifetime renewal, warning, or destruction grace bounds are invalid")
	}
	return nil
}

func ValidateInstanceLifetimeRequest(request InstanceLifetimeRequest) error {
	if request.Persistent && request.TTLSeconds != 0 {
		return fmt.Errorf("persistent lifetime cannot include ttlSeconds")
	}
	if request.TTLSeconds > MaxInstanceTTLSeconds {
		return fmt.Errorf("ttlSeconds must be at most %d", MaxInstanceTTLSeconds)
	}
	return nil
}
