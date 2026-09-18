package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	migrationID                       = "0002_local_vertical_slice"
	targetMigrationID                 = "0003_local_provider_target_binding"
	authMigrationID                   = "0004_identity_tenancy_rbac"
	authHardeningMigrationID          = "0005_authentication_hardening"
	authLifecycleMigrationID          = "0006_authority_lifecycle"
	templateMigrationID               = "0007_classroom_governance_foundation"
	auditMigrationID                  = "0008_governance_audit_events"
	quotaMigrationID                  = "0009_classroom_quota_expiration"
	batchMigrationID                  = "0010_classroom_instance_batches"
	accessMigrationID                 = "0011_controlled_access_sessions"
	templateRepresentationMigrationID = "0012_template_representations"
	migrationLockID                   = int64(0x4c616243656e7472)
)

type Store struct {
	db       *sql.DB
	jobs     *jobsdb.Store
	dialect  jobsdb.Dialect
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

// OpenPostgreSQL opens and migrates the production control-plane store. Input
// validation errors do not echo the supplied DSN.
func OpenPostgreSQL(ctx context.Context, dsn string) (*Store, error) {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
		return nil, fmt.Errorf("PostgreSQL DSN must be a postgres URL with host and database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL control-plane database: %w", err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL control-plane database: %w", err)
	}
	store, err := NewPostgreSQL(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func New(db *sql.DB) (*Store, error) {
	return newStore(db, jobsdb.DialectSQLite)
}

// NewPostgreSQL constructs the complete control-plane store on an existing
// PostgreSQL connection pool. The caller retains no ownership of db; Close
// closes the pool just like the SQLite constructor.
func NewPostgreSQL(db *sql.DB) (*Store, error) {
	return newStore(db, jobsdb.DialectPostgreSQL)
}

func newStore(db *sql.DB, dialect jobsdb.Dialect) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("control-plane database is nil")
	}
	if dialect == jobsdb.DialectSQLite {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	jobs, err := jobsdb.New(db, dialect)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, jobs: jobs, dialect: dialect}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	unlock, err := s.lockMigrations(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if s.dialect == jobsdb.DialectSQLite {
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("enable SQLite foreign keys: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			return fmt.Errorf("enable SQLite WAL: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			return fmt.Errorf("set SQLite busy timeout: %w", err)
		}
	}
	if err := s.jobs.Migrate(ctx); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, migrationID, s.migrationStatements()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, targetMigrationID, s.targetMigrationStatements()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, authMigrationID, s.authMigrationStatements()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, authHardeningMigrationID, authenticationHardeningMigrationStatements(s.integerType())); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, authLifecycleMigrationID, authenticationLifecycleMigrationStatements(s.integerType())); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, templateMigrationID, s.templateMigrationStatements()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, auditMigrationID, auditMigrationStatements(s.integerType(), s.blobType())); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, quotaMigrationID, s.quotaMigrationStatements()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, batchMigrationID, instanceBatchMigrationStatements(s.integerType())); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, accessMigrationID, accessSessionMigrationStatements(s.integerType(), s.blobType())); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, templateRepresentationMigrationID, []string{"ALTER TABLE labcentral_template_revisions ADD COLUMN representation_json TEXT NOT NULL DEFAULT ''"}); err != nil {
		return err
	}

	if err := s.applyMigration(ctx, "0013_target_deployment_settings", []string{"CREATE TABLE labcentral_target_deployment_settings (target_id TEXT PRIMARY KEY, maximum BIGINT NOT NULL CHECK (maximum BETWEEN 1 AND 16), resource_version BIGINT NOT NULL)"}); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, "0014_extensible_access", s.extensibleAccessMigration()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, "0015_extension_work", extensionWorkMigration()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, "0016_gateway_delegation", gatewayMigration()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, "0017_build_orchestration", buildMigration()); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, "0018_template_libraries", libraryMigration()); err != nil {
		return err
	}
	return s.backfillLegacyInstanceQuota(ctx)
}

func (s *Store) lockMigrations(ctx context.Context) (func(), error) {
	if s.dialect != jobsdb.DialectPostgreSQL {
		return func() {}, nil
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL migration connection: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("acquire PostgreSQL migration lock: %w", err)
	}
	return func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.ExecContext(unlockContext, "SELECT pg_advisory_unlock($1)", migrationLockID)
		_ = connection.Close()
	}, nil
}

