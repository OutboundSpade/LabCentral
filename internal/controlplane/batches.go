package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
)

type batchPlanState struct {
	request    CreateInstanceRequest
	exclusions map[domain.LogicalResourceID][]application.AllocationKey
	plans      map[domain.LogicalResourceID]plannedResource
}

func (s *Service) CreateInstanceBatchRevisionTargetForLifetime(
	ctx context.Context,
	subjectID domain.UserID,
	projectID domain.ProjectID,
	owners []InstanceOwner,
	revisionID domain.RevisionID,
	source []byte,
	lifetime application.InstanceLifetimeRequest,
	acknowledged bool,
	idempotencyKey string,
	targetID domain.ProviderTargetID,
	providerOverride []byte,
) (InstanceBatchRecord, error) {
	if _, err := domain.NewUserID(string(subjectID)); err != nil {
		return InstanceBatchRecord{}, err
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return InstanceBatchRecord{}, err
	}
	if _, err := domain.NewRevisionID(string(revisionID)); err != nil {
		return InstanceBatchRecord{}, err
	}
	if len(owners) < 1 || len(owners) > MaximumInstanceBatchSize {
		return InstanceBatchRecord{}, fmt.Errorf("instance batch must contain 1-%d owners", MaximumInstanceBatchSize)
	}
	seenOwners := make(map[string]bool, len(owners))
	for _, owner := range owners {
		if owner.Kind != application.OwnerUser && owner.Kind != application.OwnerTeam || !validOwner(owner.Kind, owner.ID) {
			return InstanceBatchRecord{}, fmt.Errorf("instance batch owner is invalid")
		}
		key := string(owner.Kind) + "\x00" + owner.ID
		if seenOwners[key] {
			return InstanceBatchRecord{}, fmt.Errorf("instance batch contains a duplicate owner")
		}
		seenOwners[key] = true
	}
	if err := application.ValidateInstanceLifetimeRequest(lifetime); err != nil {
		return InstanceBatchRecord{}, err
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return InstanceBatchRecord{}, err
	}

	validation, err := s.ValidateTarget(ctx, source, acknowledged, targetID, providerOverride)
	if err != nil {
		return InstanceBatchRecord{}, err
	}
	if validation.ProviderKind == "proxmox" {
		_ = s.RefreshTargetAs(ctx, subjectID, targetID)
		if err := s.requireNativeCapability(ctx, targetID, "workload.vm"); err != nil {
			return InstanceBatchRecord{}, err
		}
	}
	quotaAmounts, err := quotaAmountsForCanonical(validation.Canonical)
	if err != nil {
		return InstanceBatchRecord{}, err
	}
	resourceIDs := make([]domain.LogicalResourceID, 0, len(validation.Canonical.Resources))
	for _, resource := range validation.Canonical.Resources {
		resourceIDs = append(resourceIDs, resource.ID())
	}
	batchValue, err := s.ids.New("batch")
	if err != nil {
		return InstanceBatchRecord{}, err
	}
	batchID := domain.InstanceBatchID(batchValue)
	ownerDigest, err := json.Marshal(owners)
	if err != nil {
		return InstanceBatchRecord{}, err
	}
	lifetimeDigest := []byte(fmt.Sprintf("%d:%t", lifetime.TTLSeconds, lifetime.Persistent))
	requestDigest := digestBytes("labcentral.http.create-instance-batch/v1", []byte(projectID), []byte(revisionID),
		ownerDigest, lifetimeDigest, []byte(fmt.Sprintf("%t", acknowledged)), []byte(targetID), providerOverride)
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return InstanceBatchRecord{}, err
	}

	states := make([]batchPlanState, 0, len(owners))
	for index, owner := range owners {
		instanceValue, err := s.ids.New("inst")
		if err != nil {
			return InstanceBatchRecord{}, err
		}
		operationValue, err := s.ids.New("op")
		if err != nil {
			return InstanceBatchRecord{}, err
		}
		reservationValue, err := s.ids.New("qres")
		if err != nil {
			return InstanceBatchRecord{}, err
		}
		auditID, err := s.ids.New("audit")
		if err != nil {
			return InstanceBatchRecord{}, err
		}
		eventID, err := s.ids.New("event")
		if err != nil {
			return InstanceBatchRecord{}, err
		}
		instanceID := domain.InstanceID(instanceValue)
		operationID := domain.OperationID(operationValue)
		payload, _ := json.Marshal(jobPayload{Version: jobPayloadVersion, Kind: OperationApply, InstanceID: instanceID})
		childDigest := digestBytes("labcentral.internal.instance-batch-item/v1", []byte(requestDigest.Hex()), []byte(instanceID))
		states = append(states, batchPlanState{
			request: CreateInstanceRequest{
				InstanceID: instanceID, RevisionID: revisionID, ProjectID: projectID,
				OwnerKind: owner.Kind, OwnerID: owner.ID, CreatedBy: subjectID,
				ProviderTargetID: targetID, ProviderOverrideJSON: validation.EffectiveOverride,
				OperationID: operationID, CanonicalDigest: validation.Canonical.Digest,
				CanonicalJSON: validation.Canonical.CanonicalBytes, SourceYAML: source,
				Acknowledged: acknowledged, SubjectID: subjectID,
				IdempotencyKey: fmt.Sprintf("%s.%d", batchID, index), RequestDigest: childDigest, Payload: payload,
				AuditID: auditID, EventID: eventID,
				Quota: application.InstanceQuotaRequest{
					ReservationID: domain.QuotaReservationID(reservationValue), Amounts: quotaAmounts, Lifetime: lifetime,
				},
			},
			exclusions: make(map[domain.LogicalResourceID][]application.AllocationKey),
		})
	}

	for attempt := 0; attempt < application.MaximumAllocationPlanAttempts; attempt++ {
		requests := make([]CreateInstanceRequest, 0, len(states))
		priorAllocations := make([]application.AllocationKey, 0)
		for index := range states {
			state := &states[index]
			effectiveExclusions := mergeBatchExclusions(state.exclusions, resourceIDs, priorAllocations)
			_, providerPlans, err := s.planCanonical(ctx, validation, state.request.InstanceID, effectiveExclusions)
			if err != nil {
				return InstanceBatchRecord{}, err
			}
			resources := make([]ResourceSeed, 0, len(validation.Canonical.Resources))
			for ordinal, resource := range validation.Canonical.Resources {
				canonicalResource, err := render.CanonicalResource(resource)
				if err != nil {
					return InstanceBatchRecord{}, err
				}
				planned := providerPlans[resource.ID()]
				resources = append(resources, ResourceSeed{
					ID: resource.ID(), Kind: resource.Kind(), Ordinal: ordinal, CanonicalJSON: canonicalResource,
					ProviderPlan: planned.plan, ProviderPlanDigest: planned.planDigest, Allocations: planned.allocations,
					OwnershipToken: ownershipToken(targetID, state.request.InstanceID, resource.ID()),
				})
			}
			state.plans = providerPlans
			state.request.Resources = resources
			requests = append(requests, state.request)
			for _, planned := range providerPlans {
				for _, allocation := range planned.allocations {
					priorAllocations = appendUniqueClaim(priorAllocations, allocation)
				}
			}
		}
		record, createErr := s.store.CreateInstanceBatch(ctx, CreateInstanceBatchRequest{
			GovernanceRecordIDs: recordIDs, ID: batchID, RevisionID: revisionID, ProjectID: projectID,
			CreatedBy: subjectID, TargetID: targetID, IdempotencyKey: idempotencyKey,
			RequestDigest: requestDigest, Instances: requests,
		})
		if createErr == nil {
			return record, nil
		}
		var conflict *application.AllocationConflictError
		if !errors.As(createErr, &conflict) || len(conflict.Conflicts) == 0 {
			return InstanceBatchRecord{}, createErr
		}
		matched := false
		for index := range states {
			if mergePlanConflicts(states[index].exclusions, states[index].plans, conflict.Conflicts) {
				matched = true
			}
		}
		if !matched {
			return InstanceBatchRecord{}, createErr
		}
	}
	return InstanceBatchRecord{}, fmt.Errorf("allocation planning exhausted %d conflict attempts", application.MaximumAllocationPlanAttempts)
}

func mergeBatchExclusions(persistent map[domain.LogicalResourceID][]application.AllocationKey, resourceIDs []domain.LogicalResourceID, priorItems []application.AllocationKey) map[domain.LogicalResourceID][]application.AllocationKey {
	merged := make(map[domain.LogicalResourceID][]application.AllocationKey, len(persistent)+len(resourceIDs))
	for resourceID, claims := range persistent {
		merged[resourceID] = append([]application.AllocationKey(nil), claims...)
	}
	for _, resourceID := range resourceIDs {
		for _, claim := range priorItems {
			merged[resourceID] = appendUniqueClaim(merged[resourceID], claim)
		}
	}
	return merged
}

func (s *Service) GetInstanceBatch(ctx context.Context, id domain.InstanceBatchID) (InstanceBatchRecord, error) {
	if _, err := domain.NewInstanceBatchID(string(id)); err != nil {
		return InstanceBatchRecord{}, err
	}
	return s.store.GetInstanceBatch(ctx, id)
}
