package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type nativeCreateConfig struct {
	Output            string
	Release           string
	CoreStateRoot     string
	ProviderStateRoot string
	RecipientFile     string
	SigningKeyFile    string
	Confirmation      string
}

type nativeRestoreConfig struct {
	Input             string
	IdentityFile      string
	TrustFile         string
	ExpectedRelease   string
	CoreStateRoot     string
	ProviderStateRoot string
	Confirmation      string
	PreserveOwnership bool
}

func createNativeRecoverySet(ctx context.Context, cfg nativeCreateConfig, now func() time.Time) (createResult, error) {
	if err := requireNativeRecoveryPlatform(); err != nil {
		return createResult{}, err
	}
	if cfg.Confirmation != offlineConfirmation {
		return createResult{}, fmt.Errorf("create-native requires --offline-confirmation=%s", offlineConfirmation)
	}
	if !safeRecoveryToken(cfg.Release) || now == nil {
		return createResult{}, fmt.Errorf("release must be a safe exact identifier")
	}
	coreRoot, err := checkedStateRoot(cfg.CoreStateRoot)
	if err != nil {
		return createResult{}, fmt.Errorf("core state root: %w", err)
	}
	providerRoot, err := checkedStateRoot(cfg.ProviderStateRoot)
	if err != nil {
		return createResult{}, fmt.Errorf("provider state root: %w", err)
	}
	if coreRoot == providerRoot {
		return createResult{}, fmt.Errorf("core and provider state roots must be distinct")
	}
	lock, err := acquireNativeDatabaseLock(coreRoot)
	if err != nil {
		return createResult{}, err
	}
	defer releaseNativeDatabaseLock(lock)
	database, version, err := openNativeDatabase(coreRoot)
	if err != nil {
		return createResult{}, err
	}
	defer database.Close()
	before, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return createResult{}, err
	}
	if before.ActiveJobs != 0 {
		return createResult{}, fmt.Errorf("refusing backup while %d durable operations are active", before.ActiveJobs)
	}
	temporary, err := os.MkdirTemp("", "labcentral-native-recovery-create-")
	if err != nil {
		return createResult{}, err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return createResult{}, err
	}
	coreArchive, providerArchive := filepath.Join(temporary, "core-state.tar"), filepath.Join(temporary, "provider-state.tar")
	if err := archiveStateRoot(coreRoot, coreArchive); err != nil {
		return createResult{}, err
	}
	if err := archiveStateRoot(providerRoot, providerArchive); err != nil {
		return createResult{}, err
	}
	after, err := readDatabaseWatermark(ctx, database)
	if err != nil {
		return createResult{}, err
	}
	if before != after {
		return createResult{}, fmt.Errorf("SQLite watermark changed during backup; keep every native service stopped and retry")
	}
	coreDigest, coreSize, err := fileDigest(coreArchive)
	if err != nil || coreSize <= 0 || coreSize > maximumStateArchiveBytes {
		return createResult{}, fmt.Errorf("core state archive is empty, unreadable, or too large")
	}
	providerDigest, providerSize, err := fileDigest(providerArchive)
	if err != nil || providerSize <= 0 || providerSize > maximumStateArchiveBytes {
		return createResult{}, fmt.Errorf("provider state archive is empty, unreadable, or too large")
	}
	manifest := recoveryManifest{
		CreatedAt: now().UTC().Format(time.RFC3339Nano), Release: cfg.Release,
		Database: recoveryDatabaseManifest{
			Engine: "sqlite", Format: "filesystem", ToolVersion: "SQLite " + version,
			AuditPosition: before.AuditPosition, EventPosition: before.EventPosition, Jobs: before.Jobs,
			Component: recoveryComponent{Name: "core-state.tar", SHA256: coreDigest, SizeBytes: coreSize},
		},
		State: recoveryComponent{Name: "provider-state.tar", SHA256: providerDigest, SizeBytes: providerSize},
	}
	return writeRecoverySet(cfg.Output, cfg.RecipientFile, cfg.SigningKeyFile, manifest, coreArchive, providerArchive)
}

