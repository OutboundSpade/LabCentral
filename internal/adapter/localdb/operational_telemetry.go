package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// ReadOperationalSnapshot returns aggregate durable state using database time.
// The result is deliberately identity-free and suitable for fixed-cardinality
// operational telemetry.
func (s *Store) ReadOperationalSnapshot(ctx context.Context) (application.OperationalSnapshot, error) {
	if s == nil || s.db == nil {
		return application.OperationalSnapshot{}, fmt.Errorf("control-plane database is not initialized")
	}
	var nowMicros int64
	if err := s.db.QueryRowContext(ctx, `SELECT `+s.nowMicros()).Scan(&nowMicros); err != nil {
		return application.OperationalSnapshot{}, fmt.Errorf("read operational snapshot database time: %w", err)
	}
	if nowMicros <= 0 {
		return application.OperationalSnapshot{}, fmt.Errorf("operational snapshot database time is invalid")
	}

	snapshot := application.OperationalSnapshot{}
	if err := s.readJobTelemetry(ctx, nowMicros, &snapshot); err != nil {
		return application.OperationalSnapshot{}, err
	}
	jobPending, jobOldest, err := s.readOutboxTelemetry(ctx,
		`SELECT COUNT(*), MIN(available_at_us) FROM labcentral_outbox_deliveries WHERE delivered_at_us IS NULL`, nowMicros)
	if err != nil {
		return application.OperationalSnapshot{}, fmt.Errorf("read job outbox telemetry: %w", err)
	}
	eventPending, eventOldest, err := s.readOutboxTelemetry(ctx,
		`SELECT COUNT(*), MIN(available_at_us) FROM labcentral_event_outbox_deliveries WHERE delivered_at_us = 0`, nowMicros)
	if err != nil {
		return application.OperationalSnapshot{}, fmt.Errorf("read event outbox telemetry: %w", err)
	}
	snapshot.Outbox = application.OutboxSnapshot{
		JobPending: jobPending, EventPending: eventPending,
		OldestJobAge: jobOldest, OldestEventAge: eventOldest,
	}
	if err := s.readAccessTelemetry(ctx, &snapshot); err != nil {
		return application.OperationalSnapshot{}, err
	}
	if err := s.readResourceTelemetry(ctx, &snapshot); err != nil {
		return application.OperationalSnapshot{}, err
	}
	if err := s.readTargetTelemetry(ctx, nowMicros, &snapshot); err != nil {
		return application.OperationalSnapshot{}, err
	}
	var orphaned int64
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_provider_allocation_claims WHERE state = ?`), application.AllocationOrphaned).Scan(&orphaned); err != nil {
		return application.OperationalSnapshot{}, fmt.Errorf("read orphaned allocation telemetry: %w", err)
	}
	converted, err := operationalCount(orphaned)
	if err != nil {
		return application.OperationalSnapshot{}, err
	}
	snapshot.Cleanup.OrphanedAllocations = converted
	return snapshot, nil
}

func (s *Store) readJobTelemetry(ctx context.Context, nowMicros int64, snapshot *application.OperationalSnapshot) error {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT state, recovery_required, COUNT(*), MIN(available_at_us),
		MIN(CASE WHEN state = ? AND lease_until_us <= ? THEN lease_until_us END)
		FROM labcentral_jobs GROUP BY state, recovery_required`), domain.OperationRunning, nowMicros)
	if err != nil {
		return fmt.Errorf("read durable job telemetry: %w", err)
	}
	defer rows.Close()
	var oldestQueued, oldestExpiredLease int64
	for rows.Next() {
		var state string
		var recovery int
		var count int64
		var availableAt, expiredLease sql.NullInt64
		if err := rows.Scan(&state, &recovery, &count, &availableAt, &expiredLease); err != nil {
			return fmt.Errorf("scan durable job telemetry: %w", err)
		}
		if recovery != 0 && recovery != 1 {
			return fmt.Errorf("durable job recovery state is invalid")
		}
		converted, err := operationalCount(count)
		if err != nil {
			return err
		}
		operationState := domain.OperationState(state)
		if !knownOperationState(operationState) {
			return fmt.Errorf("durable job operation state %q is invalid", state)
		}
		snapshot.Operations = append(snapshot.Operations, application.OperationStateCount{State: operationState, Count: converted})
		switch operationState {
		case domain.OperationQueued:
			snapshot.Jobs.Queued += converted
			if availableAt.Valid {
				oldestQueued = earlierOperationalTime(oldestQueued, availableAt.Int64)
			}
		case domain.OperationRunning:
			snapshot.Jobs.Running += converted
		}
		if recovery == 1 {
			snapshot.Jobs.RecoveryRequired += converted
		}
		if expiredLease.Valid {
			oldestExpiredLease = earlierOperationalTime(oldestExpiredLease, expiredLease.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate durable job telemetry: %w", err)
	}
	snapshot.Jobs.OldestQueuedAge = operationalAge(nowMicros, oldestQueued)
	snapshot.Jobs.OldestExpiredLeaseAge = operationalAge(nowMicros, oldestExpiredLease)
	return nil
}

func (s *Store) readOutboxTelemetry(ctx context.Context, query string, nowMicros int64) (uint64, time.Duration, error) {
	var count int64
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query).Scan(&count, &oldest); err != nil {
		return 0, 0, err
	}
	converted, err := operationalCount(count)
	if err != nil {
		return 0, 0, err
	}
	return converted, operationalAge(nowMicros, oldest.Int64), nil
}

