package jobsdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type Store struct {
	db      *sql.DB
	dialect Dialect
}

func New(db *sql.DB, dialect Dialect) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("jobs database is nil")
	}
	if dialect != DialectSQLite && dialect != DialectPostgreSQL {
		return nil, fmt.Errorf("unsupported jobs database dialect %q", dialect)
	}
	if dialect == DialectSQLite {
		// ADR-0006 intentionally makes local SQLite a bounded single-writer
		// adapter. This also keeps connection-local safety PRAGMAs consistent.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	return &Store{db: db, dialect: dialect}, nil
}

func (s *Store) bind(query string) string {
	if s.dialect != DialectPostgreSQL {
		return query
	}
	var result strings.Builder
	argument := 1
	for _, character := range query {
		if character == '?' {
			fmt.Fprintf(&result, "$%d", argument)
			argument++
			continue
		}
		result.WriteRune(character)
	}
	return result.String()
}

func (s *Store) nowMicros() string {
	if s.dialect == DialectPostgreSQL {
		return "CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000 AS BIGINT)"
	}
	return "(CAST(strftime('%s', 'now') AS INTEGER) * 1000000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER) * 1000)"
}

func (s *Store) CreateAggregate(ctx context.Context, aggregateID, phase string) error {
	if aggregateID == "" || phase == "" {
		return fmt.Errorf("aggregate ID and phase are required")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_aggregates
		(aggregate_id, phase, resource_version, operation_state, instance_phase)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT (aggregate_id) DO NOTHING`), aggregateID, phase, string(domain.OperationQueued), string(domain.InstancePending))
	if err != nil {
		return fmt.Errorf("create jobs aggregate: %w", err)
	}
	return nil
}

func (s *Store) ApplyAtomic(ctx context.Context, mutation AtomicMutation) error {
	return s.applyAtomic(ctx, mutation, faultNone)
}

func (s *Store) applyAtomic(ctx context.Context, mutation AtomicMutation, fault faultPoint) error {
	if mutation.AggregateID == "" || mutation.AuditID == "" || mutation.EventID == "" || mutation.OutboxSink == "" {
		return fmt.Errorf("atomic mutation identity fields are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin atomic jobs mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var aggregateSequence int64
	err = tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_job_aggregates
		SET phase = ?, resource_version = resource_version + 1, event_sequence = event_sequence + 1
		WHERE aggregate_id = ? AND resource_version = ?
		RETURNING event_sequence`), mutation.NewPhase, mutation.AggregateID, mutation.ExpectedResourceVersion).Scan(&aggregateSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVersionConflict
	}
	if err != nil {
		return fmt.Errorf("update jobs aggregate: %w", err)
	}
	if aggregateSequence <= 0 {
		return fmt.Errorf("allocated aggregate event sequence is invalid")
	}
	if fault == faultBeforeCommit {
		return errInjectedTransactionFault
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_audit_intents
		(audit_id, aggregate_id, actor_id, action, created_at_us)
		VALUES (?, ?, ?, ?, `+s.nowMicros()+`)`), mutation.AuditID, mutation.AggregateID, mutation.ActorID, mutation.Action); err != nil {
		return fmt.Errorf("insert audit intent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_events
		(event_id, aggregate_id, aggregate_sequence, event_type, payload, created_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`)`), mutation.EventID, mutation.AggregateID, aggregateSequence, mutation.EventType, mutation.EventPayload); err != nil {
		return fmt.Errorf("insert aggregate event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_outbox_deliveries
		(event_id, sink, payload, available_at_us)
		VALUES (?, ?, ?, `+s.nowMicros()+`)`), mutation.EventID, mutation.OutboxSink, mutation.EventPayload); err != nil {
		return fmt.Errorf("insert outbox delivery: %w", err)
	}
	if fault == faultDuringCommit {
		return errInjectedTransactionFault
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit atomic jobs mutation: %w", err)
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, operation domain.Operation, payload string) error {
	return s.EnqueueVersioned(ctx, operation, []byte(payload), CurrentPayloadVersion)
}

