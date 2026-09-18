package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func extensionWorkMigration() []string {
	return []string{
		`CREATE TABLE labcentral_extension_work (operation_id TEXT PRIMARY KEY REFERENCES labcentral_jobs(operation_id),project_id TEXT NOT NULL REFERENCES labcentral_projects(project_id),actor_id TEXT NOT NULL,invocation_id TEXT NOT NULL UNIQUE,plugin_instance TEXT NOT NULL,binding_id TEXT NOT NULL,request_json TEXT NOT NULL,result_json TEXT NOT NULL DEFAULT '',failure_code TEXT NOT NULL DEFAULT '',cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancel_requested IN(0,1)),created_at_us BIGINT NOT NULL)`,
		`CREATE TABLE labcentral_extension_binding_leases (binding_key TEXT PRIMARY KEY,operation_id TEXT NOT NULL,lease_until_us BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE labcentral_extension_artifact_refs (operation_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),artifact_ref TEXT NOT NULL,PRIMARY KEY(operation_id,artifact_ref))`,
	}
}

const workColumns = `w.operation_id,w.project_id,w.actor_id,w.invocation_id,w.request_json,j.state,w.result_json,w.failure_code,w.cancel_requested,w.created_at_us`

func scanExtensionWork(row scanner) (app.ExtensionWork, error) {
	var w app.ExtensionWork
	var request, result string
	var cancelled int
	var created int64
	err := row.Scan(&w.ID, &w.ProjectID, &w.ActorID, &w.InvocationID, &request, &w.State, &result, &w.FailureCode, &cancelled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return w, app.ErrNotFound
	}
	if err != nil {
		return w, err
	}
	if err = json.Unmarshal([]byte(request), &w.Request); err != nil {
		return w, err
	}
	w.Result = json.RawMessage(result)
	w.CancelRequested = cancelled == 1
	w.CreatedAt = time.UnixMicro(created).UTC()
	return w, nil
}
func (s *Store) GetExtensionWork(ctx context.Context, id domain.OperationID) (app.ExtensionWork, error) {
	return scanExtensionWork(s.db.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), id))
}
func (s *Store) ListExtensionWork(ctx context.Context, project domain.ProjectID) ([]app.ExtensionWork, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.project_id=? ORDER BY w.created_at_us DESC,w.operation_id LIMIT 200`), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.ExtensionWork{}
	for rows.Next() {
		w, e := scanExtensionWork(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *Store) workTx(ctx context.Context, f func(*sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		err = f(tx)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return err
		}
		if err = waitTransactionRetry(ctx, attempt); err != nil {
			return err
		}
	}
}
func (s *Store) CreateExtensionWork(ctx context.Context, r app.CreateExtensionWork) (out app.ExtensionWork, err error) {
	w := r.Work
	if (w.Request.Version != 1 && !(w.Request.Version == 2 && w.Request.Kind == domain.WorkImageImport && w.Request.Import != nil && w.Request.Import.RetireBindingID != "")) || w.Request.Kind == domain.WorkInstance || w.Request.PluginInstance == "" || w.Request.BindingID == "" || w.InvocationID == "" {
		return out, fmt.Errorf("invalid extension work")
	}
	if err := validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return out, err
	}
	payload, e := json.Marshal(w.Request)
	if e != nil {
		return out, e
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if e := s.lockIdempotency(ctx, tx, w.ActorID, "extension."+string(w.Request.Kind), r.IdempotencyKey); e != nil {
			return e
		}
		if existing, found, e := s.findIdempotency(ctx, tx, w.ActorID, "extension."+string(w.Request.Kind), r.IdempotencyKey, r.RequestDigest); e != nil {
			return e
		} else if found {
			out, e = scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), existing))
			return e
		}
		if e := s.requireExtensionAuthorTx(ctx, tx, w.ProjectID, w.ActorID); e != nil {
			return e
		}

		if w.Request.Version == 2 && w.Request.Import != nil && w.Request.Import.RetireBindingID != "" {
			if err := s.requireImageAdministratorTx(ctx, tx, w.ActorID); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_image_bindings SET retired=1,approved=0 WHERE binding_id=? AND project_id=? AND target_id=? AND provider_digest=?`), w.Request.Import.RetireBindingID, w.ProjectID, w.Request.Import.TargetID, w.Request.PluginDigest)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return app.ErrNotFound
			}
		}
		if w.Request.LibraryID != "" {
			lock := ""
			if s.dialect == jobsdb.DialectPostgreSQL {
				lock = " FOR UPDATE"
			}
			library, e := scanLibrary(tx.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`+lock), w.Request.LibraryID))
			if e != nil {
				return e
			}
			if library.ProjectID != w.ProjectID || library.AuthorID != w.ActorID || library.Binding != w.Request.PluginInstance+"/"+w.Request.BindingID || library.Reference != w.Request.Reference || w.Request.Kind != domain.WorkSource || w.Request.TemplateID != "" {
				return app.ErrNotFound
			}
			existing, e := scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id JOIN labcentral_library_syncs ls ON ls.operation_id=w.operation_id WHERE ls.library_id=? AND j.state IN('queued','running') ORDER BY w.created_at_us LIMIT 1`), library.ID))
			if e == nil {
				out = existing
				return s.bindIdempotency(ctx, tx, w.ActorID, "extension."+string(w.Request.Kind), r.IdempotencyKey, r.RequestDigest, string(out.ID))
			}
			if !errors.Is(e, app.ErrNotFound) {
				return e
			}
		}
		if w.Request.DedupKey != "" {
			var old domain.OperationID
			e := tx.QueryRowContext(ctx, s.bind(`SELECT d.operation_id FROM labcentral_extension_dedup d JOIN labcentral_jobs j ON j.operation_id=d.operation_id WHERE d.project_id=? AND d.kind=? AND d.dedup_key=? AND j.state IN ('queued','running','succeeded') AND NOT EXISTS(SELECT 1 FROM labcentral_image_bindings ib WHERE ib.import_operation_id=d.operation_id AND ib.retired=1)`), w.ProjectID, w.Request.Kind, w.Request.DedupKey).Scan(&old)
			if e == nil {
				out, e = scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), old))
				if e != nil {
					return e
				}
				if e = s.recordExtensionRequestTx(ctx, tx, w.ActorID, w.Request.Kind, r.IdempotencyKey, old); e != nil {
					return e
				}
				return s.bindIdempotency(ctx, tx, w.ActorID, "extension."+string(w.Request.Kind), r.IdempotencyKey, r.RequestDigest, string(old))
			}
			if e != sql.ErrNoRows {
				return e
			}
		}
		if w.Request.TemplateID != "" {
			var project string
			if e := tx.QueryRowContext(ctx, s.bind("SELECT project_id FROM labcentral_lab_templates WHERE template_id=?"), w.Request.TemplateID).Scan(&project); e != nil {
				return e
			}
			if project != string(w.ProjectID) {
				return app.ErrNotFound
			}
		}
		if e := s.jobs.EnqueueWorkTx(ctx, tx, domain.Operation{ID: w.ID, Subject: domain.WorkSubject{Kind: w.Request.Kind, ID: string(w.ID)}, Generation: 1}, payload); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_work(operation_id,project_id,actor_id,invocation_id,plugin_instance,binding_id,request_json,created_at_us) VALUES(?,?,?,?,?,?,?,`+s.nowMicros()+`)`), w.ID, w.ProjectID, w.ActorID, w.InvocationID, w.Request.PluginInstance, w.Request.BindingID, string(payload)); e != nil {
			return e
		}
		if w.Request.LibraryID != "" {
			if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_library_syncs(operation_id,library_id) VALUES(?,?)`), w.ID, w.Request.LibraryID); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_template_libraries SET next_sync_at_us=`+s.nowMicros()+`+interval_seconds*1000000 WHERE library_id=?`), w.Request.LibraryID); e != nil {
				return e
			}
		}
		if w.Request.Build != nil {
			if len(w.Request.Build.Dependencies) > 8 {
				return fmt.Errorf("build dependency limit")
			}
			for _, dependency := range w.Request.Build.Dependencies {
				child, e := scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), dependency.BuildID))
				if e != nil {
					return e
				}
				if child.ProjectID != w.ProjectID || child.Request.Kind != domain.WorkBuild || child.CancelRequested || child.State == domain.OperationFailed || child.State == domain.OperationCanceled {
					return fmt.Errorf("build dependency is unavailable")
				}
				if _, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_dependencies(parent_id,child_id) VALUES(?,?)`), w.ID, child.ID); e != nil {
					return e
				}
			}

			if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_artifact_refs(operation_id,artifact_ref) VALUES(?,?)`), w.ID, w.Request.Build.ContextArtifact); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_plugin_invocations(invocation_id,operation_id,idempotency_key,request_digest,state,created_at_us) VALUES(?,?,?,?,'prepared',`+s.nowMicros()+`)`), w.InvocationID, w.ID, w.InvocationID, r.RequestDigest); e != nil {
			return e
		}
		if e := s.bindIdempotency(ctx, tx, w.ActorID, "extension."+string(w.Request.Kind), r.IdempotencyKey, r.RequestDigest, string(w.ID)); e != nil {
			return e
		}
		if w.Request.DedupKey != "" {
			if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_dedup(project_id,kind,dedup_key,operation_id) VALUES(?,?,?,?) ON CONFLICT(project_id,kind,dedup_key) DO UPDATE SET operation_id=excluded.operation_id`), w.ProjectID, w.Request.Kind, w.Request.DedupKey, w.ID); e != nil {
				return e
			}
		}
		if e := s.recordExtensionRequestTx(ctx, tx, w.ActorID, w.Request.Kind, r.IdempotencyKey, w.ID); e != nil {
			return e
		}
		if e := s.appendExtensionGovernance(ctx, tx, w, r.GovernanceRecordIDs, "extension.queued"); e != nil {
			return e
		}
		out, e = scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), w.ID))
		return e
	})
	return
}
func (s *Store) ClaimExtensionWork(ctx context.Context, owner, binding string, kind domain.WorkSubjectKind, duration time.Duration) (out app.ExtensionWorkClaim, err error) {
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		// One active invocation per configured binding, including unknown recovery.
		if _, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_binding_leases(binding_key,operation_id,lease_until_us) VALUES(?,'',0) ON CONFLICT DO NOTHING`), binding); e != nil {
			return e
		}
		lock := ""
		if s.dialect == jobsdb.DialectPostgreSQL {
			lock = " FOR UPDATE"
		}
		var active string
		var until int64
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT operation_id,lease_until_us FROM labcentral_extension_binding_leases WHERE binding_key=?`+lock), binding).Scan(&active, &until); e != nil {
			return e
		}
		now, e := s.databaseNowTx(ctx, tx)
		if e != nil {
			return e
		}
		if until > now.UnixMicro() {
			return jobsdb.ErrNoClaimableJob
		}
		rows, e := tx.QueryContext(ctx, s.bind(`SELECT w.operation_id FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.plugin_instance || '/' || w.binding_id=? AND j.available_at_us<=? AND (j.state='queued' OR (j.state='running' AND j.lease_until_us<=?)) ORDER BY j.available_at_us,w.created_at_us,w.operation_id LIMIT 1`), binding, now.UnixMicro(), now.UnixMicro())
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				_ = rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return e
		}
		claim, e := s.jobs.ClaimWorkTx(ctx, tx, owner, duration, kind, ids)
		if e != nil {
			return e
		}
		out.Work, e = scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), claim.Operation.ID))
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_plugin_invocations SET state='unknown' WHERE operation_id=?`), claim.Operation.ID); e != nil {
			return e
		}
		out.Operation = claim.Operation
		out.LeaseOwner = owner
		out.Recover = claim.Operation.Attempt > 1
		_, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_binding_leases SET operation_id=?,lease_until_us=? WHERE binding_key=?`), claim.Operation.ID, claim.Operation.LeaseExpiresAt.UnixMicro(), binding)
		return e
	})
	return
}
func (s *Store) RenewExtensionWork(ctx context.Context, c app.ExtensionWorkClaim, duration time.Duration) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET lease_until_us=`+s.nowMicros()+`+? WHERE operation_id=? AND lease_owner=? AND fencing_token=? AND state='running' AND lease_until_us>`+s.nowMicros()), duration.Microseconds(), c.Work.ID, c.LeaseOwner, c.Operation.FencingToken)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return jobsdb.ErrFencingConflict
		}
		_, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_binding_leases SET lease_until_us=`+s.nowMicros()+`+? WHERE binding_key=? AND operation_id=?`), duration.Microseconds(), c.Work.Request.PluginInstance+"/"+c.Work.Request.BindingID, c.Work.ID)
		return e
	})
}
func (s *Store) FinishExtensionWork(ctx context.Context, c app.ExtensionWorkClaim, state domain.OperationState, result json.RawMessage, code string, ids app.GovernanceRecordIDs) error {
	if state != domain.OperationSucceeded && state != domain.OperationFailed && state != domain.OperationCanceled {
		return fmt.Errorf("invalid work completion state")
	}
	if len(result) > 1<<20 || len(result) > 0 && !json.Valid(result) || len(code) > 128 {
		return fmt.Errorf("invalid work result")
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET state=?,lease_until_us=0 WHERE operation_id=? AND lease_owner=? AND fencing_token=? AND attempt=? AND state='running' AND lease_until_us>`+s.nowMicros()), state, c.Work.ID, c.LeaseOwner, c.Operation.FencingToken, c.Operation.Attempt)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return jobsdb.ErrFencingConflict
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_work SET result_json=?,failure_code=? WHERE operation_id=?`), string(result), code, c.Work.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_binding_leases SET lease_until_us=0 WHERE binding_key=? AND operation_id=?`), c.Work.Request.PluginInstance+"/"+c.Work.Request.BindingID, c.Work.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_plugin_invocations SET state=? WHERE operation_id=?`), string(state), c.Work.ID); e != nil {
			return e
		}
		return s.appendExtensionGovernance(ctx, tx, c.Work, ids, "extension."+string(state))
	})
}
func (s *Store) CancelExtensionWork(ctx context.Context, id domain.OperationID, actor domain.UserID, ids app.GovernanceRecordIDs) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		w, e := scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), id))
		if e != nil {
			return e
		}
		if w.ActorID != actor {
			return app.ErrAuthorityChanged
		}
		if w.Request.Kind == domain.WorkBuild || w.Request.Kind == domain.WorkImageImport {
			var count int
			if e := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_extension_dependencies d JOIN labcentral_jobs j ON j.operation_id=d.parent_id JOIN labcentral_extension_work p ON p.operation_id=d.parent_id WHERE d.child_id=? AND j.state IN ('queued','running') AND p.cancel_requested=0`), id).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return fmt.Errorf("shared work still has active preparation waiters")
			}
			if e := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_extension_requests WHERE operation_id=?`), id).Scan(&count); e != nil {
				return e
			}
			if count > 1 {
				return fmt.Errorf("shared work has multiple explicit requests; cancel an individual preparation instead")
			}

		}
		if w.State != domain.OperationQueued && w.State != domain.OperationRunning {
			return nil
		}
		if _, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_work SET cancel_requested=1 WHERE operation_id=?`), id); e != nil {
			return e
		}
		return s.appendExtensionGovernance(ctx, tx, w, ids, "extension.cancel-requested")
	})
}
func (s *Store) appendExtensionGovernance(ctx context.Context, tx *sql.Tx, w app.ExtensionWork, ids app.GovernanceRecordIDs, action string) error {
	var organization domain.OrganizationID
	if e := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id=?`), w.ProjectID).Scan(&organization); e != nil {
		return e
	}
	payload, _ := json.Marshal(struct {
		OperationID string                 `json:"operationId"`
		Kind        domain.WorkSubjectKind `json:"kind"`
	}{string(w.ID), w.Request.Kind})
	audit, e := app.NewRedactedAuditPayload(payload)
	if e != nil {
		return e
	}
	event, e := app.NewSafeEventPayload(payload)
	if e != nil {
		return e
	}
	scope := app.RecordScope{OrganizationID: organization, ProjectID: w.ProjectID}
	actor := app.RecordActor{Kind: app.RecordActorUser, ID: string(w.ActorID)}
	if _, e = s.appendAuditRecordTx(ctx, tx, app.AppendAuditRecord{ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: app.AuditOperation, Action: action, Outcome: app.AuditAccepted, Scope: scope, Actor: actor, AggregateType: "extension-work", AggregateID: string(w.ID), RedactedPayload: audit}); e != nil {
		return e
	}
	_, e = s.appendEventRecordTx(ctx, tx, app.AppendEventRecord{ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: action, Scope: scope, Actor: actor, AggregateType: "extension-work", AggregateID: string(w.ID), SafePayload: event})
	return e
}

func (s *Store) requireExtensionFenceTx(ctx context.Context, tx *sql.Tx, f app.Fence) error {
	// Lock the job through a no-op fenced write so a new owner cannot reclaim it
	// between this check and committing publication.
	r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET recovery_required=1 WHERE operation_id=? AND state='running' AND lease_owner=? AND fencing_token=? AND attempt=? AND desired_generation=? AND lease_until_us>`+s.nowMicros()+` AND EXISTS(SELECT 1 FROM labcentral_extension_work w WHERE w.operation_id=labcentral_jobs.operation_id AND w.cancel_requested=0)`), f.OperationID, f.LeaseOwner, f.Token, f.Attempt, f.Generation)
	if e != nil {
		return e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return jobsdb.ErrFencingConflict
	}
	return nil
}

