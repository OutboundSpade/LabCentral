package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRecoveryKeygenIsProtectedAndNonOverwriting(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := generateRecoveryKeys(directory, "backup-key-1")
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		paths.Identity: 0o600, paths.SigningKey: 0o600, paths.Recipient: 0o644, paths.Trust: 0o644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode=%v", path, info.Mode().Perm())
		}
	}
	if _, err := generateRecoveryKeys(directory, "backup-key-1"); err == nil {
		t.Fatal("key generation overwrote an existing recovery key set")
	}
	if contents, err := os.ReadFile(paths.Trust); err != nil || bytes.Contains(contents, []byte("privateKey")) {
		t.Fatalf("public trust file contains private material: %v", err)
	}
	if err := os.Chmod(paths.Trust, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningTrust(paths.Trust, "backup-key-1"); err == nil {
		t.Fatal("group/other-writable recovery trust was accepted")
	}
	_, privateKey, err := loadSigningKey(paths.SigningKey)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schemaVersion":"labcentral.recovery/v1"}`)
	signature := ed25519.Sign(privateKey, recoverySignatureMessage(manifest))
	if ed25519.Verify(privateKey.Public().(ed25519.PublicKey), manifest, signature) {
		t.Fatal("recovery signature was valid without its domain separation prefix")
	}
	clear(privateKey)
}