func (s *Store) applyMigration(ctx context.Context, id string, statements []string) error {
	checksum := checksum(statements)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	var recorded string
	err = tx.QueryRowContext(ctx, s.bind("SELECT checksum FROM labcentral_schema_migrations WHERE id = ?"), id).Scan(&recorded)
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
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_schema_migrations (id, checksum, applied_at_us)
		VALUES (?, ?, `+s.nowMicros()+`)`), id, checksum); err != nil {
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

func (s *Store) targetMigrationStatements() []string {
	if s.dialect == jobsdb.DialectSQLite {
		return targetMigrationStatements()
	}
	return []string{
		`ALTER TABLE labcentral_local_instances
			ADD COLUMN provider_target_id TEXT NOT NULL DEFAULT 'target.local-docker'`,
		`ALTER TABLE labcentral_local_instances
			ADD COLUMN provider_override_json BYTEA NOT NULL DEFAULT '\x'::bytea`,
		`CREATE TABLE labcentral_local_provider_targets (
			target_id TEXT PRIMARY KEY,
			provider_kind TEXT NOT NULL,
			config_fingerprint TEXT NOT NULL,
			protocol_major INTEGER NOT NULL,
			protocol_minor INTEGER NOT NULL,
			features_json BYTEA NOT NULL,
			discovery_json BYTEA NOT NULL,
			discovered_at_us BIGINT NOT NULL,
			valid_until_us BIGINT NOT NULL,
			updated_at_us BIGINT NOT NULL
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

func (s *Store) migrationStatements() []string {
	if s.dialect == jobsdb.DialectSQLite {
		return migrationStatements()
	}
	return []string{
		`CREATE TABLE labcentral_local_instances (
			instance_id TEXT PRIMARY KEY,
			canonical_digest TEXT NOT NULL,
			canonical_lab_json BYTEA NOT NULL,
			source_yaml BYTEA NOT NULL,
			phase TEXT NOT NULL,
			desired_lifecycle TEXT NOT NULL,
			generation BIGINT NOT NULL CHECK (generation > 0),
			resource_version BIGINT NOT NULL CHECK (resource_version > 0),
			isolation_warning_acknowledged INTEGER NOT NULL CHECK (isolation_warning_acknowledged IN (0, 1)),
			outputs_json BYTEA NOT NULL DEFAULT '{}'::bytea,
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at_us BIGINT NOT NULL,
			updated_at_us BIGINT NOT NULL
		)`,
		`CREATE TABLE labcentral_local_resources (
			instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),
			resource_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
			canonical_resource_json BYTEA NOT NULL,
			provider_plan BYTEA NOT NULL,
			phase TEXT NOT NULL,
			ownership_token TEXT NOT NULL,
			provider_state BYTEA,
			observed_state_json BYTEA NOT NULL DEFAULT '{}'::bytea,
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			updated_at_us BIGINT NOT NULL,
			PRIMARY KEY (instance_id, resource_id),
			UNIQUE (instance_id, ordinal)
		)`,
		`CREATE TABLE labcentral_local_operations (
			operation_id TEXT PRIMARY KEY REFERENCES labcentral_jobs(operation_id),
			instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),
			kind TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			created_at_us BIGINT NOT NULL,
			updated_at_us BIGINT NOT NULL
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
			provider_result BYTEA,
			created_at_us BIGINT NOT NULL,
			updated_at_us BIGINT NOT NULL,
			UNIQUE (operation_id, resource_id, action)
		)`,
	}
}

func authMigrationStatements(blobType, integerType string) []string {
	return []string{
		`CREATE TABLE labcentral_installation_settings (
			setting_key TEXT PRIMARY KEY,
			setting_value TEXT NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE TABLE labcentral_users (
			user_id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
			created_at_us ` + integerType + ` NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE TABLE labcentral_auth_identities (
			issuer TEXT NOT NULL,
			subject TEXT NOT NULL,
			user_id TEXT NOT NULL REFERENCES labcentral_users(user_id),
			created_at_us ` + integerType + ` NOT NULL,
			PRIMARY KEY (issuer, subject),
			UNIQUE (issuer, user_id)
		)`,
		`CREATE TABLE labcentral_sessions (
			session_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES labcentral_users(user_id),
			token_hash TEXT NOT NULL UNIQUE,
			csrf_hash TEXT NOT NULL,
			key_id TEXT NOT NULL,
			expires_at_us ` + integerType + ` NOT NULL,
			rotated_from_id TEXT NOT NULL DEFAULT '',
			revoked_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE INDEX labcentral_sessions_user_expiry_idx ON labcentral_sessions(user_id, expires_at_us)`,
		`CREATE TABLE labcentral_password_recovery_tokens (
			token_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES labcentral_users(user_id),
			token_hash TEXT NOT NULL UNIQUE,
			key_id TEXT NOT NULL,
			expires_at_us ` + integerType + ` NOT NULL,
			consumed_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE TABLE labcentral_organizations (
			organization_id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			created_at_us ` + integerType + ` NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE TABLE labcentral_projects (
			project_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL REFERENCES labcentral_organizations(organization_id),
			name TEXT NOT NULL,
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			created_at_us ` + integerType + ` NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL,
			UNIQUE (organization_id, name),
			UNIQUE (project_id, organization_id)
		)`,
		`CREATE TABLE labcentral_teams (
			team_id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL REFERENCES labcentral_organizations(organization_id),
			name TEXT NOT NULL,
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			created_at_us ` + integerType + ` NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL,
			UNIQUE (organization_id, name),
			UNIQUE (team_id, organization_id)
		)`,
		`CREATE TABLE labcentral_memberships (
			membership_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES labcentral_users(user_id),
			organization_id TEXT NOT NULL REFERENCES labcentral_organizations(organization_id),
			team_id TEXT,
			valid_from_us ` + integerType + ` NOT NULL,
			valid_until_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL,
			FOREIGN KEY (team_id, organization_id) REFERENCES labcentral_teams(team_id, organization_id)
		)`,
		`CREATE UNIQUE INDEX labcentral_memberships_unique_idx ON labcentral_memberships(user_id, organization_id, COALESCE(team_id, ''))`,
		`CREATE TABLE labcentral_role_assignments (
			assignment_id TEXT PRIMARY KEY,
			subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user', 'team')),
			subject_id TEXT NOT NULL,
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('platform', 'organization', 'project', 'team')),
			scope_id TEXT NOT NULL,
			role TEXT NOT NULL,
			valid_from_us ` + integerType + ` NOT NULL,
			valid_until_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL,
			UNIQUE (subject_kind, subject_id, scope_kind, scope_id, role)
		)`,
		`CREATE INDEX labcentral_role_assignments_subject_idx ON labcentral_role_assignments(subject_kind, subject_id, valid_from_us, valid_until_us)`,
		`CREATE TABLE labcentral_provider_target_grants (
			grant_id TEXT PRIMARY KEY,
			target_id TEXT NOT NULL,
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('platform', 'organization', 'project', 'team')),
			scope_id TEXT NOT NULL,
			valid_from_us ` + integerType + ` NOT NULL,
			valid_until_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL,
			UNIQUE (target_id, scope_kind, scope_id)
		)`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN project_id TEXT NOT NULL DEFAULT 'project.local'`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN owner_kind TEXT NOT NULL DEFAULT 'user' CHECK (owner_kind IN ('user', 'team', 'project'))`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN owner_id TEXT NOT NULL DEFAULT 'local-admin'`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN created_by TEXT NOT NULL DEFAULT 'local-admin'`,
		`CREATE INDEX labcentral_local_instances_scope_idx ON labcentral_local_instances(project_id, owner_kind, owner_id, created_at_us, instance_id)`,
		`ALTER TABLE labcentral_local_operations ADD COLUMN actor_id TEXT NOT NULL DEFAULT 'local-admin'`,
		`CREATE TABLE labcentral_oidc_flows (
			flow_id TEXT PRIMARY KEY,
			state_hash TEXT NOT NULL UNIQUE,
			browser_hash TEXT NOT NULL,
			nonce_hash TEXT NOT NULL,
			pkce_verifier ` + blobType + ` NOT NULL,
			key_id TEXT NOT NULL,
			redirect_path TEXT NOT NULL,
			expires_at_us ` + integerType + ` NOT NULL,
			consumed_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			created_at_us ` + integerType + ` NOT NULL
		)`,
	}
}

func (s *Store) authMigrationStatements() []string {
	if s.dialect == jobsdb.DialectPostgreSQL {
		return authMigrationStatements("BYTEA", "BIGINT")
	}
	return authMigrationStatements("BLOB", "INTEGER")
}

func (s *Store) integerType() string {
	if s.dialect == jobsdb.DialectPostgreSQL {
		return "BIGINT"
	}
	return "INTEGER"
}

func (s *Store) blobType() string {
	if s.dialect == jobsdb.DialectPostgreSQL {
		return "BYTEA"
	}
	return "BLOB"
}

func (s *Store) templateMigrationStatements() []string {
	integerType := s.integerType()
	statements := []string{
		`INSERT INTO labcentral_organizations
			(organization_id, name, resource_version, created_at_us, updated_at_us)
			VALUES ('org.local', 'labcentral-internal-local', 1, ` + s.nowMicros() + `, ` + s.nowMicros() + `)
			ON CONFLICT (organization_id) DO NOTHING`,
		`INSERT INTO labcentral_projects
			(project_id, organization_id, name, resource_version, created_at_us, updated_at_us)
			VALUES ('project.local', 'org.local', 'labcentral-internal-local', 1, ` + s.nowMicros() + `, ` + s.nowMicros() + `)
			ON CONFLICT (project_id) DO NOTHING`,
		`CREATE TABLE labcentral_lab_templates (
			template_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES labcentral_projects(project_id),
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			created_by TEXT NOT NULL,
			created_at_us ` + integerType + ` NOT NULL,
			updated_at_us ` + integerType + ` NOT NULL,
			UNIQUE (project_id, name)
		)`,
		`CREATE INDEX labcentral_lab_templates_scope_idx
			ON labcentral_lab_templates(project_id, created_at_us, template_id)`,
		`CREATE TABLE labcentral_template_revisions (
			revision_id TEXT PRIMARY KEY,
			template_id TEXT NOT NULL REFERENCES labcentral_lab_templates(template_id),
			revision_number ` + integerType + ` NOT NULL CHECK (revision_number > 0),
			bundle_artifact_ref TEXT NOT NULL,
			bundle_digest TEXT NOT NULL,
			bundle_size_bytes ` + integerType + ` NOT NULL CHECK (bundle_size_bytes > 0),
			author_api_version TEXT NOT NULL,
			renderer_version TEXT NOT NULL,
			canonical_version TEXT NOT NULL,
			canonical_digest TEXT NOT NULL,
			published_by TEXT NOT NULL,
			created_at_us ` + integerType + ` NOT NULL,
			UNIQUE (template_id, revision_number)
		)`,
		`CREATE INDEX labcentral_template_revisions_template_idx
			ON labcentral_template_revisions(template_id, revision_number, revision_id)`,
		`ALTER TABLE labcentral_local_instances
			ADD COLUMN revision_id TEXT REFERENCES labcentral_template_revisions(revision_id)`,
	}
	if s.dialect == jobsdb.DialectPostgreSQL {
		return append(statements,
			`CREATE FUNCTION labcentral_reject_template_revision_mutation() RETURNS trigger
				LANGUAGE plpgsql AS $$ BEGIN
					RAISE EXCEPTION 'template revisions are immutable' USING ERRCODE = '55000';
				END $$`,
			`CREATE TRIGGER labcentral_template_revisions_immutable
				BEFORE UPDATE OR DELETE ON labcentral_template_revisions
				FOR EACH ROW EXECUTE FUNCTION labcentral_reject_template_revision_mutation()`,
		)
	}
	return append(statements,
		`CREATE TRIGGER labcentral_template_revisions_immutable_update
			BEFORE UPDATE ON labcentral_template_revisions
			BEGIN SELECT RAISE(ABORT, 'template revisions are immutable'); END`,
		`CREATE TRIGGER labcentral_template_revisions_immutable_delete
			BEFORE DELETE ON labcentral_template_revisions
			BEGIN SELECT RAISE(ABORT, 'template revisions are immutable'); END`,
	)
}

func authenticationHardeningMigrationStatements(integerType string) []string {
	return []string{
		`CREATE INDEX labcentral_sessions_expiry_gc_idx ON labcentral_sessions(expires_at_us)`,
		`CREATE INDEX labcentral_password_recovery_expiry_gc_idx ON labcentral_password_recovery_tokens(expires_at_us)`,
		`CREATE INDEX labcentral_oidc_flows_expiry_gc_idx ON labcentral_oidc_flows(expires_at_us)`,
		`CREATE TABLE labcentral_authentication_attempts (
			attempt_key TEXT PRIMARY KEY,
			window_started_at_us ` + integerType + ` NOT NULL,
			attempt_count ` + integerType + ` NOT NULL CHECK (attempt_count > 0),
			updated_at_us ` + integerType + ` NOT NULL
		)`,
		`CREATE INDEX labcentral_authentication_attempts_updated_idx ON labcentral_authentication_attempts(updated_at_us)`,
	}
}

func authenticationLifecycleMigrationStatements(integerType string) []string {
	return []string{
		`ALTER TABLE labcentral_users ADD COLUMN resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0)`,
		`ALTER TABLE labcentral_memberships ADD COLUMN resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0)`,
		`ALTER TABLE labcentral_role_assignments ADD COLUMN resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0)`,
		`ALTER TABLE labcentral_provider_target_grants ADD COLUMN resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0)`,
		`ALTER TABLE labcentral_sessions ADD COLUMN credential_kind TEXT NOT NULL DEFAULT 'browser' CHECK (credential_kind IN ('browser', 'bearer'))`,
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

func (s *Store) bind(query string) string {
	if s.dialect != jobsdb.DialectPostgreSQL {
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
	if s.dialect == jobsdb.DialectPostgreSQL {
		return "CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000 AS BIGINT)"
	}
	return "(CAST(strftime('%s', 'now') AS INTEGER) * 1000000 + CAST(substr(strftime('%f', 'now'), 4, 3) AS INTEGER) * 1000)"
}

func (s *Store) CreateInstance(ctx context.Context, request application.CreateInstanceRequest) (application.EnqueueResult, error) {
	if err := validateCreate(request); err != nil {
		return application.EnqueueResult{}, err
	}
	for attempt := 0; ; attempt++ {
		result, err := s.createInstance(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return result, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.EnqueueResult{}, err
		}
	}
}

func (s *Store) createInstance(ctx context.Context, request application.CreateInstanceRequest) (application.EnqueueResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.SubjectID, "create_instance", request.IdempotencyKey); err != nil {
		return application.EnqueueResult{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.SubjectID, "create_instance", request.IdempotencyKey, request.RequestDigest); err != nil {
		return application.EnqueueResult{}, err
	} else if found {
		var instanceID string
		if err := tx.QueryRowContext(ctx, s.bind("SELECT instance_id FROM labcentral_local_operations WHERE operation_id = ?"), existing).Scan(&instanceID); err != nil {
			return application.EnqueueResult{}, err
		}
		return application.EnqueueResult{InstanceID: domain.InstanceID(instanceID), OperationID: domain.OperationID(existing), Replay: true}, tx.Commit()
	}
	if err := s.insertInstanceTx(ctx, tx, request); err != nil {
		return application.EnqueueResult{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.SubjectID, "create_instance", request.IdempotencyKey, request.RequestDigest, string(request.OperationID)); err != nil {
		return application.EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.EnqueueResult{}, err
	}
	return application.EnqueueResult{InstanceID: request.InstanceID, OperationID: request.OperationID}, nil
}

// insertInstanceTx persists one independently owned instance and its complete
// scheduling unit. Callers are responsible for transaction-level
// idempotency; all other writes, including quota and allocation reservations,
// are atomic with the enclosing transaction.
func (s *Store) insertInstanceTx(ctx context.Context, tx *sql.Tx, request application.CreateInstanceRequest) error {
	if request.WorkFence != nil {
		if e := s.requireExtensionFenceTx(ctx, tx, *request.WorkFence); e != nil {
			return e
		}
	}
	for _, id := range request.ImageBindingIDs {
		var count int
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_image_bindings WHERE binding_id=? AND project_id=? AND target_id=? AND approved=1 AND retired=0`), id, request.ProjectID, request.ProviderTargetID).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			return fmt.Errorf("image binding approval changed before admission")
		}
	}

	if err := s.validateInstanceScope(ctx, tx, request); err != nil {
		return err
	}
	lifetime, err := s.reserveInstanceQuotaTx(ctx, tx, request)
	if err != nil {
		return err
	}
	ack := 0
	if request.Acknowledged {
		ack = 1
	}
	providerOverride := request.ProviderOverrideJSON
	if providerOverride == nil {
		providerOverride = []byte{}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_local_instances
		(instance_id, revision_id, project_id, owner_kind, owner_id, created_by, provider_target_id, provider_override_json, canonical_digest, canonical_lab_json, source_yaml, phase, desired_lifecycle, generation, resource_version,
		 isolation_warning_acknowledged, expires_at_us, maximum_expires_at_us, expiration_warning_at_us,
		 expiration_grace_seconds, lifetime_renewal_allowed, lifetime_renewal_window_seconds, lifetime_policy_digest,
		 created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`),
		request.InstanceID, nullableString(string(request.RevisionID)), request.ProjectID, request.OwnerKind, request.OwnerID, request.CreatedBy,
		request.ProviderTargetID, providerOverride, request.CanonicalDigest.Hex(), request.CanonicalJSON, request.SourceYAML,
		domain.InstancePending, domain.LifecycleRunning, ack, timeMicrosOrZero(lifetime.ExpiresAt),
		timeMicrosOrZero(lifetime.MaximumExpiresAt), timeMicrosOrZero(lifetime.ExpirationWarningAt),
		lifetime.DestructionGraceSeconds, quotaBoolean(lifetime.RenewalAllowed), lifetime.RenewalWindowSeconds,
		lifetime.PolicyDigest.Hex()); err != nil {
		return fmt.Errorf("insert local instance: %w", err)
	}
	for _, resource := range request.Resources {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_local_resources
			(instance_id, resource_id, kind, ordinal, canonical_resource_json, provider_plan, phase, ownership_token, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), request.InstanceID, resource.ID, resource.Kind, resource.Ordinal,
			resource.CanonicalJSON, resource.ProviderPlan, domain.ResourcePending, resource.OwnershipToken); err != nil {
			return fmt.Errorf("insert local resource %s: %w", resource.ID, err)
		}
	}
	if err := s.commitAllocationClaims(ctx, tx, request); err != nil {
		return err
	}
	if err := s.enqueue(ctx, tx, request.InstanceID, request.OperationID, application.OperationApply, request.Payload, request.SubjectID, request.AuditID, request.EventID); err != nil {
		return err
	}
	return nil
}

func (s *Store) validateInstanceScope(ctx context.Context, tx *sql.Tx, request application.CreateInstanceRequest) error {
	if request.RevisionID != "" {
		if _, err := domain.NewRevisionID(string(request.RevisionID)); err != nil {
			return err
		}
		var revisionExists int
		err := tx.QueryRowContext(ctx, s.bind(`SELECT 1
			FROM labcentral_template_revisions revision
			JOIN labcentral_lab_templates template ON template.template_id = revision.template_id
			WHERE revision.revision_id = ? AND template.project_id = ?`), request.RevisionID, request.ProjectID).Scan(&revisionExists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("instance template revision is outside the project scope")
		}
		if err != nil {
			return fmt.Errorf("load instance template revision: %w", err)
		}
	}
	if request.ProjectID == "project.local" && request.OwnerKind == application.OwnerUser && request.OwnerID == string(application.LocalAdminID) && request.CreatedBy == application.LocalAdminID {
		return nil
	}
	var organizationID string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), request.ProjectID).Scan(&organizationID); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("instance project scope is unavailable")
	} else if err != nil {
		return fmt.Errorf("load instance project scope: %w", err)
	}
	var creatorExists int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_users WHERE user_id = ? AND disabled = 0`), request.CreatedBy).Scan(&creatorExists); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("instance creator is unavailable")
	} else if err != nil {
		return fmt.Errorf("load instance creator: %w", err)
	}
	switch request.OwnerKind {
	case application.OwnerUser:
		var ownerExists int
		query := `SELECT 1 FROM labcentral_memberships
			WHERE user_id = ? AND organization_id = ? AND valid_from_us <= ` + s.nowMicros() + `
			AND (valid_until_us = 0 OR valid_until_us > ` + s.nowMicros() + `) LIMIT 1`
		if err := tx.QueryRowContext(ctx, s.bind(query), request.OwnerID, organizationID).Scan(&ownerExists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("instance user owner is outside the project organization")
		} else if err != nil {
			return fmt.Errorf("load instance user owner: %w", err)
		}
	case application.OwnerTeam:
		var ownerOrganizationID string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), request.OwnerID).Scan(&ownerOrganizationID); errors.Is(err, sql.ErrNoRows) || ownerOrganizationID != organizationID {
			return fmt.Errorf("instance team owner is outside the project organization")
		} else if err != nil {
			return fmt.Errorf("load instance team owner: %w", err)
		}
	case application.OwnerProject:
		if request.OwnerID != string(request.ProjectID) {
			return fmt.Errorf("instance project owner does not match the project scope")
		}
	default:
		return fmt.Errorf("instance owner kind is invalid")
	}
	return nil
}

func (s *Store) SaveTargetSnapshot(ctx context.Context, snapshot application.TargetSnapshot) error {
	for attempt := 0; ; attempt++ {
		err := s.saveTargetSnapshot(ctx, snapshot)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return err
		}
	}
}

func (s *Store) saveTargetSnapshot(ctx context.Context, snapshot application.TargetSnapshot) error {
	if _, err := domain.NewProviderTargetID(string(snapshot.ID)); err != nil {
		return err
	}
	if snapshot.ProviderKind == "" || !strings.HasPrefix(snapshot.ConfigFingerprint, "sha256:") || snapshot.ProtocolMajor != 1 || len(snapshot.DiscoveryJSON) == 0 || len(snapshot.DiscoveryJSON) > 8<<20 || snapshot.DiscoveredAt.IsZero() || !snapshot.ValidUntil.After(snapshot.DiscoveredAt) {
		return fmt.Errorf("provider target snapshot is incomplete")
	}
	if err := validateGovernanceRecordIDs(snapshot.GovernanceRecordIDs); err != nil {
		return err
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin provider target snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_local_provider_targets
		(target_id, provider_kind, config_fingerprint, protocol_major, protocol_minor, features_json, discovery_json,
		 discovered_at_us, valid_until_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)
		ON CONFLICT(target_id) DO UPDATE SET provider_kind = excluded.provider_kind,
		 config_fingerprint = excluded.config_fingerprint, protocol_major = excluded.protocol_major,
		 protocol_minor = excluded.protocol_minor, features_json = excluded.features_json,
		 discovery_json = excluded.discovery_json, discovered_at_us = excluded.discovered_at_us,
		 valid_until_us = excluded.valid_until_us, updated_at_us = `+s.nowMicros()),
		snapshot.ID, snapshot.ProviderKind, snapshot.ConfigFingerprint, snapshot.ProtocolMajor, snapshot.ProtocolMinor,
		encodedFeatures, snapshot.DiscoveryJSON, snapshot.DiscoveredAt.UnixMicro(), snapshot.ValidUntil.UnixMicro())
	if err != nil {
		return fmt.Errorf("persist provider target snapshot: %w", err)
	}
	discoveryDigest := sha256.Sum256(snapshot.DiscoveryJSON)
	payload := struct {
		TargetID        domain.ProviderTargetID `json:"targetId"`
		ProviderKind    string                  `json:"providerKind"`
		ProtocolMajor   uint32                  `json:"protocolMajor"`
		ProtocolMinor   uint32                  `json:"protocolMinor"`
		FeatureCount    int                     `json:"featureCount"`
		DiscoveryDigest string                  `json:"discoveryDigest"`
		ValidUntil      time.Time               `json:"validUntil"`
	}{snapshot.ID, snapshot.ProviderKind, snapshot.ProtocolMajor, snapshot.ProtocolMinor, len(features), hex.EncodeToString(discoveryDigest[:]), snapshot.ValidUntil.UTC()}
	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		AuditID: snapshot.AuditID, EventID: snapshot.EventID, Category: application.AuditTarget,
		Action: "provider-target.discovery.refresh", Outcome: application.AuditSucceeded,
		EventType: "provider-target.discovery-refreshed", Actor: snapshot.Actor,
		AggregateType: "provider-target", AggregateID: string(snapshot.ID), Payload: payload,
	})
	if err != nil {
		return err
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return fmt.Errorf("append provider target audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, event); err != nil {
		return fmt.Errorf("append provider target event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider target snapshot: %w", err)
	}
	return nil
}

