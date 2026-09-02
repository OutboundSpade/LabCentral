package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	migrationID       = "0002_local_vertical_slice"
	targetMigrationID = "0003_local_provider_target_binding"
)

type Store struct {
	db       *sql.DB
	jobs     *jobsdb.Store
	lockFile *os.File
}

func Open(ctx context.Context, databasePath string) (*Store, error) {
	if databasePath == "" {
		return nil, fmt.Errorf("SQLite database path is required")
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create SQLite data directory: %w", err)
	}
	lockPath := absolute + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SQLite process lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("another labd process owns %s", absolute)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=rwc"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		_ = lockFile.Close()
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store, err := New(db)
	if err != nil {
		_ = db.Close()
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		_ = lockFile.Close()
		return nil, err
	}
	store.lockFile = lockFile
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("local database is nil")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	jobs, err := jobsdb.New(db, jobsdb.DialectSQLite)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, jobs: jobs}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	if err := s.jobs.Migrate(ctx); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, migrationID, migrationStatements()); err != nil {
		return err
	}
	return s.applyMigration(ctx, targetMigrationID, targetMigrationStatements())
}

func (s *Store) applyMigration(ctx context.Context, id string, statements []string) error {
	checksum := checksum(statements)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	var recorded string
	err = tx.QueryRowContext(ctx, "SELECT checksum FROM labcentral_schema_migrations WHERE id = ?", id).Scan(&recorded)
	switch {
	case err == nil && recorded != checksum:
		return fmt.Errorf("migration %s checksum mismatch", id)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read local migration record: %w", err)
	case err == nil:
		return tx.Commit()
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %s: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_schema_migrations (id, checksum, applied_at_us)
		VALUES (?, ?, `+nowMicros()+`)`, id, checksum); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func targetMigrationStatements() []string {
	return []string{
		`ALTER TABLE labcentral_local_instances
			ADD COLUMN provider_target_id TEXT NOT NULL DEFAULT 'target.local-docker'`,
		`ALTER TABLE labcentral_local_instances
			ADD COLUMN provider_override_json BLOB NOT NULL DEFAULT X''`,
		`CREATE TABLE labcentral_local_provider_targets (
			target_id TEXT PRIMARY KEY,
			provider_kind TEXT NOT NULL,
			config_fingerprint TEXT NOT NULL,
			protocol_major INTEGER NOT NULL,
			protocol_minor INTEGER NOT NULL,
			features_json BLOB NOT NULL,
			discovery_json BLOB NOT NULL,
			discovered_at_us INTEGER NOT NULL,
			valid_until_us INTEGER NOT NULL,
			updated_at_us INTEGER NOT NULL
		)`,
	}
}

func migrationStatements() []string {
	return []string{
		`CREATE TABLE labcentral_local_instances (
			instance_id TEXT PRIMARY KEY,
			canonical_digest TEXT NOT NULL,
			canonical_lab_json BLOB NOT NULL,
			source_yaml BLOB NOT NULL,
			phase TEXT NOT NULL,
			desired_lifecycle TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK (generation > 0),
			resource_version INTEGER NOT NULL CHECK (resource_version > 0),
			isolation_warning_acknowledged INTEGER NOT NULL CHECK (isolation_warning_acknowledged IN (0, 1)),
			outputs_json BLOB NOT NULL DEFAULT '{}',
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at_us INTEGER NOT NULL,
			updated_at_us INTEGER NOT NULL
		)`,
		`CREATE TABLE labcentral_local_resources (
			instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),
			resource_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
			canonical_resource_json BLOB NOT NULL,
			provider_plan BLOB NOT NULL,
			phase TEXT NOT NULL,
			ownership_token TEXT NOT NULL,
			provider_state BLOB,
			observed_state_json BLOB NOT NULL DEFAULT '{}',
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			updated_at_us INTEGER NOT NULL,
			PRIMARY KEY (instance_id, resource_id),
			UNIQUE (instance_id, ordinal)
		)`,
		`CREATE TABLE labcentral_local_operations (
			operation_id TEXT PRIMARY KEY REFERENCES labcentral_jobs(operation_id),
			instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),
			kind TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			created_at_us INTEGER NOT NULL,
			updated_at_us INTEGER NOT NULL
		)`,
		`CREATE INDEX labcentral_local_operations_instance_idx ON labcentral_local_operations(instance_id, created_at_us, operation_id)`,
		`CREATE TABLE labcentral_runtime_invocations (
			invocation_id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL REFERENCES labcentral_jobs(operation_id),
			resource_id TEXT NOT NULL,
			action TEXT NOT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			state TEXT NOT NULL,
			provider_result BLOB,
			created_at_us INTEGER NOT NULL,
			updated_at_us INTEGER NOT NULL,
			UNIQUE (operation_id, resource_id, action)
		)`,
	}
}

func checksum(statements []string) string {
	hash := sha256.New()
	for _, statement := range statements {
		_, _ = hash.Write([]byte(statement))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func nowMicros() string {
	return "(CAST(strftime('%s', 'now') AS INTEGER) * 1000000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER) * 1000)"
}

func (s *Store) CreateInstance(ctx context.Context, request controlplane.CreateInstanceRequest) (controlplane.EnqueueResult, error) {
	if err := validateCreate(request); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := findIdempotency(ctx, tx, request.SubjectID, "create_instance", request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.EnqueueResult{}, err
	} else if found {
		var instanceID string
		if err := tx.QueryRowContext(ctx, "SELECT instance_id FROM labcentral_local_operations WHERE operation_id = ?", existing).Scan(&instanceID); err != nil {
			return controlplane.EnqueueResult{}, err
		}
		return controlplane.EnqueueResult{InstanceID: domain.InstanceID(instanceID), OperationID: domain.OperationID(existing), Replay: true}, tx.Commit()
	}
	ack := 0
	if request.Acknowledged {
		ack = 1
	}
	providerOverride := request.ProviderOverrideJSON
	if providerOverride == nil {
		providerOverride = []byte{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_local_instances
		(instance_id, provider_target_id, provider_override_json, canonical_digest, canonical_lab_json, source_yaml, phase, desired_lifecycle, generation, resource_version,
		 isolation_warning_acknowledged, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, `+nowMicros()+`, `+nowMicros()+`)`,
		request.InstanceID, request.ProviderTargetID, providerOverride, request.CanonicalDigest.Hex(), request.CanonicalJSON, request.SourceYAML,
		domain.InstancePending, domain.LifecycleRunning, ack); err != nil {
		return controlplane.EnqueueResult{}, fmt.Errorf("insert local instance: %w", err)
	}
	for _, resource := range request.Resources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_local_resources
			(instance_id, resource_id, kind, ordinal, canonical_resource_json, provider_plan, phase, ownership_token, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nowMicros()+`)`, request.InstanceID, resource.ID, resource.Kind, resource.Ordinal,
			resource.CanonicalJSON, resource.ProviderPlan, domain.ResourcePending, resource.OwnershipToken); err != nil {
			return controlplane.EnqueueResult{}, fmt.Errorf("insert local resource %s: %w", resource.ID, err)
		}
	}
	if err := commitAllocationClaims(ctx, tx, request); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if err := enqueue(ctx, tx, request.InstanceID, request.OperationID, controlplane.OperationApply, request.Payload, request.SubjectID, request.AuditID, request.EventID); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if err := bindIdempotency(ctx, tx, request.SubjectID, "create_instance", request.IdempotencyKey, request.RequestDigest, request.OperationID); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	return controlplane.EnqueueResult{InstanceID: request.InstanceID, OperationID: request.OperationID}, nil
}

