package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type createConfig struct {
	Output                        string
	Release                       string
	PostgreSQLURLFile             string
	StateRoot                     string
	RecipientFile                 string
	SigningKeyFile                string
	PGDump                        string
	Confirmation                  string
	AllowInsecureLoopbackDatabase bool
}

type restoreConfig struct {
	Input                         string
	IdentityFile                  string
	TrustFile                     string
	ExpectedRelease               string
	PostgreSQLURLFile             string
	StateRoot                     string
	PGRestore                     string
	Confirmation                  string
	PreserveOwnership             bool
	AllowInsecureLoopbackDatabase bool
}

type databaseConnection struct {
	dsn      string
	host     string
	port     string
	user     string
	database string
	password string
	ssl      map[string]string
}

type databaseWatermark struct {
	AuditPosition uint64
	EventPosition uint64
	Jobs          uint64
	ActiveJobs    uint64
}

var postgresToolMajorPattern = regexp.MustCompile(`(?i)postgresql\)?\s+([0-9]+)(?:\.|\s|$)`)

func createRecoverySet(ctx context.Context, cfg createConfig, now func() time.Time) (createResult, error) {
	if cfg.Confirmation != offlineConfirmation {
		return createResult{}, fmt.Errorf("create requires --offline-confirmation=%s", offlineConfirmation)
	}
	if !safeRecoveryToken(cfg.Release) || now == nil {
		return createResult{}, fmt.Errorf("release must be a safe exact identifier")
	}
	stateRoot, err := checkedStateRoot(cfg.StateRoot)
	if err != nil {
		return createResult{}, err
	}
	connection, err := loadDatabaseConnection(cfg.PostgreSQLURLFile, cfg.AllowInsecureLoopbackDatabase)
	if err != nil {
		return createResult{}, err
	}
	database, err := sql.Open("pgx", connection.dsn)
	if err != nil {
		return createResult{}, fmt.Errorf("open recovery database connection")
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return createResult{}, fmt.Errorf("connect to recovery database")
	}
	before, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return createResult{}, err
	}
	if before.ActiveJobs != 0 {
		return createResult{}, fmt.Errorf("refusing backup while %d durable operations are active", before.ActiveJobs)
	}
	temporary, err := os.MkdirTemp("", "labcentral-recovery-create-")
	if err != nil {
		return createResult{}, err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return createResult{}, err
	}
	stateArchive := filepath.Join(temporary, "state.tar")
	if err := archiveStateRoot(stateRoot, stateArchive); err != nil {
		return createResult{}, err
	}
	databaseDump := filepath.Join(temporary, "database.dump")
	toolVersion, err := postgresToolVersion(ctx, cfg.PGDump)
	if err != nil {
		return createResult{}, err
	}
	if err := runPostgresTool(ctx, cfg.PGDump, []string{
		"--format=custom", "--no-owner", "--no-privileges", "--file", databaseDump,
	}, connection, temporary); err != nil {
		return createResult{}, fmt.Errorf("create PostgreSQL custom-format dump: %w", err)
	}
	after, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return createResult{}, err
	}
	if before != after {
		return createResult{}, fmt.Errorf("database watermark changed during backup; keep every replica stopped and retry")
	}
	databaseDigest, databaseSize, err := fileDigest(databaseDump)
	if err != nil || databaseSize <= 0 || databaseSize > maximumDatabaseDumpBytes {
		return createResult{}, fmt.Errorf("database dump is empty, unreadable, or too large")
	}
	stateDigest, stateSize, err := fileDigest(stateArchive)
	if err != nil || stateSize <= 0 || stateSize > maximumStateArchiveBytes {
		return createResult{}, fmt.Errorf("state archive is empty, unreadable, or too large")
	}
	manifest := recoveryManifest{
		CreatedAt: now().UTC().Format(time.RFC3339Nano), Release: cfg.Release,
		Database: recoveryDatabaseManifest{
			Engine: "postgresql", Format: "custom", ToolVersion: toolVersion,
			AuditPosition: before.AuditPosition, EventPosition: before.EventPosition, Jobs: before.Jobs,
			Component: recoveryComponent{Name: "database.dump", SHA256: databaseDigest, SizeBytes: databaseSize},
		},
		State: recoveryComponent{Name: "state.tar", SHA256: stateDigest, SizeBytes: stateSize},
	}
	return writeRecoverySet(cfg.Output, cfg.RecipientFile, cfg.SigningKeyFile, manifest, databaseDump, stateArchive)
}

