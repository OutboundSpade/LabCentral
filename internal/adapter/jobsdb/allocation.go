package jobsdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var (
	ErrAllocationBinding = &domain.DomainError{
		Code:    "allocation_binding_mismatch",
		Class:   domain.ErrorStateVersionIncompatible,
		Message: "allocation claim is not bound to the expected plan and resource",
	}
	ErrAllocationState = &domain.DomainError{
		Code:    "allocation_state_conflict",
		Class:   domain.ErrorStateVersionIncompatible,
		Message: "allocation claim state does not permit this transition",
	}
	ErrAllocationFence = &domain.DomainError{
		Code:    "stale_allocation_fence",
		Class:   domain.ErrorStateVersionIncompatible,
		Message: "allocation claim fencing token is stale",
	}
	ErrProviderAbsenceRequired = &domain.DomainError{
		Code:    "provider_absence_required",
		Class:   domain.ErrorInvalidDeclaration,
		Message: "provider absence must be confirmed before allocation release",
	}
)

func (s *Store) ReserveClaims(ctx context.Context, request application.ReserveAllocationClaimsRequest) error {
	claims, err := validateAllocationRequest(request.Binding, request.Claims)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin allocation reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conflicts := make([]application.AllocationKey, 0)
	for _, key := range claims {
		claim, found, err := s.readAllocationClaim(ctx, tx, key)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if sameAllocationBinding(claim.Binding, request.Binding) && claim.State != application.AllocationReleased {
			continue
		}
		if claim.State != application.AllocationReleased || sameAllocationBinding(claim.Binding, request.Binding) {
			conflicts = append(conflicts, key)
		}
	}
	if len(conflicts) > 0 {
		return &application.AllocationConflictError{Conflicts: conflicts}
	}
	for _, key := range claims {
		claim, found, err := s.readAllocationClaim(ctx, tx, key)
		if err != nil {
			return err
		}
		if found && sameAllocationBinding(claim.Binding, request.Binding) && claim.State != application.AllocationReleased {
			continue
		}
		if found {
			result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims
				SET instance_id = ?, resource_id = ?, plan_digest = ?, state = ?, fencing_token = 0,
				    created_at_us = `+s.nowMicros()+`, updated_at_us = `+s.nowMicros()+`
				WHERE target_id = ? AND namespace = ? AND claim_value = ? AND state = ?`),
				request.Binding.InstanceID, request.Binding.ResourceID, request.Binding.PlanDigest.Hex(), application.AllocationHeld,
				key.TargetID, key.Namespace, key.Value, application.AllocationReleased)
			if err != nil {
				return fmt.Errorf("reuse released allocation claim: %w", err)
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return &application.AllocationConflictError{Conflicts: []application.AllocationKey{key}}
			}
			continue
		}
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_provider_allocation_claims
			(target_id, namespace, claim_value, instance_id, resource_id, plan_digest, state, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`),
			key.TargetID, key.Namespace, key.Value, request.Binding.InstanceID, request.Binding.ResourceID,
			request.Binding.PlanDigest.Hex(), application.AllocationHeld)
		if err != nil {
			if isAllocationUniqueConflict(err) {
				return &application.AllocationConflictError{Conflicts: []application.AllocationKey{key}}
			}
			return fmt.Errorf("insert allocation claim: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit allocation reservation: %w", err)
	}
	return nil
}

func (s *Store) CommitClaims(ctx context.Context, request application.MutateAllocationClaimsRequest) error {
	return s.transitionClaims(ctx, request, application.AllocationCommitted,
		application.AllocationHeld, application.AllocationCommitted)
}

func (s *Store) RetainClaims(ctx context.Context, request application.MutateAllocationClaimsRequest) error {
	return s.transitionClaims(ctx, request, "",
		application.AllocationHeld, application.AllocationCommitted, application.AllocationOrphaned)
}

func (s *Store) OrphanClaims(ctx context.Context, request application.MutateAllocationClaimsRequest) error {
	return s.transitionClaims(ctx, request, application.AllocationOrphaned,
		application.AllocationHeld, application.AllocationCommitted, application.AllocationOrphaned)
}

func (s *Store) ReleaseClaims(ctx context.Context, request application.ReleaseAllocationClaimsRequest) error {
	if !request.ProviderAbsenceConfirmed {
		return ErrProviderAbsenceRequired
	}
	return s.transitionClaims(ctx, request.MutateAllocationClaimsRequest, application.AllocationReleased,
		application.AllocationHeld, application.AllocationCommitted, application.AllocationOrphaned, application.AllocationReleased)
}