func (s *Store) SaveTargetSnapshot(ctx context.Context, snapshot controlplane.TargetSnapshot) error {
	if _, err := domain.NewProviderTargetID(string(snapshot.ID)); err != nil {
		return err
	}
	if snapshot.ProviderKind == "" || !strings.HasPrefix(snapshot.ConfigFingerprint, "sha256:") || snapshot.ProtocolMajor != 1 || len(snapshot.DiscoveryJSON) == 0 || len(snapshot.DiscoveryJSON) > 8<<20 || snapshot.DiscoveredAt.IsZero() || !snapshot.ValidUntil.After(snapshot.DiscoveredAt) {
		return fmt.Errorf("provider target snapshot is incomplete")
	}
	if _, err := domain.ParseSHA256Digest(strings.TrimPrefix(snapshot.ConfigFingerprint, "sha256:")); err != nil {
		return fmt.Errorf("provider target config fingerprint is invalid: %w", err)
	}
	features := append([]string(nil), snapshot.Features...)
	sort.Strings(features)
	encodedFeatures, err := json.Marshal(features)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO labcentral_local_provider_targets
		(target_id, provider_kind, config_fingerprint, protocol_major, protocol_minor, features_json, discovery_json,
		 discovered_at_us, valid_until_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, `+nowMicros()+`)
		ON CONFLICT(target_id) DO UPDATE SET provider_kind = excluded.provider_kind,
		 config_fingerprint = excluded.config_fingerprint, protocol_major = excluded.protocol_major,
		 protocol_minor = excluded.protocol_minor, features_json = excluded.features_json,
		 discovery_json = excluded.discovery_json, discovered_at_us = excluded.discovered_at_us,
		 valid_until_us = excluded.valid_until_us, updated_at_us = `+nowMicros(),
		snapshot.ID, snapshot.ProviderKind, snapshot.ConfigFingerprint, snapshot.ProtocolMajor, snapshot.ProtocolMinor,
		encodedFeatures, snapshot.DiscoveryJSON, snapshot.DiscoveredAt.UnixMicro(), snapshot.ValidUntil.UnixMicro())
	if err != nil {
		return fmt.Errorf("persist provider target snapshot: %w", err)
	}
	return nil
}

func (s *Store) GetTargetSnapshot(ctx context.Context, targetID domain.ProviderTargetID) (controlplane.TargetSnapshot, error) {
	var snapshot controlplane.TargetSnapshot
	var id string
	var features []byte
	var protocolMajor, protocolMinor, discoveredAt, validUntil int64
	err := s.db.QueryRowContext(ctx, `SELECT target_id, provider_kind, config_fingerprint, protocol_major, protocol_minor,
		features_json, discovery_json, discovered_at_us, valid_until_us
		FROM labcentral_local_provider_targets WHERE target_id = ?`, targetID).Scan(
		&id, &snapshot.ProviderKind, &snapshot.ConfigFingerprint, &protocolMajor, &protocolMinor,
		&features, &snapshot.DiscoveryJSON, &discoveredAt, &validUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.TargetSnapshot{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.TargetSnapshot{}, err
	}
	if err := json.Unmarshal(features, &snapshot.Features); err != nil {
		return controlplane.TargetSnapshot{}, fmt.Errorf("decode provider target features: %w", err)
	}
	snapshot.ID = domain.ProviderTargetID(id)
	snapshot.ProtocolMajor = uint32(protocolMajor)
	snapshot.ProtocolMinor = uint32(protocolMinor)
	snapshot.DiscoveredAt = time.UnixMicro(discoveredAt).UTC()
	snapshot.ValidUntil = time.UnixMicro(validUntil).UTC()
	return snapshot, nil
}

func validateCreate(request controlplane.CreateInstanceRequest) error {
	if request.InstanceID == "" || request.ProviderTargetID == "" || request.OperationID == "" || request.CanonicalDigest == "" || len(request.CanonicalJSON) == 0 || len(request.SourceYAML) == 0 || len(request.Resources) == 0 || request.SubjectID == "" || request.IdempotencyKey == "" || request.RequestDigest == "" || len(request.Payload) == 0 || request.AuditID == "" || request.EventID == "" {
		return fmt.Errorf("local instance request is incomplete")
	}
	if request.ProviderTargetID == controlplane.LocalDockerTarget && !request.Acknowledged {
		return fmt.Errorf("local Docker instance request must acknowledge Docker isolation")
	}
	for _, resource := range request.Resources {
		if resource.ID == "" || resource.ProviderPlanDigest == "" || len(resource.ProviderPlan) == 0 || resource.OwnershipToken == "" {
			return fmt.Errorf("local resource request is incomplete")
		}
		computedPlanDigest := sha256.Sum256(resource.ProviderPlan)
		if resource.ProviderPlanDigest.Hex() != hex.EncodeToString(computedPlanDigest[:]) {
			return fmt.Errorf("local resource provider plan digest does not match its bytes")
		}
		if len(resource.Allocations) == 0 {
			continue
		}
		binding := application.AllocationBinding{InstanceID: request.InstanceID, ResourceID: resource.ID, PlanDigest: resource.ProviderPlanDigest}
		claims, err := application.ValidateAllocationClaims(binding, resource.Allocations)
		if err != nil {
			return fmt.Errorf("validate allocation claims for %s: %w", resource.ID, err)
		}
		for _, claim := range claims {
			if claim.TargetID != request.ProviderTargetID {
				return fmt.Errorf("allocation claim target does not match the instance target")
			}
		}
	}
	return nil
}

type committedClaim struct {
	key     application.AllocationKey
	binding application.AllocationBinding
}

func commitAllocationClaims(ctx context.Context, tx *sql.Tx, request controlplane.CreateInstanceRequest) error {
	claims := make([]committedClaim, 0)
	seen := make(map[string]bool)
	for _, resource := range request.Resources {
		if len(resource.Allocations) == 0 {
			continue
		}
		binding := application.AllocationBinding{InstanceID: request.InstanceID, ResourceID: resource.ID, PlanDigest: resource.ProviderPlanDigest}
		validated, err := application.ValidateAllocationClaims(binding, resource.Allocations)
		if err != nil {
			return err
		}
		for _, key := range validated {
			identity := string(key.TargetID) + "\x00" + key.Namespace + "\x00" + key.Value
			if seen[identity] {
				return fmt.Errorf("provider proposed one allocation claim for multiple resources")
			}
			seen[identity] = true
			claims = append(claims, committedClaim{key: key, binding: binding})
		}
	}
	conflicts := make([]application.AllocationKey, 0)
	for _, candidate := range claims {
		var instanceID, resourceID, planDigest, state string
		err := tx.QueryRowContext(ctx, `SELECT instance_id, resource_id, plan_digest, state
			FROM labcentral_provider_allocation_claims WHERE target_id = ? AND namespace = ? AND claim_value = ?`,
			candidate.key.TargetID, candidate.key.Namespace, candidate.key.Value).Scan(&instanceID, &resourceID, &planDigest, &state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read allocation claim: %w", err)
		}
		sameBinding := instanceID == string(candidate.binding.InstanceID) && resourceID == string(candidate.binding.ResourceID) && planDigest == candidate.binding.PlanDigest.Hex()
		if state != string(application.AllocationReleased) && !sameBinding {
			conflicts = append(conflicts, candidate.key)
		}
	}
	if len(conflicts) > 0 {
		return &application.AllocationConflictError{Conflicts: conflicts}
	}
	for _, candidate := range claims {
		result, err := tx.ExecContext(ctx, `UPDATE labcentral_provider_allocation_claims
			SET instance_id = ?, resource_id = ?, plan_digest = ?, state = ?, fencing_token = 0,
			    created_at_us = `+nowMicros()+`, updated_at_us = `+nowMicros()+`
			WHERE target_id = ? AND namespace = ? AND claim_value = ? AND state = ?`,
			candidate.binding.InstanceID, candidate.binding.ResourceID, candidate.binding.PlanDigest.Hex(), application.AllocationCommitted,
			candidate.key.TargetID, candidate.key.Namespace, candidate.key.Value, application.AllocationReleased)
		if err != nil {
			return fmt.Errorf("reuse released allocation claim: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO labcentral_provider_allocation_claims
			(target_id, namespace, claim_value, instance_id, resource_id, plan_digest, state, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, `+nowMicros()+`, `+nowMicros()+`)`,
			candidate.key.TargetID, candidate.key.Namespace, candidate.key.Value, candidate.binding.InstanceID,
			candidate.binding.ResourceID, candidate.binding.PlanDigest.Hex(), application.AllocationCommitted)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: labcentral_provider_allocation_claims") {
				return &application.AllocationConflictError{Conflicts: []application.AllocationKey{candidate.key}}
			}
			return fmt.Errorf("insert committed allocation claim: %w", err)
		}
	}
	return nil
}

