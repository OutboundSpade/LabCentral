package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func gatewayMigration() []string {
	return []string{
		`CREATE TABLE labcentral_gateway_access_sessions(access_session_id TEXT PRIMARY KEY,grant_id TEXT NOT NULL)`,
		`CREATE TABLE labcentral_gateway_clients(client_id TEXT PRIMARY KEY,name TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0,secret_key_id TEXT NOT NULL,secret_hash TEXT NOT NULL)`,
		`CREATE TABLE labcentral_gateway_devices(device_id TEXT PRIMARY KEY,client_id TEXT NOT NULL REFERENCES labcentral_gateway_clients(client_id),scope_json TEXT NOT NULL,device_key_id TEXT NOT NULL,device_hash TEXT NOT NULL,user_key_id TEXT NOT NULL,user_hash TEXT NOT NULL,state TEXT NOT NULL,subject_id TEXT NOT NULL DEFAULT '',expires_at_us BIGINT NOT NULL,next_poll_us BIGINT NOT NULL,poll_seconds BIGINT NOT NULL DEFAULT 5,UNIQUE(device_key_id,device_hash),UNIQUE(user_key_id,user_hash))`,
		`CREATE TABLE labcentral_gateway_grants(grant_id TEXT PRIMARY KEY,client_id TEXT NOT NULL REFERENCES labcentral_gateway_clients(client_id),subject_id TEXT NOT NULL,scope_json TEXT NOT NULL,token_key_id TEXT NOT NULL,token_hash TEXT NOT NULL,expires_at_us BIGINT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,UNIQUE(token_key_id,token_hash))`,
		`CREATE TABLE labcentral_gateway_code_limits(subject_id TEXT PRIMARY KEY,window_us BIGINT NOT NULL,attempts BIGINT NOT NULL)`,
		`ALTER TABLE labcentral_audit_records ADD COLUMN gateway_client_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_events ADD COLUMN gateway_client_id TEXT NOT NULL DEFAULT ''`,
	}
}
func (s *Store) CreateGatewayClient(ctx context.Context, c app.GatewayClient, actor domain.UserID, ids app.GovernanceRecordIDs) error {
	if _, e := domain.NewOperationID(c.ID); e != nil {
		return e
	}
	if len(c.Name) == 0 || len(c.Name) > 128 || c.SecretHash.KeyID == "" || len(c.SecretHash.Digest) != 64 {
		return fmt.Errorf("invalid gateway client")
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_gateway_clients(client_id,name,secret_key_id,secret_hash) VALUES(?,?,?,?)`), c.ID, c.Name, c.SecretHash.KeyID, c.SecretHash.Digest); e != nil {
			return e
		}
		return s.appendGatewayGovernance(ctx, tx, actor, c.ID, c.ID, "", ids, "gateway.client.created")
	})
}
func scanGatewayClient(row scanner) (app.GatewayClient, error) {
	var c app.GatewayClient
	var disabled int
	e := row.Scan(&c.ID, &c.Name, &disabled, &c.SecretHash.KeyID, &c.SecretHash.Digest)
	if e != nil {
		return c, app.ErrGatewayInvalid
	}
	c.Disabled = disabled != 0
	return c, nil
}
func (s *Store) GatewayClient(ctx context.Context, id string) (app.GatewayClient, error) {
	return scanGatewayClient(s.db.QueryRowContext(ctx, s.bind(`SELECT client_id,name,disabled,secret_key_id,secret_hash FROM labcentral_gateway_clients WHERE client_id=?`), id))
}
func (s *Store) ListGatewayClients(ctx context.Context) ([]app.GatewayClient, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT client_id,name,disabled,secret_key_id,secret_hash FROM labcentral_gateway_clients ORDER BY client_id LIMIT 200`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []app.GatewayClient{}
	for rows.Next() {
		c, e := scanGatewayClient(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) SetGatewayClientDisabled(ctx context.Context, id string, disabled bool, actor domain.UserID, ids app.GovernanceRecordIDs) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_clients SET disabled=? WHERE client_id=?`), quotaBoolean(disabled), id)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return app.ErrNotFound
		}
		return s.appendGatewayGovernance(ctx, tx, actor, id, id, "", ids, "gateway.client.updated")
	})
}

// DeleteGatewayClient removes authentication material atomically. Access-session
// grant bindings remain so their next authorization check fails closed and the
// existing durable closure path revokes the adapter's traffic grant.
func (s *Store) DeleteGatewayClient(ctx context.Context, id string, actor domain.UserID, ids app.GovernanceRecordIDs) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_clients SET disabled=1 WHERE client_id=?`), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		} // Repeated deletion is safe after a lost response.
		for _, query := range []string{
			`DELETE FROM labcentral_gateway_devices WHERE client_id=?`,
			`DELETE FROM labcentral_gateway_grants WHERE client_id=?`,
			`DELETE FROM labcentral_gateway_clients WHERE client_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, s.bind(query), id); err != nil {
				return err
			}
		}
		return s.appendGatewayGovernance(ctx, tx, actor, id, id, "", ids, "gateway.client.deleted")
	})
}

func (s *Store) CreateGatewayDevice(ctx context.Context, d app.GatewayDevice) (out app.GatewayDevice, e error) {
	scope, e := json.Marshal(d.Scope)
	if e != nil || len(scope) > 32768 {
		return out, app.ErrGatewayDenied
	}
	e = s.workTx(ctx, func(tx *sql.Tx) error {
		var enabled int
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_gateway_clients WHERE client_id=? AND disabled=0`), d.ClientID).Scan(&enabled); e != nil {
			return app.ErrGatewayInvalid
		}
		var count int
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_gateway_devices WHERE client_id=? AND expires_at_us>`+s.nowMicros()+` AND state IN('pending','approved')`), d.ClientID).Scan(&count); e != nil {
			return e
		}
		if count >= 5 {
			return app.ErrGatewaySlowDown
		}
		now, e := s.databaseNowTx(ctx, tx)
		if e != nil {
			return e
		}
		d.ExpiresAt = now.Add(5 * time.Minute)
		d.State = "pending"
		_, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_gateway_devices(device_id,client_id,scope_json,device_key_id,device_hash,user_key_id,user_hash,state,expires_at_us,next_poll_us) VALUES(?,?,?,?,?,?,?,'pending',?,?)`), d.ID, d.ClientID, string(scope), d.DeviceHash.KeyID, d.DeviceHash.Digest, d.UserCodeHash.KeyID, d.UserCodeHash.Digest, d.ExpiresAt.UnixMicro(), now.Add(5*time.Second).UnixMicro())
		out = d
		return e
	})
	return
}

