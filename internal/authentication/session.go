package authentication

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const keyringSchemaV1 = "labcentral.session-keys/v1"

var ErrUnauthenticated = errors.New("authentication required")

type keyringDocument struct {
	SchemaVersion string        `json:"schemaVersion"`
	CurrentKeyID  string        `json:"currentKeyId"`
	Keys          []keyDocument `json:"keys"`
}

type keyDocument struct {
	ID       string `json:"id"`
	Material string `json:"material"`
}

type sessionKey struct {
	id       string
	material []byte
}

type Keyring struct {
	current sessionKey
	keys    []sessionKey
}

func ParseKeyring(contents []byte) (*Keyring, error) {
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var document keyringDocument
	if err := decoder.Decode(&document); err != nil || document.SchemaVersion != keyringSchemaV1 || document.CurrentKeyID == "" || len(document.Keys) == 0 || len(document.Keys) > 3 {
		return nil, fmt.Errorf("session keyring is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("session keyring contains trailing data")
	}
	result := &Keyring{keys: make([]sessionKey, 0, len(document.Keys))}
	seen := make(map[string]bool, len(document.Keys))
	for _, encoded := range document.Keys {
		if encoded.ID == "" || len(encoded.ID) > 64 || seen[encoded.ID] {
			return nil, fmt.Errorf("session keyring key identity is invalid")
		}
		material, err := base64.RawURLEncoding.Strict().DecodeString(encoded.Material)
		if err != nil || len(material) != 32 {
			return nil, fmt.Errorf("session keyring key material is invalid")
		}
		seen[encoded.ID] = true
		key := sessionKey{id: encoded.ID, material: material}
		result.keys = append(result.keys, key)
		if key.id == document.CurrentKeyID {
			result.current = key
		}
	}
	if result.current.id == "" {
		return nil, fmt.Errorf("session keyring current key is missing")
	}
	return result, nil
}

type ManagerOptions struct {
	SessionLifetime  time.Duration
	RotationAge      time.Duration
	RecoveryLifetime time.Duration
	Now              func() time.Time
}

type Manager struct {
	repository application.AuthRepository
	keyring    *Keyring
	lifetime   time.Duration
	rotation   time.Duration
	recovery   time.Duration
	now        func() time.Time
	dummyHash  string
}

type SessionCredentials struct {
	Session        application.SessionRecord
	RequestSession application.SessionRecord
	CookieToken    string
	CSRFToken      string
	Rotated        bool
}

func NewManager(repository application.AuthRepository, keyring *Keyring, options ManagerOptions) (*Manager, error) {
	if repository == nil || keyring == nil || keyring.current.id == "" {
		return nil, fmt.Errorf("authentication repository and session keyring are required")
	}
	if options.SessionLifetime <= 0 {
		options.SessionLifetime = 12 * time.Hour
	}
	if options.RotationAge <= 0 {
		options.RotationAge = 15 * time.Minute
	}
	if options.RotationAge >= options.SessionLifetime {
		return nil, fmt.Errorf("session rotation age must be shorter than session lifetime")
	}
	if options.RecoveryLifetime <= 0 {
		options.RecoveryLifetime = 30 * time.Minute
	}
	if options.RecoveryLifetime > time.Hour {
		return nil, fmt.Errorf("password recovery lifetime must not exceed one hour")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	dummy, err := HashPassword([]byte("not-a-real-password-value"))
	if err != nil {
		return nil, err
	}
	return &Manager{repository: repository, keyring: keyring, lifetime: options.SessionLifetime, rotation: options.RotationAge, recovery: options.RecoveryLifetime, now: options.Now, dummyHash: dummy}, nil
}

func (m *Manager) BootstrapLocalAdministrator(ctx context.Context, id domain.UserID, username, displayName string, password []byte) error {
	encoded, err := HashPassword(password)
	if err != nil {
		return err
	}
	assignmentID, err := randomID("role")
	if err != nil {
		return err
	}
	created, err := m.repository.CreateBootstrapAdministrator(ctx,
		application.UserRecord{ID: id, Username: username, DisplayName: displayName, PasswordHash: encoded},
		application.RoleAssignmentRecord{
			ID: assignmentID, SubjectKind: application.SubjectUser, SubjectID: string(id), ScopeKind: application.ScopePlatform,
			ScopeID: "platform", Role: application.RolePlatformAdministrator, ValidFrom: m.now().UTC(),
		}, nil)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("local bootstrap is allowed only when no users exist")
	}
	return nil
}

func (m *Manager) BootstrapOIDCAdministrator(ctx context.Context, id domain.UserID, username, displayName, issuer, subject string) error {
	assignmentID, err := randomID("role")
	if err != nil {
		return err
	}
	identity := &application.ExternalIdentityRecord{Issuer: issuer, Subject: subject, UserID: id}
	created, err := m.repository.CreateBootstrapAdministrator(ctx,
		application.UserRecord{ID: id, Username: username, DisplayName: displayName},
		application.RoleAssignmentRecord{
			ID: assignmentID, SubjectKind: application.SubjectUser, SubjectID: string(id), ScopeKind: application.ScopePlatform,
			ScopeID: "platform", Role: application.RolePlatformAdministrator, ValidFrom: m.now().UTC(),
		}, identity)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("OIDC bootstrap is allowed only when no users exist")
	}
	return nil
}

