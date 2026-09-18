package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func buildMigration() []string {
	return []string{
		`CREATE TABLE labcentral_extension_requests(actor_id TEXT NOT NULL,kind TEXT NOT NULL,request_key TEXT NOT NULL,operation_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),PRIMARY KEY(actor_id,kind,request_key))`,
		`CREATE TABLE labcentral_extension_dedup(project_id TEXT NOT NULL,kind TEXT NOT NULL,dedup_key TEXT NOT NULL,operation_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),PRIMARY KEY(project_id,kind,dedup_key))`,
		`CREATE TABLE labcentral_extension_dependencies(parent_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),child_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),PRIMARY KEY(parent_id,child_id))`,
		`CREATE TABLE labcentral_image_bindings(binding_id TEXT PRIMARY KEY,project_id TEXT NOT NULL,target_id TEXT NOT NULL,provider_digest TEXT NOT NULL,import_operation_id TEXT NOT NULL REFERENCES labcentral_extension_work(operation_id),binding_json TEXT NOT NULL,opaque_binding BYTEA NOT NULL,approved INTEGER NOT NULL DEFAULT 0,retired INTEGER NOT NULL DEFAULT 0)`,
	}
}
func (s *Store) YieldExtensionWork(ctx context.Context, c app.ExtensionWorkClaim) error {
	return s.yieldExtensionWork(ctx, c, nil)
}
func (s *Store) YieldExtensionWorkProgress(ctx context.Context, c app.ExtensionWorkClaim, result json.RawMessage) error {
	if len(result) == 0 || len(result) > 1<<20 || !json.Valid(result) {
		return fmt.Errorf("invalid work progress")
	}
	return s.yieldExtensionWork(ctx, c, result)
}
func (s *Store) yieldExtensionWork(ctx context.Context, c app.ExtensionWorkClaim, result json.RawMessage) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET state='queued',lease_until_us=0,available_at_us=`+s.nowMicros()+`+1000000 WHERE operation_id=? AND lease_owner=? AND fencing_token=? AND state='running' AND lease_until_us>`+s.nowMicros()), c.Work.ID, c.LeaseOwner, c.Operation.FencingToken)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return jobsdb.ErrFencingConflict
		}
		if result != nil {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_work SET result_json=? WHERE operation_id=?`), string(result), c.Work.ID); err != nil {
				return err
			}
		}
		_, e = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_binding_leases SET lease_until_us=0 WHERE binding_key=? AND operation_id=?`), c.Work.Request.PluginInstance+"/"+c.Work.Request.BindingID, c.Work.ID)
		return e
	})
}
func (s *Store) ReferenceExtensionArtifact(ctx context.Context, id domain.OperationID, ref domain.ArtifactRef) error {
	_, e := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_artifact_refs(operation_id,artifact_ref) VALUES(?,?) ON CONFLICT DO NOTHING`), id, ref)
	return e
}
func (s *Store) LinkExtensionWork(ctx context.Context, parent, child domain.OperationID) error {
	_, e := s.db.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_dependencies(parent_id,child_id) SELECT ?,? WHERE EXISTS(SELECT 1 FROM labcentral_extension_work p JOIN labcentral_extension_work c ON p.project_id=c.project_id WHERE p.operation_id=? AND c.operation_id=?) ON CONFLICT DO NOTHING`), parent, child, parent, child)
	return e
}
func (s *Store) PutImageBinding(ctx context.Context, c app.ExtensionWorkClaim, b app.StoredImageBinding) error {
	raw, e := json.Marshal(b.Binding)
	if e != nil {
		return e
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET recovery_required=1 WHERE operation_id=? AND state='running' AND lease_owner=? AND fencing_token=? AND attempt=? AND lease_until_us>`+s.nowMicros()), c.Work.ID, c.LeaseOwner, c.Operation.FencingToken, c.Operation.Attempt)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return jobsdb.ErrFencingConflict
		}
		_, e = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_image_bindings(binding_id,project_id,target_id,provider_digest,import_operation_id,binding_json,opaque_binding) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`), b.ID, b.ProjectID, b.TargetID, b.ProviderDigest, b.ImportOperationID, string(raw), b.Binding.OpaqueBinding)
		return e
	})
}
func (s *Store) GetImageBinding(ctx context.Context, id string) (b app.StoredImageBinding, e error) {
	var raw string
	var approved, retired int
	e = s.db.QueryRowContext(ctx, s.bind(`SELECT binding_id,project_id,target_id,provider_digest,import_operation_id,binding_json,opaque_binding,approved,retired FROM labcentral_image_bindings WHERE binding_id=?`), id).Scan(&b.ID, &b.ProjectID, &b.TargetID, &b.ProviderDigest, &b.ImportOperationID, &raw, &b.Binding.OpaqueBinding, &approved, &retired)
	if e == sql.ErrNoRows {
		return b, app.ErrNotFound
	}
	if e != nil {
		return b, e
	}
	e = json.Unmarshal([]byte(raw), &b.Binding)
	b.Approved = approved == 1
	b.Retired = retired == 1
	return
}
func (s *Store) SetImageBindingApproval(ctx context.Context, id string, actor domain.UserID, approved, retired bool, ids app.GovernanceRecordIDs) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		var op domain.OperationID
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT import_operation_id FROM labcentral_image_bindings WHERE binding_id=?`), id).Scan(&op); e != nil {
			return e
		}
		w, e := scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), op))
		if e != nil {
			return e
		}
		if err := s.requireImageAdministratorTx(ctx, tx, actor); err != nil {
			return err
		}

		a, r := 0, 0
		if approved {
			a = 1
		}
		if retired {
			r = 1
			a = 0
		}
		res, e := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_image_bindings SET approved=?,retired=? WHERE binding_id=? AND (retired=0 OR ?=1)`), a, r, id, r)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("retired image binding cannot be reactivated")
		}
		w.ActorID = actor
		return s.appendExtensionGovernance(ctx, tx, w, ids, "image-binding.policy-updated")
	})
}

func (s *Store) requireImageAdministratorTx(ctx context.Context, tx *sql.Tx, actor domain.UserID) error {
	var enabled int
	if actor != app.LocalAdminID {
		if e := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_role_assignments r JOIN labcentral_users u ON u.user_id=r.subject_id WHERE u.user_id=? AND u.disabled=0 AND r.role='platform-administrator' AND r.scope_kind='platform' AND r.subject_kind='user' AND r.valid_from_us<=`+s.nowMicros()+` AND (r.valid_until_us=0 OR r.valid_until_us>`+s.nowMicros()+`)`), actor).Scan(&enabled); e != nil {
			return e
		}
		if enabled == 0 {
			return app.ErrAuthorityChanged
		}
	}
	return nil
}