func (s *Store) GetTargetSnapshot(ctx context.Context, targetID domain.ProviderTargetID) (application.TargetSnapshot, error) {
	var snapshot application.TargetSnapshot
	var id string
	var features []byte
	var protocolMajor, protocolMinor, discoveredAt, validUntil int64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT target_id, provider_kind, config_fingerprint, protocol_major, protocol_minor,
		features_json, discovery_json, discovered_at_us, valid_until_us
		FROM labcentral_local_provider_targets WHERE target_id = ?`), targetID).Scan(
		&id, &snapshot.ProviderKind, &snapshot.ConfigFingerprint, &protocolMajor, &protocolMinor,
		&features, &snapshot.DiscoveryJSON, &discoveredAt, &validUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.TargetSnapshot{}, application.ErrNotFound
	}
	if err != nil {
		return application.TargetSnapshot{}, err
	}
	if err := json.Unmarshal(features, &snapshot.Features); err != nil {
		return application.TargetSnapshot{}, fmt.Errorf("decode provider target features: %w", err)
	}
	snapshot.ID = domain.ProviderTargetID(id)
	snapshot.ProtocolMajor = uint32(protocolMajor)
	snapshot.ProtocolMinor = uint32(protocolMinor)
	snapshot.DiscoveredAt = time.UnixMicro(discoveredAt).UTC()
	snapshot.ValidUntil = time.UnixMicro(validUntil).UTC()
	return snapshot, nil
}

func validateCreate(request application.CreateInstanceRequest) error {
	if request.InstanceID == "" || request.ProjectID == "" || request.OwnerKind == "" || request.OwnerID == "" || request.CreatedBy == "" || request.ProviderTargetID == "" || request.OperationID == "" || request.CanonicalDigest == "" || len(request.CanonicalJSON) == 0 || len(request.SourceYAML) == 0 || len(request.Resources) == 0 || request.SubjectID == "" || request.IdempotencyKey == "" || request.RequestDigest == "" || len(request.Payload) == 0 || request.AuditID == "" || request.EventID == "" || request.Quota.ReservationID == "" {
		return fmt.Errorf("local instance request is incomplete")
	}
	if request.SubjectID != request.CreatedBy {
		return fmt.Errorf("instance creator must match the idempotency subject")
	}
	if _, err := domain.NewProjectID(string(request.ProjectID)); err != nil {
		return err
	}
	if err := validateInstanceOwner(request.OwnerKind, request.OwnerID); err != nil {
		return err
	}
	if request.ProviderTargetID == application.LocalDockerTarget && !request.Acknowledged {
		return fmt.Errorf("local Docker instance request must acknowledge Docker isolation")
	}
	if _, err := domain.NewQuotaReservationID(string(request.Quota.ReservationID)); err != nil {
		return err
	}
	amounts, err := application.NormalizeQuotaAmounts(request.Quota.Amounts)
	if err != nil {
		return fmt.Errorf("local instance quota amounts are invalid: %w", err)
	}
	if len(amounts) == 0 {
		return fmt.Errorf("local instance quota amounts are required")
	}
	if err := application.ValidateInstanceLifetimeRequest(request.Quota.Lifetime); err != nil {
		return err
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

func validateInstanceOwner(kind application.OwnerKind, id string) error {
	switch kind {
	case application.OwnerUser:
		_, err := domain.NewUserID(id)
		return err
	case application.OwnerTeam:
		_, err := domain.NewTeamID(id)
		return err
	case application.OwnerProject:
		_, err := domain.NewProjectID(id)
		return err
	default:
		return fmt.Errorf("instance owner kind is invalid")
	}
}

type committedClaim struct {
	key     application.AllocationKey
	binding application.AllocationBinding
}

func (s *Store) commitAllocationClaims(ctx context.Context, tx *sql.Tx, request application.CreateInstanceRequest) error {
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
		err := tx.QueryRowContext(ctx, s.bind(`SELECT instance_id, resource_id, plan_digest, state
			FROM labcentral_provider_allocation_claims WHERE target_id = ? AND namespace = ? AND claim_value = ?`),
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
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims
			SET instance_id = ?, resource_id = ?, plan_digest = ?, state = ?, fencing_token = 0,
			    created_at_us = `+s.nowMicros()+`, updated_at_us = `+s.nowMicros()+`
			WHERE target_id = ? AND namespace = ? AND claim_value = ? AND state = ?`),
			candidate.binding.InstanceID, candidate.binding.ResourceID, candidate.binding.PlanDigest.Hex(), application.AllocationCommitted,
			candidate.key.TargetID, candidate.key.Namespace, candidate.key.Value, application.AllocationReleased)
		if err != nil {
			return fmt.Errorf("reuse released allocation claim: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			continue
		}
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_provider_allocation_claims
			(target_id, namespace, claim_value, instance_id, resource_id, plan_digest, state, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`),
			candidate.key.TargetID, candidate.key.Namespace, candidate.key.Value, candidate.binding.InstanceID,
			candidate.binding.ResourceID, candidate.binding.PlanDigest.Hex(), application.AllocationCommitted)
		if err != nil {
			if isUniqueViolation(err) {
				return &application.AllocationConflictError{Conflicts: []application.AllocationKey{candidate.key}}
			}
			return fmt.Errorf("insert committed allocation claim: %w", err)
		}
	}
	return nil
}

func (s *Store) QueueDestroy(ctx context.Context, request application.QueueDestroyRequest) (application.EnqueueResult, error) {
	if request.InstanceID == "" || request.OperationID == "" || request.SubjectID == "" || request.IdempotencyKey == "" || request.RequestDigest == "" || len(request.Payload) == 0 || request.AuditID == "" || request.EventID == "" {
		return application.EnqueueResult{}, fmt.Errorf("destroy request is incomplete")
	}
	if !request.ExpirationDueAt.IsZero() && request.SubjectID != application.SystemExpirationActorID {
		return application.EnqueueResult{}, fmt.Errorf("expiration-gated destroy requires the expiration system actor")
	}
	for attempt := 0; ; attempt++ {
		result, err := s.queueDestroy(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return result, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.EnqueueResult{}, err
		}
	}
}

func (s *Store) queueDestroy(ctx context.Context, request application.QueueDestroyRequest) (application.EnqueueResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.EnqueueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	action := "destroy_instance:" + string(request.InstanceID)
	if err := s.lockIdempotency(ctx, tx, request.SubjectID, action, request.IdempotencyKey); err != nil {
		return application.EnqueueResult{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.SubjectID, action, request.IdempotencyKey, request.RequestDigest); err != nil {
		return application.EnqueueResult{}, err
	} else if found {
		return application.EnqueueResult{InstanceID: request.InstanceID, OperationID: domain.OperationID(existing), Replay: true}, tx.Commit()
	}
	instanceQuery := `SELECT phase, expires_at_us, expiration_grace_seconds
		FROM labcentral_local_instances WHERE instance_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		instanceQuery += ` FOR UPDATE`
	}
	var phase domain.InstancePhase
	var expiresAtMicros, graceSeconds int64
	if err := tx.QueryRowContext(ctx, s.bind(instanceQuery), request.InstanceID).Scan(&phase, &expiresAtMicros, &graceSeconds); errors.Is(err, sql.ErrNoRows) {
		return application.EnqueueResult{}, application.ErrNotFound
	} else if err != nil {
		return application.EnqueueResult{}, fmt.Errorf("lock instance for destroy: %w", err)
	}
	if phase == domain.InstanceDestroyed {
		return application.EnqueueResult{}, application.ErrNotFound
	}
	if !request.ExpirationDueAt.IsZero() {
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return application.EnqueueResult{}, err
		}
		if expiresAtMicros <= 0 || graceSeconds < 0 || request.ExpirationDueAt.UTC().UnixMicro() != expiresAtMicros ||
			now.UnixMicro() < expiresAtMicros+graceSeconds*1_000_000 {
			return application.EnqueueResult{}, application.ErrVersionConflict
		}
	}
	activeOperation, activeKind, err := s.activeOperationTx(ctx, tx, request.InstanceID)
	if err != nil {
		return application.EnqueueResult{}, err
	}
	if activeOperation != "" {
		if activeKind != application.OperationDestroy {
			return application.EnqueueResult{}, application.ErrVersionConflict
		}
		if err := s.bindIdempotency(ctx, tx, request.SubjectID, action, request.IdempotencyKey, request.RequestDigest, string(activeOperation)); err != nil {
			return application.EnqueueResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return application.EnqueueResult{}, err
		}
		return application.EnqueueResult{InstanceID: request.InstanceID, OperationID: activeOperation, Replay: true}, nil
	}
	update := `UPDATE labcentral_local_instances
		SET desired_lifecycle = ?, resource_version = resource_version + 1, updated_at_us = ` + s.nowMicros() + `
		WHERE instance_id = ? AND phase <> ?`
	arguments := []any{domain.LifecycleDestroyed, request.InstanceID, domain.InstanceDestroyed}
	if !request.ExpirationDueAt.IsZero() {
		update += ` AND expires_at_us = ? AND expires_at_us > 0
			AND expires_at_us + expiration_grace_seconds * 1000000 <= ` + s.nowMicros()
		arguments = append(arguments, request.ExpirationDueAt.UTC().UnixMicro())
	}
	result, err := tx.ExecContext(ctx, s.bind(update), arguments...)
	if err != nil {
		return application.EnqueueResult{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.EnqueueResult{}, application.ErrNotFound
	}
	if err := s.enqueue(ctx, tx, request.InstanceID, request.OperationID, application.OperationDestroy, request.Payload, request.SubjectID, request.AuditID, request.EventID); err != nil {
		return application.EnqueueResult{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.SubjectID, action, request.IdempotencyKey, request.RequestDigest, string(request.OperationID)); err != nil {
		return application.EnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.EnqueueResult{}, err
	}
	return application.EnqueueResult{InstanceID: request.InstanceID, OperationID: request.OperationID}, nil
}

func (s *Store) activeOperationTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID) (domain.OperationID, application.OperationKind, error) {
	var operationID domain.OperationID
	var kind application.OperationKind
	err := tx.QueryRowContext(ctx, s.bind(`SELECT operation.operation_id, operation.kind
		FROM labcentral_local_operations operation
		JOIN labcentral_jobs job ON job.operation_id = operation.operation_id
		WHERE operation.instance_id = ?
		AND job.state IN (?, ?, ?, ?)
		ORDER BY operation.created_at_us DESC, operation.operation_id DESC LIMIT 1`),
		instanceID, domain.OperationQueued, domain.OperationRunning,
		domain.OperationCancelRequested, domain.OperationCompensating).Scan(&operationID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("load active instance operation: %w", err)
	}
	return operationID, kind, nil
}