func restoreRecoverySet(ctx context.Context, cfg restoreConfig) (recoveryManifest, error) {
	if cfg.Confirmation != offlineConfirmation {
		return recoveryManifest{}, fmt.Errorf("restore requires --offline-confirmation=%s", offlineConfirmation)
	}
	if !safeRecoveryToken(cfg.ExpectedRelease) {
		return recoveryManifest{}, fmt.Errorf("expected-release must be a safe exact identifier")
	}
	verified, cleanup, err := openRecoverySet(cfg.Input, cfg.IdentityFile, cfg.TrustFile)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return recoveryManifest{}, err
	}
	if verified.Manifest.Database.Engine != "postgresql" {
		return recoveryManifest{}, fmt.Errorf("recovery set is not a PostgreSQL recovery set")
	}
	if verified.Manifest.Release != cfg.ExpectedRelease {
		return recoveryManifest{}, fmt.Errorf("recovery release %q does not match expected release %q", verified.Manifest.Release, cfg.ExpectedRelease)
	}
	stateRoot, err := checkedNewPath(cfg.StateRoot)
	if err != nil {
		return recoveryManifest{}, err
	}
	stagedState, err := stageStateArchive(verified.StateComponent, stateRoot, cfg.PreserveOwnership)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("validate shared state before database restore: %w", err)
	}
	defer func() {
		if stagedState != "" {
			_ = os.RemoveAll(stagedState)
		}
	}()
	connection, err := loadDatabaseConnection(cfg.PostgreSQLURLFile, cfg.AllowInsecureLoopbackDatabase)
	if err != nil {
		return recoveryManifest{}, err
	}
	database, err := sql.Open("pgx", connection.dsn)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("open restore database connection")
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return recoveryManifest{}, fmt.Errorf("connect to restore database")
	}
	empty, err := databaseIsEmpty(ctx, database)
	if err != nil {
		return recoveryManifest{}, err
	}
	if !empty {
		return recoveryManifest{}, fmt.Errorf("restore database is not empty")
	}
	restoreVersion, err := postgresToolVersion(ctx, cfg.PGRestore)
	if err != nil {
		return recoveryManifest{}, err
	}
	if postgresToolMajor(restoreVersion) != postgresToolMajor(verified.Manifest.Database.ToolVersion) {
		return recoveryManifest{}, fmt.Errorf("pg_restore major version does not match the backup pg_dump major version")
	}
	if err := runPostgresTool(ctx, cfg.PGRestore, []string{
		"--exit-on-error", "--no-owner", "--no-privileges", verified.DatabaseComponent,
	}, connection, filepath.Dir(verified.DatabaseComponent)); err != nil {
		return recoveryManifest{}, fmt.Errorf("restore PostgreSQL dump: %w", err)
	}
	stateCommitted, err := commitStateArchive(stagedState, stateRoot)
	if err != nil {
		if stateCommitted {
			if rollbackErr := rollbackStateArchive(stateRoot, stagedState); rollbackErr != nil {
				return recoveryManifest{}, fmt.Errorf("commit shared state after database restore: %w (state rollback also failed: %v)", err, rollbackErr)
			}
		}
		return recoveryManifest{}, fmt.Errorf("commit shared state after database restore: %w", err)
	}
	stagedState = ""
	after, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return recoveryManifest{}, err
	}
	if after.AuditPosition != verified.Manifest.Database.AuditPosition || after.EventPosition != verified.Manifest.Database.EventPosition || after.Jobs != verified.Manifest.Database.Jobs || after.ActiveJobs != 0 {
		return recoveryManifest{}, fmt.Errorf("restored database watermark does not match the signed manifest")
	}
	return verified.Manifest, nil
}

func loadDatabaseConnection(path string, allowInsecureLoopback bool) (databaseConnection, error) {
	contents, err := readProtectedFile(path, 16<<10, true)
	if err != nil {
		return databaseConnection{}, fmt.Errorf("read PostgreSQL URL: %w", err)
	}
	defer clear(contents)
	dsn := strings.TrimSpace(string(contents))
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Host == "" || parsed.User == nil || parsed.Fragment != "" {
		return databaseConnection{}, fmt.Errorf("PostgreSQL URL is invalid")
	}
	databaseName := strings.TrimPrefix(parsed.EscapedPath(), "/")
	databaseName, err = url.PathUnescape(databaseName)
	if err != nil || databaseName == "" || strings.Contains(databaseName, "/") {
		return databaseConnection{}, fmt.Errorf("PostgreSQL database name is invalid")
	}
	host, port, username := parsed.Hostname(), parsed.Port(), parsed.User.Username()
	if host == "" || username == "" {
		return databaseConnection{}, fmt.Errorf("PostgreSQL host is invalid")
	}
	if port == "" {
		port = "5432"
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return databaseConnection{}, fmt.Errorf("PostgreSQL port is invalid")
	}
	query := parsed.Query()
	allowed := map[string]bool{"sslmode": true, "sslrootcert": true, "sslcert": true, "sslkey": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return databaseConnection{}, fmt.Errorf("PostgreSQL URL option %q is unsupported", key)
		}
	}
	sslMode := query.Get("sslmode")
	loopback := host == "localhost"
	if address := net.ParseIP(host); address != nil {
		loopback = address.IsLoopback()
	}
	if sslMode != "verify-full" && !(allowInsecureLoopback && loopback && sslMode == "disable") {
		return databaseConnection{}, fmt.Errorf("PostgreSQL recovery requires sslmode=verify-full")
	}
	for _, option := range []struct {
		name      string
		sensitive bool
	}{{"sslrootcert", false}, {"sslcert", false}, {"sslkey", true}} {
		value := query.Get(option.name)
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			return databaseConnection{}, fmt.Errorf("PostgreSQL %s must be absolute", option.name)
		}
		if _, err := readProtectedFile(value, 4<<20, option.sensitive); err != nil {
			return databaseConnection{}, fmt.Errorf("PostgreSQL %s is invalid: %w", option.name, err)
		}
	}
	password, _ := parsed.User.Password()
	for _, value := range []string{host, port, username, databaseName, password} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return databaseConnection{}, fmt.Errorf("PostgreSQL URL contains an unsafe control character")
		}
	}
	return databaseConnection{
		dsn: dsn, host: host, port: port, user: username, database: databaseName, password: password,
		ssl: map[string]string{"PGSSLMODE": sslMode, "PGSSLROOTCERT": query.Get("sslrootcert"), "PGSSLCERT": query.Get("sslcert"), "PGSSLKEY": query.Get("sslkey")},
	}, nil
}

