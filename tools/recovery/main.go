// Command recovery creates, verifies, and restores encrypted LabCentral
// PostgreSQL/shared-state recovery sets.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const offlineConfirmation = "I_HAVE_STOPPED_ALL_LABCENTRAL_REPLICAS"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "recovery:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("expected keygen, create, create-native, verify, restore, or restore-native")
	}
	switch args[0] {
	case "keygen":
		return runKeygen(args[1:], output)
	case "create":
		return runCreate(ctx, args[1:], output)
	case "create-native":
		return runCreateNative(ctx, args[1:], output)
	case "verify":
		return runVerify(args[1:], output)
	case "restore":
		return runRestore(ctx, args[1:], output)
	case "restore-native":
		return runRestoreNative(ctx, args[1:], output)
	default:
		return fmt.Errorf("unknown command %q; expected keygen, create, create-native, verify, restore, or restore-native", args[0])
	}
}

func runCreateNative(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("create-native", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg nativeCreateConfig
	flags.StringVar(&cfg.Output, "output", "", "new encrypted recovery-set path")
	flags.StringVar(&cfg.Release, "release", "", "exact LabCentral release identifier")
	flags.StringVar(&cfg.CoreStateRoot, "core-state-root", "", "stopped native core state root")
	flags.StringVar(&cfg.ProviderStateRoot, "provider-state-root", "", "stopped native provider state root")
	flags.StringVar(&cfg.RecipientFile, "recipient-file", "", "age recipient file")
	flags.StringVar(&cfg.SigningKeyFile, "signing-key-file", "", "recovery-set Ed25519 signing key")
	flags.StringVar(&cfg.Confirmation, "offline-confirmation", "", "required stopped-service confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("create-native accepts no positional arguments")
	}
	result, err := createNativeRecoverySet(ctx, cfg, time.Now)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "wrote=%s\nsha256=%s\nwatermark=%d/%d\n", result.Path, result.SHA256, result.AuditPosition, result.EventPosition)
	return err
}

func runKeygen(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var directory, keyID string
	flags.StringVar(&directory, "directory", "", "existing private output directory")
	flags.StringVar(&keyID, "key-id", "", "stable signing key identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("keygen accepts no positional arguments")
	}
	paths, err := generateRecoveryKeys(directory, keyID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "identity=%s\nrecipient=%s\nsigningKey=%s\ntrust=%s\n", paths.Identity, paths.Recipient, paths.SigningKey, paths.Trust)
	return err
}

func runCreate(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg createConfig
	flags.StringVar(&cfg.Output, "output", "", "new encrypted recovery-set path")
	flags.StringVar(&cfg.Release, "release", "", "exact LabCentral release identifier")
	flags.StringVar(&cfg.PostgreSQLURLFile, "postgres-url-file", "", "protected PostgreSQL URL file")
	flags.StringVar(&cfg.StateRoot, "state-root", "", "shared artifacts and provider-journal root")
	flags.StringVar(&cfg.RecipientFile, "recipient-file", "", "age recipient file")
	flags.StringVar(&cfg.SigningKeyFile, "signing-key-file", "", "recovery-set Ed25519 signing key")
	flags.StringVar(&cfg.PGDump, "pg-dump", "pg_dump", "pg_dump executable")
	flags.StringVar(&cfg.Confirmation, "offline-confirmation", "", "required stopped-replica confirmation")
	flags.BoolVar(&cfg.AllowInsecureLoopbackDatabase, "allow-insecure-loopback-database", false, "test-only: permit sslmode=disable on loopback")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("create accepts no positional arguments")
	}
	result, err := createRecoverySet(ctx, cfg, time.Now)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "wrote=%s\nsha256=%s\nwatermark=%d/%d\n", result.Path, result.SHA256, result.AuditPosition, result.EventPosition)
	return err
}

func runVerify(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var input, identity, trust string
	flags.StringVar(&input, "input", "", "encrypted recovery-set path")
	flags.StringVar(&identity, "identity-file", "", "protected age identity file")
	flags.StringVar(&trust, "trust-file", "", "recovery signing trust file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("verify accepts no positional arguments")
	}
	verified, cleanup, err := openRecoverySet(input, identity, trust)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "verified=true\nrelease=%s\ncreatedAt=%s\nsigner=%s\n", verified.Manifest.Release, verified.Manifest.CreatedAt, verified.Manifest.SignerKeyID)
	return err
}

func runRestore(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg restoreConfig
	flags.StringVar(&cfg.Input, "input", "", "encrypted recovery-set path")
	flags.StringVar(&cfg.IdentityFile, "identity-file", "", "protected age identity file")
	flags.StringVar(&cfg.TrustFile, "trust-file", "", "recovery signing trust file")
	flags.StringVar(&cfg.ExpectedRelease, "expected-release", "", "exact LabCentral release that will open the restore")
	flags.StringVar(&cfg.PostgreSQLURLFile, "postgres-url-file", "", "empty target PostgreSQL URL file")
	flags.StringVar(&cfg.StateRoot, "state-root", "", "new shared state root")
	flags.StringVar(&cfg.PGRestore, "pg-restore", "pg_restore", "pg_restore executable")
	flags.StringVar(&cfg.Confirmation, "offline-confirmation", "", "required stopped-replica confirmation")
	flags.BoolVar(&cfg.PreserveOwnership, "preserve-ownership", true, "restore numeric file ownership")
	flags.BoolVar(&cfg.AllowInsecureLoopbackDatabase, "allow-insecure-loopback-database", false, "test-only: permit sslmode=disable on loopback")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("restore accepts no positional arguments")
	}
	manifest, err := restoreRecoverySet(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "restored=true\nrelease=%s\nwatermark=%d/%d\n", manifest.Release, manifest.Database.AuditPosition, manifest.Database.EventPosition)
	return err
}

func runRestoreNative(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore-native", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var cfg nativeRestoreConfig
	flags.StringVar(&cfg.Input, "input", "", "encrypted recovery-set path")
	flags.StringVar(&cfg.IdentityFile, "identity-file", "", "protected age identity file")
	flags.StringVar(&cfg.TrustFile, "trust-file", "", "recovery signing trust file")
	flags.StringVar(&cfg.ExpectedRelease, "expected-release", "", "exact LabCentral release that will open the restore")
	flags.StringVar(&cfg.CoreStateRoot, "core-state-root", "", "new native core state root")
	flags.StringVar(&cfg.ProviderStateRoot, "provider-state-root", "", "new native provider state root")
	flags.StringVar(&cfg.Confirmation, "offline-confirmation", "", "required stopped-service confirmation")
	flags.BoolVar(&cfg.PreserveOwnership, "preserve-ownership", true, "restore numeric file ownership")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("restore-native accepts no positional arguments")
	}
	manifest, err := restoreNativeRecoverySet(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "restored=true\nrelease=%s\nwatermark=%d/%d\n", manifest.Release, manifest.Database.AuditPosition, manifest.Database.EventPosition)
	return err
}