func (s *Store) EnqueueVersioned(ctx context.Context, operation domain.Operation, payload []byte, payloadVersion uint32) error {
	if _, err := domain.NewOperationID(string(operation.ID)); err != nil {
		return err
	}
	if _, err := domain.NewInstanceID(string(operation.InstanceID)); err != nil {
		return err
	}
	if operation.Generation == 0 || payloadVersion == 0 {
		return fmt.Errorf("operation generation and payload version must be positive")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_aggregates
		(aggregate_id, phase, resource_version, operation_state, instance_phase)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT (aggregate_id) DO NOTHING`), operation.InstanceID, "queued", string(domain.OperationQueued), string(domain.InstancePending)); err != nil {
		return fmt.Errorf("ensure operation aggregate: %w", err)
	}
	state := operation.State
	if state == "" {
		state = domain.OperationQueued
	}
	if state != domain.OperationQueued {
		return fmt.Errorf("new operation must be queued")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_jobs
		(id, operation_id, instance_id, aggregate_id, state, desired_generation, payload_version, payload, available_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), operation.ID, operation.ID, operation.InstanceID, operation.InstanceID, state, operation.Generation, payloadVersion, append([]byte(nil), payload...)); err != nil {
		return fmt.Errorf("enqueue operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enqueue: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, operationID domain.OperationID) (domain.Operation, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT operation_id, instance_id, state, desired_generation,
		fencing_token, lease_until_us, attempt, recovery_required, payload
		FROM labcentral_jobs WHERE operation_id = ?`), operationID)
	operation, _, _, err := scanOperation(row)
	return operation, err
}

func (s *Store) Claim(ctx context.Context, leaseOwner string, leaseDuration time.Duration) (application.OperationClaim, error) {
	claim, err := s.ClaimJob(ctx, leaseOwner, leaseDuration)
	if err != nil {
		return application.OperationClaim{}, err
	}
	return application.OperationClaim{Operation: claim.Operation, Token: claim.Operation.FencingToken, Action: claim.Action}, nil
}

func (s *Store) ClaimJob(ctx context.Context, leaseOwner string, leaseDuration time.Duration) (ClaimedJob, error) {
	return s.claimJob(ctx, leaseOwner, leaseDuration, nil)
}

// ClaimJobForInstances restricts claiming without taking leases on unavailable targets.
func (s *Store) ClaimJobForInstances(ctx context.Context, owner string, duration time.Duration, instances []string) (ClaimedJob, error) {
	if len(instances) == 0 {
		return ClaimedJob{}, ErrNoClaimableJob
	}
	if len(instances) > 1024 {
		return ClaimedJob{}, fmt.Errorf("instance claim filter exceeds limit")
	}
	return s.claimJob(ctx, owner, duration, instances)
}
func (s *Store) claimJob(ctx context.Context, leaseOwner string, leaseDuration time.Duration, instances []string) (ClaimedJob, error) {
	if leaseOwner == "" || leaseDuration <= 0 {
		return ClaimedJob{}, fmt.Errorf("lease owner and positive duration are required")
	}
	leaseMicros := leaseDuration.Microseconds()
	filter := ""
	filterArgs := make([]any, 0, len(instances))
	if instances != nil {
		filter = " AND instance_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(instances)), ",") + ") "
		for _, id := range instances {
			filterArgs = append(filterArgs, id)
		}
	}
	var row *sql.Row
	if s.dialect == DialectPostgreSQL {
		args := []any{CurrentPayloadVersion, domain.OperationQueued, domain.OperationRunning}
		args = append(args, filterArgs...)
		args = append(args, domain.OperationRunning, leaseOwner, leaseMicros)
		row = s.db.QueryRowContext(ctx, s.bind(`WITH candidate AS (
			SELECT id FROM labcentral_jobs
			WHERE payload_version = ?
			  AND available_at_us <= `+s.nowMicros()+`
			  AND (state = ? OR (state = ? AND lease_until_us <= `+s.nowMicros()+`))
			`+filter+` ORDER BY available_at_us, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE labcentral_jobs AS job
		SET state = ?, lease_owner = ?, lease_until_us = `+s.nowMicros()+` + ?,
			fencing_token = fencing_token + 1, attempt = attempt + 1
		FROM candidate
		WHERE job.id = candidate.id
		RETURNING job.operation_id, job.instance_id, job.state, job.desired_generation,
			job.fencing_token, job.lease_until_us, job.attempt, job.recovery_required, job.payload`),
			args...)
	} else {
		args := []any{domain.OperationRunning, leaseOwner, leaseMicros, CurrentPayloadVersion, domain.OperationQueued, domain.OperationRunning}
		args = append(args, filterArgs...)
		row = s.db.QueryRowContext(ctx, s.bind(`UPDATE labcentral_jobs
		SET state = ?, lease_owner = ?, lease_until_us = `+s.nowMicros()+` + ?,
			fencing_token = fencing_token + 1, attempt = attempt + 1
		WHERE id = (
			SELECT id FROM labcentral_jobs
			WHERE payload_version = ?
			  AND available_at_us <= `+s.nowMicros()+`
			  AND (state = ? OR (state = ? AND lease_until_us <= `+s.nowMicros()+`))
			`+filter+` ORDER BY available_at_us, id
			LIMIT 1
		)
		RETURNING operation_id, instance_id, state, desired_generation,
			fencing_token, lease_until_us, attempt, recovery_required, payload`),
			args...)
	}
	operation, recoveryRequired, payload, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedJob{}, ErrNoClaimableJob
	}
	if err != nil {
		return ClaimedJob{}, fmt.Errorf("claim operation: %w", err)
	}
	action := application.ClaimDispatch
	if recoveryRequired {
		action = application.ClaimGetInvocation
	}
	return ClaimedJob{Operation: operation, LeaseOwner: leaseOwner, Action: action, Payload: payload}, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanOperation(row rowScanner) (domain.Operation, bool, []byte, error) {
	var operationID, instanceID, state string
	var generation, fencingToken, leaseUntil, attempt int64
	var recoveryRequired int
	var payload []byte
	if err := row.Scan(&operationID, &instanceID, &state, &generation, &fencingToken, &leaseUntil, &attempt, &recoveryRequired, &payload); err != nil {
		return domain.Operation{}, false, nil, err
	}
	if generation < 0 || fencingToken < 0 || attempt < 0 {
		return domain.Operation{}, false, nil, fmt.Errorf("operation row contains negative monotonic values")
	}
	return domain.Operation{
		ID:             domain.OperationID(operationID),
		InstanceID:     domain.InstanceID(instanceID),
		State:          domain.OperationState(state),
		Generation:     domain.Generation(generation),
		FencingToken:   domain.FencingToken(fencingToken),
		LeaseExpiresAt: time.UnixMicro(leaseUntil).UTC(),
		Attempt:        uint32(attempt),
	}, recoveryRequired == 1, append([]byte(nil), payload...), nil
}

