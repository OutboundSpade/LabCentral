package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

var identityNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Store) EnsureAuthenticationMode(ctx context.Context, mode string) error {
	if mode != "local" && mode != "oidc" {
		return fmt.Errorf("durable authentication mode is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_installation_settings (setting_key, setting_value, updated_at_us)
		VALUES ('authentication_mode', ?, `+s.nowMicros()+`) ON CONFLICT (setting_key) DO NOTHING`), mode); err != nil {
		return err
	}
	var recorded string
	if err := tx.QueryRowContext(ctx, `SELECT setting_value FROM labcentral_installation_settings WHERE setting_key = 'authentication_mode'`).Scan(&recorded); err != nil {
		return err
	}
	if recorded != mode {
		return fmt.Errorf("configured authentication mode %q conflicts with the installation mode", mode)
	}
	return tx.Commit()
}

func (s *Store) RefuseAuthenticationDowngrade(ctx context.Context) error {
	var recorded string
	err := s.db.QueryRowContext(ctx, `SELECT setting_value FROM labcentral_installation_settings WHERE setting_key = 'authentication_mode'`).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("installation authentication mode %q cannot be downgraded to no-auth", recorded)
}

func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM labcentral_users LIMIT 1`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) CreateBootstrapAdministrator(ctx context.Context, user application.UserRecord, assignment application.RoleAssignmentRecord, identity *application.ExternalIdentityRecord) (bool, error) {
	if _, err := domain.NewUserID(string(user.ID)); err != nil || !identityNamePattern.MatchString(user.Username) || user.DisplayName == "" || user.PasswordHash == "" && identity == nil ||
		assignment.SubjectKind != application.SubjectUser || assignment.SubjectID != string(user.ID) || assignment.ScopeKind != application.ScopePlatform || assignment.ScopeID != "platform" || assignment.Role != application.RolePlatformAdministrator || !assignment.ValidUntil.IsZero() {
		return false, fmt.Errorf("bootstrap administrator is invalid")
	}
	if identity != nil {
		issuer, err := url.Parse(identity.Issuer)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || issuer.String() != identity.Issuer || identity.Subject == "" || len(identity.Subject) > 512 || identity.UserID != user.ID {
			return false, fmt.Errorf("bootstrap external identity is invalid")
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.dialect == "postgresql" {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4c6142626f6f7473)); err != nil {
			return false, err
		}
	}
	var users int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_users`).Scan(&users); err != nil {
		return false, err
	}
	if users != 0 {
		return false, tx.Commit()
	}
	disabled := 0
	if user.Disabled {
		disabled = 1
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_users
		(user_id, username, display_name, password_hash, disabled, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`), user.ID, user.Username, user.DisplayName, user.PasswordHash, disabled); err != nil {
		return false, err
	}
	from, until, err := validity(assignment.ValidFrom, assignment.ValidUntil)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_role_assignments
		(assignment_id, subject_kind, subject_id, scope_kind, scope_id, role, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), assignment.ID, assignment.SubjectKind, assignment.SubjectID,
		assignment.ScopeKind, assignment.ScopeID, assignment.Role, from, until); err != nil {
		return false, err
	}
	if identity != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_auth_identities
			(issuer, subject, user_id, created_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`)`), identity.Issuer, identity.Subject, identity.UserID); err != nil {
			return false, err
		}
	}
	mode := "local"
	if identity != nil {
		mode = "oidc"
	}
	modeResult, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_installation_settings (setting_key, setting_value, updated_at_us)
		VALUES ('authentication_mode', ?, `+s.nowMicros()+`)
		ON CONFLICT(setting_key) DO UPDATE SET updated_at_us = `+s.nowMicros()+`
		WHERE labcentral_installation_settings.setting_value = excluded.setting_value`), mode)
	if err != nil {
		return false, err
	}
	if affected, _ := modeResult.RowsAffected(); affected != 1 {
		return false, fmt.Errorf("bootstrap authentication mode conflicts with installation mode")
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) PutUser(ctx context.Context, record application.UserRecord) error {
	if _, err := domain.NewUserID(string(record.ID)); err != nil {
		return err
	}
	if !identityNamePattern.MatchString(record.Username) || strings.TrimSpace(record.DisplayName) != record.DisplayName || record.DisplayName == "" || len(record.DisplayName) > 128 || len(record.PasswordHash) > 1024 {
		return fmt.Errorf("user record is invalid")
	}
	disabled := 0
	if record.Disabled {
		disabled = 1
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previousPassword string
	lookupErr := tx.QueryRowContext(ctx, s.bind(`SELECT password_hash FROM labcentral_users WHERE user_id = ?`), record.ID).Scan(&previousPassword)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_users
		(user_id, username, display_name, password_hash, disabled, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
		ON CONFLICT (user_id) DO UPDATE SET username = excluded.username, display_name = excluded.display_name,
		password_hash = excluded.password_hash, disabled = excluded.disabled,
		resource_version = labcentral_users.resource_version + 1, updated_at_us = `+s.nowMicros()),
		record.ID, record.Username, record.DisplayName, record.PasswordHash, disabled)
	if err != nil {
		return fmt.Errorf("save user: %w", err)
	}
	if disabled == 1 || lookupErr == nil && previousPassword != record.PasswordHash {
		if _, err := tx.ExecContext(ctx, `UPDATE labcentral_sessions SET revoked_at_us = `+s.nowMicros()+` WHERE user_id = `+s.bind("?")+` AND revoked_at_us = 0`, record.ID); err != nil {
			return fmt.Errorf("revoke user sessions: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) CreateUser(ctx context.Context, record application.UserRecord, identity *application.ExternalIdentityRecord) error {
	if _, err := domain.NewUserID(string(record.ID)); err != nil {
		return err
	}
	if !identityNamePattern.MatchString(record.Username) || strings.TrimSpace(record.DisplayName) != record.DisplayName || record.DisplayName == "" || len(record.DisplayName) > 128 || len(record.PasswordHash) > 1024 || record.Disabled {
		return fmt.Errorf("user record is invalid")
	}
	if (record.PasswordHash == "") == (identity == nil) {
		return fmt.Errorf("user must have exactly one configured authentication identity")
	}
	if identity != nil {
		issuer, err := url.Parse(identity.Issuer)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || issuer.String() != identity.Issuer || identity.Subject == "" || len(identity.Subject) > 512 || identity.UserID != record.ID {
			return fmt.Errorf("external identity is invalid")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_users
		(user_id, username, display_name, password_hash, disabled, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, 0, `+s.nowMicros()+`, `+s.nowMicros()+`)`), record.ID, record.Username, record.DisplayName, record.PasswordHash); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if identity != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_auth_identities
			(issuer, subject, user_id, created_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`)`), identity.Issuer, identity.Subject, identity.UserID); err != nil {
			return fmt.Errorf("create external identity: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) UserByID(ctx context.Context, id domain.UserID) (application.UserRecord, error) {
	if _, err := domain.NewUserID(string(id)); err != nil {
		return application.UserRecord{}, err
	}
	return scanUser(s.db.QueryRowContext(ctx, s.bind(`SELECT user_id, username, display_name, password_hash, disabled, created_at_us, updated_at_us, resource_version
		FROM labcentral_users WHERE user_id = ?`), id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (application.UserRecord, error) {
	if !identityNamePattern.MatchString(username) {
		return application.UserRecord{}, application.ErrAuthRecordNotFound
	}
	return scanUser(s.db.QueryRowContext(ctx, s.bind(`SELECT user_id, username, display_name, password_hash, disabled, created_at_us, updated_at_us, resource_version
		FROM labcentral_users WHERE username = ?`), username))
}

func scanUser(row scanner) (application.UserRecord, error) {
	var record application.UserRecord
	var id string
	var disabled int
	var created, updated int64
	if err := row.Scan(&id, &record.Username, &record.DisplayName, &record.PasswordHash, &disabled, &created, &updated, &record.ResourceVersion); errors.Is(err, sql.ErrNoRows) {
		return application.UserRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.UserRecord{}, err
	}
	record.ID = domain.UserID(id)
	record.Disabled = disabled == 1
	record.CreatedAt = time.UnixMicro(created).UTC()
	record.UpdatedAt = time.UnixMicro(updated).UTC()
	return record, nil
}

func (s *Store) LinkExternalIdentity(ctx context.Context, record application.ExternalIdentityRecord) error {
	issuer, err := url.Parse(record.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" || len(record.Issuer) > 2048 || record.Subject == "" || len(record.Subject) > 512 {
		return fmt.Errorf("external identity is invalid")
	}
	if _, err := domain.NewUserID(string(record.UserID)); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_auth_identities
		(issuer, subject, user_id, created_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`)
		ON CONFLICT (issuer, subject) DO NOTHING`), record.Issuer, record.Subject, record.UserID)
	if err != nil {
		return fmt.Errorf("link external identity: %w", err)
	}
	var userID string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT user_id FROM labcentral_auth_identities WHERE issuer = ? AND subject = ?`), record.Issuer, record.Subject).Scan(&userID); err != nil {
		return err
	}
	if userID != string(record.UserID) {
		return fmt.Errorf("external identity is already linked")
	}
	return nil
}

