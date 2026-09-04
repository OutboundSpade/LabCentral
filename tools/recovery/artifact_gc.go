//go:build !windows

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const artifactDeleteConfirmation = "DELETE_UNREFERENCED_LABCENTRAL_ARTIFACTS"

var artifactReferenceSchemaMigrations = []string{
	"0001_jobs_outbox_prototype",
	"0002_local_vertical_slice",
	"0002_provider_allocation_claims",
	"0003_local_provider_target_binding",
	"0004_identity_tenancy_rbac",
	"0005_authentication_hardening",
	"0006_authority_lifecycle",
	"0007_classroom_governance_foundation",
	"0008_governance_audit_events",
	"0009_classroom_quota_expiration",
	"0010_classroom_instance_batches",
	"0011_controlled_access_sessions",
}

type artifactGCConfig struct {
	ArtifactRoot                  string
	PostgreSQLURLFile             string
	OfflineConfirmation           string
	DeleteConfirmation            string
	Apply                         bool
	AllowInsecureLoopbackDatabase bool
}

type nativeArtifactGCConfig struct {
	CoreStateRoot       string
	OfflineConfirmation string
	DeleteConfirmation  string
	Apply               bool
}

func runArtifactGC(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("gc-artifacts", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg artifactGCConfig
	flags.StringVar(&cfg.ArtifactRoot, "artifact-root", "", "stopped deployment's exact shared artifact root")
	flags.StringVar(&cfg.PostgreSQLURLFile, "postgres-url-file", "", "protected PostgreSQL URL file")
	flags.StringVar(&cfg.OfflineConfirmation, "offline-confirmation", "", "required stopped-replica confirmation")
	flags.StringVar(&cfg.DeleteConfirmation, "delete-confirmation", "", "required exact deletion confirmation with --apply")
	flags.BoolVar(&cfg.Apply, "apply", false, "delete verified unreferenced objects and abandoned staging files")
	flags.BoolVar(&cfg.AllowInsecureLoopbackDatabase, "allow-insecure-loopback-database", false, "test-only: permit sslmode=disable on loopback")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gc-artifacts accepts no positional arguments")
	}
	if cfg.OfflineConfirmation != offlineConfirmation {
		return fmt.Errorf("gc-artifacts requires --offline-confirmation=%s", offlineConfirmation)
	}
	if cfg.Apply && cfg.DeleteConfirmation != artifactDeleteConfirmation {
		return fmt.Errorf("gc-artifacts --apply requires --delete-confirmation=%s", artifactDeleteConfirmation)
	}
	maintenance, err := artifactfs.OpenMaintenance(cfg.ArtifactRoot)
	if err != nil {
		return err
	}
	defer maintenance.Close()
	connection, err := loadDatabaseConnection(cfg.PostgreSQLURLFile, cfg.AllowInsecureLoopbackDatabase)
	if err != nil {
		return err
	}
	database, err := sql.Open("pgx", connection.dsn)
	if err != nil {
		return fmt.Errorf("open artifact-reference database")
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to artifact-reference database")
	}
	return collectDatabaseArtifacts(ctx, database, maintenance, cfg.Apply, output)
}

