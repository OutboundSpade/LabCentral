package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func consoleControlMigration() []string {
	return []string{
		`ALTER TABLE labcentral_access_sessions ADD COLUMN shared_console INTEGER NOT NULL DEFAULT 0 CHECK(shared_console IN(0,1))`,
		`CREATE TABLE labcentral_console_control(instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),resource_id TEXT NOT NULL,generation BIGINT NOT NULL CHECK(generation>0),holder_session TEXT NOT NULL DEFAULT '',requested_session TEXT NOT NULL DEFAULT '',request_until_us BIGINT NOT NULL DEFAULT 0,fence BIGINT NOT NULL DEFAULT 0,lease_until_us BIGINT NOT NULL DEFAULT 0,PRIMARY KEY(instance_id,resource_id,generation))`,
	}
}

type consoleLease struct {
	holder, requested   domain.AccessSessionID
	requestUntil, until int64
	fence               uint64
}

func (s *Store) consoleLeaseTx(ctx context.Context, tx *sql.Tx, r app.AccessSessionRecord, lock bool) (v consoleLease, err error) {
	query := `SELECT holder_session,requested_session,request_until_us,fence,lease_until_us FROM labcentral_console_control WHERE instance_id=? AND resource_id=? AND generation=?`
	if lock && s.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	err = tx.QueryRowContext(ctx, s.bind(query), r.InstanceID, r.ResourceID, r.ResourceGeneration).Scan(&v.holder, &v.requested, &v.requestUntil, &v.fence, &v.until)
	return
}
func (s *Store) saveConsoleLeaseTx(ctx context.Context, tx *sql.Tx, r app.AccessSessionRecord, v consoleLease) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_console_control SET holder_session=?,requested_session=?,request_until_us=?,fence=?,lease_until_us=? WHERE instance_id=? AND resource_id=? AND generation=?`), v.holder, v.requested, v.requestUntil, v.fence, v.until, r.InstanceID, r.ResourceID, r.ResourceGeneration)
	return err
}
func (s *Store) beginConsoleControlTx(ctx context.Context, tx *sql.Tx, r app.BeginAccessSessionRequest) error {
	if r.SharedConsole {
		if r.Method != "vnc" {
			return fmt.Errorf("shared control requires VNC")
		}
		// A persisted resource marker forbids downgraded sessions. Any pre-upgrade
		// driver's last authorization lease must expire before the first new driver.
		_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_console_control(instance_id,resource_id,generation,lease_until_us) VALUES(?,?,?,(SELECT COALESCE(MAX(lease_expires_at_us),0) FROM labcentral_access_sessions WHERE instance_id=? AND resource_id=? AND resource_generation=? AND shared_console=0 AND state='active')) ON CONFLICT DO NOTHING`), r.InstanceID, r.ResourceID, r.ResourceGeneration, r.InstanceID, r.ResourceID, r.ResourceGeneration)
		return err
	}
	if r.Method != "vnc" {
		return nil
	}
	var blocked int
	err := tx.QueryRowContext(ctx, s.bind(`SELECT CASE WHEN EXISTS(SELECT 1 FROM labcentral_console_control WHERE instance_id=? AND resource_id=? AND generation=?) OR EXISTS(SELECT 1 FROM labcentral_local_instances i JOIN labcentral_project_groups g ON i.owner_kind='team' AND i.owner_id=g.team_id WHERE i.instance_id=?) THEN 1 ELSE 0 END`), r.InstanceID, r.ResourceID, r.ResourceGeneration, r.InstanceID).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked != 0 {
		return app.ErrAuthorityChanged
	}
	return nil
}

