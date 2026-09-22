package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Store) ConfigureParticipantDeployment(ctx context.Context, enabled bool) error {
	value := "disabled"
	if enabled {
		value = "enabled"
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_installation_settings(setting_key,setting_value,updated_at_us) VALUES('participant_deployment',?,`+s.nowMicros()+`) ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value,updated_at_us=excluded.updated_at_us`), value)
	return err
}
func (s *Store) requireDeploymentPolicyTx(ctx context.Context, tx *sql.Tx, project domain.ProjectID, actor domain.UserID) error {
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM labcentral_installation_settings WHERE setting_key='participant_deployment' AND setting_value='disabled') THEN 1 ELSE 0 END`).Scan(&disabled); err != nil {
		return err
	}
	if !disabled {
		return nil
	}
	return s.requireGroupRolesTx(ctx, tx, project, actor, "'platform-administrator','instructor'")
}

// Called only after OIDC token verification. Never links accounts by mutable claims.
func (s *Store) SyncOIDCUser(ctx context.Context, issuer, subject, name, project string) (out app.UserRecord, err error) {
	u, e := url.Parse(issuer)
	if e != nil || u.Scheme != "https" || u.Host == "" || subject == "" || len(subject) > 512 {
		return out, fmt.Errorf("invalid external identity")
	}
	if project != "" {
		if _, e = domain.NewProjectID(project); e != nil {
			return out, e
		}
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	for len(name) > 128 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	suffix := hex.EncodeToString(digest[:20])
	id := domain.UserID("user.oidc-" + suffix)
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if s.dialect == jobsdb.DialectPostgreSQL {
			if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(digest[:8]))); e != nil {
				return e
			}
		}
		existing, e := scanUser(tx.QueryRowContext(ctx, s.bind(`SELECT u.user_id,u.username,u.display_name,u.password_hash,u.disabled,u.created_at_us,u.updated_at_us,u.resource_version FROM labcentral_users u JOIN labcentral_auth_identities i ON i.user_id=u.user_id WHERE i.issuer=? AND i.subject=?`), issuer, subject))
		if e == nil {
			if existing.Disabled {
				return app.ErrAuthRecordNotFound
			}
			out = existing
			if name != "" && name != existing.DisplayName {
				if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_users SET display_name=?,updated_at_us=`+s.nowMicros()+`,resource_version=resource_version+1 WHERE user_id=? AND disabled=0`), name, existing.ID); e != nil {
					return e
				}
				out.DisplayName = name
				out.ResourceVersion++
			}
			return nil
		}
		if !errors.Is(e, app.ErrAuthRecordNotFound) {
			return e
		}
		if project == "" {
			return app.ErrAuthRecordNotFound
		}
		var org string
		if e = tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id=?`), project).Scan(&org); e != nil {
			return e
		}
		if name == "" {
			name = "Student"
		}
		now, e := s.databaseNowTx(ctx, tx)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_users(user_id,username,display_name,password_hash,disabled,created_at_us,updated_at_us) VALUES(?,?,?,'',0,?,?)`), id, "oidc-"+suffix, name, now.UnixMicro(), now.UnixMicro()); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_auth_identities(issuer,subject,user_id,created_at_us) VALUES(?,?,?,?)`), issuer, subject, id, now.UnixMicro()); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_memberships(membership_id,user_id,organization_id,team_id,valid_from_us,valid_until_us,created_at_us) VALUES(?,?,?,NULL,?,0,?)`), "membership.oidc-"+suffix, id, org, now.UnixMicro(), now.UnixMicro()); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_role_assignments(assignment_id,subject_kind,subject_id,scope_kind,scope_id,role,valid_from_us,valid_until_us,created_at_us) VALUES(?,'user',?,'project',?,'participant',?,0,?)`), "role.oidc-"+suffix, id, project, now.UnixMicro(), now.UnixMicro()); e != nil {
			return e
		}
		if e = s.appendAdministratorGovernanceTx(ctx, tx, app.ScopeProject, project, "oidc.participant.provision", "oidc.participant.provisioned", "user", string(id), 1, "active"); e != nil {
			return e
		}
		out = app.UserRecord{ID: id, Username: "oidc-" + suffix, DisplayName: name, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}
		return nil
	})
	return
}