func (s *Store) QueueDestroy(ctx context.Context, request controlplane.QueueDestroyRequest) (controlplane.EnqueueResult, error) {
	if request.InstanceID == "" || request.OperationID == "" || request.SubjectID == "" || request.IdempotencyKey == "" || request.RequestDigest == "" || len(request.Payload) == 0 || request.AuditID == "" || request.EventID == "" {
		return controlplane.EnqueueResult{}, fmt.Errorf("destroy request is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := findIdempotency(ctx, tx, request.SubjectID, "destroy_instance:"+string(request.InstanceID), request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.EnqueueResult{}, err
	} else if found {
		return controlplane.EnqueueResult{InstanceID: request.InstanceID, OperationID: domain.OperationID(existing), Replay: true}, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE labcentral_local_instances
		SET desired_lifecycle = ?, resource_version = resource_version + 1, updated_at_us = `+nowMicros()+`
		WHERE instance_id = ? AND phase <> ?`, domain.LifecycleDestroyed, request.InstanceID, domain.InstanceDestroyed)
	if err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return controlplane.EnqueueResult{}, controlplane.ErrNotFound
	}
	if err := enqueue(ctx, tx, request.InstanceID, request.OperationID, controlplane.OperationDestroy, request.Payload, request.SubjectID, request.AuditID, request.EventID); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if err := bindIdempotency(ctx, tx, request.SubjectID, "destroy_instance:"+string(request.InstanceID), request.IdempotencyKey, request.RequestDigest, request.OperationID); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.EnqueueResult{}, err
	}
	return controlplane.EnqueueResult{InstanceID: request.InstanceID, OperationID: request.OperationID}, nil
}

func enqueue(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, operationID domain.OperationID, kind controlplane.OperationKind, payload []byte, subjectID domain.UserID, auditID, eventID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM labcentral_job_aggregates WHERE aggregate_id = ?", instanceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_job_aggregates
			(aggregate_id, phase, resource_version, event_sequence, operation_state, instance_phase)
			VALUES (?, ?, 0, 0, ?, ?)`, instanceID, "queued", domain.OperationQueued, domain.InstancePending); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_jobs
		(id, operation_id, instance_id, aggregate_id, state, desired_generation, payload_version, payload, available_at_us)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?, `+nowMicros()+`)`, operationID, operationID, instanceID, instanceID, domain.OperationQueued, payload); err != nil {
		return fmt.Errorf("insert local job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_local_operations
		(operation_id, instance_id, kind, message, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, `+nowMicros()+`, `+nowMicros()+`)`, operationID, instanceID, kind, "Queued"); err != nil {
		return fmt.Errorf("insert local operation: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `UPDATE labcentral_job_aggregates
		SET phase = ?, resource_version = resource_version + 1, event_sequence = event_sequence + 1,
		operation_state = ? WHERE aggregate_id = ? RETURNING event_sequence`, "queued", domain.OperationQueued, instanceID).Scan(&sequence); err != nil {
		return err
	}
	eventPayload := []byte(fmt.Sprintf(`{"operationId":%q,"kind":%q,"state":"queued"}`, operationID, kind))
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_audit_intents
		(audit_id, aggregate_id, actor_id, action, created_at_us) VALUES (?, ?, ?, ?, `+nowMicros()+`)`, auditID, instanceID, subjectID, kind); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_job_events
		(event_id, aggregate_id, aggregate_sequence, event_type, payload, created_at_us)
		VALUES (?, ?, ?, ?, ?, `+nowMicros()+`)`, eventID, instanceID, sequence, "operation.queued", eventPayload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_outbox_deliveries
		(event_id, sink, payload, available_at_us) VALUES (?, 'local-events', ?, `+nowMicros()+`)`, eventID, eventPayload); err != nil {
		return err
	}
	return nil
}