func (m *Manager) CreateLocalUser(ctx context.Context, id domain.UserID, username, displayName string, password []byte) error {
	encoded, err := HashPassword(password)
	if err != nil {
		return err
	}
	return m.repository.CreateUser(ctx, application.UserRecord{ID: id, Username: username, DisplayName: displayName, PasswordHash: encoded}, nil)
}

func (m *Manager) CreateOIDCUser(ctx context.Context, id domain.UserID, username, displayName, issuer, subject string) error {
	identity := &application.ExternalIdentityRecord{Issuer: issuer, Subject: subject, UserID: id}
	return m.repository.CreateUser(ctx, application.UserRecord{ID: id, Username: username, DisplayName: displayName}, identity)
}

func (m *Manager) AuthenticateLocal(ctx context.Context, username string, password []byte) (SessionCredentials, error) {
	user, err := m.repository.UserByUsername(ctx, username)
	if err != nil {
		_ = VerifyPassword(m.dummyHash, password)
		return SessionCredentials{}, ErrInvalidCredentials
	}
	if user.Disabled || user.PasswordHash == "" || VerifyPassword(user.PasswordHash, password) != nil {
		return SessionCredentials{}, ErrInvalidCredentials
	}
	return m.newSession(ctx, user.ID, "")
}

func (m *Manager) NewSessionForUser(ctx context.Context, userID domain.UserID) (SessionCredentials, error) {
	user, err := m.repository.UserByID(ctx, userID)
	if err != nil || user.Disabled {
		return SessionCredentials{}, ErrUnauthenticated
	}
	return m.newSession(ctx, userID, "")
}

func (m *Manager) AuthenticateSession(ctx context.Context, cookieToken string) (SessionCredentials, error) {
	if len(cookieToken) < 32 || len(cookieToken) > 256 {
		return SessionCredentials{}, ErrUnauthenticated
	}
	now := m.now().UTC()
	for _, key := range m.keyring.keys {
		hash := keyedHash(key.material, "session", cookieToken)
		session, err := m.repository.SessionByTokenHash(ctx, hash, now)
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			continue
		}
		if err != nil {
			return SessionCredentials{}, err
		}
		credentials := SessionCredentials{Session: session, RequestSession: session, CookieToken: cookieToken}
		if session.KeyID != m.keyring.current.id || !session.CreatedAt.IsZero() && now.Sub(session.CreatedAt) >= m.rotation {
			replacement, err := m.newSessionRecord(session.UserID, session.ID)
			if err != nil {
				return SessionCredentials{}, err
			}
			if err := m.repository.RotateSession(ctx, session.ID, replacement.Session, now); err != nil {
				return SessionCredentials{}, err
			}
			replacement.Rotated = true
			replacement.RequestSession = session
			return replacement, nil
		}
		return credentials, nil
	}
	return SessionCredentials{}, ErrUnauthenticated
}

