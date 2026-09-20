package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func projectGroupMigration() []string {
	return []string{
		`CREATE TABLE labcentral_project_groups (team_id TEXT PRIMARY KEY REFERENCES labcentral_teams(team_id),project_id TEXT NOT NULL REFERENCES labcentral_projects(project_id),name TEXT NOT NULL,enrollment_open INTEGER NOT NULL CHECK(enrollment_open IN(0,1)),resource_version BIGINT NOT NULL CHECK(resource_version>0),created_by TEXT NOT NULL REFERENCES labcentral_users(user_id),created_at_us BIGINT NOT NULL,UNIQUE(project_id,name),UNIQUE(project_id,team_id))`,
		`CREATE TABLE labcentral_project_enrollments (project_id TEXT NOT NULL,user_id TEXT NOT NULL REFERENCES labcentral_users(user_id),team_id TEXT NOT NULL,joined_at_us BIGINT NOT NULL,PRIMARY KEY(project_id,user_id),FOREIGN KEY(project_id,team_id) REFERENCES labcentral_project_groups(project_id,team_id))`,
		`CREATE INDEX labcentral_project_enrollment_team_idx ON labcentral_project_enrollments(project_id,team_id,user_id)`,
	}
}
func (s *Store) requireGroupProjectAuthorityTx(ctx context.Context, tx *sql.Tx, project domain.ProjectID, actor domain.UserID, admin bool) error {
	roles := "'platform-administrator','organization-administrator','lab-author','instructor','participant','viewer'"
	if admin {
		roles = "'platform-administrator','organization-administrator'"
	}
	return s.requireGroupRolesTx(ctx, tx, project, actor, roles)
}
func (s *Store) requireGroupRolesTx(ctx context.Context, tx *sql.Tx, project domain.ProjectID, actor domain.UserID, roles string) error {
	if project == "project.local" && actor == app.LocalAdminID {
		return nil
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return err
	}
	n := now.UnixMicro()
	var allowed int
	err = tx.QueryRowContext(ctx, s.bind(`WITH scope AS(SELECT organization_id FROM labcentral_projects WHERE project_id=?), memberships AS(SELECT organization_id,COALESCE(team_id,'') team_id FROM labcentral_memberships WHERE user_id=? AND valid_from_us<=? AND(valid_until_us=0 OR valid_until_us>?)) SELECT CASE WHEN EXISTS(SELECT 1 FROM labcentral_users WHERE user_id=? AND disabled=0) AND EXISTS(SELECT 1 FROM labcentral_role_assignments a CROSS JOIN scope s WHERE a.role IN(`+roles+`) AND a.valid_from_us<=? AND(a.valid_until_us=0 OR a.valid_until_us>?) AND((a.subject_kind='user' AND a.subject_id=?) OR(a.subject_kind='team' AND EXISTS(SELECT 1 FROM memberships m WHERE m.team_id=a.subject_id))) AND(a.subject_kind<>'user' OR a.scope_kind='platform' OR EXISTS(SELECT 1 FROM memberships m WHERE m.organization_id=s.organization_id)) AND((a.scope_kind='platform' AND a.scope_id='platform') OR(a.scope_kind='organization' AND a.scope_id=s.organization_id) OR(a.scope_kind='project' AND a.scope_id=?))) THEN 1 ELSE 0 END`), project, actor, n, n, actor, n, n, actor, project).Scan(&allowed)
	if err != nil {
		return err
	}
	if allowed != 1 {
		return app.ErrAuthorityChanged
	}
	return nil
}

const groupColumns = `g.team_id,g.project_id,g.name,g.enrollment_open,g.resource_version,g.created_at_us,(SELECT COUNT(*) FROM labcentral_project_enrollments e WHERE e.project_id=g.project_id AND e.team_id=g.team_id),CASE WHEN EXISTS(SELECT 1 FROM labcentral_project_enrollments e WHERE e.project_id=g.project_id AND e.team_id=g.team_id AND e.user_id=?) THEN 1 ELSE 0 END`

