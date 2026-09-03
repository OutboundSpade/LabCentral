package authentication_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestArgon2idPasswordHashIsSaltedStrictAndVerifiable(t *testing.T) {
	password := []byte("correct horse battery staple")
	first, err := authentication.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authentication.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected password encodings %q and %q", first, second)
	}
	if err := authentication.VerifyPassword(first, password); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		encoded  string
		password []byte
	}{{first, []byte("incorrect password value")}, {first + "garbage", password}, {"$argon2id$invalid", password}} {
		if err := authentication.VerifyPassword(candidate.encoded, candidate.password); !errors.Is(err, authentication.ErrInvalidCredentials) {
			t.Fatalf("invalid password/hash returned %v", err)
		}
	}
	if _, err := authentication.HashPassword([]byte("too-short")); err == nil {
		t.Fatal("short password was accepted")
	}
}

func TestLocalSessionsRotateAcrossKeyringsValidateCSRFAndRevoke(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:authentication-manager?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	oldKeys := parseKeyring(t, "old", map[string]byte{"old": 'a'})
	manager, err := authentication.NewManager(store, oldKeys, authentication.ManagerOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	password := []byte("a-production-length-password")
	if err := manager.BootstrapLocalAdministrator(ctx, domain.UserID("user.admin"), "admin", "Administrator", password); err != nil {
		t.Fatal(err)
	}
	if err := manager.BootstrapLocalAdministrator(ctx, domain.UserID("user.other"), "admin", "Other", password); err == nil {
		t.Fatal("second local bootstrap replaced the administrator")
	}
	if _, err := manager.AuthenticateLocal(ctx, "admin", []byte("wrong-password-value")); !errors.Is(err, authentication.ErrInvalidCredentials) {
		t.Fatalf("wrong password returned %v", err)
	}
	credentials, err := manager.AuthenticateLocal(ctx, "admin", password)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.CookieToken == "" || credentials.CSRFToken == "" || credentials.Session.KeyID != "old" {
		t.Fatalf("initial credentials = %#v", credentials)
	}
	if err := manager.ValidateCSRF(credentials.Session, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidateCSRF(credentials.Session, "wrong-csrf-token"); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("invalid CSRF returned %v", err)
	}

	rotatedKeys := parseKeyring(t, "new", map[string]byte{"new": 'b', "old": 'a'})
	rotatedManager, err := authentication.NewManager(store, rotatedKeys, authentication.ManagerOptions{Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := rotatedManager.AuthenticateSession(ctx, credentials.CookieToken)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.Rotated || rotated.CookieToken == credentials.CookieToken || rotated.CSRFToken == "" || rotated.Session.KeyID != "new" || rotated.Session.RotatedFromID != credentials.Session.ID {
		t.Fatalf("rotated credentials = %#v", rotated)
	}
	if _, err := rotatedManager.AuthenticateSession(ctx, credentials.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("old session remained valid after rotation: %v", err)
	}
	recoveryToken, err := rotatedManager.IssuePasswordRecovery(ctx, "user.admin")
	if err != nil {
		t.Fatal(err)
	}
	siblingRecoveryToken, err := rotatedManager.IssuePasswordRecovery(ctx, "user.admin")
	if err != nil {
		t.Fatal(err)
	}
	newPassword := []byte("a-new-production-length-password")
	if err := rotatedManager.RecoverLocalPassword(ctx, recoveryToken, newPassword); err != nil {
		t.Fatal(err)
	}
	if err := rotatedManager.RecoverLocalPassword(ctx, siblingRecoveryToken, []byte("another-production-password")); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("sibling recovery token remained valid: %v", err)
	}
	if err := rotatedManager.RecoverLocalPassword(ctx, recoveryToken, newPassword); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("recovery token replay returned %v", err)
	}
	if _, err := rotatedManager.AuthenticateSession(ctx, rotated.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("password recovery did not revoke sessions: %v", err)
	}
	if _, err := rotatedManager.AuthenticateLocal(ctx, "admin", password); !errors.Is(err, authentication.ErrInvalidCredentials) {
		t.Fatalf("old password remained valid: %v", err)
	}
	newCredentials, err := rotatedManager.AuthenticateLocal(ctx, "admin", newPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.UserByID(ctx, "user.admin")
	if err != nil {
		t.Fatal(err)
	}
	user.Disabled = true
	if err := store.PutUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := rotatedManager.AuthenticateSession(ctx, newCredentials.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("disabled user's session remained valid: %v", err)
	}
	user.Disabled = false
	if err := store.PutUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := rotatedManager.AuthenticateSession(ctx, newCredentials.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("re-enabled user resurrected a revoked session: %v", err)
	}
	reenabledCredentials, err := rotatedManager.AuthenticateLocal(ctx, "admin", newPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotatedManager.Revoke(ctx, reenabledCredentials.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rotatedManager.AuthenticateSession(ctx, reenabledCredentials.CookieToken); !errors.Is(err, authentication.ErrUnauthenticated) {
		t.Fatalf("revoked session remained valid: %v", err)
	}
}

func TestSessionKeyringRejectsUnknownDuplicateAndTrailingInput(t *testing.T) {
	valid := fmt.Sprintf(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"one","keys":[{"id":"one","material":%q}]}`, keyMaterial('a'))
	for _, input := range []string{
		strings.Replace(valid, `"material"`, `"unknown":true,"material"`, 1),
		valid + `{}`,
		fmt.Sprintf(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"one","keys":[{"id":"one","material":%q},{"id":"one","material":%q}]}`, keyMaterial('a'), keyMaterial('b')),
	} {
		if _, err := authentication.ParseKeyring([]byte(input)); err == nil {
			t.Fatalf("unsafe keyring was accepted: %s", input)
		}
	}
}

func parseKeyring(t *testing.T, current string, keys map[string]byte) *authentication.Keyring {
	t.Helper()
	encoded := make([]string, 0, len(keys))
	for id, value := range keys {
		encoded = append(encoded, fmt.Sprintf(`{"id":%q,"material":%q}`, id, keyMaterial(value)))
	}
	document := fmt.Sprintf(`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":%q,"keys":[%s]}`, current, strings.Join(encoded, ","))
	keyring, err := authentication.ParseKeyring([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func keyMaterial(value byte) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(value), 32)))
}