func (m *Manager) ValidateCSRF(session application.SessionRecord, token string) error {
	if token == "" || len(token) > 256 {
		return ErrUnauthenticated
	}
	key, found := m.keyByID(session.KeyID)
	if !found || !hmac.Equal([]byte(session.CSRFHash), []byte(keyedHash(key.material, "csrf", token))) {
		return ErrUnauthenticated
	}
	return nil
}

func (m *Manager) Revoke(ctx context.Context, sessionID string) error {
	if err := m.repository.RevokeSession(ctx, sessionID, m.now().UTC()); err != nil && !errors.Is(err, application.ErrAuthRecordNotFound) {
		return err
	}
	return nil
}

func (m *Manager) IssuePasswordRecovery(ctx context.Context, userID domain.UserID) (string, error) {
	user, err := m.repository.UserByID(ctx, userID)
	if err != nil || user.Disabled || user.PasswordHash == "" {
		return "", ErrUnauthenticated
	}
	id, err := randomID("recovery")
	if err != nil {
		return "", err
	}
	valueBytes := make([]byte, 32)
	if _, err := rand.Read(valueBytes); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(valueBytes)
	zero(valueBytes)
	record := application.PasswordRecoveryRecord{
		ID: id, UserID: userID, TokenHash: keyedHash(m.keyring.current.material, "recovery", value),
		KeyID: m.keyring.current.id, ExpiresAt: m.now().UTC().Add(m.recovery),
	}
	if err := m.repository.CreatePasswordRecovery(ctx, record); err != nil {
		return "", err
	}
	return value, nil
}

func (m *Manager) RecoverLocalPassword(ctx context.Context, token string, password []byte) error {
	if len(token) < 32 || len(token) > 256 {
		return ErrUnauthenticated
	}
	encoded, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	for _, key := range m.keyring.keys {
		err := m.repository.ConsumePasswordRecovery(ctx, keyedHash(key.material, "recovery", token), key.id, encoded, now)
		if errors.Is(err, application.ErrAuthRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		return nil
	}
	return ErrUnauthenticated
}

func (m *Manager) newSession(ctx context.Context, userID domain.UserID, rotatedFrom string) (SessionCredentials, error) {
	credentials, err := m.newSessionRecord(userID, rotatedFrom)
	if err != nil {
		return SessionCredentials{}, err
	}
	if err := m.repository.CreateSession(ctx, credentials.Session); err != nil {
		return SessionCredentials{}, err
	}
	return credentials, nil
}

func (m *Manager) newSessionRecord(userID domain.UserID, rotatedFrom string) (SessionCredentials, error) {
	sessionID, err := randomID("session")
	if err != nil {
		return SessionCredentials{}, err
	}
	tokenBytes := make([]byte, 32)
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return SessionCredentials{}, err
	}
	if _, err := rand.Read(csrfBytes); err != nil {
		return SessionCredentials{}, err
	}
	cookieToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	csrfToken := base64.RawURLEncoding.EncodeToString(csrfBytes)
	zero(tokenBytes)
	zero(csrfBytes)
	now := m.now().UTC()
	record := application.SessionRecord{
		ID: sessionID, UserID: userID,
		TokenHash: keyedHash(m.keyring.current.material, "session", cookieToken),
		CSRFHash:  keyedHash(m.keyring.current.material, "csrf", csrfToken),
		KeyID:     m.keyring.current.id, ExpiresAt: now.Add(m.lifetime), RotatedFromID: rotatedFrom,
	}
	return SessionCredentials{Session: record, RequestSession: record, CookieToken: cookieToken, CSRFToken: csrfToken}, nil
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	id := prefix + "." + hex.EncodeToString(value)
	zero(value)
	return id, nil
}

func (m *Manager) keyByID(id string) (sessionKey, bool) {
	for _, key := range m.keyring.keys {
		if key.id == id {
			return key, true
		}
	}
	return sessionKey{}, false
}

func keyedHash(key []byte, purpose, value string) string {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte("labcentral." + purpose + ".v1\x00" + value))
	return hex.EncodeToString(hash.Sum(nil))
}