func scanProjectGroup(row scanner) (app.ProjectGroup, error) {
	var g app.ProjectGroup
	var open, joined int
	var created int64
	err := row.Scan(&g.ID, &g.ProjectID, &g.Name, &open, &g.ResourceVersion, &created, &g.MemberCount, &joined)
	if errors.Is(err, sql.ErrNoRows) {
		return g, app.ErrNotFound
	}
	g.APIVersion = app.ProjectGroupVersion
	g.EnrollmentOpen = open == 1
	g.Joined = joined == 1
	g.CreatedAt = time.UnixMicro(created).UTC()
	return g, err
}
func (s *Store) groupTx(ctx context.Context, tx *sql.Tx, project domain.ProjectID, group domain.TeamID, actor domain.UserID, lock bool) (app.ProjectGroup, error) {
	// Lock the group independently of the membership-count projection.
	if lock {
		query := `SELECT team_id FROM labcentral_project_groups WHERE project_id=? AND team_id=?`
		if s.dialect == jobsdb.DialectPostgreSQL {
			query += ` FOR UPDATE`
		}
		var id string
		if err := tx.QueryRowContext(ctx, s.bind(query), project, group).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return app.ProjectGroup{}, app.ErrNotFound
			}
			return app.ProjectGroup{}, err
		}
	}
	return scanProjectGroup(tx.QueryRowContext(ctx, s.bind(`SELECT `+groupColumns+` FROM labcentral_project_groups g WHERE g.project_id=? AND g.team_id=? AND NOT EXISTS(SELECT 1 FROM labcentral_deleted_project_groups d WHERE d.team_id=g.team_id)`), actor, project, group))
}
func (s *Store) ListProjectGroups(ctx context.Context, project domain.ProjectID, actor domain.UserID, after string, limit uint32) (out app.ProjectGroupPage, err error) {
	if limit == 0 || limit > 200 || len(after) > 128 {
		return out, fmt.Errorf("group list bounds")
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.requireGroupProjectAuthorityTx(ctx, tx, project, actor, false); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, s.bind(`SELECT `+groupColumns+` FROM labcentral_project_groups g WHERE g.project_id=? AND g.team_id>? AND NOT EXISTS(SELECT 1 FROM labcentral_deleted_project_groups d WHERE d.team_id=g.team_id) ORDER BY g.team_id LIMIT ?`), actor, project, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = app.ProjectGroupPage{Items: []app.ProjectGroup{}}
		for rows.Next() {
			g, e := scanProjectGroup(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, g)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > int(limit) {
			out.Items = out.Items[:limit]
			out.NextCursor = string(out.Items[len(out.Items)-1].ID)
		}
		return nil
	})
	return
}
func (s *Store) appendGroupAuditTx(ctx context.Context, tx *sql.Tx, actor domain.UserID, project domain.ProjectID, id, action string, payload any) error {
	scope, err := s.recordScopeTx(ctx, tx, app.ScopeProject, string(project))
	if err != nil {
		return err
	}
	audit, event, err := app.NewGovernanceRecords(app.GovernanceRecordInput{Category: app.AuditAdministrator, Action: action, Outcome: app.AuditSucceeded, EventType: action, Scope: scope, Actor: app.RecordActor{Kind: app.RecordActorUser, ID: string(actor)}, AggregateType: "project-group", AggregateID: id, Payload: payload})
	if err != nil {
		return err
	}
	if _, err = s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return err
	}
	_, err = s.appendEventRecordTx(ctx, tx, event)
	return err
}
func (s *Store) CreateProjectGroups(ctx context.Context, r app.CreateProjectGroups) (out []app.ProjectGroup, err error) {
	if len(r.Groups) == 0 || len(r.Groups) > app.MaximumGroupBatch {
		return nil, fmt.Errorf("group batch must contain 1-100 groups")
	}
	seen := map[string]bool{}
	for _, g := range r.Groups {
		if _, e := domain.NewTeamID(string(g.ID)); e != nil {
			return nil, e
		}
		if !app.ValidWorkspaceName(g.Name) || seen[g.Name] {
			return nil, fmt.Errorf("invalid or duplicate group name")
		}
		seen[g.Name] = true
	}
	if err = validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return nil, err
	}
	const action = "projectGroup.create"
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.requireGroupProjectAuthorityTx(ctx, tx, r.ProjectID, r.ActorID, true); e != nil {
			return e
		}
		if e := s.lockIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey); e != nil {
			return e
		}
		_, found, e := s.findIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest)
		if e != nil {
			return e
		}
		if found {
			return s.loadGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, &out)
		}
		var org domain.OrganizationID
		if e = tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id=?`), r.ProjectID).Scan(&org); e != nil {
			return e
		}
		out = []app.ProjectGroup{}
		for _, g := range r.Groups {
			if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_teams(team_id,organization_id,name,resource_version,created_at_us,updated_at_us) VALUES(?,?,?,1,`+s.nowMicros()+`,`+s.nowMicros()+`)`), g.ID, org, "project-group:"+string(g.ID)); e != nil {
				return wrapIdentityError("create collaboration team", e)
			}
			if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_project_groups(team_id,project_id,name,enrollment_open,resource_version,created_by,created_at_us) VALUES(?,?,?,?,1,?,`+s.nowMicros()+`)`), g.ID, r.ProjectID, g.Name, quotaBoolean(r.EnrollmentOpen), r.ActorID); e != nil {
				return wrapIdentityError("create project group", e)
			}
			group, e := s.groupTx(ctx, tx, r.ProjectID, g.ID, r.ActorID, false)
			if e != nil {
				return e
			}
			out = append(out, group)
		}
		if e = s.appendGroupAuditTx(ctx, tx, r.ActorID, r.ProjectID, string(out[0].ID), action, struct {
			Groups []app.ProjectGroup `json:"groups"`
		}{out}); e != nil {
			return e
		}
		if e = s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(out[0].ID)); e != nil {
			return e
		}
		return s.saveGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, out)
	})
	return
}

func (s *Store) ChangeProjectGroupEnrollment(ctx context.Context, r app.ChangeProjectGroupEnrollment) (out app.ProjectGroup, err error) {
	if r.ExpectedVersion == 0 {
		return out, app.ErrAuthVersionConflict
	}
	if err = validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return out, err
	}
	const action = "projectGroup.enrollment"
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.requireGroupProjectAuthorityTx(ctx, tx, r.ProjectID, r.ActorID, true); e != nil {
			return e
		}
		if e := s.lockIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey); e != nil {
			return e
		}
		_, found, e := s.findIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest)
		if e != nil {
			return e
		}
		if found {
			return s.loadGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, &out)
		}
		g, e := s.groupTx(ctx, tx, r.ProjectID, r.GroupID, r.ActorID, true)
		if e != nil {
			return e
		}
		if g.ResourceVersion != r.ExpectedVersion {
			return app.ErrAuthVersionConflict
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_project_groups SET enrollment_open=?,resource_version=resource_version+1 WHERE project_id=? AND team_id=?`), quotaBoolean(r.Open), r.ProjectID, r.GroupID); e != nil {
			return e
		}
		out, e = s.groupTx(ctx, tx, r.ProjectID, r.GroupID, r.ActorID, false)
		if e != nil {
			return e
		}
		if e = s.appendGroupAuditTx(ctx, tx, r.ActorID, r.ProjectID, string(r.GroupID), action, out); e != nil {
			return e
		}
		if e = s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(r.GroupID)); e != nil {
			return e
		}
		return s.saveGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, out)
	})
	return
}
func (s *Store) ChangeProjectGroupMembership(ctx context.Context, r app.ChangeProjectGroupMembership) (out app.ProjectGroup, err error) {
	if err = validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return out, err
	}
	action := "projectGroup.leave"
	if r.Join {
		action = "projectGroup.join"
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.requireGroupProjectAuthorityTx(ctx, tx, r.ProjectID, r.ActorID, false); e != nil {
			return e
		}
		// A project/user lock also serializes requests for two different groups.
		if e := s.lockIdempotency(ctx, tx, r.ActorID, "projectGroup.member", string(r.ProjectID)); e != nil {
			return e
		}
		_, found, e := s.findIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest)
		if e != nil {
			return e
		}
		if found {
			return s.loadGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, &out)
		}
		out, e = s.groupTx(ctx, tx, r.ProjectID, r.GroupID, r.ActorID, true)
		if e != nil {
			return e
		}
		var current domain.TeamID
		e = tx.QueryRowContext(ctx, s.bind(`SELECT team_id FROM labcentral_project_enrollments WHERE project_id=? AND user_id=?`), r.ProjectID, r.ActorID).Scan(&current)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		changed := false
		if r.Join {
			if current != "" && current != r.GroupID {
				return app.ErrGroupMembershipConflict
			}
			if current == "" {
				if !out.EnrollmentOpen {
					return app.ErrGroupEnrollmentClosed
				}
				if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_project_enrollments(project_id,user_id,team_id,joined_at_us) VALUES(?,?,?,`+s.nowMicros()+`)`), r.ProjectID, r.ActorID, r.GroupID); e != nil {
					return e
				}
				changed = true
			}
		} else if current == r.GroupID {
			if _, e = tx.ExecContext(ctx, s.bind(`DELETE FROM labcentral_project_enrollments WHERE project_id=? AND user_id=? AND team_id=?`), r.ProjectID, r.ActorID, r.GroupID); e != nil {
				return e
			}
			changed = true
			// Existing closure delivery/reconciliation handles every issued plugin session.
			if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state='revoked',launch_token_key_id='',launch_token_hash='',launch_expires_at_us=0,lease_key_id='',lease_hash='',lease_expires_at_us=0,revoked_at_us=`+s.nowMicros()+`,revoked_by=?,revocation_reason='project-group-departure',closure_required=1,resource_version=resource_version+1,updated_at_us=`+s.nowMicros()+` WHERE subject_id=? AND state IN ('pending','ready','active') AND instance_id IN(SELECT instance_id FROM labcentral_local_instances WHERE project_id=? AND owner_kind='team' AND owner_id=?)`), r.ActorID, r.ActorID, r.ProjectID, r.GroupID); e != nil {
				return e
			}
		}
		out, e = s.groupTx(ctx, tx, r.ProjectID, r.GroupID, r.ActorID, false)
		if e != nil {
			return e
		}
		if changed {
			if e = s.appendGroupAuditTx(ctx, tx, r.ActorID, r.ProjectID, string(r.GroupID), action, struct {
				GroupID domain.TeamID `json:"groupId"`
				UserID  domain.UserID `json:"userId"`
			}{r.GroupID, r.ActorID}); e != nil {
				return e
			}
		}
		if e = s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(r.GroupID)); e != nil {
			return e
		}
		return s.saveGovernanceIdempotencyResult(ctx, tx, r.ActorID, action, r.IdempotencyKey, out)
	})
	return
}

