package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

func TestSQLiteAuthenticationStartupRejectsConflictsAndLegacyWork(t *testing.T) {
	db, err := sql.Open("sqlite", "file:authentication-startup?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runAuthenticationStartupConformance(t, store)
}

func TestPostgreSQLAuthenticationStartupRejectsConflictsAndLegacyWork(t *testing.T) {
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("LABCENTRAL_TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("labcentral_auth_startup_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`) }()
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgreSQL(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runAuthenticationStartupConformance(t, store)
}

func runAuthenticationStartupConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.ValidateAuthenticationStartup(ctx, "none"); err != nil {
		t.Fatalf("explicit no-auth on a new database: %v", err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "local"); err != nil {
		t.Fatalf("authenticated preflight on a new database: %v", err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "none"); err != nil {
		t.Fatalf("read-only preflight persisted authentication mode: %v", err)
	}
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.inconsistent", Username: "inconsistent", DisplayName: "Inconsistent"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "local"); err == nil || !strings.Contains(err.Error(), "no recorded authentication mode") {
		t.Fatalf("inconsistent identity metadata error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM labcentral_users WHERE user_id = 'user.inconsistent'`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.ExecContext(ctx, `INSERT INTO labcentral_job_aggregates
		(aggregate_id, phase, resource_version, operation_state, instance_phase)
		VALUES ('aggregate.legacy', 'pending', 0, 'pending', 'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO labcentral_jobs
		(id, operation_id, instance_id, aggregate_id, state, desired_generation, payload_version, payload, available_at_us)
		VALUES ('job.legacy', 'operation.legacy', 'instance.legacy', 'aggregate.legacy', 'pending', 1, 1, ?, 0)`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "local"); err == nil || !strings.Contains(err.Error(), "legacy no-auth database") || !strings.Contains(err.Error(), "new database") {
		t.Fatalf("legacy authenticated startup error = %v", err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "none"); err != nil {
		t.Fatalf("legacy database could not remain explicitly in no-auth: %v", err)
	}

	if err := store.EnsureAuthenticationMode(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "none"); err == nil || !strings.Contains(err.Error(), "--auth-mode=local") {
		t.Fatalf("authenticated-to-no-auth conflict error = %v", err)
	}
	if err := store.ValidateAuthenticationStartup(ctx, "oidc"); err == nil || !strings.Contains(err.Error(), "installation mode \"local\"") {
		t.Fatalf("local-to-OIDC conflict error = %v", err)
	}
}

func TestBootstrapAdministratorAcceptsMatchingPreRecordedMode(t *testing.T) {
	db, err := sql.Open("sqlite", "file:authentication-bootstrap-recorded?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAuthenticationMode(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateBootstrapAdministrator(ctx,
		application.UserRecord{ID: "user.admin", Username: "admin", DisplayName: "Administrator", PasswordHash: "encoded"},
		application.RoleAssignmentRecord{ID: "role.admin", SubjectKind: application.SubjectUser, SubjectID: "user.admin", ScopeKind: application.ScopePlatform, ScopeID: "platform", Role: application.RolePlatformAdministrator, ValidFrom: time.Now().UTC()},
		nil,
	)
	if err != nil || !created {
		t.Fatalf("bootstrap with matching recorded mode: created=%t err=%v", created, err)
	}
}