func TestRecoveryBundleIsEncryptedSignedAndRestoresSafeState(t *testing.T) {
	directory := privateTestDirectory(t)
	paths, err := generateRecoveryKeys(directory, "backup-key-1")
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(directory, "state")
	if err := os.MkdirAll(filepath.Join(stateRoot, "artifacts", "sha256"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "artifacts", "sha256", "object"), []byte("artifact-secret-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	databaseDump := filepath.Join(directory, "database.dump")
	if err := os.WriteFile(databaseDump, []byte("postgres-custom-dump-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateArchive := filepath.Join(directory, "state.tar")
	if err := archiveStateRoot(stateRoot, stateArchive); err != nil {
		t.Fatal(err)
	}
	databaseDigest, databaseSize, _ := fileDigest(databaseDump)
	stateDigest, stateSize, _ := fileDigest(stateArchive)
	manifest := recoveryManifest{
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Release: "v1.2.3",
		Database: recoveryDatabaseManifest{
			Engine: "postgresql", Format: "custom", ToolVersion: "pg_dump (PostgreSQL) 18.1",
			AuditPosition: 12, EventPosition: 34, Jobs: 2,
			Component: recoveryComponent{Name: "database.dump", SHA256: databaseDigest, SizeBytes: databaseSize},
		},
		State: recoveryComponent{Name: "state.tar", SHA256: stateDigest, SizeBytes: stateSize},
	}
	output := filepath.Join(directory, "backup.age")
	result, err := writeRecoverySet(output, paths.Recipient, paths.SigningKey, manifest, databaseDump, stateArchive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeRecoverySet(output, paths.Recipient, paths.SigningKey, manifest, databaseDump, stateArchive); err == nil {
		t.Fatal("recovery set creation overwrote an existing output")
	}
	if result.SHA256 == "" || result.AuditPosition != 12 || result.EventPosition != 34 {
		t.Fatalf("create result = %#v", result)
	}
	encrypted, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{"artifact-secret-canary", "postgres-custom-dump-canary", "v1.2.3"} {
		if bytes.Contains(encrypted, []byte(plaintext)) {
			t.Fatalf("encrypted recovery set exposes %q", plaintext)
		}
	}
	verified, cleanup, err := openRecoverySet(output, paths.Identity, paths.Trust)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || verified.Manifest.Release != "v1.2.3" || verified.Manifest.SignerKeyID != "backup-key-1" {
		t.Fatalf("verified set=%#v err=%v", verified.Manifest, err)
	}
	restored := filepath.Join(directory, "restored")
	if err := restoreStateArchive(verified.StateComponent, restored, false); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(restored, "artifacts", "sha256", "object"))
	if err != nil || string(contents) != "artifact-secret-canary" {
		t.Fatalf("restored artifact=%q err=%v", contents, err)
	}
	if info, err := os.Stat(filepath.Join(restored, "artifacts", "sha256", "object")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestRecoveryRejectsTamperingRevocationAndArchiveTraversal(t *testing.T) {
	directory := privateTestDirectory(t)
	paths, err := generateRecoveryKeys(directory, "backup-key-1")
	if err != nil {
		t.Fatal(err)
	}
	databaseDump := filepath.Join(directory, "database.dump")
	if err := os.WriteFile(databaseDump, []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	maliciousState := filepath.Join(directory, "state.tar")
	file, err := os.OpenFile(maliciousState, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	databaseDigest, databaseSize, _ := fileDigest(databaseDump)
	stateDigest, stateSize, _ := fileDigest(maliciousState)
	manifest := recoveryManifest{
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Release: "v1.2.3",
		Database: recoveryDatabaseManifest{Engine: "postgresql", Format: "custom", ToolVersion: "pg_dump (PostgreSQL) 18.1",
			Component: recoveryComponent{Name: "database.dump", SHA256: databaseDigest, SizeBytes: databaseSize}},
		State: recoveryComponent{Name: "state.tar", SHA256: stateDigest, SizeBytes: stateSize},
	}
	output := filepath.Join(directory, "backup.age")
	if _, err := writeRecoverySet(output, paths.Recipient, paths.SigningKey, manifest, databaseDump, maliciousState); err != nil {
		t.Fatal(err)
	}
	verified, cleanup, err := openRecoverySet(output, paths.Identity, paths.Trust)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreStateArchive(verified.StateComponent, filepath.Join(directory, "escape-target"), false); err == nil {
		t.Fatal("path-traversing state archive was restored")
	}
	if _, err := os.Stat(filepath.Join(directory, "escape")); !os.IsNotExist(err) {
		t.Fatal("path-traversing state archive wrote outside the staging root")
	}
	if _, err := os.Stat(filepath.Join(directory, "escape-target")); !os.IsNotExist(err) {
		t.Fatal("path-traversing state archive committed its destination")
	}
	if runtime.GOOS != "windows" {
		linkRoot := filepath.Join(directory, "link-state")
		if err := os.Mkdir(linkRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../database.dump", filepath.Join(linkRoot, "linked")); err != nil {
			t.Fatal(err)
		}
		if err := archiveStateRoot(linkRoot, filepath.Join(directory, "linked-state.tar")); err == nil {
			t.Fatal("state archiver accepted a symbolic link")
		}
	}

	tampered := append([]byte(nil), mustReadFile(t, output)...)
	tampered[len(tampered)-1] ^= 0xff
	tamperedPath := filepath.Join(directory, "tampered.age")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := openRecoverySet(tamperedPath, paths.Identity, paths.Trust); err == nil {
		cleanup()
		t.Fatal("tampered encrypted recovery set verified")
	}

	var trust signingTrustFile
	if err := strictJSON(mustReadFile(t, paths.Trust), &trust); err != nil {
		t.Fatal(err)
	}
	trust.Keys[0].Revoked = true
	encoded, _ := json.Marshal(trust)
	if err := os.WriteFile(paths.Trust, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := openRecoverySet(output, paths.Identity, paths.Trust); err == nil {
		cleanup()
		t.Fatal("recovery set signed by a revoked key verified")
	}
}

func TestNativeRestorePrestagesBothComponents(t *testing.T) {
	directory := privateTestDirectory(t)
	paths, err := generateRecoveryKeys(directory, "native-prestage")
	if err != nil {
		t.Fatal(err)
	}
	coreRoot := filepath.Join(directory, "core-source")
	store, err := localdb.Open(context.Background(), filepath.Join(coreRoot, "labcentral.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	coreArchive := filepath.Join(directory, "core-state.tar")
	if err := archiveStateRoot(coreRoot, coreArchive); err != nil {
		t.Fatal(err)
	}
	providerArchive := filepath.Join(directory, "provider-state.tar")
	file, err := os.OpenFile(providerArchive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: "../provider-escape", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	coreDigest, coreSize, _ := fileDigest(coreArchive)
	providerDigest, providerSize, _ := fileDigest(providerArchive)
	manifest := recoveryManifest{
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Release: "v1.2.3",
		Database: recoveryDatabaseManifest{
			Engine: "sqlite", Format: "filesystem", ToolVersion: "SQLite test",
			Component: recoveryComponent{Name: "core-state.tar", SHA256: coreDigest, SizeBytes: coreSize},
		},
		State: recoveryComponent{Name: "provider-state.tar", SHA256: providerDigest, SizeBytes: providerSize},
	}
	backup := filepath.Join(directory, "prestage.age")
	if _, err := writeRecoverySet(backup, paths.Recipient, paths.SigningKey, manifest, coreArchive, providerArchive); err != nil {
		t.Fatal(err)
	}
	coreDestination, providerDestination := filepath.Join(directory, "core-destination"), filepath.Join(directory, "provider-destination")
	if _, err := restoreNativeRecoverySet(context.Background(), nativeRestoreConfig{
		Input: backup, IdentityFile: paths.Identity, TrustFile: paths.Trust, ExpectedRelease: "v1.2.3",
		CoreStateRoot: coreDestination, ProviderStateRoot: providerDestination, Confirmation: offlineConfirmation,
	}); err == nil {
		t.Fatal("native restore accepted an unsafe provider archive")
	}
	for _, destination := range []string{coreDestination, providerDestination} {
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("failed prestage committed %s", destination)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "provider-escape")); !os.IsNotExist(err) {
		t.Fatal("unsafe provider archive escaped its staging directory")
	}
}

func TestRecoveryDatabaseURLAndPostgresEnvironmentAreFailClosed(t *testing.T) {
	directory := privateTestDirectory(t)
	urlFile := filepath.Join(directory, "postgres.url")
	if err := os.WriteFile(urlFile, []byte("postgresql://user:password-secret-canary@127.0.0.1:5432/labcentral?sslmode=disable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDatabaseConnection(urlFile, false); err == nil {
		t.Fatal("plaintext PostgreSQL URL was accepted without the test-only loopback opt-in")
	}
	connection, err := loadDatabaseConnection(urlFile, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSWORD", "inherited-secret-canary")
	environment := strings.Join(postgresToolEnvironment(connection, "/private/.pgpass"), "\n")
	if strings.Contains(environment, "password-secret-canary") || strings.Contains(environment, "inherited-secret-canary") || !strings.Contains(environment, "PGPASSFILE=/private/.pgpass") {
		t.Fatalf("PostgreSQL environment leaked or omitted credentials: %s", environment)
	}
	if major := postgresToolMajor("pg_restore (PostgreSQL) 18.1"); major != "18" {
		t.Fatalf("PostgreSQL tool major = %q", major)
	}
	if runtime.GOOS != "windows" && pgPassEscape(`a:b\c`) != `a\:b\\c` {
		t.Fatalf("pgpass escaping is invalid: %q", pgPassEscape(`a:b\c`))
	}
}

func TestRecoveryRestoreRequiresExactReleaseAndOfflineConfirmation(t *testing.T) {
	if _, err := restoreRecoverySet(context.Background(), restoreConfig{}); err == nil || !strings.Contains(err.Error(), "offline-confirmation") {
		t.Fatalf("restore without confirmation returned %v", err)
	}
	if _, err := createRecoverySet(context.Background(), createConfig{}, time.Now); err == nil || !strings.Contains(err.Error(), "offline-confirmation") {
		t.Fatalf("create without confirmation returned %v", err)
	}
}

func TestPostgreSQLRecoveryCreateVerifyRestore(t *testing.T) {
	baseDSN := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	container := os.Getenv("LABCENTRAL_TEST_POSTGRES_TOOLS_CONTAINER")
	if baseDSN == "" || container == "" {
		t.Skip("PostgreSQL recovery test environment is not configured")
	}
	if !safeRecoveryToken(container) {
		t.Fatal("PostgreSQL tools container name is unsafe")
	}
	if err := exec.Command("docker", "inspect", container).Run(); err != nil {
		t.Skipf("PostgreSQL tools container is unavailable: %v", err)
	}
	parsed, err := url.Parse(baseDSN)
	if err != nil || parsed.User == nil {
		t.Fatalf("parse PostgreSQL test URL: %v", err)
	}
	password, _ := parsed.User.Password()
	t.Setenv("RECOVERY_TEST_CONTAINER", container)
	t.Setenv("RECOVERY_TEST_PASSWORD", password)
	t.Setenv("RECOVERY_TEST_USER", parsed.User.Username())

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
	sourceDatabase, restoredDatabase := "labcentral_recovery_source_"+suffix, "labcentral_recovery_restored_"+suffix
	for _, database := range []string{sourceDatabase, restoredDatabase} {
		if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+database+`"`); err != nil {
			t.Fatal(err)
		}
		defer func(database string) {
			_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+database+`" WITH (FORCE)`)
		}(database)
	}
	sourceDSN := recoveryTestDatabaseURL(parsed, sourceDatabase)
	restoredDSN := recoveryTestDatabaseURL(parsed, restoredDatabase)
	store, err := localdb.OpenPostgreSQL(context.Background(), sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("pgx", restoredDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(context.Background(), `CREATE SCHEMA preexisting_restore_object`); err != nil {
		t.Fatal(err)
	}
	if empty, err := databaseIsEmpty(context.Background(), target); err != nil || empty {
		t.Fatalf("database with a non-public object reported empty: empty=%v err=%v", empty, err)
	}
	if _, err := target.ExecContext(context.Background(), `DROP SCHEMA preexisting_restore_object`); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	directory := privateTestDirectory(t)
	paths, err := generateRecoveryKeys(directory, "postgres-rehearsal")
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(directory, "state")
	if err := os.MkdirAll(filepath.Join(stateRoot, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stateRoot, "proxmox-journal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "artifacts", "object"), []byte("restored-artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceURLFile, restoredURLFile := filepath.Join(directory, "source.url"), filepath.Join(directory, "restored.url")
	if err := os.WriteFile(sourceURLFile, []byte(sourceDSN+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoredURLFile, []byte(restoredDSN+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pgDump, pgRestore := writePostgresContainerWrappers(t, directory)
	backupPath := filepath.Join(directory, "postgres-rehearsal.age")
	created, err := createRecoverySet(context.Background(), createConfig{
		Output: backupPath, Release: "v1.2.3", PostgreSQLURLFile: sourceURLFile, StateRoot: stateRoot,
		RecipientFile: paths.Recipient, SigningKeyFile: paths.SigningKey, PGDump: pgDump,
		Confirmation: offlineConfirmation, AllowInsecureLoopbackDatabase: true,
	}, time.Now)
	if err != nil || created.SHA256 == "" {
		t.Fatalf("create recovery set = %#v, %v", created, err)
	}
	if verified, cleanup, err := openRecoverySet(backupPath, paths.Identity, paths.Trust); err != nil {
		t.Fatal(err)
	} else {
		cleanup()
		if verified.Manifest.Database.ToolVersion == "" {
			t.Fatal("verified recovery set omitted the PostgreSQL version")
		}
	}
	restoredState := filepath.Join(directory, "restored-state")
	manifest, err := restoreRecoverySet(context.Background(), restoreConfig{
		Input: backupPath, IdentityFile: paths.Identity, TrustFile: paths.Trust, ExpectedRelease: "v1.2.3",
		PostgreSQLURLFile: restoredURLFile, StateRoot: restoredState, PGRestore: pgRestore,
		Confirmation: offlineConfirmation, PreserveOwnership: false, AllowInsecureLoopbackDatabase: true,
	})
	if err != nil || manifest.Release != "v1.2.3" {
		t.Fatalf("restore recovery set = %#v, %v", manifest, err)
	}
	restored, err := localdb.OpenPostgreSQL(context.Background(), restoredDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(restoredState, "artifacts", "object")); err != nil || string(contents) != "restored-artifact" {
		t.Fatalf("restored state=%q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(restoredState, "proxmox-journal")); err != nil {
		t.Fatalf("restored provider journal: %v", err)
	}
}

func TestNativeSQLiteRecoveryCreateVerifyRestoreAndLockExclusion(t *testing.T) {
	directory := privateTestDirectory(t)
	paths, err := generateRecoveryKeys(directory, "native-rehearsal")
	if err != nil {
		t.Fatal(err)
	}
	coreRoot, providerRoot := filepath.Join(directory, "core"), filepath.Join(directory, "provider")
	store, err := localdb.Open(context.Background(), filepath.Join(coreRoot, "labcentral.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(coreRoot, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreRoot, "artifacts", "object"), []byte("native-artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(providerRoot, "proxmox-journal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerRoot, "proxmox-journal", "entry"), []byte("native-journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := nativeCreateConfig{
		Output: filepath.Join(directory, "native.age"), Release: "v1.2.3", CoreStateRoot: coreRoot,
		ProviderStateRoot: providerRoot, RecipientFile: paths.Recipient, SigningKeyFile: paths.SigningKey,
		Confirmation: offlineConfirmation,
	}
	if _, err := createNativeRecoverySet(context.Background(), configuration, time.Now); err == nil || !strings.Contains(err.Error(), "still owns") {
		t.Fatalf("native backup while labd lock was held returned %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	created, err := createNativeRecoverySet(context.Background(), configuration, time.Now)
	if err != nil || created.SHA256 == "" {
		t.Fatalf("create native recovery set = %#v, %v", created, err)
	}
	verified, cleanup, err := openRecoverySet(configuration.Output, paths.Identity, paths.Trust)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || verified.Manifest.Database.Engine != "sqlite" || verified.Manifest.Database.Format != "filesystem" {
		t.Fatalf("verify native recovery set = %#v, %v", verified.Manifest, err)
	}
	if _, err := restoreNativeRecoverySet(context.Background(), nativeRestoreConfig{
		Input: configuration.Output, IdentityFile: paths.Identity, TrustFile: paths.Trust,
		ExpectedRelease: "v1.2.4", Confirmation: offlineConfirmation,
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("native restore accepted a mismatched release: %v", err)
	}
	if _, err := restoreRecoverySet(context.Background(), restoreConfig{
		Input: configuration.Output, IdentityFile: paths.Identity, TrustFile: paths.Trust,
		ExpectedRelease: "v1.2.3", Confirmation: offlineConfirmation,
	}); err == nil || !strings.Contains(err.Error(), "not a PostgreSQL") {
		t.Fatalf("PostgreSQL restore accepted native recovery set: %v", err)
	}
	restoredCore, restoredProvider := filepath.Join(directory, "restored-core"), filepath.Join(directory, "restored-provider")
	manifest, err := restoreNativeRecoverySet(context.Background(), nativeRestoreConfig{
		Input: configuration.Output, IdentityFile: paths.Identity, TrustFile: paths.Trust, ExpectedRelease: "v1.2.3",
		CoreStateRoot: restoredCore, ProviderStateRoot: restoredProvider, Confirmation: offlineConfirmation, PreserveOwnership: true,
	})
	if err != nil || manifest.Release != "v1.2.3" {
		t.Fatalf("restore native recovery set = %#v, %v", manifest, err)
	}
	if contents, err := os.ReadFile(filepath.Join(restoredCore, "artifacts", "object")); err != nil || string(contents) != "native-artifact" {
		t.Fatalf("restored native artifact=%q err=%v", contents, err)
	}
	if contents, err := os.ReadFile(filepath.Join(restoredProvider, "proxmox-journal", "entry")); err != nil || string(contents) != "native-journal" {
		t.Fatalf("restored native journal=%q err=%v", contents, err)
	}
	restoredStore, err := localdb.Open(context.Background(), filepath.Join(restoredCore, "labcentral.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := restoredStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func recoveryTestDatabaseURL(base *url.URL, database string) string {
	copyOf := *base
	copyOf.Path = "/" + database
	query := copyOf.Query()
	query.Set("sslmode", "disable")
	copyOf.RawQuery = query.Encode()
	return copyOf.String()
}

func writePostgresContainerWrappers(t *testing.T, directory string) (string, string) {
	t.Helper()
	dump := filepath.Join(directory, "pg_dump-wrapper")
	restore := filepath.Join(directory, "pg_restore-wrapper")
	dumpScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  exec docker exec "$RECOVERY_TEST_CONTAINER" pg_dump --version
fi
output=
database=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --file) output=$2; shift 2 ;;
    --dbname) database=$2; shift 2 ;;
    *) shift ;;
  esac
done
test -n "$output"
test -n "$database"
docker exec -e PGPASSWORD="$RECOVERY_TEST_PASSWORD" "$RECOVERY_TEST_CONTAINER" pg_dump \
  --format=custom --no-owner --no-privileges --username "$RECOVERY_TEST_USER" --dbname "$database" > "$output"
`
	restoreScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  exec docker exec "$RECOVERY_TEST_CONTAINER" pg_restore --version
fi
dump=
database=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dbname) database=$2; shift 2 ;;
    --host|--port|--username) shift 2 ;;
    --*) shift ;;
    *) if [ -z "$dump" ]; then dump=$1; fi; shift ;;
  esac
done
test -n "$dump"
test -n "$database"
docker exec -i -e PGPASSWORD="$RECOVERY_TEST_PASSWORD" "$RECOVERY_TEST_CONTAINER" pg_restore \
  --exit-on-error --no-owner --no-privileges --username "$RECOVERY_TEST_USER" --dbname "$database" < "$dump"
`
	if err := os.WriteFile(dump, []byte(dumpScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restore, []byte(restoreScript), 0o700); err != nil {
		t.Fatal(err)
	}
	return dump, restore
}

func privateTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
