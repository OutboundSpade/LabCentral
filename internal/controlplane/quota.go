package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const SystemExpirationActorID = application.SystemExpirationActorID

type ExpirationSweepResult struct {
	ReservationsExpired uint32
	WarningsEmitted     uint32
	DestroysEnqueued    uint32
}

func quotaAmountsForCanonical(lab domain.CanonicalLab) ([]application.QuotaAmount, error) {
	amounts := []application.QuotaAmount{{Dimension: application.QuotaInstances, Value: 1}}
	add := func(dimension application.QuotaDimension, value uint64) error {
		for index := range amounts {
			if amounts[index].Dimension != dimension {
				continue
			}
			if amounts[index].Value > application.MaxQuotaValue-value {
				return fmt.Errorf("%s quota amount exceeds the supported range", dimension)
			}
			amounts[index].Value += value
			return nil
		}
		amounts = append(amounts, application.QuotaAmount{Dimension: dimension, Value: value})
		return nil
	}
	for _, resource := range lab.Resources {
		switch typed := resource.(type) {
		case domain.VM:
			if err := add(application.QuotaVMs, 1); err != nil {
				return nil, err
			}
			if err := add(application.QuotaCPUMillis, uint64(typed.Spec.Compute.CPUMillis)); err != nil {
				return nil, err
			}
			if err := add(application.QuotaMemoryBytes, uint64(typed.Spec.Compute.MemoryBytes)); err != nil {
				return nil, err
			}
		case domain.Container:
			if typed.Spec.Isolation == domain.IsolationVirtualMachine && len(typed.Spec.RealizationPreference) == 1 && typed.Spec.RealizationPreference[0] == domain.ContainerViaVM {
				if err := add(application.QuotaVMs, 1); err != nil {
					return nil, err
				}
			}
			if err := add(application.QuotaContainers, 1); err != nil {
				return nil, err
			}
			if err := add(application.QuotaCPUMillis, uint64(typed.Spec.Compute.CPUMillis)); err != nil {
				return nil, err
			}
			if err := add(application.QuotaMemoryBytes, uint64(typed.Spec.Compute.MemoryBytes)); err != nil {
				return nil, err
			}
			publicAddress := false
			for _, port := range typed.Spec.PublishedPorts {
				if err := add(application.QuotaPublishedPorts, 1); err != nil {
					return nil, err
				}
				if port.Exposure == domain.ExposurePublic {
					publicAddress = true
				}
			}
			if publicAddress {
				if err := add(application.QuotaPublicIPs, 1); err != nil {
					return nil, err
				}
			}
		case domain.Volume:
			if err := add(application.QuotaStorageBytes, uint64(typed.Spec.SizeBytes)); err != nil {
				return nil, err
			}
		}
	}
	return application.NormalizeQuotaAmounts(amounts)
}

func (s *Service) PutQuotaLimit(ctx context.Context, actor domain.UserID, scope application.QuotaScope, dimension application.QuotaDimension, limit uint64, expected domain.ResourceVersion, idempotencyKey string) (application.QuotaLimitRecord, error) {
	if s == nil || s.store == nil || s.ids == nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("quota service is unavailable")
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := application.ValidateQuotaScope(scope); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := application.ValidateQuotaDimension(dimension); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if limit > application.MaxQuotaValue {
		return application.QuotaLimitRecord{}, fmt.Errorf("quota limit exceeds the supported range")
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return application.QuotaLimitRecord{}, err
	}
	return s.store.PutQuotaLimit(ctx, PutQuotaLimitRequest{
		GovernanceRecordIDs: ids, Scope: scope, Dimension: dimension, Limit: limit,
		ExpectedResourceVersion: expected, UpdatedBy: actor, IdempotencyKey: idempotencyKey,
		RequestDigest: digestBytes("labcentral.http.put-quota-limit/v1", []byte(scope.Kind), []byte(scope.ID),
			[]byte(scope.OrganizationID), []byte(dimension), []byte(fmt.Sprint(limit)), []byte(fmt.Sprint(expected))),
	})
}

func (s *Service) GetQuotaLimit(ctx context.Context, scope application.QuotaScope, dimension application.QuotaDimension) (application.QuotaLimitRecord, error) {
	if s == nil || s.store == nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("quota service is unavailable")
	}
	return s.store.GetQuotaLimit(ctx, scope, dimension)
}

func (s *Service) ListQuotaUsage(ctx context.Context, scope application.QuotaScope) ([]application.QuotaUsageRecord, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("quota service is unavailable")
	}
	return s.store.ListQuotaUsage(ctx, scope)
}

func (s *Service) PutLifetimePolicy(ctx context.Context, actor domain.UserID, policy application.LifetimePolicyRecord, expected domain.ResourceVersion, idempotencyKey string) (application.LifetimePolicyRecord, error) {
	if s == nil || s.store == nil || s.ids == nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("lifetime policy service is unavailable")
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if err := application.ValidateLifetimePolicy(policy); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	encodedPolicy, err := json.Marshal(struct {
		Scope                application.QuotaScope `json:"scope"`
		DefaultTTLSeconds    uint64                 `json:"defaultTtlSeconds"`
		MaximumTTLSeconds    uint64                 `json:"maximumTtlSeconds"`
		RenewalWindowSeconds uint64                 `json:"renewalWindowSeconds"`
		WarningBeforeSeconds uint64                 `json:"warningBeforeSeconds"`
		DestructionGraceSecs uint64                 `json:"destructionGraceSeconds"`
		RenewalAllowed       bool                   `json:"renewalAllowed"`
		PersistentAllowed    bool                   `json:"persistentAllowed"`
	}{policy.Scope, policy.DefaultTTLSeconds, policy.MaximumTTLSeconds, policy.RenewalWindowSeconds,
		policy.WarningBeforeSeconds, policy.DestructionGraceSecs, policy.RenewalAllowed, policy.PersistentAllowed})
	if err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	return s.store.PutLifetimePolicy(ctx, PutLifetimePolicyRequest{
		GovernanceRecordIDs: ids, Policy: policy, ExpectedResourceVersion: expected, UpdatedBy: actor,
		IdempotencyKey: idempotencyKey,
		RequestDigest:  digestBytes("labcentral.http.put-lifetime-policy/v1", encodedPolicy, []byte(fmt.Sprint(expected))),
	})
}