var _ app.ProjectGroupStore = (*Store)(nil)

func (s *Store) ListProjectGroupMembers(ctx context.Context, project domain.ProjectID, group domain.TeamID, actor domain.UserID, after string, limit uint32) (out app.ProjectGroupMemberPage, err error) {
	if limit == 0 || limit > 200 || len(after) > 128 {
		return out, fmt.Errorf("member list bounds")
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.requireGroupProjectAuthorityTx(ctx, tx, project, actor, false); e != nil {
			return e
		}
		g, e := s.groupTx(ctx, tx, project, group, actor, false)
		if e != nil {
			return e
		}
		// A project user may discover groups, but member identities require current
		// enrollment, scoped instruction or project administration. Check both in this read transaction.
		if !g.Joined {
			if e = s.requireGroupRolesTx(ctx, tx, project, actor, "'platform-administrator','organization-administrator','instructor'"); e != nil {
				return e
			}
		}
		rows, e := tx.QueryContext(ctx, s.bind(`SELECT u.user_id,u.display_name FROM labcentral_project_enrollments e JOIN labcentral_users u ON u.user_id=e.user_id WHERE e.project_id=? AND e.team_id=? AND e.user_id>? ORDER BY e.user_id LIMIT ?`), project, group, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = app.ProjectGroupMemberPage{Items: []app.ProjectGroupMember{}}
		for rows.Next() {
			var m app.ProjectGroupMember
			if e = rows.Scan(&m.ID, &m.DisplayName); e != nil {
				return e
			}
			out.Items = append(out.Items, m)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > int(limit) {
			out.Items = out.Items[:limit]
			out.NextCursor = string(out.Items[len(out.Items)-1].ID)
		}
		return nil
	})
	return
}

// Retain the ownership identity and audit history; only the active group is removed.
// The group row lock is shared by enrollment and admission, fencing pending launches.
func (s *Store) DeleteProjectGroup(ctx context.Context, r app.DeleteProjectGroup) error {
	if r.ExpectedVersion == 0 {
		return app.ErrAuthVersionConflict
	}
	if err := validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return err
	}
	const action = "projectGroup.delete"
	return s.workTx(ctx, func(tx *sql.Tx) error {
		if err := s.requireGroupProjectAuthorityTx(ctx, tx, r.ProjectID, r.ActorID, true); err != nil {
			return err
		}
		if err := s.lockIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey); err != nil {
			return err
		}
		_, found, err := s.findIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest)
		if err != nil || found {
			return err
		}
		g, err := s.groupTx(ctx, tx, r.ProjectID, r.GroupID, r.ActorID, true)
		if err != nil {
			return err
		}
		if g.ResourceVersion != r.ExpectedVersion {
			return app.ErrAuthVersionConflict
		}
		var count int
		if err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_local_instances WHERE project_id=? AND owner_kind='team' AND owner_id=? AND phase<>'destroyed'`), r.ProjectID, r.GroupID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return app.ErrGroupHasLabs
		}
		if _, err = tx.ExecContext(ctx, s.bind(`DELETE FROM labcentral_project_enrollments WHERE project_id=? AND team_id=?`), r.ProjectID, r.GroupID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_project_groups SET name=?,enrollment_open=0,resource_version=resource_version+1 WHERE project_id=? AND team_id=?`), "deleted:"+string(r.GroupID), r.ProjectID, r.GroupID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_deleted_project_groups(team_id,name,deleted_at_us) VALUES(?,?,`+s.nowMicros()+`)`), r.GroupID, g.Name); err != nil {
			return err
		}
		if err = s.appendGroupAuditTx(ctx, tx, r.ActorID, r.ProjectID, string(r.GroupID), action, g); err != nil {
			return err
		}
		return s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(r.GroupID))
	})
}

func (s *Store) InstancePresentation(ctx context.Context, id domain.InstanceID) (app.InstancePresentation, error) {
	var out app.InstancePresentation
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT COALESCE(t.name,''),COALESCE(t.description,''),COALESCE(d.name,g.name,u.display_name,tm.name,'') FROM labcentral_local_instances i LEFT JOIN labcentral_template_revisions r ON r.revision_id=i.revision_id LEFT JOIN labcentral_lab_templates t ON t.template_id=r.template_id AND t.project_id=i.project_id LEFT JOIN labcentral_project_groups g ON i.owner_kind='team' AND g.team_id=i.owner_id AND g.project_id=i.project_id LEFT JOIN labcentral_deleted_project_groups d ON d.team_id=g.team_id LEFT JOIN labcentral_users u ON i.owner_kind='user' AND u.user_id=i.owner_id LEFT JOIN labcentral_teams tm ON i.owner_kind='team' AND tm.team_id=i.owner_id WHERE i.instance_id=?`), id).Scan(&out.Name, &out.Instructions, &out.OwnerName)
	return out, err
}