func (s *Store) enqueue(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, operationID domain.OperationID, kind application.OperationKind, payload []byte, subjectID domain.UserID, auditID, eventID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, s.bind("SELECT 1 FROM labcentral_job_aggregates WHERE aggregate_id = ?"), instanceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_aggregates
			(aggregate_id, phase, resource_version, event_sequence, operation_state, instance_phase)
			VALUES (?, ?, 0, 0, ?, ?)`), instanceID, "queued", domain.OperationQueued, domain.InstancePending); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_jobs
		(id, operation_id, instance_id, aggregate_id, state, desired_generation, payload_version, payload, available_at_us)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?, `+s.nowMicros()+`)`), operationID, operationID, instanceID, instanceID, domain.OperationQueued, payload); err != nil {
		return fmt.Errorf("insert local job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_local_operations
		(operation_id, instance_id, actor_id, kind, message, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`), operationID, instanceID, subjectID, kind, "Queued"); err != nil {
		return fmt.Errorf("insert local operation: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_job_aggregates
		SET phase = ?, resource_version = resource_version + 1, event_sequence = event_sequence + 1,
		operation_state = ? WHERE aggregate_id = ? RETURNING event_sequence`), "queued", domain.OperationQueued, instanceID).Scan(&sequence); err != nil {
		return err
	}
	eventPayload := []byte(fmt.Sprintf(`{"operationId":%q,"kind":%q,"state":"queued"}`, operationID, kind))
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_audit_intents
		(audit_id, aggregate_id, actor_id, action, created_at_us) VALUES (?, ?, ?, ?, `+s.nowMicros()+`)`), auditID, instanceID, subjectID, kind); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_events
		(event_id, aggregate_id, aggregate_sequence, event_type, payload, created_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`)`), eventID, instanceID, sequence, "operation.queued", eventPayload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_outbox_deliveries
		(event_id, sink, payload, available_at_us) VALUES (?, 'local-events', ?, `+s.nowMicros()+`)`), eventID, eventPayload); err != nil {
		return err
	}
	var organizationID domain.OrganizationID
	var projectID domain.ProjectID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT project.organization_id, instance.project_id
		FROM labcentral_local_instances instance JOIN labcentral_projects project ON project.project_id = instance.project_id
		WHERE instance.instance_id = ?`), instanceID).Scan(&organizationID, &projectID); err != nil {
		return fmt.Errorf("load operation governance scope: %w", err)
	}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: string(subjectID)}
	if subjectID == application.SystemExpirationActorID {
		actor = application.RecordActor{Kind: application.RecordActorSystem, ID: string(subjectID)}
	}
	governancePayload := struct {
		OperationID domain.OperationID `json:"operationId"`
		InstanceID  domain.InstanceID  `json:"instanceId"`
		Kind        string             `json:"kind"`
		State       string             `json:"state"`
	}{operationID, instanceID, string(kind), string(domain.OperationQueued)}
	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		AuditID: auditID, EventID: eventID, Category: application.AuditOperation,
		Action: "operation.queue", Outcome: application.AuditAccepted, EventType: "operation.queued",
		Scope: application.RecordScope{OrganizationID: organizationID, ProjectID: projectID}, Actor: actor,
		AggregateType: "operation", AggregateID: string(operationID), Links: application.RecordLinks{OperationID: operationID}, Payload: governancePayload,
	})
	if err != nil {
		return err
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return fmt.Errorf("append queued operation audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, event); err != nil {
		return fmt.Errorf("append queued operation event: %w", err)
	}
	return nil
}

func (s *Store) findIdempotency(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, digest domain.SHA256Digest) (string, bool, error) {
	var recordedDigest, resultID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT request_digest, result_id FROM labcentral_idempotency_records
		WHERE subject_id = ? AND route_action = ? AND idempotency_key = ?`), subject, action, key).Scan(&recordedDigest, &resultID)
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