func (s *Store) UserByExternalIdentity(ctx context.Context, issuer, subject string) (application.UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, s.bind(`SELECT users.user_id, users.username, users.display_name, users.password_hash,
		users.disabled, users.created_at_us, users.updated_at_us, users.resource_version
		FROM labcentral_auth_identities identities JOIN labcentral_users users ON users.user_id = identities.user_id
		WHERE identities.issuer = ? AND identities.subject = ?`), issuer, subject))
}

func (s *Store) PutOrganization(ctx context.Context, record application.OrganizationRecord) error {
	if _, err := domain.NewOrganizationID(string(record.ID)); err != nil || !identityNamePattern.MatchString(record.Name) {
		return fmt.Errorf("organization record is invalid")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_organizations
		(organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
		ON CONFLICT (organization_id) DO UPDATE SET name = excluded.name,
		resource_version = labcentral_organizations.resource_version + 1, updated_at_us = `+s.nowMicros()), record.ID, record.Name)
	return wrapIdentityError("save organization", err)
}

func (s *Store) PutProject(ctx context.Context, record application.ProjectRecord) error {
	if _, err := domain.NewProjectID(string(record.ID)); err != nil || !identityNamePattern.MatchString(record.Name) {
		return fmt.Errorf("project record is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_projects
		(project_id, organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
		ON CONFLICT (project_id) DO UPDATE SET name = excluded.name,
		resource_version = labcentral_projects.resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE labcentral_projects.organization_id = excluded.organization_id`), record.ID, record.OrganizationID, record.Name)
	if err != nil {
		return wrapIdentityError("save project", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("save project: project identity belongs to another organization")
	}
	return nil
}

func (s *Store) PutTeam(ctx context.Context, record application.TeamRecord) error {
	if _, err := domain.NewTeamID(string(record.ID)); err != nil || !identityNamePattern.MatchString(record.Name) {
		return fmt.Errorf("team record is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_teams
		(team_id, organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
		ON CONFLICT (team_id) DO UPDATE SET name = excluded.name,
		resource_version = labcentral_teams.resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE labcentral_teams.organization_id = excluded.organization_id`), record.ID, record.OrganizationID, record.Name)
	if err != nil {
		return wrapIdentityError("save team", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("save team: team identity belongs to another organization")
	}
	return nil
}

func (s *Store) PutMembership(ctx context.Context, record application.MembershipRecord) error {
	if record.ID == "" || len(record.ID) > 128 {
		return fmt.Errorf("membership record is invalid")
	}
	if _, err := domain.NewUserID(string(record.UserID)); err != nil {
		return err
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return err
	}
	if record.TeamID != "" {
		if _, err := domain.NewTeamID(string(record.TeamID)); err != nil {
			return err
		}
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return err
	}
	var team any
	if record.TeamID != "" {
		team = record.TeamID
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_memberships
		(membership_id, user_id, organization_id, team_id, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.UserID, record.OrganizationID, team, from, until)
	return wrapIdentityError("save membership", err)
}

func (s *Store) PutRoleAssignment(ctx context.Context, record application.RoleAssignmentRecord) error {
	if record.ID == "" || len(record.ID) > 128 || !validSubjectKind(record.SubjectKind) || !validScope(record.ScopeKind, record.ScopeID) || !validRole(record.Role) || !validRoleScope(record.Role, record.ScopeKind) || record.SubjectID == "" || len(record.SubjectID) > 128 {
		return fmt.Errorf("role assignment is invalid")
	}
	if record.Role == application.RolePlatformAdministrator && !record.ValidUntil.IsZero() {
		return fmt.Errorf("platform-administrator assignments must not expire")
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return err
	}
	organizationID, err := s.scopeOrganization(ctx, record.ScopeKind, record.ScopeID)
	if err != nil {
		return fmt.Errorf("role assignment scope is invalid")
	}
	switch record.SubjectKind {
	case application.SubjectUser:
		var exists int
		query := `SELECT 1 FROM labcentral_users WHERE user_id = ?`
		arguments := []any{record.SubjectID}
		if record.ScopeKind != application.ScopePlatform {
			query = `SELECT 1 FROM labcentral_users users JOIN labcentral_memberships memberships ON memberships.user_id = users.user_id
				WHERE users.user_id = ? AND memberships.organization_id = ? AND memberships.valid_from_us <= ?
				AND (memberships.valid_until_us = 0 OR memberships.valid_until_us > ?) LIMIT 1`
			arguments = []any{record.SubjectID, organizationID, from, from}
		}
		if err := s.db.QueryRowContext(ctx, s.bind(query), arguments...).Scan(&exists); err != nil {
			return fmt.Errorf("role assignment subject is not eligible for its scope")
		}
	case application.SubjectTeam:
		if record.ScopeKind == application.ScopePlatform {
			return fmt.Errorf("team subjects cannot receive platform roles")
		}
		var subjectOrganization string
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), record.SubjectID).Scan(&subjectOrganization); err != nil || subjectOrganization != string(organizationID) {
			return fmt.Errorf("role assignment team does not belong to its scope")
		}
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_role_assignments
		(assignment_id, subject_kind, subject_id, scope_kind, scope_id, role, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.SubjectKind, record.SubjectID, record.ScopeKind, record.ScopeID, record.Role, from, until)
	return wrapIdentityError("save role assignment", err)
}

func (s *Store) PutTargetGrant(ctx context.Context, record application.TargetGrantRecord) error {
	if record.ID == "" || len(record.ID) > 128 || !validScope(record.ScopeKind, record.ScopeID) {
		return fmt.Errorf("target grant is invalid")
	}
	if _, err := domain.NewProviderTargetID(string(record.TargetID)); err != nil {
		return err
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return err
	}
	if _, err := s.scopeOrganization(ctx, record.ScopeKind, record.ScopeID); err != nil {
		return fmt.Errorf("provider target grant scope is invalid")
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_provider_target_grants
		(grant_id, target_id, scope_kind, scope_id, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.TargetID, record.ScopeKind, record.ScopeID, from, until)
	return wrapIdentityError("save provider target grant", err)
}

func (s *Store) AuthorizationSnapshot(ctx context.Context, userID domain.UserID, now time.Time) (application.AuthorizationSnapshot, error) {
	user, err := s.UserByID(ctx, userID)
	if err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	if user.Disabled {
		return application.AuthorizationSnapshot{}, application.ErrAuthRecordNotFound
	}
	result := application.AuthorizationSnapshot{User: user}
	nowMicros := now.UTC().UnixMicro()
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT membership_id, organization_id, COALESCE(team_id, ''), valid_from_us, valid_until_us, resource_version
		FROM labcentral_memberships WHERE user_id = ? AND valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?)
		ORDER BY organization_id, team_id, membership_id`), userID, nowMicros, nowMicros)
	if err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	for rows.Next() {
		var membership application.MembershipRecord
		var organizationID, teamID string
		var from, until int64
		if err := rows.Scan(&membership.ID, &organizationID, &teamID, &from, &until, &membership.ResourceVersion); err != nil {
			_ = rows.Close()
			return application.AuthorizationSnapshot{}, err
		}
		membership.UserID, membership.OrganizationID, membership.TeamID = userID, domain.OrganizationID(organizationID), domain.TeamID(teamID)
		membership.ValidFrom, membership.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
		result.Memberships = append(result.Memberships, membership)
	}
	if err := rows.Close(); err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	rows, err = s.db.QueryContext(ctx, s.bind(`SELECT assignment_id, subject_kind, subject_id, scope_kind, scope_id, role, valid_from_us, valid_until_us, resource_version
		FROM labcentral_role_assignments assignments
		WHERE valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?) AND
		((subject_kind = 'user' AND subject_id = ?) OR (subject_kind = 'team' AND subject_id IN
		 (SELECT team_id FROM labcentral_memberships WHERE user_id = ? AND team_id IS NOT NULL AND valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?))))
		ORDER BY scope_kind, scope_id, role, assignment_id`), nowMicros, nowMicros, userID, userID, nowMicros, nowMicros)
	if err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	for rows.Next() {
		var assignment application.RoleAssignmentRecord
		var from, until int64
		if err := rows.Scan(&assignment.ID, &assignment.SubjectKind, &assignment.SubjectID, &assignment.ScopeKind, &assignment.ScopeID, &assignment.Role, &from, &until, &assignment.ResourceVersion); err != nil {
			_ = rows.Close()
			return application.AuthorizationSnapshot{}, err
		}
		assignment.ValidFrom, assignment.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
		result.Assignments = append(result.Assignments, assignment)
	}
	if err := rows.Close(); err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	rows, err = s.db.QueryContext(ctx, s.bind(`SELECT grant_id, target_id, scope_kind, scope_id, valid_from_us, valid_until_us, resource_version
		FROM labcentral_provider_target_grants WHERE valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?)
		ORDER BY target_id, scope_kind, scope_id`), nowMicros, nowMicros)
	if err != nil {
		return application.AuthorizationSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var grant application.TargetGrantRecord
		var targetID string
		var from, until int64
		if err := rows.Scan(&grant.ID, &targetID, &grant.ScopeKind, &grant.ScopeID, &from, &until, &grant.ResourceVersion); err != nil {
			return application.AuthorizationSnapshot{}, err
		}
		grant.TargetID, grant.ValidFrom, grant.ValidUntil = domain.ProviderTargetID(targetID), time.UnixMicro(from).UTC(), optionalTime(until)
		result.Grants = append(result.Grants, grant)
	}
	return result, rows.Err()
}

func (s *Store) ResolveScope(ctx context.Context, kind application.ScopeKind, id string) (application.ScopeHierarchy, error) {
	switch kind {
	case application.ScopePlatform:
		if id != "platform" {
			return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
		}
		return application.ScopeHierarchy{Kind: kind, ID: id}, nil
	case application.ScopeOrganization:
		var organizationID string
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_organizations WHERE organization_id = ?`), id).Scan(&organizationID)
		if errors.Is(err, sql.ErrNoRows) {
			return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
		}
		return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: domain.OrganizationID(organizationID)}, err
	case application.ScopeProject:
		var projectID, organizationID string
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT project_id, organization_id FROM labcentral_projects WHERE project_id = ?`), id).Scan(&projectID, &organizationID)
		if errors.Is(err, sql.ErrNoRows) {
			return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
		}
		return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: domain.OrganizationID(organizationID), ProjectID: domain.ProjectID(projectID)}, err
	case application.ScopeTeam:
		var teamID, organizationID string
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT team_id, organization_id FROM labcentral_teams WHERE team_id = ?`), id).Scan(&teamID, &organizationID)
		if errors.Is(err, sql.ErrNoRows) {
			return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
		}
		return application.ScopeHierarchy{Kind: kind, ID: id, OrganizationID: domain.OrganizationID(organizationID), TeamID: domain.TeamID(teamID)}, err
	default:
		return application.ScopeHierarchy{}, application.ErrAuthRecordNotFound
	}
}

func (s *Store) CreateSession(ctx context.Context, record application.SessionRecord) error {
	if err := validateSession(record); err != nil {
		return err
	}
	if !record.CreatedAt.IsZero() {
		if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_sessions WHERE expires_at_us <= ?`), record.CreatedAt.UTC().UnixMicro()); err != nil {
			return fmt.Errorf("prune expired sessions: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_sessions
		(session_id, user_id, credential_kind, token_hash, csrf_hash, key_id, expires_at_us, rotated_from_id, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.UserID, record.Kind, record.TokenHash, record.CSRFHash, record.KeyID, record.ExpiresAt.UnixMicro(), record.RotatedFromID)
	return wrapIdentityError("create session", err)
}

func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string, now time.Time) (application.SessionRecord, error) {
	var record application.SessionRecord
	var userID string
	var expires, revoked, created int64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT sessions.session_id, sessions.user_id, sessions.credential_kind, sessions.token_hash, sessions.csrf_hash,
		sessions.key_id, sessions.expires_at_us, sessions.rotated_from_id, sessions.revoked_at_us, sessions.created_at_us
		FROM labcentral_sessions sessions JOIN labcentral_users users ON users.user_id = sessions.user_id
		WHERE sessions.token_hash = ? AND sessions.expires_at_us > ? AND sessions.revoked_at_us = 0 AND users.disabled = 0`), tokenHash, now.UTC().UnixMicro()).Scan(
		&record.ID, &userID, &record.Kind, &record.TokenHash, &record.CSRFHash, &record.KeyID, &expires, &record.RotatedFromID, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return application.SessionRecord{}, application.ErrAuthRecordNotFound
	}
	if err != nil {
		return application.SessionRecord{}, err
	}
	record.UserID, record.ExpiresAt, record.RevokedAt, record.CreatedAt = domain.UserID(userID), time.UnixMicro(expires).UTC(), optionalTime(revoked), time.UnixMicro(created).UTC()
	return record, nil
}

func (s *Store) RotateSession(ctx context.Context, oldID string, replacement application.SessionRecord, now time.Time) error {
	if err := validateSession(replacement); err != nil || replacement.RotatedFromID != oldID {
		return fmt.Errorf("replacement session is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM labcentral_sessions WHERE expires_at_us <= ? AND session_id <> ?`), now.UTC().UnixMicro(), oldID); err != nil {
		return fmt.Errorf("prune expired sessions: %w", err)
	}
	updated, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_sessions SET revoked_at_us = ?
		WHERE session_id = ? AND revoked_at_us = 0 AND expires_at_us > ?`), now.UTC().UnixMicro(), oldID, now.UTC().UnixMicro())
	if err != nil {
		return err
	}
	if affected, _ := updated.RowsAffected(); affected != 1 {
		return application.ErrAuthRecordNotFound
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_sessions
		(session_id, user_id, credential_kind, token_hash, csrf_hash, key_id, expires_at_us, rotated_from_id, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), replacement.ID, replacement.UserID, replacement.Kind, replacement.TokenHash, replacement.CSRFHash, replacement.KeyID, replacement.ExpiresAt.UnixMicro(), oldID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeSession(ctx context.Context, sessionID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_sessions SET revoked_at_us = ? WHERE session_id = ? AND revoked_at_us = 0`), now.UTC().UnixMicro(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.ErrAuthRecordNotFound
	}
	return nil
}