func findIdempotency(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, digest domain.SHA256Digest) (string, bool, error) {
	var recordedDigest, resultID string
	err := tx.QueryRowContext(ctx, `SELECT request_digest, result_id FROM labcentral_idempotency_records
		WHERE subject_id = ? AND route_action = ? AND idempotency_key = ?`, subject, action, key).Scan(&recordedDigest, &resultID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if recordedDigest != digest.Hex() {
		return "", false, jobsdb.ErrIdempotencyConflict
	}
	return resultID, true, nil
}

func bindIdempotency(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, digest domain.SHA256Digest, resultID domain.OperationID) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO labcentral_idempotency_records
		(subject_id, route_action, idempotency_key, request_digest, result_id, expires_at_us)
		VALUES (?, ?, ?, ?, ?, `+nowMicros()+` + ?)`, subject, action, key, digest.Hex(), resultID, (24 * time.Hour).Microseconds())
	return err
}

func (s *Store) GetInstance(ctx context.Context, id domain.InstanceID) (controlplane.InstanceRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT instance_id, provider_target_id, provider_override_json, canonical_digest, canonical_lab_json, source_yaml, phase,
		desired_lifecycle, generation, resource_version, isolation_warning_acknowledged, outputs_json,
		error_code, error_message, created_at_us, updated_at_us
		FROM labcentral_local_instances WHERE instance_id = ?`, id)
	return scanInstance(row)
}