func (s *Store) lockIdempotency(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string) error {
	if s.dialect != jobsdb.DialectPostgreSQL {
		return nil
	}
	digest := sha256.Sum256([]byte(string(subject) + "\x00" + action + "\x00" + key))
	lockID := int64(binary.BigEndian.Uint64(digest[:8]))
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lockID); err != nil {
		return fmt.Errorf("serialize idempotency binding: %w", err)
	}
	return nil
}

func (s *Store) bindIdempotency(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, digest domain.SHA256Digest, resultID string) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_idempotency_records
		(subject_id, route_action, idempotency_key, request_digest, result_id, expires_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+` + ?)`), subject, action, key, digest.Hex(), resultID, (24 * time.Hour).Microseconds())
	return err
}

const instanceRecordSelect = `SELECT instance_id, revision_id, project_id, owner_kind, owner_id, created_by, provider_target_id, provider_override_json, canonical_digest, canonical_lab_json, source_yaml, phase,
		desired_lifecycle, generation, resource_version, isolation_warning_acknowledged, outputs_json,
		error_code, error_message, expires_at_us, maximum_expires_at_us, expiration_warning_at_us,
		expiration_warning_emitted_at_us, expiration_grace_seconds, lifetime_renewal_allowed,
		lifetime_renewal_window_seconds, lifetime_policy_digest, created_at_us, updated_at_us
		FROM labcentral_local_instances`