// This is the publication subset of ADR-0010, evaluated in the same serializable
// transaction as the durable write. It closes membership/role-revocation races.
func (s *Store) requireExtensionAuthorTx(ctx context.Context, tx *sql.Tx, project domain.ProjectID, actor domain.UserID) error {
	if project == "project.local" && actor == app.LocalAdminID {
		return nil
	}
	now, e := s.databaseNowTx(ctx, tx)
	if e != nil {
		return e
	}
	n := now.UnixMicro()
	var allowed int
	e = tx.QueryRowContext(ctx, s.bind(`WITH scope AS(SELECT organization_id FROM labcentral_projects WHERE project_id=?), memberships AS(SELECT organization_id,COALESCE(team_id,'') team_id FROM labcentral_memberships WHERE user_id=? AND valid_from_us<=? AND(valid_until_us=0 OR valid_until_us>?)) SELECT CASE WHEN EXISTS(SELECT 1 FROM labcentral_users WHERE user_id=? AND disabled=0) AND EXISTS(SELECT 1 FROM labcentral_role_assignments a CROSS JOIN scope s WHERE a.role IN('platform-administrator','organization-administrator','lab-author') AND a.valid_from_us<=? AND(a.valid_until_us=0 OR a.valid_until_us>?) AND((a.subject_kind='user' AND a.subject_id=?) OR(a.subject_kind='team' AND EXISTS(SELECT 1 FROM memberships m WHERE m.team_id=a.subject_id))) AND(a.subject_kind<>'user' OR a.scope_kind='platform' OR EXISTS(SELECT 1 FROM memberships m WHERE m.organization_id=s.organization_id)) AND((a.scope_kind='platform' AND a.scope_id='platform') OR(a.scope_kind='organization' AND a.scope_id=s.organization_id) OR(a.scope_kind='project' AND a.scope_id=?))) THEN 1 ELSE 0 END`), project, actor, n, n, actor, n, n, actor, project).Scan(&allowed)
	if e != nil {
		return e
	}
	if allowed != 1 {
		return app.ErrAuthorityChanged
	}
	return nil
}

func (s *Store) recordExtensionRequestTx(ctx context.Context, tx *sql.Tx, actor domain.UserID, kind domain.WorkSubjectKind, key string, id domain.OperationID) error {
	_, e := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_requests(actor_id,kind,request_key,operation_id) VALUES(?,?,?,?) ON CONFLICT(actor_id,kind,request_key) DO UPDATE SET operation_id=excluded.operation_id`), actor, kind, key, id)
	return e
}