func (s *Store) RenewLease(ctx context.Context, operationID domain.OperationID, leaseOwner string, token domain.FencingToken, leaseDuration time.Duration) error {
	if leaseOwner == "" || token == 0 || leaseDuration <= 0 {
		return fmt.Errorf("operation, owner, token, and positive duration are required")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs
		SET lease_until_us = `+s.nowMicros()+` + ?
		WHERE operation_id = ? AND state = ? AND lease_owner = ? AND fencing_token = ?`),
		leaseDuration.Microseconds(), operationID, domain.OperationRunning, leaseOwner, token)
	if err != nil {
		return fmt.Errorf("renew operation lease: %w", err)
	}
	return requireSingleFencedRow(result)
}

func (s *Store) Checkpoint(ctx context.Context, checkpoint application.OperationCheckpoint) error {
	if checkpoint.LeaseOwner == "" || checkpoint.Token == 0 || checkpoint.Generation == 0 {
		return fmt.Errorf("checkpoint owner, token, and generation are required")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs
		SET state = ?, checkpoint = ?
		WHERE operation_id = ? AND lease_owner = ? AND fencing_token = ? AND desired_generation = ? AND attempt = ?`),
		checkpoint.State, append([]byte(nil), checkpoint.Payload...), checkpoint.OperationID,
		checkpoint.LeaseOwner, checkpoint.Token, checkpoint.Generation, checkpoint.Attempt)
	if err != nil {
		return fmt.Errorf("checkpoint operation: %w", err)
	}
	return requireSingleFencedRow(result)
}

func requireSingleFencedRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read fenced update count: %w", err)
	}
	if affected != 1 {
		return ErrFencingConflict
	}
	return nil
}