const deviceCols = `d.device_id,d.client_id,c.name,d.scope_json,d.state,d.subject_id,d.expires_at_us`

func scanGatewayDevice(row scanner) (d app.GatewayDevice, e error) {
	var scope string
	var expiry int64
	e = row.Scan(&d.ID, &d.ClientID, &d.ClientName, &scope, &d.State, &d.SubjectID, &expiry)
	if e != nil {
		return d, app.ErrGatewayExpired
	}
	e = json.Unmarshal([]byte(scope), &d.Scope)
	d.ExpiresAt = time.UnixMicro(expiry).UTC()
	return
}
func (s *Store) deviceByUserCode(ctx context.Context, tx *sql.Tx, hashes []app.KeyedTokenHash) (app.GatewayDevice, error) {
	for _, h := range hashes {
		d, e := scanGatewayDevice(tx.QueryRowContext(ctx, s.bind(`SELECT `+deviceCols+` FROM labcentral_gateway_devices d JOIN labcentral_gateway_clients c ON c.client_id=d.client_id WHERE d.user_key_id=? AND d.user_hash=? AND c.disabled=0 AND d.expires_at_us>`+s.nowMicros()), h.KeyID, h.Digest))
		if e == nil {
			return d, nil
		}
	}
	return app.GatewayDevice{}, app.ErrGatewayExpired
}
func (s *Store) InspectGatewayDevice(ctx context.Context, hashes []app.KeyedTokenHash, actor domain.UserID) (out app.GatewayDevice, err error) {
	// Persist attempt accounting even when the code is incorrect.
	var decision error
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		decision = nil
		allowed, e := s.gatewayCodeAttempt(ctx, tx, actor)
		if e != nil {
			return e
		}
		if !allowed {
			decision = app.ErrGatewaySlowDown
			return nil
		}
		out, decision = s.deviceByUserCode(ctx, tx, hashes)
		return nil
	})
	if err == nil {
		err = decision
	}
	return
}
func (s *Store) gatewayCodeAttempt(ctx context.Context, tx *sql.Tx, actor domain.UserID) (bool, error) {
	now, e := s.databaseNowTx(ctx, tx)
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_gateway_code_limits(subject_id,window_us,attempts) VALUES(?,?,0) ON CONFLICT DO NOTHING`), actor, now.UnixMicro()); e != nil {
		return false, e
	}
	var attempts int
	e = tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_gateway_code_limits SET attempts=CASE WHEN window_us<=? THEN 1 ELSE attempts+1 END,window_us=CASE WHEN window_us<=? THEN ? ELSE window_us END WHERE subject_id=? RETURNING attempts`), now.Add(-5*time.Minute).UnixMicro(), now.Add(-5*time.Minute).UnixMicro(), now.UnixMicro(), actor).Scan(&attempts)
	return attempts <= 20, e
}
func (s *Store) ApproveGatewayDevice(ctx context.Context, hashes []app.KeyedTokenHash, actor domain.UserID, approve bool, ids app.GovernanceRecordIDs) error {
	var decision error
	err := s.workTx(ctx, func(tx *sql.Tx) error {
		decision = nil
		allowed, e := s.gatewayCodeAttempt(ctx, tx, actor)
		if e != nil {
			return e
		}
		if !allowed {
			decision = app.ErrGatewaySlowDown
			return nil
		}
		d, e := s.deviceByUserCode(ctx, tx, hashes)
		if e != nil {
			decision = e
			return nil
		}
		if d.State != "pending" {
			decision = app.ErrGatewayConsumed
			return nil
		}
		var enabled int
		if e = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_users WHERE user_id=? AND disabled=0`), actor).Scan(&enabled); e != nil {
			return app.ErrGatewayDenied
		}
		state := "denied"
		if approve {
			state = "approved"
		}
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_devices SET state=?,subject_id=? WHERE device_id=? AND state='pending' AND expires_at_us>`+s.nowMicros()), state, actor, d.ID)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return app.ErrGatewayConsumed
		}
		return s.appendGatewayGovernance(ctx, tx, actor, d.ClientID, d.ID, d.Scope.ProjectID, ids, "gateway.device."+state)
	})
	if err != nil {
		return err
	}
	return decision
}
func (s *Store) PollGatewayDevice(ctx context.Context, client string, hashes []app.KeyedTokenHash, grant app.GatewayGrant, ids app.GovernanceRecordIDs) (out app.GatewayGrant, err error) {
	var decision error
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		decision = nil
		var d app.GatewayDevice
		var found bool
		for _, h := range hashes {
			var e error
			d, e = scanGatewayDevice(tx.QueryRowContext(ctx, s.bind(`SELECT `+deviceCols+` FROM labcentral_gateway_devices d JOIN labcentral_gateway_clients c ON c.client_id=d.client_id WHERE d.client_id=? AND d.device_key_id=? AND d.device_hash=? AND c.disabled=0`), client, h.KeyID, h.Digest))
			if e == nil {
				found = true
				break
			}
		}
		if !found {
			decision = app.ErrGatewayExpired
			return nil
		}
		now, e := s.databaseNowTx(ctx, tx)
		if e != nil {
			return e
		}
		if !d.ExpiresAt.After(now) {
			decision = app.ErrGatewayExpired
			return nil
		}
		if d.State == "consumed" {
			decision = app.ErrGatewayConsumed
			return nil
		}
		if d.State == "denied" {
			decision = app.ErrGatewayDenied
			return nil
		}
		var next, interval int64
		if e = tx.QueryRowContext(ctx, s.bind(`SELECT next_poll_us,poll_seconds FROM labcentral_gateway_devices WHERE device_id=?`), d.ID).Scan(&next, &interval); e != nil {
			return e
		}
		if next > now.UnixMicro() {
			interval = min(interval+5, 300)
			decision = app.ErrGatewaySlowDown
		} else if d.State == "pending" {
			decision = app.ErrGatewayPending
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_devices SET poll_seconds=?,next_poll_us=? WHERE device_id=?`), interval, now.Add(time.Duration(interval)*time.Second).UnixMicro(), d.ID); e != nil {
			return e
		}
		if decision != nil {
			return nil
		}
		grant.ClientID = d.ClientID
		grant.ClientName = d.ClientName
		grant.SubjectID = d.SubjectID
		grant.Scope = d.Scope
		grant.ExpiresAt = now.Add(15 * time.Minute)
		scope, e := json.Marshal(grant.Scope)
		if e != nil {
			return e
		}
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_devices SET state='consumed' WHERE device_id=? AND state='approved'`), d.ID)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return app.ErrGatewayConsumed
		}
		if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_gateway_grants(grant_id,client_id,subject_id,scope_json,token_key_id,token_hash,expires_at_us) VALUES(?,?,?,?,?,?,?)`), grant.ID, grant.ClientID, grant.SubjectID, string(scope), grant.TokenHash.KeyID, grant.TokenHash.Digest, grant.ExpiresAt.UnixMicro()); e != nil {
			return e
		}
		if e = s.appendGatewayGovernance(ctx, tx, grant.SubjectID, grant.ClientID, grant.ID, grant.Scope.ProjectID, ids, "gateway.grant.issued"); e != nil {
			return e
		}
		out = grant
		return nil
	})
	if err == nil {
		err = decision
	}
	return
}

const grantCols = `g.grant_id,g.client_id,c.name,g.subject_id,g.scope_json,g.expires_at_us,g.revoked`

func scanGatewayGrant(row scanner) (g app.GatewayGrant, e error) {
	var scope string
	var expires int64
	var revoked int
	e = row.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.SubjectID, &scope, &expires, &revoked)
	if e != nil {
		return g, app.ErrGatewayExpired
	}
	e = json.Unmarshal([]byte(scope), &g.Scope)
	g.ExpiresAt = time.UnixMicro(expires).UTC()
	g.Revoked = revoked != 0
	return
}
func (s *Store) AuthenticateGatewayGrant(ctx context.Context, client string, hashes []app.KeyedTokenHash) (app.GatewayGrant, error) {
	for _, h := range hashes {
		g, e := scanGatewayGrant(s.db.QueryRowContext(ctx, s.bind(`SELECT `+grantCols+` FROM labcentral_gateway_grants g JOIN labcentral_gateway_clients c ON c.client_id=g.client_id JOIN labcentral_users u ON u.user_id=g.subject_id WHERE g.client_id=? AND g.token_key_id=? AND g.token_hash=? AND g.revoked=0 AND c.disabled=0 AND u.disabled=0 AND g.expires_at_us>`+s.nowMicros()), client, h.KeyID, h.Digest))
		if e == nil {
			return g, nil
		}
	}
	return app.GatewayGrant{}, app.ErrGatewayExpired
}
func (s *Store) ListGatewayGrants(ctx context.Context, subject domain.UserID) ([]app.GatewayGrant, error) {
	rows, e := s.db.QueryContext(ctx, s.bind(`SELECT `+grantCols+` FROM labcentral_gateway_grants g JOIN labcentral_gateway_clients c ON c.client_id=g.client_id WHERE g.subject_id=? ORDER BY g.expires_at_us DESC,g.grant_id LIMIT 200`), subject)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []app.GatewayGrant{}
	for rows.Next() {
		g, e := scanGatewayGrant(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *Store) RevokeGatewayGrant(ctx context.Context, id string, subject domain.UserID, ids app.GovernanceRecordIDs) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		g, e := scanGatewayGrant(tx.QueryRowContext(ctx, s.bind(`SELECT `+grantCols+` FROM labcentral_gateway_grants g JOIN labcentral_gateway_clients c ON c.client_id=g.client_id WHERE g.grant_id=? AND g.subject_id=?`), id, subject))
		if e != nil {
			return app.ErrNotFound
		}
		if g.Revoked {
			return nil
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_gateway_grants SET revoked=1 WHERE grant_id=? AND subject_id=?`), id, subject); e != nil {
			return e
		}
		return s.appendGatewayGovernance(ctx, tx, subject, g.ClientID, id, g.Scope.ProjectID, ids, "gateway.grant.revoked")
	})
}
func (s *Store) appendGatewayGovernance(ctx context.Context, tx *sql.Tx, subject domain.UserID, client, id string, project domain.ProjectID, ids app.GovernanceRecordIDs, action string) error {
	scope := app.RecordScope{ProjectID: project}
	if project != "" {
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id=?`), project).Scan(&scope.OrganizationID); e != nil {
			return e
		}
	}
	data, _ := json.Marshal(struct {
		ClientID string `json:"clientId"`
	}{client})
	audit, e := app.NewRedactedAuditPayload(data)
	if e != nil {
		return e
	}
	event, e := app.NewSafeEventPayload(data)
	if e != nil {
		return e
	}
	actor := app.RecordActor{Kind: app.RecordActorUser, ID: string(subject), GatewayClientID: client}
	if _, e = s.appendAuditRecordTx(ctx, tx, app.AppendAuditRecord{ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: app.AuditAuthentication, Action: action, Outcome: app.AuditSucceeded, Scope: scope, Actor: actor, AggregateType: "gateway", AggregateID: id, RedactedPayload: audit}); e != nil {
		return e
	}
	_, e = s.appendEventRecordTx(ctx, tx, app.AppendEventRecord{ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: action, Scope: scope, Actor: actor, AggregateType: "gateway", AggregateID: id, SafePayload: event})
	return e
}