func runNativeArtifactGC(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("gc-artifacts-native", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg nativeArtifactGCConfig
	flags.StringVar(&cfg.CoreStateRoot, "core-state-root", "", "stopped native core state root")
	flags.StringVar(&cfg.OfflineConfirmation, "offline-confirmation", "", "required stopped-service confirmation")
	flags.StringVar(&cfg.DeleteConfirmation, "delete-confirmation", "", "required exact deletion confirmation with --apply")
	flags.BoolVar(&cfg.Apply, "apply", false, "delete verified unreferenced objects and abandoned staging files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gc-artifacts-native accepts no positional arguments")
	}
	if cfg.OfflineConfirmation != offlineConfirmation {
		return fmt.Errorf("gc-artifacts-native requires --offline-confirmation=%s", offlineConfirmation)
	}
	if cfg.Apply && cfg.DeleteConfirmation != artifactDeleteConfirmation {
		return fmt.Errorf("gc-artifacts-native --apply requires --delete-confirmation=%s", artifactDeleteConfirmation)
	}
	if err := requireNativeRecoveryPlatform(); err != nil {
		return err
	}
	coreRoot, err := checkedStateRoot(cfg.CoreStateRoot)
	if err != nil {
		return fmt.Errorf("core state root: %w", err)
	}
	databaseLock, err := acquireNativeDatabaseLock(coreRoot)
	if err != nil {
		return err
	}
	defer releaseNativeDatabaseLock(databaseLock)
	maintenance, err := artifactfs.OpenMaintenance(filepath.Join(coreRoot, "artifacts"))
	if err != nil {
		return err
	}
	defer maintenance.Close()
	database, _, err := openNativeDatabase(coreRoot)
	if err != nil {
		return err
	}
	defer database.Close()
	return collectDatabaseArtifacts(ctx, database, maintenance, cfg.Apply, output)
}

func collectDatabaseArtifacts(ctx context.Context, database *sql.DB, maintenance *artifactfs.Maintenance, apply bool, output io.Writer) error {
	watermark, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return err
	}
	if watermark.ActiveJobs != 0 {
		return fmt.Errorf("refusing artifact collection while %d durable operations are active", watermark.ActiveJobs)
	}
	references, err := readCommittedArtifactReferences(ctx, database)
	if err != nil {
		return err
	}
	result, err := maintenance.Collect(ctx, references, apply)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"applied=%t\nobjects=%d\nreferenced=%d\nunreferenced=%d\nstagingFiles=%d\nbytesReclaimable=%d\nobjectsDeleted=%d\nstagingFilesDeleted=%d\n",
		result.Applied, result.Objects, result.ReferencedObjects, result.UnreferencedObjects, result.StagingFiles,
		result.BytesReclaimable, result.ObjectsDeleted, result.StagingFilesDeleted,
	)
	return err
}

func readCommittedArtifactReferences(ctx context.Context, database *sql.DB) ([]domain.ArtifactRef, error) {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, fmt.Errorf("begin artifact-reference snapshot")
	}
	defer func() { _ = tx.Rollback() }()
	migrationRows, err := tx.QueryContext(ctx, `SELECT id FROM labcentral_schema_migrations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read artifact-reference schema")
	}
	var migrations []string
	for migrationRows.Next() {
		var id string
		if err := migrationRows.Scan(&id); err != nil {
			_ = migrationRows.Close()
			return nil, fmt.Errorf("read artifact-reference schema")
		}
		migrations = append(migrations, id)
	}
	if err := migrationRows.Close(); err != nil || migrationRows.Err() != nil {
		return nil, fmt.Errorf("read artifact-reference schema")
	}
	expected := append([]string(nil), artifactReferenceSchemaMigrations...)
	sort.Strings(expected)
	if len(migrations) != len(expected) {
		return nil, fmt.Errorf("database migration set is not the exact artifact-reference schema")
	}
	for index := range expected {
		if migrations[index] != expected[index] {
			return nil, fmt.Errorf("database migration set is not the exact artifact-reference schema")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT bundle_artifact_ref FROM labcentral_template_revisions ORDER BY bundle_artifact_ref`)
	if err != nil {
		return nil, fmt.Errorf("read committed artifact references")
	}
	defer rows.Close()
	references := make([]domain.ArtifactRef, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("read committed artifact reference")
		}
		reference, err := domain.NewArtifactRef(value)
		if err != nil {
			return nil, fmt.Errorf("database contains an invalid artifact reference")
		}
		if _, err := domain.ParseSHA256Digest(value); err != nil {
			return nil, fmt.Errorf("database contains a non-content-addressed artifact reference")
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read committed artifact references")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit artifact-reference snapshot")
	}
	return references, nil
}