func (s *Store) readAccessTelemetry(ctx context.Context, snapshot *application.OperationalSnapshot) error {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM labcentral_access_sessions GROUP BY state`)
	if err != nil {
		return fmt.Errorf("read access-session telemetry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return fmt.Errorf("scan access-session telemetry: %w", err)
		}
		accessState := domain.AccessSessionState(state)
		if !knownAccessState(accessState) {
			return fmt.Errorf("access-session state %q is invalid", state)
		}
		converted, err := operationalCount(count)
		if err != nil {
			return err
		}
		snapshot.Access = append(snapshot.Access, application.AccessStateCount{State: accessState, Count: converted})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate access-session telemetry: %w", err)
	}
	return nil
}

func (s *Store) readResourceTelemetry(ctx context.Context, snapshot *application.OperationalSnapshot) error {
	rows, err := s.db.QueryContext(ctx, `SELECT phase, COUNT(*) FROM labcentral_local_resources GROUP BY phase`)
	if err != nil {
		return fmt.Errorf("read resource telemetry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var phase string
		var count int64
		if err := rows.Scan(&phase, &count); err != nil {
			return fmt.Errorf("scan resource telemetry: %w", err)
		}
		resourcePhase := domain.ResourcePhase(phase)
		if !knownResourcePhase(resourcePhase) {
			return fmt.Errorf("resource phase %q is invalid", phase)
		}
		converted, err := operationalCount(count)
		if err != nil {
			return err
		}
		snapshot.Resources = append(snapshot.Resources, application.ResourcePhaseCount{Phase: resourcePhase, Count: converted})
		switch resourcePhase {
		case domain.ResourceUnknown:
			snapshot.Cleanup.UnknownResources = converted
		case domain.ResourceCleanupFailed:
			snapshot.Cleanup.FailedResources = converted
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate resource telemetry: %w", err)
	}
	return nil
}

func (s *Store) readTargetTelemetry(ctx context.Context, nowMicros int64, snapshot *application.OperationalSnapshot) error {
	var total, stale int64
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN valid_until_us <= ? THEN 1 ELSE 0 END), 0), MIN(discovered_at_us)
		FROM labcentral_local_provider_targets`), nowMicros).Scan(&total, &stale, &oldest); err != nil {
		return fmt.Errorf("read target capability telemetry: %w", err)
	}
	convertedTotal, err := operationalCount(total)
	if err != nil {
		return err
	}
	convertedStale, err := operationalCount(stale)
	if err != nil {
		return err
	}
	snapshot.Targets = application.TargetCapabilitySnapshot{
		Total: convertedTotal, Stale: convertedStale, OldestAge: operationalAge(nowMicros, oldest.Int64),
	}
	return nil
}

func operationalCount(value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("operational telemetry count is negative")
	}
	return uint64(value), nil
}

func operationalAge(nowMicros, thenMicros int64) time.Duration {
	if thenMicros <= 0 || thenMicros >= nowMicros {
		return 0
	}
	return time.Duration(nowMicros-thenMicros) * time.Microsecond
}

func earlierOperationalTime(current, candidate int64) int64 {
	if candidate > 0 && (current == 0 || candidate < current) {
		return candidate
	}
	return current
}

func knownOperationState(state domain.OperationState) bool {
	switch state {
	case domain.OperationQueued, domain.OperationRunning, domain.OperationCancelRequested, domain.OperationCompensating,
		domain.OperationSucceeded, domain.OperationFailed, domain.OperationCanceled, domain.OperationTimedOut, domain.OperationPartial:
		return true
	default:
		return false
	}
}

func knownAccessState(state domain.AccessSessionState) bool {
	switch state {
	case domain.AccessSessionPending, domain.AccessSessionReady, domain.AccessSessionActive,
		domain.AccessSessionExpired, domain.AccessSessionRevoked, domain.AccessSessionFailed:
		return true
	default:
		return false
	}
}

func knownResourcePhase(phase domain.ResourcePhase) bool {
	switch phase {
	case domain.ResourcePending, domain.ResourceApplying, domain.ResourceReady, domain.ResourceFailed,
		domain.ResourceUnknown, domain.ResourceStopped, domain.ResourcePaused, domain.ResourceDestroying,
		domain.ResourceDestroyed, domain.ResourceCleanupFailed:
		return true
	default:
		return false
	}
}