func (s *Store) CreatePasswordRecovery(ctx context.Context, record application.PasswordRecoveryRecord) error {
	if record.ID == "" || len(record.ID) > 128 || record.TokenHash == "" || len(record.TokenHash) > 256 || record.KeyID == "" || len(record.KeyID) > 64 || record.ExpiresAt.IsZero() {
		return fmt.Errorf("password recovery record is invalid")
	}
	if _, err := domain.NewUserID(string(record.UserID)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_password_recovery_tokens WHERE expires_at_us <= ?`), record.ExpiresAt.UTC().Add(-time.Hour).UnixMicro()); err != nil {
		return fmt.Errorf("prune expired password recovery tokens: %w", err)
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_password_recovery_tokens
		(token_id, user_id, token_hash, key_id, expires_at_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.UserID, record.TokenHash, record.KeyID, record.ExpiresAt.UTC().UnixMicro())
	return wrapIdentityError("create password recovery token", err)
}

func (s *Store) ConsumePasswordRecovery(ctx context.Context, tokenHash, keyID, passwordHash string, now time.Time) error {
	if tokenHash == "" || keyID == "" || passwordHash == "" || len(passwordHash) > 1024 {
		return application.ErrAuthRecordNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT user_id FROM labcentral_password_recovery_tokens
		WHERE token_hash = ? AND key_id = ? AND expires_at_us > ? AND consumed_at_us = 0`
	if s.dialect == "postgresql" {
		query += " FOR UPDATE"
	}
	var userID string
	if err := tx.QueryRowContext(ctx, s.bind(query), tokenHash, keyID, now.UTC().UnixMicro()).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
		return application.ErrAuthRecordNotFound
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_password_recovery_tokens SET consumed_at_us = ?
		WHERE user_id = ? AND consumed_at_us = 0`), now.UTC().UnixMicro(), userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected < 1 {
		return application.ErrAuthRecordNotFound
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_users SET password_hash = ?, updated_at_us = ? WHERE user_id = ?`), passwordHash, now.UTC().UnixMicro(), userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_sessions SET revoked_at_us = ? WHERE user_id = ? AND revoked_at_us = 0`), now.UTC().UnixMicro(), userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConsumeAuthenticationAttempt(ctx context.Context, key string, now time.Time, window time.Duration, limit uint32) (bool, error) {
	if len(key) != 64 || now.IsZero() || window <= 0 || window > 24*time.Hour || limit == 0 || limit > 10000 {
		return false, fmt.Errorf("authentication attempt limit is invalid")
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_authentication_attempts WHERE updated_at_us <= `+s.nowMicros()+` - ?`), (24 * time.Hour).Microseconds()); err != nil {
		return false, fmt.Errorf("prune authentication attempts: %w", err)
	}
	var count int64
	query := `INSERT INTO labcentral_authentication_attempts
		(attempt_key, window_started_at_us, attempt_count, updated_at_us) VALUES (?, ` + s.nowMicros() + `, 1, ` + s.nowMicros() + `)
		ON CONFLICT(attempt_key) DO UPDATE SET
		window_started_at_us = CASE WHEN labcentral_authentication_attempts.window_started_at_us <= ` + s.nowMicros() + ` - ? THEN excluded.window_started_at_us ELSE labcentral_authentication_attempts.window_started_at_us END,
		attempt_count = CASE WHEN labcentral_authentication_attempts.window_started_at_us <= ` + s.nowMicros() + ` - ? THEN 1 WHEN labcentral_authentication_attempts.attempt_count < ? THEN labcentral_authentication_attempts.attempt_count + 1 ELSE labcentral_authentication_attempts.attempt_count END,
		updated_at_us = excluded.updated_at_us RETURNING attempt_count`
	if err := s.db.QueryRowContext(ctx, s.bind(query), key, window.Microseconds(), window.Microseconds(), int64(limit)+1).Scan(&count); err != nil {
		return false, fmt.Errorf("consume authentication attempt: %w", err)
	}
	return count <= int64(limit), nil
}

