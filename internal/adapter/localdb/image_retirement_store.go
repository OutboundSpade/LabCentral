package localdb

import (
	"context"
	"database/sql"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Store) imageRetirementReadyTx(ctx context.Context, tx *sql.Tx, c app.ExtensionWorkClaim, id string) (bool, domain.OperationID, error) {
	if c.Work.Request.Version != 2 || c.Work.Request.Import == nil || c.Work.Request.Import.RetireBindingID != id {
		return false, "", fmt.Errorf("retirement work binding mismatch")
	}
	if err := s.requireExtensionFenceTx(ctx, tx, app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt}); err != nil {
		return false, "", err
	}
	var operation domain.OperationID
	var retired int
	var project domain.ProjectID
	var target domain.ProviderTargetID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT import_operation_id,retired,project_id,target_id FROM labcentral_image_bindings WHERE binding_id=?`), id).Scan(&operation, &retired, &project, &target); err != nil {
		return false, "", err
	}
	if retired != 1 || project != c.Work.ProjectID || target != c.Work.Request.Import.TargetID {
		return false, "", fmt.Errorf("binding must be retired in its original project and target")
	}
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_instance_image_bindings ref JOIN labcentral_local_instances i ON i.instance_id=ref.instance_id WHERE ref.binding_id=? AND i.phase<>'destroyed'`), id).Scan(&count); err != nil {
		return false, "", err
	}
	return count == 0, operation, nil
}
func (s *Store) CanRetireImage(ctx context.Context, c app.ExtensionWorkClaim, id string) (ready bool, err error) {
	err = s.workTx(ctx, func(tx *sql.Tx) error { var e error; ready, _, e = s.imageRetirementReadyTx(ctx, tx, c, id); return e })
	return
}
func (s *Store) ReleaseImageImportClaims(ctx context.Context, c app.ExtensionWorkClaim, id string) error {
	return s.workTx(ctx, func(tx *sql.Tx) error {
		ready, operation, err := s.imageRetirementReadyTx(ctx, tx, c, id)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("image still has retained deployment references")
		}
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims SET state='released',updated_at_us=`+s.nowMicros()+` WHERE instance_id='' AND resource_id=? AND state<>'released'`), operation)
		return err
	})
}
