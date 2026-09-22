package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type administratorGovernancePayload struct {
	ResourceType    string                 `json:"resourceType"`
	ResourceID      string                 `json:"resourceId"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	State           string                 `json:"state,omitempty"`
}

func (s *Store) appendAdministratorGovernanceTx(ctx context.Context, tx *sql.Tx, scopeKind application.ScopeKind, scopeID, action, eventType, aggregateType, aggregateID string, version domain.ResourceVersion, state string) error {
	scope, err := s.recordScopeTx(ctx, tx, scopeKind, scopeID)
	if err != nil {
		return err
	}
	actor, ok := application.RecordActorFromContext(ctx)
	if !ok {
		actor = application.RecordActor{Kind: application.RecordActorSystem, ID: "system.persistence"}
	}
	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		Category: application.AuditAdministrator, Action: action, Outcome: application.AuditSucceeded,
		EventType: eventType, Scope: scope, Actor: actor, AggregateType: aggregateType, AggregateID: aggregateID,
		Payload: administratorGovernancePayload{ResourceType: aggregateType, ResourceID: aggregateID, ResourceVersion: version, State: state},
	})
	if err != nil {
		return err
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return fmt.Errorf("append administrator audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, event); err != nil {
		return fmt.Errorf("append administrator event: %w", err)
	}
	return nil
}

func (s *Store) recordScopeTx(ctx context.Context, tx *sql.Tx, kind application.ScopeKind, id string) (application.RecordScope, error) {
	switch kind {
	case application.ScopePlatform:
		if id != "platform" {
			return application.RecordScope{}, fmt.Errorf("platform record scope is invalid")
		}
		return application.RecordScope{}, nil
	case application.ScopeOrganization:
		if _, err := domain.NewOrganizationID(id); err != nil {
			return application.RecordScope{}, err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_organizations WHERE organization_id = ?`), id).Scan(&exists); err != nil {
			return application.RecordScope{}, fmt.Errorf("resolve organization record scope: %w", err)
		}
		return application.RecordScope{OrganizationID: domain.OrganizationID(id)}, nil
	case application.ScopeProject:
		var organizationID domain.OrganizationID
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), id).Scan(&organizationID); err != nil {
			return application.RecordScope{}, fmt.Errorf("resolve project record scope: %w", err)
		}
		return application.RecordScope{OrganizationID: organizationID, ProjectID: domain.ProjectID(id)}, nil
	case application.ScopeTeam:
		var organizationID domain.OrganizationID
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), id).Scan(&organizationID); err != nil {
			return application.RecordScope{}, fmt.Errorf("resolve team record scope: %w", err)
		}
		return application.RecordScope{OrganizationID: organizationID}, nil
	default:
		return application.RecordScope{}, fmt.Errorf("record scope kind is invalid")
	}
}