func restoreNativeRecoverySet(ctx context.Context, cfg nativeRestoreConfig) (recoveryManifest, error) {
	if err := requireNativeRecoveryPlatform(); err != nil {
		return recoveryManifest{}, err
	}
	if cfg.Confirmation != offlineConfirmation {
		return recoveryManifest{}, fmt.Errorf("restore-native requires --offline-confirmation=%s", offlineConfirmation)
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
	if verified.Manifest.Database.Engine != "sqlite" {
		return recoveryManifest{}, fmt.Errorf("recovery set is not a native SQLite recovery set")
	}
	if verified.Manifest.Release != cfg.ExpectedRelease {
		return recoveryManifest{}, fmt.Errorf("recovery release %q does not match expected release %q", verified.Manifest.Release, cfg.ExpectedRelease)
	}
	coreRoot, err := checkedNewPath(cfg.CoreStateRoot)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("core state root: %w", err)
	}
	providerRoot, err := checkedNewPath(cfg.ProviderStateRoot)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("provider state root: %w", err)
	}
	if coreRoot == providerRoot {
		return recoveryManifest{}, fmt.Errorf("core and provider state roots must be distinct")
	}
	coreStage, err := stageStateArchive(verified.DatabaseComponent, coreRoot, cfg.PreserveOwnership)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("validate native core state: %w", err)
	}
	defer func() {
		if coreStage != "" {
			_ = os.RemoveAll(coreStage)
		}
	}()
	providerStage, err := stageStateArchive(verified.StateComponent, providerRoot, cfg.PreserveOwnership)
	if err != nil {
		return recoveryManifest{}, fmt.Errorf("validate native provider state: %w", err)
	}
	defer func() {
		if providerStage != "" {
			_ = os.RemoveAll(providerStage)
		}
	}()
	database, _, err := openNativeDatabase(coreStage)
	if err != nil {
		return recoveryManifest{}, err
	}
	after, err := readDatabaseWatermark(ctx, database)
	closeErr := database.Close()
	if err != nil {
		return recoveryManifest{}, err
	}
	if closeErr != nil {
		return recoveryManifest{}, closeErr
	}
	if after.AuditPosition != verified.Manifest.Database.AuditPosition || after.EventPosition != verified.Manifest.Database.EventPosition || after.Jobs != verified.Manifest.Database.Jobs || after.ActiveJobs != 0 {
		return recoveryManifest{}, fmt.Errorf("restored SQLite watermark does not match the signed manifest")
	}
	coreCommitted, err := commitStateArchive(coreStage, coreRoot)
	if err != nil {
		if coreCommitted {
			if rollbackErr := rollbackStateArchive(coreRoot, coreStage); rollbackErr != nil {
				return recoveryManifest{}, fmt.Errorf("commit native core state: %w (rollback also failed: %v)", err, rollbackErr)
			}
		}
		return recoveryManifest{}, fmt.Errorf("commit native core state: %w", err)
	}
	providerCommitted, err := commitStateArchive(providerStage, providerRoot)
	if err != nil {
		var providerRollbackErr error
		if providerCommitted {
			providerRollbackErr = rollbackStateArchive(providerRoot, providerStage)
		}
		coreRollbackErr := rollbackStateArchive(coreRoot, coreStage)
		if providerRollbackErr != nil || coreRollbackErr != nil {
			return recoveryManifest{}, fmt.Errorf("commit native provider state: %w (provider rollback: %v; core rollback: %v)", err, providerRollbackErr, coreRollbackErr)
		}
		return recoveryManifest{}, fmt.Errorf("commit native provider state: %w; staged core state was rolled back", err)
	}
	coreStage = ""
	providerStage = ""
	return verified.Manifest, nil
}

func acquireNativeDatabaseLock(coreRoot string) (*os.File, error) {
	path := filepath.Join(coreRoot, "labcentral.db.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("native database lock file is missing or unsafe")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open native database lock: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("native database lock changed while it was being opened")
	}
	if err := lockNativeDatabaseFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("native labd still owns the database lock; stop every native service first")
	}
	return file, nil
}

func releaseNativeDatabaseLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unlockNativeDatabaseFile(file)
	_ = file.Close()
}

func openNativeDatabase(coreRoot string) (*sql.DB, string, error) {
	path := filepath.Join(coreRoot, "labcentral.db")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return nil, "", fmt.Errorf("native SQLite database is missing or unsafe")
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "", fmt.Errorf("open native SQLite database")
	}
	var integrity, version string
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		_ = database.Close()
		return nil, "", fmt.Errorf("native SQLite integrity check failed")
	}
	if err := database.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil || version == "" {
		_ = database.Close()
		return nil, "", fmt.Errorf("read native SQLite version")
	}
	return database, version, nil
}