func (s *Store) transitionClaims(
	ctx context.Context,
	request application.MutateAllocationClaimsRequest,
	destination application.AllocationClaimState,
	allowed ...application.AllocationClaimState,
) error {
	claims, err := validateAllocationRequest(request.Binding, request.Claims)
	if err != nil {
		return err
	}
	allowedStates := make(map[application.AllocationClaimState]bool, len(allowed))
	for _, state := range allowed {
		allowedStates[state] = true
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin allocation transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range claims {
		claim, found, err := s.readAllocationClaim(ctx, tx, key)
		if err != nil {
			return err
		}
		if !found || !sameAllocationBinding(claim.Binding, request.Binding) {
			return ErrAllocationBinding
		}
		if !allowedStates[claim.State] {
			return ErrAllocationState
		}
		if request.FencingToken < claim.FencingToken {
			return ErrAllocationFence
		}
	}
	for _, key := range claims {
		var result sql.Result
		var err error
		if destination == "" {
			result, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims
				SET fencing_token = ?, updated_at_us = `+s.nowMicros()+`
				WHERE target_id = ? AND namespace = ? AND claim_value = ?
				  AND instance_id = ? AND resource_id = ? AND plan_digest = ? AND fencing_token <= ?`),
				request.FencingToken, key.TargetID, key.Namespace, key.Value, request.Binding.InstanceID,
				request.Binding.ResourceID, request.Binding.PlanDigest.Hex(), request.FencingToken)
		} else {
			result, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims
				SET state = ?, fencing_token = ?, updated_at_us = `+s.nowMicros()+`
				WHERE target_id = ? AND namespace = ? AND claim_value = ?
				  AND instance_id = ? AND resource_id = ? AND plan_digest = ? AND fencing_token <= ?`),
				destination, request.FencingToken, key.TargetID, key.Namespace, key.Value, request.Binding.InstanceID,
				request.Binding.ResourceID, request.Binding.PlanDigest.Hex(), request.FencingToken)
		}
		if err != nil {
			return fmt.Errorf("transition allocation claim: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return ErrAllocationFence
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit allocation transition: %w", err)
	}
	return nil
}

func (s *Store) ListClaims(ctx context.Context, instanceID domain.InstanceID) ([]application.AllocationClaim, error) {
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT target_id, namespace, claim_value, resource_id, plan_digest, state, fencing_token
		FROM labcentral_provider_allocation_claims WHERE instance_id = ?
		ORDER BY target_id, namespace, claim_value`), instanceID)
	if err != nil {
		return nil, fmt.Errorf("list allocation claims: %w", err)
	}
	defer rows.Close()
	claims := make([]application.AllocationClaim, 0)
	for rows.Next() {
		var targetID, namespace, value, resourceID, planDigest, state string
		var fencingToken int64
		if err := rows.Scan(&targetID, &namespace, &value, &resourceID, &planDigest, &state, &fencingToken); err != nil {
			return nil, fmt.Errorf("scan allocation claim: %w", err)
		}
		digest, err := domain.ParseSHA256Digest(planDigest)
		if err != nil {
			return nil, fmt.Errorf("stored allocation digest is invalid: %w", err)
		}
		claims = append(claims, application.AllocationClaim{
			Key:     application.AllocationKey{TargetID: domain.ProviderTargetID(targetID), Namespace: namespace, Value: value},
			Binding: application.AllocationBinding{InstanceID: instanceID, ResourceID: domain.LogicalResourceID(resourceID), PlanDigest: digest},
			State:   application.AllocationClaimState(state), FencingToken: domain.FencingToken(fencingToken),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate allocation claims: %w", err)
	}
	return claims, nil
}

func (s *Store) readAllocationClaim(ctx context.Context, tx *sql.Tx, key application.AllocationKey) (application.AllocationClaim, bool, error) {
	var instanceID, resourceID, digest, state string
	var fencingToken int64
	err := tx.QueryRowContext(ctx, s.bind(`SELECT instance_id, resource_id, plan_digest, state, fencing_token
		FROM labcentral_provider_allocation_claims
		WHERE target_id = ? AND namespace = ? AND claim_value = ?`), key.TargetID, key.Namespace, key.Value).
		Scan(&instanceID, &resourceID, &digest, &state, &fencingToken)
	if errors.Is(err, sql.ErrNoRows) {
		return application.AllocationClaim{}, false, nil
	}
	if err != nil {
		return application.AllocationClaim{}, false, fmt.Errorf("read allocation claim: %w", err)
	}
	parsedDigest, err := domain.ParseSHA256Digest(digest)
	if err != nil {
		return application.AllocationClaim{}, false, fmt.Errorf("stored allocation digest is invalid: %w", err)
	}
	return application.AllocationClaim{
		Key: key,
		Binding: application.AllocationBinding{
			InstanceID: domain.InstanceID(instanceID), ResourceID: domain.LogicalResourceID(resourceID), PlanDigest: parsedDigest,
		},
		State: application.AllocationClaimState(state), FencingToken: domain.FencingToken(fencingToken),
	}, true, nil
}

func validateAllocationRequest(binding application.AllocationBinding, source []application.AllocationKey) ([]application.AllocationKey, error) {
	return application.ValidateAllocationClaims(binding, source)
}

func sameAllocationBinding(left, right application.AllocationBinding) bool {
	return left.InstanceID == right.InstanceID && left.ResourceID == right.ResourceID && left.PlanDigest == right.PlanDigest
}

func isAllocationUniqueConflict(err error) bool {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return true
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: labcentral_provider_allocation_claims")
}

var _ application.AllocationRepository = (*Store)(nil)