func (s *Store) GetInstance(ctx context.Context, id domain.InstanceID) (application.InstanceRecord, error) {
	row := s.db.QueryRowContext(ctx, s.bind(instanceRecordSelect+` WHERE instance_id = ?`), id)
	return scanInstance(row)
}

func (s *Store) ListInstances(ctx context.Context) ([]application.InstanceRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(instanceRecordSelect+` ORDER BY created_at_us, instance_id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []application.InstanceRecord
	for rows.Next() {
		record, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ListInstancesByID(ctx context.Context, ids []domain.InstanceID) ([]application.InstanceRecord, error) {
	if len(ids) == 0 {
		return []application.InstanceRecord{}, nil
	}
	if len(ids) > 10000 {
		return nil, fmt.Errorf("authorized instance filter exceeds its limit")
	}
	arguments := make([]any, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := domain.NewInstanceID(string(id)); err != nil {
			return nil, err
		}
		arguments = append(arguments, id)
		placeholders = append(placeholders, "?")
	}
	query := instanceRecordSelect + ` WHERE instance_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY created_at_us, instance_id`
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]application.InstanceRecord, 0, len(ids))
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

func scanInstance(row scanner) (application.InstanceRecord, error) {
	var record application.InstanceRecord
	var id, projectID, ownerKind, createdBy, targetID, digest, phase, lifecycle string
	var revisionID sql.NullString
	var generation, resourceVersion, acknowledged, expiresAt, maximumExpiresAt, warningAt, warningEmittedAt int64
	var graceSeconds, renewalAllowed, renewalWindow, createdAt, updatedAt int64
	var lifetimePolicyDigest string
	err := row.Scan(&id, &revisionID, &projectID, &ownerKind, &record.OwnerID, &createdBy, &targetID, &record.ProviderOverrideJSON, &digest, &record.CanonicalLabJSON, &record.SourceYAML, &phase, &lifecycle,
		&generation, &resourceVersion, &acknowledged, &record.OutputsJSON, &record.ErrorCode, &record.ErrorMessage,
		&expiresAt, &maximumExpiresAt, &warningAt, &warningEmittedAt, &graceSeconds, &renewalAllowed, &renewalWindow,
		&lifetimePolicyDigest, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.InstanceRecord{}, application.ErrNotFound
	}
	if err != nil {
		return application.InstanceRecord{}, err
	}
	parsedDigest, err := domain.ParseSHA256Digest(digest)
	if err != nil {
		return application.InstanceRecord{}, err
	}
	record.ID = domain.InstanceID(id)
	if revisionID.Valid {
		record.RevisionID = domain.RevisionID(revisionID.String)
	}
	record.ProjectID = domain.ProjectID(projectID)
	record.OwnerKind = application.OwnerKind(ownerKind)
	record.CreatedBy = domain.UserID(createdBy)
	record.ProviderTargetID = domain.ProviderTargetID(targetID)
	record.CanonicalDigest = parsedDigest
	record.Phase = domain.InstancePhase(phase)
	record.DesiredLifecycle = domain.DesiredLifecycle(lifecycle)
	record.Generation = domain.Generation(generation)
	record.ResourceVersion = domain.ResourceVersion(resourceVersion)
	record.IsolationWarningAcknowledged = acknowledged == 1
	if expiresAt > 0 {
		record.ExpiresAt = time.UnixMicro(expiresAt).UTC()
	}
	if maximumExpiresAt > 0 {
		record.MaximumExpiresAt = time.UnixMicro(maximumExpiresAt).UTC()
	}
	if warningAt > 0 {
		record.ExpirationWarningAt = time.UnixMicro(warningAt).UTC()
	}
	if warningEmittedAt > 0 {
		record.ExpirationWarningEmittedAt = time.UnixMicro(warningEmittedAt).UTC()
	}
	if graceSeconds < 0 || renewalWindow < 0 {
		return application.InstanceRecord{}, fmt.Errorf("stored instance lifetime values are invalid")
	}
	record.ExpirationGraceSeconds = uint64(graceSeconds)
	record.LifetimeRenewalAllowed = renewalAllowed != 0
	record.LifetimeRenewalWindowSeconds = uint64(renewalWindow)
	if lifetimePolicyDigest != "" {
		record.LifetimePolicyDigest, err = domain.ParseSHA256Digest(lifetimePolicyDigest)
		if err != nil {
			return application.InstanceRecord{}, fmt.Errorf("stored instance lifetime policy digest is invalid: %w", err)
		}
	}
	record.CreatedAt = time.UnixMicro(createdAt).UTC()
	record.UpdatedAt = time.UnixMicro(updatedAt).UTC()
	return record, nil
}

func (s *Store) ListResources(ctx context.Context, instanceID domain.InstanceID) ([]application.ResourceRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT instance_id, resource_id, kind, ordinal, canonical_resource_json, provider_plan, phase,
		ownership_token, provider_state, observed_state_json, error_code, error_message, updated_at_us
		FROM labcentral_local_resources WHERE instance_id = ? ORDER BY ordinal`), instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []application.ResourceRecord
	for rows.Next() {
		var record application.ResourceRecord
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

func (s *Store) GetOperation(ctx context.Context, id domain.OperationID) (application.OperationRecord, error) {
	var record application.OperationRecord
	var operationID, instanceID, kind, state string
	var generation, attempt, createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT operation.operation_id, operation.instance_id, operation.actor_id, operation.kind, job.state,
		job.desired_generation, job.attempt, operation.message, operation.error_code, operation.created_at_us, operation.updated_at_us
		FROM labcentral_local_operations operation JOIN labcentral_jobs job ON job.operation_id = operation.operation_id
		WHERE operation.operation_id = ?`), id).Scan(&operationID, &instanceID, &record.ActorID, &kind, &state, &generation, &attempt,
		&record.Message, &record.ErrorCode, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return application.OperationRecord{}, application.ErrNotFound
	}
	if err != nil {
		return application.OperationRecord{}, err
	}
	record.ID = domain.OperationID(operationID)
	record.InstanceID = domain.InstanceID(instanceID)
	record.Kind = application.OperationKind(kind)
	record.State = domain.OperationState(state)
	record.Generation = domain.Generation(generation)
	record.Attempt = uint32(attempt)
	record.CreatedAt = time.UnixMicro(createdAt).UTC()
	record.UpdatedAt = time.UnixMicro(updatedAt).UTC()
	return record, nil
}

func (s *Store) Claim(ctx context.Context, owner string, duration time.Duration) (application.WorkClaim, error) {
	claim, err := s.jobs.ClaimJob(ctx, owner, duration)
	return s.finishClaim(ctx, claim, err)
}
func (s *Store) ClaimTargets(ctx context.Context, owner string, duration time.Duration, targets []application.TargetExecutionLimit) (application.WorkClaim, error) {
	if len(targets) > 64 {
		return application.WorkClaim{}, fmt.Errorf("target claim filter exceeds limit")
	}
	for _, target := range targets {
		if target.Maximum < 1 || target.Maximum > 16 {
			return application.WorkClaim{}, fmt.Errorf("target deployment limit must be between 1 and 16")
		}
	}
	for _, target := range targets {
		claim, err := s.claimTarget(ctx, owner, duration, target)
		if errors.Is(err, jobsdb.ErrNoClaimableJob) {
			continue
		}
		return s.finishClaim(ctx, claim, err)
	}
	return application.WorkClaim{}, application.ErrNoWork
}

func (s *Store) claimTarget(ctx context.Context, owner string, duration time.Duration, target application.TargetExecutionLimit) (jobsdb.ClaimedJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	defer tx.Rollback()
	if err := s.lockTargetAdmissionTx(ctx, tx, target.TargetID); err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	var configured uint32
	if err := tx.QueryRowContext(ctx, s.bind("SELECT maximum FROM labcentral_target_deployment_settings WHERE target_id=?"), target.TargetID).Scan(&configured); err == nil {
		target.Maximum = configured
	} else if !errors.Is(err, sql.ErrNoRows) {
		return jobsdb.ClaimedJob{}, err
	}
	var running uint32
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_jobs job
  JOIN labcentral_local_instances instance ON instance.instance_id=job.instance_id
  WHERE instance.provider_target_id=? AND job.state='running'`), target.TargetID).Scan(&running); err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	// An expired lease still occupies capacity until recovered. Reclaiming it
	// resumes the same deployment, even when the target is at its limit.
	queued := ""
	if running < target.Maximum {
		queued = "job.state='queued' OR "
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT job.operation_id
  FROM labcentral_local_instances instance JOIN labcentral_jobs job ON job.instance_id=instance.instance_id
  WHERE instance.provider_target_id=? AND job.payload_version=1 AND job.available_at_us <= `+s.nowMicros()+`
  AND (`+queued+`(job.state='running' AND job.lease_until_us <= `+s.nowMicros()+`))
  ORDER BY job.available_at_us, job.id LIMIT 1024`), target.TargetID)
	if err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return jobsdb.ClaimedJob{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	claim, err := s.jobs.ClaimJobForOperationsTx(ctx, tx, owner, duration, ids)
	if err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return jobsdb.ClaimedJob{}, err
	}
	return claim, nil
}
func (s *Store) finishClaim(ctx context.Context, claim jobsdb.ClaimedJob, err error) (application.WorkClaim, error) {
	if errors.Is(err, jobsdb.ErrNoClaimableJob) {
		return application.WorkClaim{}, application.ErrNoWork
	}
	if err != nil {
		return application.WorkClaim{}, err
	}
	result := application.WorkClaim{Operation: claim.Operation, LeaseOwner: claim.LeaseOwner, Action: claim.Action, Payload: claim.Payload}
	if claim.Action == application.ClaimGetInvocation {
		var recovery application.InvocationRecord
		var digest string
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT invocation_id, operation_id, resource_id, action, idempotency_key, request_digest, state, provider_result
			FROM labcentral_runtime_invocations WHERE operation_id = ? AND state = 'unknown'
			ORDER BY updated_at_us DESC, invocation_id DESC LIMIT 1`), claim.Operation.ID).Scan(
			&recovery.ID, &recovery.OperationID, &recovery.ResourceID, &recovery.Action, &recovery.IdempotencyKey,
			&digest, &recovery.State, &recovery.ProviderResult)
		if errors.Is(err, sql.ErrNoRows) {
			return application.WorkClaim{}, fmt.Errorf("operation requires invocation recovery but has no unknown invocation")
		}
		if err != nil {
			return application.WorkClaim{}, fmt.Errorf("load unknown runtime invocation: %w", err)
		}
		recovery.RequestDigest = domain.SHA256Digest(digest)
		if _, err := domain.ParseSHA256Digest(digest); err != nil {
			return application.WorkClaim{}, fmt.Errorf("stored runtime invocation digest is invalid")
		}
		result.Recovery = &recovery
	}
	return result, nil
}

