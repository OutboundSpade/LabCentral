package jobsdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const (
	migrationID           = "0001_jobs_outbox_prototype"
	allocationMigrationID = "0002_provider_allocation_claims"
)

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("jobs database is not initialized")
	}
	if s.dialect == DialectSQLite {
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("enable SQLite foreign keys: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin jobs migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := s.migrationStatements()
	if _, err := tx.ExecContext(ctx, statements[0]); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	if err := s.applyMigration(ctx, tx, migrationID, statements[1:]); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, tx, allocationMigrationID, s.allocationMigrationStatements()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit jobs migrations: %w", err)
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, tx *sql.Tx, id string, statements []string) error {
	checksum := migrationChecksum(statements)
	var recorded string
	err := tx.QueryRowContext(ctx, s.bind("SELECT checksum FROM labcentral_schema_migrations WHERE id = ?"), id).Scan(&recorded)
	switch {
	case err == nil && recorded != checksum:
		return fmt.Errorf("migration %s checksum mismatch", id)
	case err != nil && err != sql.ErrNoRows:
		return fmt.Errorf("read migration record: %w", err)
	case err == nil:
		return nil
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %s: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(
		ctx,
		s.bind("INSERT INTO labcentral_schema_migrations (id, checksum, applied_at_us) VALUES (?, ?, "+s.nowMicros()+")"),
		id,
		checksum,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	return nil
}

func migrationChecksum(statements []string) string {
	hash := sha256.New()
	for _, statement := range statements {
		_, _ = hash.Write([]byte(statement))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (s *Store) migrationStatements() []string {
	bytesType := "BLOB"
	if s.dialect == DialectPostgreSQL {
		bytesType = "BYTEA"
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS labcentral_schema_migrations (
			id TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at_us BIGINT NOT NULL
		)`,
		`CREATE TABLE labcentral_job_aggregates (
			aggregate_id TEXT PRIMARY KEY,
			phase TEXT NOT NULL,
			resource_version BIGINT NOT NULL CHECK (resource_version >= 0),
			event_sequence BIGINT NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
			operation_state TEXT NOT NULL,
			instance_phase TEXT NOT NULL
		)`,
		fmt.Sprintf(`CREATE TABLE labcentral_jobs (
			id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE,
			instance_id TEXT NOT NULL,
			aggregate_id TEXT NOT NULL REFERENCES labcentral_job_aggregates(aggregate_id),
			state TEXT NOT NULL,
			desired_generation BIGINT NOT NULL CHECK (desired_generation >= 0),
			payload_version INTEGER NOT NULL,
			payload %s NOT NULL,
			available_at_us BIGINT NOT NULL,
			lease_owner TEXT,
			lease_until_us BIGINT NOT NULL DEFAULT 0,
			fencing_token BIGINT NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
			attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
			checkpoint %s,
			recovery_required INTEGER NOT NULL DEFAULT 0 CHECK (recovery_required IN (0, 1))
		)`, bytesType, bytesType),
		`CREATE INDEX labcentral_jobs_claim_idx ON labcentral_jobs(state, available_at_us, lease_until_us, id)`,
		`CREATE TABLE labcentral_plugin_invocations (
			invocation_id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE REFERENCES labcentral_jobs(operation_id),
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			state TEXT NOT NULL,
			created_at_us BIGINT NOT NULL
		)`,
		fmt.Sprintf(`CREATE TABLE labcentral_job_events (
			event_id TEXT PRIMARY KEY,
			aggregate_id TEXT NOT NULL REFERENCES labcentral_job_aggregates(aggregate_id),
			aggregate_sequence BIGINT NOT NULL CHECK (aggregate_sequence > 0),
			event_type TEXT NOT NULL,
			payload %s NOT NULL,
			created_at_us BIGINT NOT NULL,
			UNIQUE (aggregate_id, aggregate_sequence)
		)`, bytesType),
		`CREATE TABLE labcentral_audit_intents (
			audit_id TEXT PRIMARY KEY,
			aggregate_id TEXT NOT NULL REFERENCES labcentral_job_aggregates(aggregate_id),
			actor_id TEXT NOT NULL,
			action TEXT NOT NULL,
			created_at_us BIGINT NOT NULL
		)`,
		fmt.Sprintf(`CREATE TABLE labcentral_outbox_deliveries (
			event_id TEXT NOT NULL REFERENCES labcentral_job_events(event_id),
			sink TEXT NOT NULL,
			payload %s NOT NULL,
			available_at_us BIGINT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
			lease_owner TEXT,
			lease_until_us BIGINT NOT NULL DEFAULT 0,
			delivered_at_us BIGINT,
			PRIMARY KEY (event_id, sink)
		)`, bytesType),
		`CREATE INDEX labcentral_outbox_claim_idx ON labcentral_outbox_deliveries(delivered_at_us, available_at_us, lease_until_us, event_id, sink)`,
		`CREATE TABLE labcentral_idempotency_records (
			subject_id TEXT NOT NULL,
			route_action TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			request_digest TEXT NOT NULL,
			result_id TEXT NOT NULL,
			expires_at_us BIGINT NOT NULL,
			PRIMARY KEY (subject_id, route_action, idempotency_key)
		)`,
		`CREATE TABLE labcentral_quota_reservations (
			reservation_id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL,
			state TEXT NOT NULL,
			expires_at_us BIGINT NOT NULL,
			reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation_required IN (0, 1))
		)`,
	}
}

func (s *Store) allocationMigrationStatements() []string {
	return []string{
		`CREATE TABLE labcentral_provider_allocation_claims (
			target_id TEXT NOT NULL,
			namespace TEXT NOT NULL,
			claim_value TEXT NOT NULL,
			instance_id TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			plan_digest TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('held', 'committed', 'orphaned', 'released')),
			fencing_token BIGINT NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
			created_at_us BIGINT NOT NULL,
			updated_at_us BIGINT NOT NULL,
			PRIMARY KEY (target_id, namespace, claim_value)
		)`,
		`CREATE INDEX labcentral_provider_allocation_owner_idx
			ON labcentral_provider_allocation_claims(instance_id, resource_id, state, target_id, namespace, claim_value)`,
	}
}