func (s *Store) RecordInvocation(ctx context.Context, record InvocationRecord) error {
	if record.ID == "" || record.OperationID == "" || record.IdempotencyKey == "" || record.RequestDigest == "" {
		return fmt.Errorf("invocation identity, operation, idempotency key, and digest are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin invocation record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_plugin_invocations
		(invocation_id, operation_id, idempotency_key, request_digest, state, created_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`)
		ON CONFLICT DO NOTHING`), record.ID, record.OperationID, record.IdempotencyKey, record.RequestDigest, record.State); err != nil {
		return fmt.Errorf("insert invocation record: %w", err)
	}
	var operationID, key, digest, state string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT operation_id, idempotency_key, request_digest, state
		FROM labcentral_plugin_invocations WHERE invocation_id = ?`), record.ID).Scan(&operationID, &key, &digest, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdempotencyConflict
	}
	if err != nil {
		return fmt.Errorf("read invocation record: %w", err)
	}
	if operationID != string(record.OperationID) || key != record.IdempotencyKey || digest != string(record.RequestDigest) {
		return ErrIdempotencyConflict
	}
	recovery := 0
	if InvocationState(state) == InvocationUnknown || record.State == InvocationUnknown {
		recovery = 1
	}
	updated, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET recovery_required = ? WHERE operation_id = ?`), recovery, record.OperationID)
	if err != nil {
		return fmt.Errorf("mark invocation recovery state: %w", err)
	}
	if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("invocation operation is unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit invocation record: %w", err)
	}
	return nil
}

func (s *Store) BindIdempotency(ctx context.Context, binding IdempotencyBinding) (IdempotencyResult, error) {
	if binding.SubjectID == "" || binding.RouteAction == "" || binding.Key == "" || binding.RequestDigest == "" || binding.ResultID == "" {
		return IdempotencyResult{}, fmt.Errorf("idempotency binding fields are required")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_idempotency_records
		(subject_id, route_action, idempotency_key, request_digest, result_id, expires_at_us)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (subject_id, route_action, idempotency_key) DO NOTHING`),
		binding.SubjectID, binding.RouteAction, binding.Key, binding.RequestDigest, binding.ResultID, binding.ExpiresAt.UnixMicro())
	if err != nil {
		return IdempotencyResult{}, fmt.Errorf("bind idempotency key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return IdempotencyResult{}, fmt.Errorf("read idempotency insert count: %w", err)
	}
	var digest, resultID string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT request_digest, result_id
		FROM labcentral_idempotency_records
		WHERE subject_id = ? AND route_action = ? AND idempotency_key = ?`),
		binding.SubjectID, binding.RouteAction, binding.Key).Scan(&digest, &resultID); err != nil {
		return IdempotencyResult{}, fmt.Errorf("read idempotency binding: %w", err)
	}
	if digest != string(binding.RequestDigest) {
		return IdempotencyResult{}, ErrIdempotencyConflict
	}
	return IdempotencyResult{ResultID: resultID, Replay: affected == 0}, nil
}

func (s *Store) CreateQuotaReservation(ctx context.Context, reservation application.QuotaReservation) error {
	if reservation.ID == "" || reservation.OperationID == "" || reservation.ExpiresAt.IsZero() {
		return fmt.Errorf("quota reservation identity, operation, and expiry are required")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservations
		(reservation_id, operation_id, state, expires_at_us)
		VALUES (?, ?, 'active', ?)`), reservation.ID, reservation.OperationID, reservation.ExpiresAt.UnixMicro())
	if err != nil {
		return fmt.Errorf("create quota reservation: %w", err)
	}
	return nil
}

func (s *Store) ResolveQuota(ctx context.Context, reservationID string, outcome QuotaOutcome) (QuotaResolution, error) {
	switch outcome {
	case QuotaOutcomeUnknown:
		result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_reservations
			SET reconciliation_required = 1 WHERE reservation_id = ? AND state = 'active'`), reservationID)
		if err != nil {
			return QuotaResolution{}, fmt.Errorf("retain unknown quota reservation: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return QuotaResolution{}, sql.ErrNoRows
		}
		return QuotaResolution{ReleaseAllowed: false, ReconciliationRequired: true}, nil
	case QuotaOutcomeRealized:
		result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_reservations
			SET state = 'committed', reconciliation_required = 0 WHERE reservation_id = ? AND state = 'active'`), reservationID)
		if err != nil {
			return QuotaResolution{}, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return QuotaResolution{}, sql.ErrNoRows
		}
		return QuotaResolution{ReleaseAllowed: false}, nil
	case QuotaOutcomeKnownAbsent:
		result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_reservations
			SET state = 'released', reconciliation_required = 0 WHERE reservation_id = ? AND state = 'active'`), reservationID)
		if err != nil {
			return QuotaResolution{}, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return QuotaResolution{}, sql.ErrNoRows
		}
		return QuotaResolution{ReleaseAllowed: true}, nil
	default:
		return QuotaResolution{}, fmt.Errorf("unsupported quota outcome %q", outcome)
	}
}