func (s *Store) Renew(ctx context.Context, fence application.Fence, duration time.Duration) error {
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

func (s *Store) MarkInstancePhase(ctx context.Context, fence application.Fence, phase domain.InstancePhase, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET phase = ?, updated_at_us = `+s.nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)`), phase, fence.OperationID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_operations SET message = ?, updated_at_us = `+s.nowMicros()+`
		WHERE operation_id = ?`), message, fence.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PrepareInvocation(ctx context.Context, request application.PrepareInvocationRequest) (application.InvocationRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.InvocationRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, request.Fence); err != nil {
		return application.InvocationRecord{}, err
	}
	var existing application.InvocationRecord
	var operationID, resourceID, digest string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT invocation_id, operation_id, resource_id, action, idempotency_key, request_digest, state, provider_result
		FROM labcentral_runtime_invocations WHERE operation_id = ? AND resource_id = ? AND action = ?`),
		request.Fence.OperationID, request.ResourceID, request.Action).Scan(&existing.ID, &operationID, &resourceID, &existing.Action,
		&existing.IdempotencyKey, &digest, &existing.State, &existing.ProviderResult)
	if err == nil {
		if digest != request.RequestDigest.Hex() || existing.IdempotencyKey != request.IdempotencyKey {
			return application.InvocationRecord{}, jobsdb.ErrIdempotencyConflict
		}
		existing.OperationID = domain.OperationID(operationID)
		existing.ResourceID = domain.LogicalResourceID(resourceID)
		existing.RequestDigest, _ = domain.ParseSHA256Digest(digest)
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return application.InvocationRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_runtime_invocations
		(invocation_id, operation_id, resource_id, action, idempotency_key, request_digest, state, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, 'prepared', `+s.nowMicros()+`, `+s.nowMicros()+`)`), request.InvocationID, request.Fence.OperationID,
		request.ResourceID, request.Action, request.IdempotencyKey, request.RequestDigest.Hex()); err != nil {
		return application.InvocationRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.InvocationRecord{}, err
	}
	return application.InvocationRecord{ID: request.InvocationID, OperationID: request.Fence.OperationID, ResourceID: request.ResourceID,
		Action: request.Action, IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest, State: "prepared"}, nil
}

func (s *Store) SaveResourceResult(ctx context.Context, update application.ResourceResultUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, update.Fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_resources
		SET phase = ?, provider_state = ?, observed_state_json = ?, error_code = ?, error_message = ?, updated_at_us = `+s.nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?) AND resource_id = ?`),
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
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_runtime_invocations
		SET state = ?, provider_result = ?, updated_at_us = `+s.nowMicros()+`
		WHERE invocation_id = ? AND operation_id = ?`), update.InvocationState, resultBytes, update.InvocationID, update.Fence.OperationID); err != nil {
		return err
	}
	recoveryRequired := 0
	if update.InvocationState == "unknown" {
		recoveryRequired = 1
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET recovery_required = ?
		WHERE operation_id = ?`), recoveryRequired, update.Fence.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveCoreResource(ctx context.Context, fence application.Fence, resourceID domain.LogicalResourceID, phase domain.ResourcePhase, observed []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_resources
		SET phase = ?, observed_state_json = ?, error_code = '', error_message = '', updated_at_us = `+s.nowMicros()+`
		WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?) AND resource_id = ?`),
		phase, defaultJSON(observed), fence.OperationID, resourceID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	return tx.Commit()
}