func (s *Store) ClearAuthenticationAttempts(ctx context.Context, key string) error {
	if len(key) != 64 {
		return fmt.Errorf("authentication attempt key is invalid")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_authentication_attempts WHERE attempt_key = ?`), key)
	return err
}

func (s *Store) CreateOIDCFlow(ctx context.Context, record application.OIDCFlowRecord) error {
	if record.ID == "" || len(record.ID) > 128 || record.StateHash == "" || len(record.StateHash) > 256 || record.BrowserHash == "" || len(record.BrowserHash) > 256 || record.NonceHash == "" || len(record.NonceHash) > 256 ||
		len(record.PKCECiphertext) < 32 || len(record.PKCECiphertext) > 1024 || record.KeyID == "" || len(record.KeyID) > 64 || !safeRedirectPath(record.RedirectPath) || record.ExpiresAt.IsZero() {
		return fmt.Errorf("OIDC flow record is invalid")
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_oidc_flows WHERE expires_at_us <= ?`), record.ExpiresAt.UTC().Add(-10*time.Minute).UnixMicro()); err != nil {
		return fmt.Errorf("prune expired OIDC flows: %w", err)
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_oidc_flows
		(flow_id, state_hash, browser_hash, nonce_hash, pkce_verifier, key_id, redirect_path, expires_at_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.StateHash, record.BrowserHash, record.NonceHash, record.PKCECiphertext,
		record.KeyID, record.RedirectPath, record.ExpiresAt.UTC().UnixMicro())
	return wrapIdentityError("create OIDC flow", err)
}