func (s *Store) CreateOrganization(ctx context.Context, record application.OrganizationRecord) (application.OrganizationRecord, error) {
	if _, err := domain.NewOrganizationID(string(record.ID)); err != nil || !application.ValidWorkspaceName(record.Name) || record.ResourceVersion != 0 {
		return application.OrganizationRecord{}, fmt.Errorf("organization record is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.OrganizationRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_organizations
		(organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`), record.ID, record.Name)
	if err != nil {
		return application.OrganizationRecord{}, wrapIdentityError("create organization", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeOrganization, string(record.ID), "organization.create", "organization.created", "organization", string(record.ID), 1, ""); err != nil {
		return application.OrganizationRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.OrganizationRecord{}, err
	}
	return s.organizationByID(ctx, record.ID)
}

func (s *Store) UpdateOrganization(ctx context.Context, record application.OrganizationRecord, expected domain.ResourceVersion) (application.OrganizationRecord, error) {
	if _, err := domain.NewOrganizationID(string(record.ID)); err != nil || !application.ValidWorkspaceName(record.Name) || expected == 0 {
		return application.OrganizationRecord{}, fmt.Errorf("organization update is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.OrganizationRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_organizations SET name = ?, resource_version = resource_version + 1,
		updated_at_us = `+s.nowMicros()+` WHERE organization_id = ? AND resource_version = ?`), record.Name, record.ID, expected)
	if err != nil {
		return application.OrganizationRecord{}, wrapIdentityError("update organization", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.OrganizationRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeOrganization, string(record.ID), "organization.update", "organization.updated", "organization", string(record.ID), expected+1, ""); err != nil {
		return application.OrganizationRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.OrganizationRecord{}, err
	}
	return s.organizationByID(ctx, record.ID)
}

func (s *Store) organizationByID(ctx context.Context, id domain.OrganizationID) (application.OrganizationRecord, error) {
	var record application.OrganizationRecord
	if err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT organization_id, name, resource_version FROM labcentral_organizations WHERE organization_id = ?`), id).Scan(&record.ID, &record.Name, &record.ResourceVersion); errors.Is(err, sql.ErrNoRows) {
		return application.OrganizationRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.OrganizationRecord{}, err
	}
	return record, nil
}

func (s *Store) CreateProject(ctx context.Context, record application.ProjectRecord) (application.ProjectRecord, error) {
	if _, err := domain.NewProjectID(string(record.ID)); err != nil || !application.ValidWorkspaceName(record.Name) || record.ResourceVersion != 0 {
		return application.ProjectRecord{}, fmt.Errorf("project record is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.ProjectRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.ProjectRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_projects
		(project_id, organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`), record.ID, record.OrganizationID, record.Name)
	if err != nil {
		return application.ProjectRecord{}, wrapIdentityError("create project", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeProject, string(record.ID), "project.create", "project.created", "project", string(record.ID), 1, ""); err != nil {
		return application.ProjectRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.ProjectRecord{}, err
	}
	return s.projectByID(ctx, record.ID)
}

func (s *Store) UpdateProject(ctx context.Context, record application.ProjectRecord, expected domain.ResourceVersion) (application.ProjectRecord, error) {
	if _, err := domain.NewProjectID(string(record.ID)); err != nil || !application.ValidWorkspaceName(record.Name) || expected == 0 {
		return application.ProjectRecord{}, fmt.Errorf("project update is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.ProjectRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.ProjectRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_projects SET name = ?, resource_version = resource_version + 1,
		updated_at_us = `+s.nowMicros()+` WHERE project_id = ? AND organization_id = ? AND resource_version = ?`), record.Name, record.ID, record.OrganizationID, expected)
	if err != nil {
		return application.ProjectRecord{}, wrapIdentityError("update project", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.ProjectRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeProject, string(record.ID), "project.update", "project.updated", "project", string(record.ID), expected+1, ""); err != nil {
		return application.ProjectRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.ProjectRecord{}, err
	}
	return s.projectByID(ctx, record.ID)
}

func (s *Store) projectByID(ctx context.Context, id domain.ProjectID) (application.ProjectRecord, error) {
	var record application.ProjectRecord
	if err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT project_id, organization_id, name, resource_version FROM labcentral_projects WHERE project_id = ?`), id).Scan(&record.ID, &record.OrganizationID, &record.Name, &record.ResourceVersion); errors.Is(err, sql.ErrNoRows) {
		return application.ProjectRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.ProjectRecord{}, err
	}
	return record, nil
}

func (s *Store) CreateTeam(ctx context.Context, record application.TeamRecord) (application.TeamRecord, error) {
	if _, err := domain.NewTeamID(string(record.ID)); err != nil || !identityNamePattern.MatchString(record.Name) || record.ResourceVersion != 0 {
		return application.TeamRecord{}, fmt.Errorf("team record is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.TeamRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.TeamRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_teams
		(team_id, organization_id, name, created_at_us, updated_at_us) VALUES (?, ?, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)`), record.ID, record.OrganizationID, record.Name)
	if err != nil {
		return application.TeamRecord{}, wrapIdentityError("create team", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeTeam, string(record.ID), "team.create", "team.created", "team", string(record.ID), 1, ""); err != nil {
		return application.TeamRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.TeamRecord{}, err
	}
	return s.teamByID(ctx, record.ID)
}

func (s *Store) UpdateTeam(ctx context.Context, record application.TeamRecord, expected domain.ResourceVersion) (application.TeamRecord, error) {
	if _, err := domain.NewTeamID(string(record.ID)); err != nil || !identityNamePattern.MatchString(record.Name) || expected == 0 {
		return application.TeamRecord{}, fmt.Errorf("team update is invalid")
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.TeamRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.TeamRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_teams SET name = ?, resource_version = resource_version + 1,
		updated_at_us = `+s.nowMicros()+` WHERE team_id = ? AND organization_id = ? AND resource_version = ? AND NOT EXISTS(SELECT 1 FROM labcentral_project_groups g WHERE g.team_id=labcentral_teams.team_id)`), record.Name, record.ID, record.OrganizationID, expected)
	if err != nil {
		return application.TeamRecord{}, wrapIdentityError("update team", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.TeamRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeTeam, string(record.ID), "team.update", "team.updated", "team", string(record.ID), expected+1, ""); err != nil {
		return application.TeamRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.TeamRecord{}, err
	}
	return s.teamByID(ctx, record.ID)
}

func (s *Store) teamByID(ctx context.Context, id domain.TeamID) (application.TeamRecord, error) {
	var record application.TeamRecord
	if err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT team_id, organization_id, name, resource_version FROM labcentral_teams WHERE team_id = ?`), id).Scan(&record.ID, &record.OrganizationID, &record.Name, &record.ResourceVersion); errors.Is(err, sql.ErrNoRows) {
		return application.TeamRecord{}, application.ErrAuthRecordNotFound
	} else if err != nil {
		return application.TeamRecord{}, err
	}
	return record, nil
}

func (s *Store) CreateMembership(ctx context.Context, record application.MembershipRecord) (application.MembershipRecord, error) {
	if record.ResourceVersion != 0 {
		return application.MembershipRecord{}, fmt.Errorf("membership resource version is invalid")
	}
	if record.ID == "" || len(record.ID) > 128 {
		return application.MembershipRecord{}, fmt.Errorf("membership record is invalid")
	}
	if _, err := domain.NewUserID(string(record.UserID)); err != nil {
		return application.MembershipRecord{}, err
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.MembershipRecord{}, err
	}
	if record.TeamID != "" {
		if _, err := domain.NewTeamID(string(record.TeamID)); err != nil {
			return application.MembershipRecord{}, err
		}
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return application.MembershipRecord{}, err
	}
	var team any
	if record.TeamID != "" {
		team = record.TeamID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.MembershipRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var groupCount int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_project_groups WHERE team_id=?`), record.TeamID).Scan(&groupCount); err != nil {
		return application.MembershipRecord{}, err
	}
	if groupCount != 0 {
		return application.MembershipRecord{}, fmt.Errorf("project groups use project enrollment")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_memberships
		(membership_id, user_id, organization_id, team_id, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.UserID, record.OrganizationID, team, from, until); err != nil {
		return application.MembershipRecord{}, wrapIdentityError("save membership", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeOrganization, string(record.OrganizationID), "membership.create", "membership.created", "membership", record.ID, 1, "active"); err != nil {
		return application.MembershipRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.MembershipRecord{}, err
	}
	return s.membershipByID(ctx, record.ID)
}

func (s *Store) UpdateMembership(ctx context.Context, record application.MembershipRecord, expected domain.ResourceVersion) (application.MembershipRecord, error) {
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil || expected == 0 || record.ID == "" || len(record.ID) > 128 {
		return application.MembershipRecord{}, fmt.Errorf("membership update is invalid")
	}
	if _, err := domain.NewUserID(string(record.UserID)); err != nil {
		return application.MembershipRecord{}, err
	}
	if _, err := domain.NewOrganizationID(string(record.OrganizationID)); err != nil {
		return application.MembershipRecord{}, err
	}
	teamID := string(record.TeamID)
	if teamID != "" {
		if _, err := domain.NewTeamID(teamID); err != nil {
			return application.MembershipRecord{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.MembershipRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_memberships SET valid_from_us = ?, valid_until_us = ?, resource_version = resource_version + 1
		WHERE membership_id = ? AND user_id = ? AND organization_id = ? AND COALESCE(team_id, '') = ? AND resource_version = ?`),
		from, until, record.ID, record.UserID, record.OrganizationID, teamID, expected)
	if err != nil {
		return application.MembershipRecord{}, wrapIdentityError("update membership", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.MembershipRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeOrganization, string(record.OrganizationID), "membership.update", "membership.updated", "membership", record.ID, expected+1, "active"); err != nil {
		return application.MembershipRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.MembershipRecord{}, err
	}
	return s.membershipByID(ctx, record.ID)
}

func (s *Store) RevokeMembership(ctx context.Context, id string, organizationID domain.OrganizationID, expected domain.ResourceVersion, now time.Time) (application.MembershipRecord, error) {
	if id == "" || len(id) > 128 || expected == 0 || now.IsZero() {
		return application.MembershipRecord{}, fmt.Errorf("membership revocation is invalid")
	}
	if _, err := domain.NewOrganizationID(string(organizationID)); err != nil {
		return application.MembershipRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.MembershipRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_memberships SET valid_until_us = ?, resource_version = resource_version + 1
		WHERE membership_id = ? AND organization_id = ? AND resource_version = ? AND (valid_until_us = 0 OR valid_until_us > ?)`),
		now.UTC().UnixMicro(), id, organizationID, expected, now.UTC().UnixMicro())
	if err != nil {
		return application.MembershipRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.MembershipRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopeOrganization, string(organizationID), "membership.revoke", "membership.revoked", "membership", id, expected+1, "revoked"); err != nil {
		return application.MembershipRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.MembershipRecord{}, err
	}
	return s.membershipByID(ctx, id)
}

func (s *Store) membershipByID(ctx context.Context, id string) (application.MembershipRecord, error) {
	var record application.MembershipRecord
	var teamID string
	var from, until int64
	err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT membership_id, user_id, organization_id, COALESCE(team_id, ''), valid_from_us, valid_until_us, resource_version
		FROM labcentral_memberships WHERE membership_id = ?`), id).Scan(&record.ID, &record.UserID, &record.OrganizationID, &teamID, &from, &until, &record.ResourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return application.MembershipRecord{}, application.ErrAuthRecordNotFound
	}
	if err != nil {
		return application.MembershipRecord{}, err
	}
	record.TeamID = domain.TeamID(teamID)
	record.ValidFrom, record.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
	return record, nil
}

func (s *Store) CreateRoleAssignment(ctx context.Context, record application.RoleAssignmentRecord) (application.RoleAssignmentRecord, error) {
	if record.ResourceVersion != 0 || record.Role == application.RolePlatformAdministrator && !record.ValidUntil.IsZero() {
		return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment resource version or platform-administrator validity is invalid")
	}
	if record.ID == "" || len(record.ID) > 128 || !validSubjectKind(record.SubjectKind) || !validScope(record.ScopeKind, record.ScopeID) || !validRole(record.Role) || !validRoleScope(record.Role, record.ScopeKind) || record.SubjectID == "" || len(record.SubjectID) > 128 {
		return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment is invalid")
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := s.recordScopeTx(ctx, tx, record.ScopeKind, record.ScopeID)
	if err != nil {
		return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment scope is invalid: %w", err)
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
			arguments = []any{record.SubjectID, scope.OrganizationID, from, from}
		}
		if err := tx.QueryRowContext(ctx, s.bind(query), arguments...).Scan(&exists); err != nil {
			return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment subject is not eligible for its scope")
		}
	case application.SubjectTeam:
		if record.ScopeKind == application.ScopePlatform {
			return application.RoleAssignmentRecord{}, fmt.Errorf("team subjects cannot receive platform roles")
		}
		var organizationID domain.OrganizationID
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_teams WHERE team_id = ?`), record.SubjectID).Scan(&organizationID); err != nil || organizationID != scope.OrganizationID {
			return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment team does not belong to its scope")
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_role_assignments
		(assignment_id, subject_kind, subject_id, scope_kind, scope_id, role, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.SubjectKind, record.SubjectID, record.ScopeKind, record.ScopeID, record.Role, from, until); err != nil {
		return application.RoleAssignmentRecord{}, wrapIdentityError("save role assignment", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, record.ScopeKind, record.ScopeID, "role-assignment.create", "role-assignment.created", "role-assignment", record.ID, 1, "active"); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	return s.roleAssignmentByID(ctx, record.ID)
}

func (s *Store) UpdateRoleAssignment(ctx context.Context, record application.RoleAssignmentRecord, expected domain.ResourceVersion) (application.RoleAssignmentRecord, error) {
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil || expected == 0 || record.ID == "" || len(record.ID) > 128 || !validSubjectKind(record.SubjectKind) || !validScope(record.ScopeKind, record.ScopeID) || !validRole(record.Role) || !validRoleScope(record.Role, record.ScopeKind) || record.SubjectID == "" || len(record.SubjectID) > 128 || record.Role == application.RolePlatformAdministrator && until != 0 {
		return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment update is invalid")
	}
	// The start and identity tuple are immutable. In particular, an update must
	// not move the last active platform administrator into the future and bypass
	// the protected revocation path.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_role_assignments SET valid_until_us = ?, resource_version = resource_version + 1
		WHERE assignment_id = ? AND subject_kind = ? AND subject_id = ? AND scope_kind = ? AND scope_id = ? AND role = ? AND valid_from_us = ? AND resource_version = ?`),
		until, record.ID, record.SubjectKind, record.SubjectID, record.ScopeKind, record.ScopeID, record.Role, from, expected)
	if err != nil {
		return application.RoleAssignmentRecord{}, wrapIdentityError("update role assignment", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.RoleAssignmentRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, record.ScopeKind, record.ScopeID, "role-assignment.update", "role-assignment.updated", "role-assignment", record.ID, expected+1, "active"); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	return s.roleAssignmentByID(ctx, record.ID)
}

func (s *Store) RevokeRoleAssignment(ctx context.Context, id string, scopeKind application.ScopeKind, scopeID string, expected domain.ResourceVersion, now time.Time) (application.RoleAssignmentRecord, error) {
	for attempt := 0; ; attempt++ {
		record, err := s.revokeRoleAssignment(ctx, id, scopeKind, scopeID, expected, now)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.RoleAssignmentRecord{}, err
		}
	}
}

func (s *Store) revokeRoleAssignment(ctx context.Context, id string, scopeKind application.ScopeKind, scopeID string, expected domain.ResourceVersion, now time.Time) (application.RoleAssignmentRecord, error) {
	if id == "" || len(id) > 128 || !validScope(scopeKind, scopeID) || expected == 0 || now.IsZero() {
		return application.RoleAssignmentRecord{}, fmt.Errorf("role assignment revocation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT subject_kind, subject_id, role, valid_from_us, valid_until_us FROM labcentral_role_assignments
		WHERE assignment_id = ? AND scope_kind = ? AND scope_id = ? AND resource_version = ?`
	if s.dialect == "postgresql" {
		query += " FOR UPDATE"
	}
	var subjectKind application.SubjectKind
	var subjectID string
	var role application.Role
	var from, until int64
	if err := tx.QueryRowContext(ctx, s.bind(query), id, scopeKind, scopeID, expected).Scan(&subjectKind, &subjectID, &role, &from, &until); errors.Is(err, sql.ErrNoRows) {
		return application.RoleAssignmentRecord{}, application.ErrAuthVersionConflict
	} else if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	nowMicros := now.UTC().UnixMicro()
	if until != 0 && until <= nowMicros {
		return application.RoleAssignmentRecord{}, application.ErrAuthVersionConflict
	}
	if role == application.RolePlatformAdministrator && scopeKind == application.ScopePlatform && scopeID == "platform" && from <= nowMicros {
		count, err := s.countOtherPlatformAdministrators(ctx, tx, subjectID, nowMicros)
		if err != nil {
			return application.RoleAssignmentRecord{}, err
		}
		if count == 0 {
			return application.RoleAssignmentRecord{}, application.ErrLastPlatformAdministrator
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_role_assignments SET valid_until_us = ?, resource_version = resource_version + 1
		WHERE assignment_id = ? AND resource_version = ?`), nowMicros, id, expected)
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.RoleAssignmentRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, scopeKind, scopeID, "role-assignment.revoke", "role-assignment.revoked", "role-assignment", id, expected+1, "revoked"); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	return s.roleAssignmentByID(ctx, id)
}

func (s *Store) roleAssignmentByID(ctx context.Context, id string) (application.RoleAssignmentRecord, error) {
	var record application.RoleAssignmentRecord
	var from, until int64
	err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT assignment_id, subject_kind, subject_id, scope_kind, scope_id, role, valid_from_us, valid_until_us, resource_version
		FROM labcentral_role_assignments WHERE assignment_id = ?`), id).Scan(&record.ID, &record.SubjectKind, &record.SubjectID, &record.ScopeKind, &record.ScopeID, &record.Role, &from, &until, &record.ResourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return application.RoleAssignmentRecord{}, application.ErrAuthRecordNotFound
	}
	if err != nil {
		return application.RoleAssignmentRecord{}, err
	}
	record.ValidFrom, record.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
	return record, nil
}

func (s *Store) CreateTargetGrant(ctx context.Context, record application.TargetGrantRecord) (application.TargetGrantRecord, error) {
	if record.ResourceVersion != 0 {
		return application.TargetGrantRecord{}, fmt.Errorf("provider target grant resource version is invalid")
	}
	if record.ID == "" || len(record.ID) > 128 || !validScope(record.ScopeKind, record.ScopeID) {
		return application.TargetGrantRecord{}, fmt.Errorf("target grant is invalid")
	}
	if _, err := domain.NewProviderTargetID(string(record.TargetID)); err != nil {
		return application.TargetGrantRecord{}, err
	}
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	if _, err := s.scopeOrganization(ctx, record.ScopeKind, record.ScopeID); err != nil {
		return application.TargetGrantRecord{}, fmt.Errorf("provider target grant scope is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_provider_target_grants
		(grant_id, target_id, scope_kind, scope_id, valid_from_us, valid_until_us, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)`), record.ID, record.TargetID, record.ScopeKind, record.ScopeID, from, until); err != nil {
		return application.TargetGrantRecord{}, wrapIdentityError("save provider target grant", err)
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, record.ScopeKind, record.ScopeID, "provider-target-grant.create", "provider-target-grant.created", "provider-target-grant", record.ID, 1, "active"); err != nil {
		return application.TargetGrantRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.TargetGrantRecord{}, err
	}
	return s.targetGrantByID(ctx, record.ID)
}

func (s *Store) UpdateTargetGrant(ctx context.Context, record application.TargetGrantRecord, expected domain.ResourceVersion) (application.TargetGrantRecord, error) {
	from, until, err := validity(record.ValidFrom, record.ValidUntil)
	if err != nil || expected == 0 || record.ID == "" || len(record.ID) > 128 || !validScope(record.ScopeKind, record.ScopeID) {
		return application.TargetGrantRecord{}, fmt.Errorf("provider target grant update is invalid")
	}
	if _, err := domain.NewProviderTargetID(string(record.TargetID)); err != nil {
		return application.TargetGrantRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_target_grants SET valid_from_us = ?, valid_until_us = ?, resource_version = resource_version + 1
		WHERE grant_id = ? AND target_id = ? AND scope_kind = ? AND scope_id = ? AND resource_version = ?`),
		from, until, record.ID, record.TargetID, record.ScopeKind, record.ScopeID, expected)
	if err != nil {
		return application.TargetGrantRecord{}, wrapIdentityError("update provider target grant", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.TargetGrantRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, record.ScopeKind, record.ScopeID, "provider-target-grant.update", "provider-target-grant.updated", "provider-target-grant", record.ID, expected+1, "active"); err != nil {
		return application.TargetGrantRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.TargetGrantRecord{}, err
	}
	return s.targetGrantByID(ctx, record.ID)
}

func (s *Store) RevokeTargetGrant(ctx context.Context, id string, expected domain.ResourceVersion, now time.Time) (application.TargetGrantRecord, error) {
	if id == "" || len(id) > 128 || expected == 0 || now.IsZero() {
		return application.TargetGrantRecord{}, fmt.Errorf("provider target grant revocation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var scopeKind application.ScopeKind
	var scopeID string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT scope_kind, scope_id FROM labcentral_provider_target_grants WHERE grant_id = ? AND resource_version = ?`), id, expected).Scan(&scopeKind, &scopeID); errors.Is(err, sql.ErrNoRows) {
		return application.TargetGrantRecord{}, application.ErrAuthVersionConflict
	} else if err != nil {
		return application.TargetGrantRecord{}, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_target_grants SET valid_until_us = ?, resource_version = resource_version + 1
		WHERE grant_id = ? AND resource_version = ? AND (valid_until_us = 0 OR valid_until_us > ?)`), now.UTC().UnixMicro(), id, expected, now.UTC().UnixMicro())
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.TargetGrantRecord{}, application.ErrAuthVersionConflict
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, scopeKind, scopeID, "provider-target-grant.revoke", "provider-target-grant.revoked", "provider-target-grant", id, expected+1, "revoked"); err != nil {
		return application.TargetGrantRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.TargetGrantRecord{}, err
	}
	return s.targetGrantByID(ctx, id)
}

func (s *Store) targetGrantByID(ctx context.Context, id string) (application.TargetGrantRecord, error) {
	var record application.TargetGrantRecord
	var from, until int64
	err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT grant_id, target_id, scope_kind, scope_id, valid_from_us, valid_until_us, resource_version
		FROM labcentral_provider_target_grants WHERE grant_id = ?`), id).Scan(&record.ID, &record.TargetID, &record.ScopeKind, &record.ScopeID, &from, &until, &record.ResourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return application.TargetGrantRecord{}, application.ErrAuthRecordNotFound
	}
	if err != nil {
		return application.TargetGrantRecord{}, err
	}
	record.ValidFrom, record.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
	return record, nil
}

func (s *Store) SetUserDisabled(ctx context.Context, userID domain.UserID, disabled bool, expected domain.ResourceVersion, now time.Time) (application.UserRecord, error) {
	for attempt := 0; ; attempt++ {
		record, err := s.setUserDisabled(ctx, userID, disabled, expected, now)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.UserRecord{}, err
		}
	}
}

func (s *Store) setUserDisabled(ctx context.Context, userID domain.UserID, disabled bool, expected domain.ResourceVersion, now time.Time) (application.UserRecord, error) {
	if _, err := domain.NewUserID(string(userID)); err != nil || expected == 0 || now.IsZero() {
		return application.UserRecord{}, fmt.Errorf("user state update is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.UserRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if disabled {
		var isAdministrator int
		err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_role_assignments
			WHERE subject_kind = 'user' AND subject_id = ? AND scope_kind = 'platform' AND scope_id = 'platform'
			AND role = 'platform-administrator' AND valid_from_us <= ? AND (valid_until_us = 0 OR valid_until_us > ?)`), userID, now.UTC().UnixMicro(), now.UTC().UnixMicro()).Scan(&isAdministrator)
		if err != nil {
			return application.UserRecord{}, err
		}
		if isAdministrator > 0 {
			count, err := s.countOtherPlatformAdministrators(ctx, tx, string(userID), now.UTC().UnixMicro())
			if err != nil {
				return application.UserRecord{}, err
			}
			if count == 0 {
				return application.UserRecord{}, application.ErrLastPlatformAdministrator
			}
		}
	}
	disabledValue := 0
	if disabled {
		disabledValue = 1
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_users SET disabled = ?, resource_version = resource_version + 1, updated_at_us = ?
		WHERE user_id = ? AND resource_version = ?`), disabledValue, now.UTC().UnixMicro(), userID, expected)
	if err != nil {
		return application.UserRecord{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.UserRecord{}, application.ErrAuthVersionConflict
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_sessions SET revoked_at_us = ? WHERE user_id = ? AND revoked_at_us = 0`), now.UTC().UnixMicro(), userID); err != nil {
			return application.UserRecord{}, err
		}
	}
	state := "enabled"
	action := "user.enable"
	eventType := "user.enabled"
	if disabled {
		state = "disabled"
		action = "user.disable"
		eventType = "user.disabled"
	}
	if err := s.appendAdministratorGovernanceTx(ctx, tx, application.ScopePlatform, "platform", action, eventType, "user", string(userID), expected+1, state); err != nil {
		return application.UserRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.UserRecord{}, err
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) countOtherPlatformAdministrators(ctx context.Context, tx *sql.Tx, excludedUserID string, nowMicros int64) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(DISTINCT assignments.subject_id)
		FROM labcentral_role_assignments assignments JOIN labcentral_users users ON users.user_id = assignments.subject_id
		WHERE assignments.subject_kind = 'user' AND assignments.scope_kind = 'platform' AND assignments.scope_id = 'platform'
		AND assignments.role = 'platform-administrator' AND assignments.subject_id <> ?
		AND assignments.valid_from_us <= ? AND (assignments.valid_until_us = 0 OR assignments.valid_until_us > ?) AND users.disabled = 0`), excludedUserID, nowMicros, nowMicros).Scan(&count)
	return count, err
}