func readDatabaseWatermark(ctx context.Context, database *sql.DB) (databaseWatermark, error) {
	var watermark databaseWatermark
	var audit, event, jobs, active int64
	err := database.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT MAX(stream_position) FROM labcentral_audit_records), 0),
		COALESCE((SELECT MAX(stream_position) FROM labcentral_events), 0),
		(SELECT COUNT(*) FROM labcentral_jobs),
		(SELECT COUNT(*) FROM labcentral_jobs WHERE state IN ('running', 'cancel_requested', 'compensating'))`).Scan(&audit, &event, &jobs, &active)
	if err != nil || audit < 0 || event < 0 || jobs < 0 || active < 0 {
		return databaseWatermark{}, fmt.Errorf("read LabCentral recovery watermark")
	}
	watermark.AuditPosition, watermark.EventPosition, watermark.Jobs, watermark.ActiveJobs = uint64(audit), uint64(event), uint64(jobs), uint64(active)
	return watermark, nil
}

func databaseIsEmpty(ctx context.Context, database *sql.DB) (bool, error) {
	var populated bool
	if err := database.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
		UNION ALL
		SELECT 1 FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
		UNION ALL
		SELECT 1 FROM pg_catalog.pg_namespace n
		WHERE n.nspname !~ '^pg_' AND n.nspname NOT IN ('information_schema', 'public')
	)`).Scan(&populated); err != nil {
		return false, fmt.Errorf("inspect restore database")
	}
	return !populated, nil
}

func postgresToolVersion(ctx context.Context, executable string) (string, error) {
	if executable == "" {
		return "", fmt.Errorf("PostgreSQL tool executable is required")
	}
	command := exec.CommandContext(ctx, executable, "--version")
	output, err := command.Output()
	if err != nil || len(output) == 0 || len(output) > 4096 {
		return "", fmt.Errorf("read PostgreSQL tool version")
	}
	version := strings.TrimSpace(string(output))
	if postgresToolMajor(version) == "" {
		return "", fmt.Errorf("PostgreSQL tool returned an unrecognized version")
	}
	return version, nil
}

func postgresToolMajor(version string) string {
	matches := postgresToolMajorPattern.FindStringSubmatch(version)
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func runPostgresTool(ctx context.Context, executable string, arguments []string, connection databaseConnection, temporaryDirectory string) error {
	passfile := filepath.Join(temporaryDirectory, ".pgpass")
	line := strings.Join([]string{
		pgPassEscape(connection.host), pgPassEscape(connection.port), pgPassEscape(connection.database),
		pgPassEscape(connection.user), pgPassEscape(connection.password),
	}, ":") + "\n"
	if err := writeExclusiveFile(passfile, []byte(line), 0o600); err != nil {
		return err
	}
	defer os.Remove(passfile)
	connectionArguments := []string{
		"--host", connection.host, "--port", connection.port, "--username", connection.user, "--dbname", connection.database,
	}
	arguments = append(connectionArguments, arguments...)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = postgresToolEnvironment(connection, passfile)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 2048 {
			message = message[:2048]
		}
		return fmt.Errorf("PostgreSQL tool failed: %s", message)
	}
	return nil
}

func postgresToolEnvironment(connection databaseConnection, passfile string) []string {
	blocked := map[string]bool{
		"PGPASSWORD": true, "PGPASSFILE": true, "PGHOST": true, "PGPORT": true, "PGUSER": true,
		"PGDATABASE": true, "PGSSLMODE": true, "PGSSLROOTCERT": true, "PGSSLCERT": true, "PGSSLKEY": true,
	}
	environment := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "PGPASSFILE="+passfile)
	for key, value := range connection.ssl {
		if value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func pgPassEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, ":", `\:`)
}