func (s *Store) Finalize(ctx context.Context, request application.FinalizeRequest) error {
	if !request.State.Terminal() {
		return fmt.Errorf("final operation state must be terminal")
	}
	if _, err := domain.NewUserID(string(request.ActorID)); err != nil {
		return fmt.Errorf("final operation actor is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, request.Fence); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET state = ?, checkpoint = ?
		WHERE operation_id = ? AND state = ? AND lease_owner = ? AND fencing_token = ? AND desired_generation = ? AND attempt = ?`),
		request.State, []byte(request.Message), request.Fence.OperationID, domain.OperationRunning, request.Fence.LeaseOwner, request.Fence.Token,
		request.Fence.Generation, request.Fence.Attempt)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return jobsdb.ErrFencingConflict
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_operations SET message = ?, error_code = ?, updated_at_us = `+s.nowMicros()+`
		WHERE operation_id = ?`), request.Message, request.ErrorCode, request.Fence.OperationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances
		SET phase = ?, outputs_json = ?, error_code = ?, error_message = ?, resource_version = resource_version + 1,
		updated_at_us = `+s.nowMicros()+` WHERE instance_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)`),
		request.InstancePhase, defaultJSON(request.OutputsJSON), request.ErrorCode, request.ErrorMessage, request.Fence.OperationID); err != nil {
		return err
	}
	var instanceID string
	var sequence int64
	if err := tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_job_aggregates SET phase = ?, operation_state = ?, instance_phase = ?,
		resource_version = resource_version + 1, event_sequence = event_sequence + 1
		WHERE aggregate_id = (SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?)
		RETURNING aggregate_id, event_sequence`), "complete", request.State, request.InstancePhase, request.Fence.OperationID).Scan(&instanceID, &sequence); err != nil {
		return err
	}
	if err := s.finalizeInstanceQuotaTx(ctx, tx, domain.InstanceID(instanceID), request.OperationKind, request.State, request.InstancePhase); err != nil {
		return err
	}
	eventPayload := []byte(fmt.Sprintf(`{"operationId":%q,"kind":%q,"state":%q,"instancePhase":%q}`, request.Fence.OperationID, request.OperationKind, request.State, request.InstancePhase))
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_audit_intents
		(audit_id, aggregate_id, actor_id, action, created_at_us) VALUES (?, ?, ?, ?, `+s.nowMicros()+`)`), request.AuditID, instanceID, request.ActorID, request.OperationKind+".complete"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_events
		(event_id, aggregate_id, aggregate_sequence, event_type, payload, created_at_us)
		VALUES (?, ?, ?, 'operation.completed', ?, `+s.nowMicros()+`)`), request.EventID, instanceID, sequence, eventPayload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_outbox_deliveries
		(event_id, sink, payload, available_at_us) VALUES (?, 'local-events', ?, `+s.nowMicros()+`)`), request.EventID, eventPayload); err != nil {
		return err
	}
	var organizationID domain.OrganizationID
	var projectID domain.ProjectID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT project.organization_id, instance.project_id
		FROM labcentral_local_instances instance JOIN labcentral_projects project ON project.project_id = instance.project_id
		WHERE instance.instance_id = ?`), instanceID).Scan(&organizationID, &projectID); err != nil {
		return fmt.Errorf("load finalized operation governance scope: %w", err)
	}
	outcome := application.AuditFailed
	if request.State == domain.OperationSucceeded {
		outcome = application.AuditSucceeded
	}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: string(request.ActorID)}
	if request.ActorID == application.SystemExpirationActorID {
		actor = application.RecordActor{Kind: application.RecordActorSystem, ID: string(request.ActorID)}
	}
	governancePayload := struct {
		OperationID   domain.OperationID    `json:"operationId"`
		InstanceID    domain.InstanceID     `json:"instanceId"`
		Kind          string                `json:"kind"`
		State         domain.OperationState `json:"state"`
		InstancePhase domain.InstancePhase  `json:"instancePhase"`
		ErrorCode     string                `json:"errorCode,omitempty"`
	}{request.Fence.OperationID, domain.InstanceID(instanceID), string(request.OperationKind), request.State, request.InstancePhase, request.ErrorCode}
	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		AuditID: request.AuditID, EventID: request.EventID, Category: application.AuditOperation,
		Action: "operation.complete", Outcome: outcome, EventType: "operation.completed",
		Scope:         application.RecordScope{OrganizationID: organizationID, ProjectID: projectID},
		Actor:         actor,
		AggregateType: "operation", AggregateID: string(request.Fence.OperationID), Links: application.RecordLinks{OperationID: request.Fence.OperationID}, Payload: governancePayload,
	})
	if err != nil {
		return err
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return fmt.Errorf("append finalized operation audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, event); err != nil {
		return fmt.Errorf("append finalized operation event: %w", err)
	}
	return tx.Commit()
}

func (s *Store) requireFence(ctx context.Context, tx *sql.Tx, fence application.Fence) error {
	query := `SELECT 1 FROM labcentral_jobs WHERE operation_id = ? AND state = ?
		AND lease_owner = ? AND fencing_token = ? AND desired_generation = ? AND attempt = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		query += " FOR UPDATE"
	}
	var exists int
	err := tx.QueryRowContext(ctx, s.bind(query), fence.OperationID, domain.OperationRunning,
		fence.LeaseOwner, fence.Token, fence.Generation, fence.Attempt).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return jobsdb.ErrFencingConflict
	}
	if err != nil {
		return err
	}
	if exists != 1 {
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

func nullableString(value string) any {
	if value == "" {
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

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return true
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed:")
}

// Retry only whole, rolled-back database transactions. The bounded exponential
// delay lets classroom audit-write bursts drain without synchronizing callers.
const maximumTransactionAttempts = 16

func isRetryableTransactionConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}

func waitTransactionRetry(ctx context.Context, attempt int) error {
	ceiling := min(5*time.Millisecond<<min(attempt, 6), 250*time.Millisecond)
	delay := ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

// Ping verifies that this process can still obtain and use a database
// connection. It is intentionally narrower than migration or application
// health and is used by the private readiness endpoint.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("control-plane database is unavailable")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("control-plane database ping failed: %w", err)
	}
	return nil
}

var _ application.Store = (*Store)(nil)

// ListTargetAllocationKeys snapshots occupied claims for planning. The subsequent
// transaction still owns uniqueness; stale snapshots use bounded conflict retries.
func (s *Store) ListTargetAllocationKeys(ctx context.Context, target domain.ProviderTargetID) ([]application.AllocationKey, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT namespace,claim_value FROM labcentral_provider_allocation_claims WHERE target_id=? AND state<>'released' ORDER BY namespace,claim_value LIMIT 4097`), target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []application.AllocationKey
	for rows.Next() {
		key := application.AllocationKey{TargetID: target}
		if err := rows.Scan(&key.Namespace, &key.Value); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) > 4096 {
		return nil, fmt.Errorf("target exceeds the supported occupied allocation snapshot")
	}
	return keys, rows.Err()
}