func (s *Store) ListInstances(ctx context.Context) ([]controlplane.InstanceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, provider_target_id, provider_override_json, canonical_digest, canonical_lab_json, source_yaml, phase,
		desired_lifecycle, generation, resource_version, isolation_warning_acknowledged, outputs_json,
		error_code, error_message, created_at_us, updated_at_us
		FROM labcentral_local_instances ORDER BY created_at_us, instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []controlplane.InstanceRecord
	for rows.Next() {
		record, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanInstance(row scanner) (controlplane.InstanceRecord, error) {
	var record controlplane.InstanceRecord
	var id, targetID, digest, phase, lifecycle string
	var generation, resourceVersion, acknowledged, createdAt, updatedAt int64
	err := row.Scan(&id, &targetID, &record.ProviderOverrideJSON, &digest, &record.CanonicalLabJSON, &record.SourceYAML, &phase, &lifecycle,
		&generation, &resourceVersion, &acknowledged, &record.OutputsJSON, &record.ErrorCode, &record.ErrorMessage, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.InstanceRecord{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.InstanceRecord{}, err
	}
	parsedDigest, err := domain.ParseSHA256Digest(digest)
	if err != nil {
		return controlplane.InstanceRecord{}, err
	}
	record.ID = domain.InstanceID(id)
	record.ProviderTargetID = domain.ProviderTargetID(targetID)
	record.CanonicalDigest = parsedDigest
	record.Phase = domain.InstancePhase(phase)
	record.DesiredLifecycle = domain.DesiredLifecycle(lifecycle)
	record.Generation = domain.Generation(generation)
	record.ResourceVersion = domain.ResourceVersion(resourceVersion)
	record.IsolationWarningAcknowledged = acknowledged == 1
	record.CreatedAt = time.UnixMicro(createdAt).UTC()
	record.UpdatedAt = time.UnixMicro(updatedAt).UTC()
	return record, nil
}

func (s *Store) ListResources(ctx context.Context, instanceID domain.InstanceID) ([]controlplane.ResourceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, resource_id, kind, ordinal, canonical_resource_json, provider_plan, phase,
		ownership_token, provider_state, observed_state_json, error_code, error_message, updated_at_us
		FROM labcentral_local_resources WHERE instance_id = ? ORDER BY ordinal`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []controlplane.ResourceRecord
	for rows.Next() {
		var record controlplane.ResourceRecord
		var id, resourceID, kind, phase string
		var providerState []byte
		var updatedAt int64
		if err := rows.Scan(&id, &resourceID, &kind, &record.Ordinal, &record.CanonicalJSON, &record.ProviderPlan, &phase,
			&record.OwnershipToken, &providerState, &record.ObservedStateJSON, &record.ErrorCode, &record.ErrorMessage, &updatedAt); err != nil {
			return nil, err
		}
		record.InstanceID = domain.InstanceID(id)
		record.ID = domain.LogicalResourceID(resourceID)
		record.Kind = domain.ResourceKind(kind)
		record.Phase = domain.ResourcePhase(phase)
		record.ProviderState = append([]byte(nil), providerState...)
		record.UpdatedAt = time.UnixMicro(updatedAt).UTC()
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) GetOperation(ctx context.Context, id domain.OperationID) (controlplane.OperationRecord, error) {
	var record controlplane.OperationRecord
	var operationID, instanceID, kind, state string
	var generation, attempt, createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT operation.operation_id, operation.instance_id, operation.kind, job.state,
		job.desired_generation, job.attempt, operation.message, operation.error_code, operation.created_at_us, operation.updated_at_us
		FROM labcentral_local_operations operation JOIN labcentral_jobs job ON job.operation_id = operation.operation_id
		WHERE operation.operation_id = ?`, id).Scan(&operationID, &instanceID, &kind, &state, &generation, &attempt,
		&record.Message, &record.ErrorCode, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.OperationRecord{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.OperationRecord{}, err
	}
	record.ID = domain.OperationID(operationID)
	record.InstanceID = domain.InstanceID(instanceID)
	record.Kind = controlplane.OperationKind(kind)
	record.State = domain.OperationState(state)
	record.Generation = domain.Generation(generation)
	record.Attempt = uint32(attempt)
	record.CreatedAt = time.UnixMicro(createdAt).UTC()
	record.UpdatedAt = time.UnixMicro(updatedAt).UTC()
	return record, nil
}

func (s *Store) Claim(ctx context.Context, owner string, duration time.Duration) (controlplane.WorkClaim, error) {
	claim, err := s.jobs.ClaimJob(ctx, owner, duration)
	if errors.Is(err, jobsdb.ErrNoClaimableJob) {
		return controlplane.WorkClaim{}, controlplane.ErrNoWork
	}
	if err != nil {
		return controlplane.WorkClaim{}, err
	}
	return controlplane.WorkClaim{Operation: claim.Operation, LeaseOwner: claim.LeaseOwner, Payload: claim.Payload}, nil
}

func (s *Store) Renew(ctx context.Context, fence controlplane.Fence, duration time.Duration) error {
	return s.jobs.RenewLease(ctx, fence.OperationID, fence.LeaseOwner, fence.Token, duration)
}

func (s *Store) RetainClaims(ctx context.Context, request application.MutateAllocationClaimsRequest) error {
	return s.jobs.RetainClaims(ctx, request)
}

func (s *Store) OrphanClaims(ctx context.Context, request application.MutateAllocationClaimsRequest) error {
	return s.jobs.OrphanClaims(ctx, request)
}

func (s *Store) ReleaseClaims(ctx context.Context, request application.ReleaseAllocationClaimsRequest) error {
	return s.jobs.ReleaseClaims(ctx, request)
}

func (s *Store) ListClaims(ctx context.Context, instanceID domain.InstanceID) ([]application.AllocationClaim, error) {
	return s.jobs.ListClaims(ctx, instanceID)
}

func (s *Store) MarkInstancePhase(ctx context.Context, fence controlplane.Fence, phase domain.InstancePhase, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireFence(ctx, tx, fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE labcentral_local_instances SET phase = ?, updated_at_us = `+nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)`, phase, fence.OperationID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE labcentral_local_operations SET message = ?, updated_at_us = `+nowMicros()+`
		WHERE operation_id = ?`, message, fence.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PrepareInvocation(ctx context.Context, request controlplane.PrepareInvocationRequest) (controlplane.InvocationRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.InvocationRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireFence(ctx, tx, request.Fence); err != nil {
		return controlplane.InvocationRecord{}, err
	}
	var existing controlplane.InvocationRecord
	var operationID, resourceID, digest string
	err = tx.QueryRowContext(ctx, `SELECT invocation_id, operation_id, resource_id, action, idempotency_key, request_digest, state, provider_result
		FROM labcentral_runtime_invocations WHERE operation_id = ? AND resource_id = ? AND action = ?`,
		request.Fence.OperationID, request.ResourceID, request.Action).Scan(&existing.ID, &operationID, &resourceID, &existing.Action,
		&existing.IdempotencyKey, &digest, &existing.State, &existing.ProviderResult)
	if err == nil {
		if digest != request.RequestDigest.Hex() || existing.IdempotencyKey != request.IdempotencyKey {
			return controlplane.InvocationRecord{}, jobsdb.ErrIdempotencyConflict
		}
		existing.OperationID = domain.OperationID(operationID)
		existing.ResourceID = domain.LogicalResourceID(resourceID)
		existing.RequestDigest, _ = domain.ParseSHA256Digest(digest)
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return controlplane.InvocationRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_runtime_invocations
		(invocation_id, operation_id, resource_id, action, idempotency_key, request_digest, state, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, 'prepared', `+nowMicros()+`, `+nowMicros()+`)`, request.InvocationID, request.Fence.OperationID,
		request.ResourceID, request.Action, request.IdempotencyKey, request.RequestDigest.Hex()); err != nil {
		return controlplane.InvocationRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.InvocationRecord{}, err
	}
	return controlplane.InvocationRecord{ID: request.InvocationID, OperationID: request.Fence.OperationID, ResourceID: request.ResourceID,
		Action: request.Action, IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest, State: "prepared"}, nil
}

func (s *Store) SaveResourceResult(ctx context.Context, update controlplane.ResourceResultUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireFence(ctx, tx, update.Fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE labcentral_local_resources
		SET phase = ?, provider_state = ?, observed_state_json = ?, error_code = ?, error_message = ?, updated_at_us = `+nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?) AND resource_id = ?`,
		update.Phase, nullableBytes(update.ProviderState), defaultJSON(update.ObservedStateJSON), update.ErrorCode, update.ErrorMessage,
		update.Fence.OperationID, update.ResourceID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	resultBytes := append([]byte(nil), update.ProviderState...)
	if len(update.ObservedStateJSON) > 0 {
		resultBytes = append(resultBytes, update.ObservedStateJSON...)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE labcentral_runtime_invocations
		SET state = ?, provider_result = ?, updated_at_us = `+nowMicros()+`
		WHERE invocation_id = ? AND operation_id = ?`, update.InvocationState, resultBytes, update.InvocationID, update.Fence.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveCoreResource(ctx context.Context, fence controlplane.Fence, resourceID domain.LogicalResourceID, phase domain.ResourcePhase, observed []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireFence(ctx, tx, fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE labcentral_local_resources
		SET phase = ?, observed_state_json = ?, error_code = '', error_message = '', updated_at_us = `+nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?) AND resource_id = ?`,
		phase, defaultJSON(observed), fence.OperationID, resourceID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	return tx.Commit()
}

func (s *Store) Finalize(ctx context.Context, request controlplane.FinalizeRequest) error {
	if !request.State.Terminal() {
		return fmt.Errorf("final operation state must be terminal")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireFence(ctx, tx, request.Fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE labcentral_jobs SET state = ?, checkpoint = ?
		WHERE operation_id = ? AND lease_owner = ? AND fencing_token = ? AND desired_generation = ? AND attempt = ?`,
		request.State, []byte(request.Message), request.Fence.OperationID, request.Fence.LeaseOwner, request.Fence.Token,
		request.Fence.Generation, request.Fence.Attempt)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE labcentral_local_operations SET message = ?, error_code = ?, updated_at_us = `+nowMicros()+`
		WHERE operation_id = ?`, request.Message, request.ErrorCode, request.Fence.OperationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE labcentral_local_instances
		SET phase = ?, outputs_json = ?, error_code = ?, error_message = ?, resource_version = resource_version + 1,
		updated_at_us = `+nowMicros()+` WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)`,
		request.InstancePhase, defaultJSON(request.OutputsJSON), request.ErrorCode, request.ErrorMessage, request.Fence.OperationID); err != nil {
		return err
	}
	var instanceID string
	var sequence int64
	if err := tx.QueryRowContext(ctx, `UPDATE labcentral_job_aggregates SET phase = ?, operation_state = ?, instance_phase = ?,
		resource_version = resource_version + 1, event_sequence = event_sequence + 1
		WHERE aggregate_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)
		RETURNING aggregate_id, event_sequence`, "complete", request.State, request.InstancePhase, request.Fence.OperationID).Scan(&instanceID, &sequence); err != nil {
		return err
	}
	eventPayload := []byte(fmt.Sprintf(`{"operationId":%q,"kind":%q,"state":%q,"instancePhase":%q}`, request.Fence.OperationID, request.OperationKind, request.State, request.InstancePhase))
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_audit_intents
		(audit_id, aggregate_id, actor_id, action, created_at_us) VALUES (?, ?, 'local-admin', ?, `+nowMicros()+`)`, request.AuditID, instanceID, request.OperationKind+".complete"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_job_events
		(event_id, aggregate_id, aggregate_sequence, event_type, payload, created_at_us)
		VALUES (?, ?, ?, 'operation.completed', ?, `+nowMicros()+`)`, request.EventID, instanceID, sequence, eventPayload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO labcentral_outbox_deliveries
		(event_id, sink, payload, available_at_us) VALUES (?, 'local-events', ?, `+nowMicros()+`)`, request.EventID, eventPayload); err != nil {
		return err
	}
	return tx.Commit()
}

func requireFence(ctx context.Context, tx *sql.Tx, fence controlplane.Fence) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_jobs WHERE operation_id = ? AND state = ?
		AND lease_owner = ? AND fencing_token = ? AND desired_generation = ? AND attempt = ?`, fence.OperationID,
		domain.OperationRunning, fence.LeaseOwner, fence.Token, fence.Generation, fence.Attempt).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return jobsdb.ErrFencingConflict
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func defaultJSON(value []byte) []byte {
	if len(value) == 0 {
		return []byte("{}")
	}
	return value
}

func (s *Store) Close() error {
	var failures []string
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if s.lockFile != nil {
		if err := unix.Flock(int(s.lockFile.Fd()), unix.LOCK_UN); err != nil {
			failures = append(failures, err.Error())
		}
		if err := s.lockFile.Close(); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("close local database: %s", strings.Join(failures, "; "))
	}
	return nil
}

var _ controlplane.Store = (*Store)(nil)