func (s *Service) GetLifetimePolicy(ctx context.Context, scope application.QuotaScope) (application.LifetimePolicyRecord, error) {
	if s == nil || s.store == nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("lifetime policy service is unavailable")
	}
	return s.store.GetLifetimePolicy(ctx, scope)
}

func (s *Service) RenewInstanceLifetime(ctx context.Context, actor domain.UserID, instanceID domain.InstanceID, ttlSeconds uint64, expected domain.ResourceVersion, idempotencyKey string) (InstanceRecord, error) {
	if s == nil || s.store == nil || s.ids == nil {
		return InstanceRecord{}, fmt.Errorf("lifetime renewal service is unavailable")
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return InstanceRecord{}, err
	}
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return InstanceRecord{}, err
	}
	if ttlSeconds == 0 || ttlSeconds > application.MaxInstanceTTLSeconds || expected == 0 {
		return InstanceRecord{}, fmt.Errorf("renewal TTL and expected resource version are required")
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return InstanceRecord{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return InstanceRecord{}, err
	}
	return s.store.RenewInstanceLifetime(ctx, RenewInstanceLifetimeRequest{
		GovernanceRecordIDs: ids, InstanceID: instanceID, TTLSeconds: ttlSeconds,
		ExpectedResourceVersion: expected, ActorID: actor, IdempotencyKey: idempotencyKey,
		RequestDigest: digestBytes("labcentral.http.renew-instance-ttl/v1", []byte(instanceID),
			[]byte(fmt.Sprint(ttlSeconds)), []byte(fmt.Sprint(expected))),
	})
}

func (s *Service) RunExpirationSweep(ctx context.Context, limit uint32) (ExpirationSweepResult, error) {
	if s == nil || s.store == nil || s.ids == nil {
		return ExpirationSweepResult{}, fmt.Errorf("expiration sweep service is unavailable")
	}
	if limit == 0 || limit > 1000 {
		return ExpirationSweepResult{}, fmt.Errorf("expiration sweep limit must be 1-1000")
	}
	result := ExpirationSweepResult{}
	reservationsExpired, err := s.store.ExpireStaleQuotaReservations(ctx, limit)
	if err != nil {
		return result, err
	}
	result.ReservationsExpired = reservationsExpired
	warnings, err := s.store.ListExpirationWarnings(ctx, limit)
	if err != nil {
		return result, err
	}
	for _, candidate := range warnings {
		ids, err := s.newGovernanceRecordIDs()
		if err != nil {
			return result, err
		}
		emitted, err := s.store.MarkExpirationWarning(ctx, MarkExpirationWarningRequest{GovernanceRecordIDs: ids, Candidate: candidate})
		if err != nil {
			return result, err
		}
		if emitted {
			result.WarningsEmitted++
		}
	}
	expired, err := s.store.ListExpiredInstances(ctx, limit)
	if err != nil {
		return result, err
	}
	for _, candidate := range expired {
		if _, err := s.destroyExpiredInstance(ctx, candidate); err != nil {
			if errorsIsExpirationRace(err) {
				continue
			}
			return result, err
		}
		result.DestroysEnqueued++
	}
	return result, nil
}

func (s *Service) destroyExpiredInstance(ctx context.Context, candidate ExpirationCandidate) (EnqueueResult, error) {
	operationID, err := s.ids.New("op")
	if err != nil {
		return EnqueueResult{}, err
	}
	payload, _ := json.Marshal(jobPayload{Version: jobPayloadVersion, Kind: OperationDestroy, InstanceID: candidate.InstanceID})
	auditID, err := s.ids.New("audit")
	if err != nil {
		return EnqueueResult{}, err
	}
	eventID, err := s.ids.New("event")
	if err != nil {
		return EnqueueResult{}, err
	}
	keyDigest := digestBytes("labcentral.expiration-destroy-key/v1", []byte(candidate.InstanceID), []byte(candidate.ExpiresAt.UTC().Format(time.RFC3339Nano)))
	return s.store.QueueDestroy(ctx, QueueDestroyRequest{
		InstanceID: candidate.InstanceID, OperationID: domain.OperationID(operationID), SubjectID: SystemExpirationActorID,
		IdempotencyKey: "expire-" + keyDigest.Hex(),
		RequestDigest: digestBytes("labcentral.expiration-destroy/v1", []byte(candidate.InstanceID),
			[]byte(candidate.ExpiresAt.UTC().Format(time.RFC3339Nano))),
		Payload: payload, AuditID: auditID, EventID: eventID, ExpirationDueAt: candidate.ExpiresAt,
	})
}

func errorsIsExpirationRace(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrVersionConflict)
}