// Called inside the same transaction as authorization renewal. A pending
// takeover prevents the previous holder from renewing, retaining its deadline.
func (s *Store) renewConsoleInputTx(ctx context.Context, tx *sql.Tx, r *app.AccessSessionRecord, now time.Time) error {
	if !r.SharedConsole {
		return nil
	}
	v, err := s.consoleLeaseTx(ctx, tx, *r, true)
	if err != nil {
		return err
	}
	before := v
	n := now.UnixMicro()
	if v.holder == v.requested && v.holder != "" && v.until <= n {
		v.requested = ""
		v.requestUntil = 0
	}
	if v.requested != "" && v.requested != v.holder && v.requestUntil <= n {
		v.requested = ""
		v.requestUntil = 0
	}
	if v.requested == r.ID && (v.holder == r.ID && v.until > n || v.until <= n) {
		if v.holder != r.ID || v.until <= n {
			v.fence++
			v.holder = r.ID
			if err = s.appendConsoleAuditTx(ctx, tx, *r, "console.control.activated", v); err != nil {
				return err
			}
		}
		until := now.Add(app.ConsoleInputLeaseDuration)
		if until.After(r.LeaseExpiresAt) {
			until = r.LeaseExpiresAt
		}
		if until.After(r.ExpiresAt) {
			until = r.ExpiresAt
		}
		v.until = until.UnixMicro()
		v.requestUntil = 0
		r.InputLease = &app.ConsoleInputLease{Fence: v.fence, ValidFor: until.Sub(now)}
	}
	if v == before {
		return nil
	}
	return s.saveConsoleLeaseTx(ctx, tx, *r, v)
}
func (s *Store) consoleActorTx(ctx context.Context, tx *sql.Tx, actor domain.UserID, id domain.AccessSessionID, active bool) (r app.AccessSessionRecord, now time.Time, err error) {
	r, err = s.getAccessSessionTx(ctx, tx, id, active)
	if err != nil {
		return
	}
	if !r.SharedConsole || r.SubjectID != actor {
		err = app.ErrNotFound
		return
	}
	now, err = s.databaseNowTx(ctx, tx)
	if err != nil {
		return
	}
	if r.State != domain.AccessSessionActive && (!(!active && r.State == domain.AccessSessionReady)) {
		err = app.ErrAuthorityChanged
		return
	}
	if !r.ExpiresAt.After(now) || (r.State == domain.AccessSessionActive && !r.LeaseExpiresAt.After(now)) {
		err = app.ErrAuthorityChanged
		return
	}
	var allowed bool
	allowed, err = s.accessCurrentlyAuthorizedSnapshotTx(ctx, tx, r, now, active)
	if err == nil && !allowed {
		err = app.ErrAuthorityChanged
	}
	return
}
func (s *Store) consolePresenceTx(ctx context.Context, tx *sql.Tx, r app.AccessSessionRecord, now time.Time) (out app.ConsolePresence, err error) {
	out = app.ConsolePresence{APIVersion: app.ConsolePresenceVersion, SessionID: r.ID, InstanceID: r.InstanceID, ResourceID: r.ResourceID, Generation: r.ResourceGeneration, Participants: []app.ConsoleParticipant{}}
	v, err := s.consoleLeaseTx(ctx, tx, r, false)
	if err != nil {
		return out, err
	}
	out.Fence = v.fence
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT a.access_session_id,a.subject_id,u.display_name FROM labcentral_access_sessions a JOIN labcentral_users u ON u.user_id=a.subject_id WHERE a.instance_id=? AND a.resource_id=? AND a.resource_generation=? AND a.shared_console=1 AND a.state='active' AND a.lease_expires_at_us>? AND a.expires_at_us>? AND u.disabled=0 ORDER BY a.access_session_id LIMIT 4097`), r.InstanceID, r.ResourceID, r.ResourceGeneration, now.UnixMicro(), now.UnixMicro())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p app.ConsoleParticipant
		if err = rows.Scan(&p.SessionID, &p.UserID, &p.DisplayName); err != nil {
			return out, err
		}
		out.Participants = append(out.Participants, p)
		if len(out.Participants) > 4096 {
			return out, fmt.Errorf("console presence limit")
		}
		if p.SessionID == v.holder && v.until > now.UnixMicro() {
			out.DriverSessionID = v.holder
		}
		if p.SessionID == v.requested && (v.requested == v.holder && v.until > now.UnixMicro() || v.requested != v.holder && v.requestUntil > now.UnixMicro()) {
			out.RequestedSessionID = v.requested
		}
	}
	return out, rows.Err()
}
func (s *Store) ConsolePresence(ctx context.Context, actor domain.UserID, id domain.AccessSessionID) (out app.ConsolePresence, err error) {
	err = s.readTx(ctx, func(tx *sql.Tx) error {
		r, now, e := s.consoleActorTx(ctx, tx, actor, id, false)
		if e != nil {
			return e
		}
		out, e = s.consolePresenceTx(ctx, tx, r, now)
		return e
	})
	return
}
func (s *Store) ChangeConsoleControl(ctx context.Context, r app.ChangeConsoleControl) (out app.ConsolePresence, err error) {
	if err = validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return
	}
	action := "console.control.release"
	if r.Take {
		action = "console.control.take"
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		current, now, e := s.consoleActorTx(ctx, tx, r.ActorID, r.SessionID, true)
		if e != nil {
			return e
		}
		if e = s.lockIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey); e != nil {
			return e
		}
		_, found, e := s.findIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest)
		if e != nil {
			return e
		}
		if found {
			out, e = s.consolePresenceTx(ctx, tx, current, now)
			return e
		}
		v, e := s.consoleLeaseTx(ctx, tx, current, true)
		if e != nil {
			return e
		}
		if r.Take && r.OnlyIfAlone {
			presence, e := s.consolePresenceTx(ctx, tx, current, now)
			if e != nil {
				return e
			}
			if len(presence.Participants) != 1 || presence.Participants[0].SessionID != current.ID || v.until > now.UnixMicro() || (v.requested != "" && v.requestUntil > now.UnixMicro()) {
				out = presence
				return s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(r.SessionID))
			}
		}
		if r.Take {
			if v.until <= now.UnixMicro() {
				v.holder = ""
			}
			v.requested = current.ID
			v.requestUntil = now.Add(30 * time.Second).UnixMicro()
		} else if v.requested == current.ID {
			v.requested = ""
			v.requestUntil = 0
		}
		// Never shorten a previously issued input lease without plugin withdrawal.
		if e = s.saveConsoleLeaseTx(ctx, tx, current, v); e != nil {
			return e
		}
		if e = s.appendConsoleAuditTx(ctx, tx, current, action, v); e != nil {
			return e
		}
		if e = s.bindIdempotency(ctx, tx, r.ActorID, action, r.IdempotencyKey, r.RequestDigest, string(r.SessionID)); e != nil {
			return e
		}
		out, e = s.consolePresenceTx(ctx, tx, current, now)
		return e
	})
	return
}

var _ app.ConsoleControlStore = (*Store)(nil)

func (s *Store) appendConsoleAuditTx(ctx context.Context, tx *sql.Tx, r app.AccessSessionRecord, action string, v consoleLease) error {
	payload := struct {
		SessionID  domain.AccessSessionID `json:"sessionId"`
		Holder     domain.AccessSessionID `json:"holderSessionId,omitempty"`
		Requested  domain.AccessSessionID `json:"requestedSessionId,omitempty"`
		Generation domain.Generation      `json:"generation"`
		Fence      uint64                 `json:"fence"`
	}{r.ID, v.holder, v.requested, r.ResourceGeneration, v.fence}
	audit, event, err := app.NewGovernanceRecords(app.GovernanceRecordInput{Category: app.AuditAccess, Action: action, Outcome: app.AuditSucceeded, EventType: action, Scope: app.RecordScope{OrganizationID: r.OrganizationID, ProjectID: r.ProjectID}, Actor: app.RecordActor{Kind: app.RecordActorUser, ID: string(r.SubjectID)}, AggregateType: "access-session", AggregateID: string(r.ID), Payload: payload})
	if err != nil {
		return err
	}
	if _, err = s.appendAuditRecordTx(ctx, tx, audit); err != nil {
		return err
	}
	_, err = s.appendEventRecordTx(ctx, tx, event)
	return err
}