func (s *Store) ConsumeOIDCFlow(ctx context.Context, stateHash, browserHash, keyID string, now time.Time) (application.OIDCFlowRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.OIDCFlowRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT flow_id, state_hash, browser_hash, nonce_hash, pkce_verifier, key_id, redirect_path, expires_at_us
		FROM labcentral_oidc_flows WHERE state_hash = ? AND browser_hash = ? AND key_id = ? AND expires_at_us > ? AND consumed_at_us = 0`
	if s.dialect == "postgresql" {
		query += " FOR UPDATE"
	}
	var record application.OIDCFlowRecord
	var expires int64
	if err := tx.QueryRowContext(ctx, s.bind(query), stateHash, browserHash, keyID, now.UTC().UnixMicro()).Scan(
		&record.ID, &record.StateHash, &record.BrowserHash, &record.NonceHash, &record.PKCECiphertext, &record.KeyID, &record.RedirectPath, &expires,
	); errors.Is(err, sql.ErrNoRows) {
		return application.OIDCFlowRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.OIDCFlowRecord{}, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_oidc_flows SET consumed_at_us = ? WHERE flow_id = ? AND consumed_at_us = 0`), now.UTC().UnixMicro(), record.ID)
	if err != nil {
		return application.OIDCFlowRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.OIDCFlowRecord{}, application.ErrAuthRecordNotFound
	}
	if err := tx.Commit(); err != nil {
		return application.OIDCFlowRecord{}, err
	}
	record.ExpiresAt = time.UnixMicro(expires).UTC()
	record.PKCECiphertext = append([]byte(nil), record.PKCECiphertext...)
	return record, nil
}

