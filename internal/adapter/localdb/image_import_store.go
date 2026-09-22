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
	"strings"
)

func imageImportMigration() []string {
	return []string{`CREATE TABLE labcentral_image_import_plans(operation_id TEXT PRIMARY KEY REFERENCES labcentral_extension_work(operation_id),plan_json TEXT NOT NULL)`,
		`CREATE TABLE labcentral_instance_image_bindings(instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),binding_id TEXT NOT NULL REFERENCES labcentral_image_bindings(binding_id),PRIMARY KEY(instance_id,binding_id))`}
}
func (s *Store) GetImageImportPlan(ctx context.Context, id domain.OperationID) (p app.ImageImportPlan, err error) {
	var raw string
	err = s.readDB().QueryRowContext(ctx, s.bind(`SELECT plan_json FROM labcentral_image_import_plans WHERE operation_id=?`), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, app.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	return
}
func (s *Store) ReserveImageImportPlan(ctx context.Context, c app.ExtensionWorkClaim, p app.ImageImportPlan) error {
	if c.Work.Request.Kind != domain.WorkImageImport || c.Work.Request.Import == nil {
		return fmt.Errorf("image import work required")
	}
	p, err := app.ValidateImageImportPlan(p, c.Work.Request.Import.TargetID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		if err := s.requireExtensionFenceTx(ctx, tx, app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt}); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT plan_json FROM labcentral_image_import_plans WHERE operation_id=?`), c.Work.ID).Scan(&existing)
		if err == nil {
			if existing != string(raw) {
				return fmt.Errorf("image import already has an immutable reservation")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Empty instance_id denotes an image, not an invented lab instance. The
		// resource_id is the owning import operation, referenced by the plan table.
		// Real instance reservations require a nonempty domain InstanceID.
		for _, key := range p.Claims {
			result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_provider_allocation_claims(target_id,namespace,claim_value,instance_id,resource_id,plan_digest,state,fencing_token,created_at_us,updated_at_us) VALUES(?,?,?,'',?,?,'committed',?,`+s.nowMicros()+`,`+s.nowMicros()+`) ON CONFLICT(target_id,namespace,claim_value) DO UPDATE SET instance_id='',resource_id=excluded.resource_id,plan_digest=excluded.plan_digest,state='committed',fencing_token=excluded.fencing_token,created_at_us=excluded.created_at_us,updated_at_us=excluded.updated_at_us WHERE labcentral_provider_allocation_claims.state='released'`), key.TargetID, key.Namespace, key.Value, c.Work.ID, p.Digest.Hex(), c.Operation.FencingToken)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return &app.AllocationConflictError{Conflicts: []app.AllocationKey{key}}
			}
		}
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_image_import_plans(operation_id,plan_json) VALUES(?,?)`), c.Work.ID, string(raw))
		return err
	})
}

// validateImportedImagesTx also covers direct YAML/API launches: knowing a
// provider image digest must not bypass approval or project isolation.
func (s *Store) validateImportedImagesTx(ctx context.Context, tx *sql.Tx, request app.CreateInstanceRequest) ([]string, error) {
	required, err := domain.ImportedImageReferences(request.CanonicalJSON)
	if err != nil {
		return nil, err
	}
	ids := append([]string(nil), request.ImageBindingIDs...)
	if len(required) == 0 {
		return ids, nil
	}
	lock := ""
	if s.dialect == jobsdb.DialectPostgreSQL {
		lock = " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT binding_id,binding_json FROM labcentral_image_bindings WHERE target_id=? AND project_id=? AND approved=1 AND retired=0 ORDER BY binding_id LIMIT 4097`+lock), request.ProviderTargetID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	matches := map[domain.ImageRef]string{}
	count := 0
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		var binding app.ImageBinding
		if err = json.Unmarshal([]byte(raw), &binding); err != nil {
			rows.Close()
			return nil, err
		}
		// Import validation has already bound this immutable reference to its
		// provider evidence. For packaged containers it names the OCI manifest,
		// while ArtifactDigest identifies the containing qcow2 disk.
		if strings.HasPrefix(binding.ImageRef, domain.ImportedImageRepository+"/") {
			ref, err := domain.NewImageRef(binding.ImageRef)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("invalid stored image reference")
			}
			matches[ref] = id
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if count > 4096 {
		return nil, fmt.Errorf("image binding lookup exceeds bound")
	}
	for _, digest := range required {
		id, found := matches[digest]
		if !found {
			return nil, fmt.Errorf("imported image requires current project approval")
		}
		ids = append(ids, id)
	}
	return ids, nil
}