func (s *Store) FinalizeDestroy(ctx context.Context, aggregateID string, ownedLeftovers []domain.OwnershipID) (DestroyResult, error) {
	result := DestroyResult{OperationState: domain.OperationSucceeded, InstancePhase: domain.InstanceDestroyed}
	if len(ownedLeftovers) > 0 {
		result.OperationState = domain.OperationPartial
		result.InstancePhase = domain.InstanceDegraded
	}
	updated, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_job_aggregates
		SET operation_state = ?, instance_phase = ?, resource_version = resource_version + 1
		WHERE aggregate_id = ?`), result.OperationState, result.InstancePhase, aggregateID)
	if err != nil {
		return DestroyResult{}, fmt.Errorf("finalize destroy: %w", err)
	}
	if affected, _ := updated.RowsAffected(); affected != 1 {
		return DestroyResult{}, sql.ErrNoRows
	}
	return result, nil
}

func (s *Store) ClaimOutbox(ctx context.Context, leaseOwner string, leaseDuration time.Duration) (OutboxClaim, error) {
	if leaseOwner == "" || leaseDuration <= 0 {
		return OutboxClaim{}, fmt.Errorf("outbox lease owner and positive duration are required")
	}
	leaseMicros := leaseDuration.Microseconds()
	var row *sql.Row
	if s.dialect == DialectPostgreSQL {
		row = s.db.QueryRowContext(ctx, s.bind(`WITH candidate AS (
			SELECT event_id, sink FROM labcentral_outbox_deliveries
			WHERE delivered_at_us IS NULL AND available_at_us <= `+s.nowMicros()+`
			  AND (lease_owner IS NULL OR lease_until_us <= `+s.nowMicros()+`)
			ORDER BY available_at_us, event_id, sink
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE labcentral_outbox_deliveries AS delivery
		SET lease_owner = ?, lease_until_us = `+s.nowMicros()+` + ?, attempts = attempts + 1
		FROM candidate
		WHERE delivery.event_id = candidate.event_id AND delivery.sink = candidate.sink
		RETURNING delivery.event_id, delivery.sink, delivery.payload, delivery.attempts`), leaseOwner, leaseMicros)
	} else {
		row = s.db.QueryRowContext(ctx, s.bind(`UPDATE labcentral_outbox_deliveries
		SET lease_owner = ?, lease_until_us = `+s.nowMicros()+` + ?, attempts = attempts + 1
		WHERE (event_id, sink) = (
			SELECT event_id, sink FROM labcentral_outbox_deliveries
			WHERE delivered_at_us IS NULL AND available_at_us <= `+s.nowMicros()+`
			  AND (lease_owner IS NULL OR lease_until_us <= `+s.nowMicros()+`)
			ORDER BY available_at_us, event_id, sink
			LIMIT 1
		)
		RETURNING event_id, sink, payload, attempts`), leaseOwner, leaseMicros)
	}
	var claim OutboxClaim
	var attempts int64
	if err := row.Scan(&claim.EventID, &claim.Sink, &claim.Payload, &attempts); errors.Is(err, sql.ErrNoRows) {
		return OutboxClaim{}, ErrNoOutboxDelivery
	} else if err != nil {
		return OutboxClaim{}, fmt.Errorf("claim outbox delivery: %w", err)
	}
	if attempts < 0 {
		return OutboxClaim{}, fmt.Errorf("outbox attempts are negative")
	}
	claim.Attempts = uint32(attempts)
	claim.LeaseOwner = leaseOwner
	var sequence int64
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT aggregate_id, aggregate_sequence
		FROM labcentral_job_events WHERE event_id = ?`), claim.EventID).Scan(&claim.AggregateID, &sequence); err != nil {
		return OutboxClaim{}, fmt.Errorf("read claimed outbox event: %w", err)
	}
	claim.AggregateSequence = domain.EventSequence(sequence)
	claim.Payload = append([]byte(nil), claim.Payload...)
	return claim, nil
}

func (s *Store) AckOutbox(ctx context.Context, eventID, sink, leaseOwner string) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_outbox_deliveries
		SET delivered_at_us = `+s.nowMicros()+`, lease_owner = NULL, lease_until_us = 0
		WHERE event_id = ? AND sink = ? AND lease_owner = ? AND delivered_at_us IS NULL`), eventID, sink, leaseOwner)
	if err != nil {
		return fmt.Errorf("acknowledge outbox delivery: %w", err)
	}
	return requireSingleFencedRow(result)
}

var _ application.OperationRepository = (*Store)(nil)