func (s *Store) InstanceAccess(ctx context.Context, instanceID domain.InstanceID) (application.InstanceAccessRecord, error) {
	return scanInstanceAccess(s.db.QueryRowContext(ctx, s.bind(`SELECT instance_id, project_id, owner_kind, owner_id, provider_target_id
		FROM labcentral_local_instances WHERE instance_id = ?`), instanceID))
}

func (s *Store) OperationInstanceAccess(ctx context.Context, operationID domain.OperationID) (application.InstanceAccessRecord, error) {
	return scanInstanceAccess(s.db.QueryRowContext(ctx, s.bind(`SELECT instance.instance_id, instance.project_id, instance.owner_kind,
		instance.owner_id, instance.provider_target_id FROM labcentral_local_operations operation
		JOIN labcentral_local_instances instance ON instance.instance_id = operation.instance_id
		WHERE operation.operation_id = ?`), operationID))
}

func (s *Store) ListInstanceAccessFiltered(ctx context.Context, filter application.InstanceAccessFilter) ([]application.InstanceAccessRecord, error) {
	conditions := make([]string, 0, 6)
	arguments := make([]any, 0)
	stringIDs := func(values any) []string {
		switch typed := values.(type) {
		case []domain.OrganizationID:
			result := make([]string, len(typed))
			for index := range typed {
				result[index] = string(typed[index])
			}
			return result
		case []domain.ProjectID:
			result := make([]string, len(typed))
			for index := range typed {
				result[index] = string(typed[index])
			}
			return result
		case []domain.TeamID:
			result := make([]string, len(typed))
			for index := range typed {
				result[index] = string(typed[index])
			}
			return result
		default:
			return nil
		}
	}
	inClause := func(values []string) string {
		placeholders := make([]string, len(values))
		for index, value := range values {
			placeholders[index] = "?"
			arguments = append(arguments, value)
		}
		return "(" + strings.Join(placeholders, ",") + ")"
	}
	ownerClause := func() string {
		parts := make([]string, 0, 2)
		if filter.OwnerUserID != "" {
			parts = append(parts, "(instance.owner_kind = 'user' AND instance.owner_id = ?)")
			arguments = append(arguments, filter.OwnerUserID)
		}
		if len(filter.OwnerTeamIDs) > 0 {
			parts = append(parts, "(instance.owner_kind = 'team' AND instance.owner_id IN "+inClause(stringIDs(filter.OwnerTeamIDs))+")")
		}
		if len(parts) == 0 {
			return "0 = 1"
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	if filter.All {
		conditions = append(conditions, "1 = 1")
	}
	if len(filter.UnrestrictedOrganizations) > 0 {
		conditions = append(conditions, "project.organization_id IN "+inClause(stringIDs(filter.UnrestrictedOrganizations)))
	}
	if len(filter.UnrestrictedProjects) > 0 {
		conditions = append(conditions, "instance.project_id IN "+inClause(stringIDs(filter.UnrestrictedProjects)))
	}
	if filter.OwnerRestrictedAll {
		conditions = append(conditions, ownerClause())
	}
	if len(filter.OwnerRestrictedOrganizations) > 0 {
		conditions = append(conditions, "(project.organization_id IN "+inClause(stringIDs(filter.OwnerRestrictedOrganizations))+" AND "+ownerClause()+")")
	}
	if len(filter.OwnerRestrictedProjects) > 0 {
		conditions = append(conditions, "(instance.project_id IN "+inClause(stringIDs(filter.OwnerRestrictedProjects))+" AND "+ownerClause()+")")
	}
	if len(filter.TeamScopedOwnerIDs) > 0 {
		conditions = append(conditions, "(instance.owner_kind = 'team' AND instance.owner_id IN "+inClause(stringIDs(filter.TeamScopedOwnerIDs))+")")
	}
	if len(conditions) == 0 {
		return []application.InstanceAccessRecord{}, nil
	}
	if len(arguments) > 10000 {
		return nil, fmt.Errorf("authorization filter exceeds its limit")
	}
	query := `SELECT instance.instance_id, instance.project_id, instance.owner_kind, instance.owner_id, instance.provider_target_id
		FROM labcentral_local_instances instance JOIN labcentral_projects project ON project.project_id = instance.project_id
		WHERE ` + strings.Join(conditions, " OR ") + ` ORDER BY instance.created_at_us, instance.instance_id`
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]application.InstanceAccessRecord, 0)
	for rows.Next() {
		record, err := scanInstanceAccess(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func scanInstanceAccess(row scanner) (application.InstanceAccessRecord, error) {
	var record application.InstanceAccessRecord
	var instanceID, projectID, ownerKind, targetID string
	if err := row.Scan(&instanceID, &projectID, &ownerKind, &record.OwnerID, &targetID); errors.Is(err, sql.ErrNoRows) {
		return application.InstanceAccessRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.InstanceAccessRecord{}, err
	}
	record.InstanceID = domain.InstanceID(instanceID)
	record.ProjectID = domain.ProjectID(projectID)
	record.OwnerKind = application.OwnerKind(ownerKind)
	record.TargetID = domain.ProviderTargetID(targetID)
	return record, nil
}

func (s *Store) OwnerBelongsToScope(ctx context.Context, kind application.OwnerKind, ownerID string, hierarchy application.ScopeHierarchy, now time.Time) (bool, error) {
	if hierarchy.Kind == application.ScopePlatform || hierarchy.OrganizationID == "" {
		return false, nil
	}
	switch kind {
	case application.OwnerUser:
		var exists int
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_memberships
			WHERE user_id = ? AND organization_id = ? AND valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?) LIMIT 1`),
			ownerID, hierarchy.OrganizationID, now.UTC().UnixMicro(), now.UTC().UnixMicro()).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	case application.OwnerTeam:
		var organizationID string
		err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), ownerID).Scan(&organizationID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return organizationID == string(hierarchy.OrganizationID), err
	case application.OwnerProject:
		return ownerID == string(hierarchy.ProjectID), nil
	default:
		return false, nil
	}
}

func validateSession(record application.SessionRecord) error {
	if record.ID == "" || len(record.ID) > 128 || record.Kind != application.SessionKindBrowser && record.Kind != application.SessionKindBearer || record.TokenHash == "" || len(record.TokenHash) > 256 || record.CSRFHash == "" || len(record.CSRFHash) > 256 || record.KeyID == "" || len(record.KeyID) > 64 || record.ExpiresAt.IsZero() {
		return fmt.Errorf("session record is invalid")
	}
	_, err := domain.NewUserID(string(record.UserID))
	return err
}

func validity(from, until time.Time) (int64, int64, error) {
	if from.IsZero() {
		from = time.Now().UTC()
	}
	if !until.IsZero() && !until.After(from) {
		return 0, 0, fmt.Errorf("validity end must follow its start")
	}
	untilMicros := int64(0)
	if !until.IsZero() {
		untilMicros = until.UTC().UnixMicro()
	}
	return from.UTC().UnixMicro(), untilMicros, nil
}

func optionalTime(microseconds int64) time.Time {
	if microseconds == 0 {
		return time.Time{}
	}
	return time.UnixMicro(microseconds).UTC()
}

func validSubjectKind(kind application.SubjectKind) bool {
	return kind == application.SubjectUser || kind == application.SubjectTeam
}

func validScope(kind application.ScopeKind, id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	return kind == application.ScopePlatform && id == "platform" || kind == application.ScopeOrganization || kind == application.ScopeProject || kind == application.ScopeTeam
}

func validRole(role application.Role) bool {
	switch role {
	case application.RolePlatformAdministrator, application.RoleOrganizationAdministrator, application.RoleProviderAdministrator,
		application.RoleLabAuthor, application.RoleInstructor, application.RoleParticipant, application.RoleViewer:
		return true
	default:
		return false
	}
}

func validRoleScope(role application.Role, scope application.ScopeKind) bool {
	switch role {
	case application.RolePlatformAdministrator, application.RoleProviderAdministrator:
		return scope == application.ScopePlatform
	case application.RoleOrganizationAdministrator:
		return scope == application.ScopeOrganization
	case application.RoleLabAuthor:
		return scope == application.ScopeProject
	case application.RoleInstructor:
		return scope == application.ScopeOrganization || scope == application.ScopeProject || scope == application.ScopeTeam
	case application.RoleParticipant:
		return scope == application.ScopeProject || scope == application.ScopeTeam
	case application.RoleViewer:
		return true
	default:
		return false
	}
}

func (s *Store) scopeOrganization(ctx context.Context, kind application.ScopeKind, id string) (domain.OrganizationID, error) {
	switch kind {
	case application.ScopePlatform:
		if id == "platform" {
			return "", nil
		}
	case application.ScopeOrganization:
		var organizationID string
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_organizations WHERE organization_id = ?`), id).Scan(&organizationID); err == nil {
			return domain.OrganizationID(organizationID), nil
		}
	case application.ScopeProject:
		var organizationID string
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), id).Scan(&organizationID); err == nil {
			return domain.OrganizationID(organizationID), nil
		}
	case application.ScopeTeam:
		var organizationID string
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), id).Scan(&organizationID); err == nil {
			return domain.OrganizationID(organizationID), nil
		}
	}
	return "", application.ErrAuthRecordNotFound
}

func safeRedirectPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n\\") && len(value) <= 2048
}

func wrapIdentityError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

var _ application.AuthRepository = (*Store)(nil)
