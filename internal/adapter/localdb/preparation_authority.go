package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func validExtensionVersion(r app.ExtensionWorkRequest) bool {
	if r.Version == 1 {
		return r.PreparationID == "" && (r.Preparation == nil || (r.Preparation.Version < 2 && r.Preparation.ValidateVersion() == nil))
	}
	if r.Version != 2 {
		return false
	}
	if r.Kind == domain.WorkPreparation {
		return r.PreparationID == "" && r.Preparation != nil && r.Preparation.Version == 2 && r.Preparation.ValidateVersion() == nil && r.Build == nil && r.Import == nil
	}
	if r.Kind == domain.WorkImageImport && r.Import != nil && r.Import.RetireBindingID != "" {
		return r.PreparationID == ""
	}
	return r.PreparationID != "" && ((r.Kind == domain.WorkBuild && r.Build != nil && r.Import == nil) || (r.Kind == domain.WorkImageImport && r.Import != nil && r.Build == nil)) && r.Preparation == nil
}
func (s *Store) preparationTx(ctx context.Context, tx *sql.Tx, id domain.OperationID) (app.ExtensionWork, error) {
	return scanExtensionWork(tx.QueryRowContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.operation_id=?`), id))
}
func (s *Store) requirePreparationAuthorityTx(ctx context.Context, tx *sql.Tx, w app.ExtensionWork) error {
	r := w.Request.Preparation
	if r == nil || r.Version != 2 || r.ValidateVersion() != nil || w.CancelRequested || (w.State != "" && w.State != domain.OperationQueued && w.State != domain.OperationRunning) {
		return app.ErrAuthorityChanged
	}
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_template_revisions r JOIN labcentral_lab_templates t ON t.template_id=r.template_id WHERE r.revision_id=? AND t.template_id=? AND t.project_id=?`), r.RevisionID, w.Request.TemplateID, w.ProjectID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return app.ErrAuthorityChanged
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return err
	}
	owner := r.EffectiveOwner(w.ActorID)
	return s.requireInstanceOwnerAuthorizationTx(ctx, tx, app.InstanceRecord{ProjectID: w.ProjectID, ProviderTargetID: r.TargetID, OwnerKind: owner.Kind, OwnerID: owner.ID}, w.ActorID, now)
}
func (s *Store) requirePreparedChildTx(ctx context.Context, tx *sql.Tx, w app.ExtensionWork) error {
	parent, err := s.preparationTx(ctx, tx, w.Request.PreparationID)
	if err != nil {
		return err
	}
	if parent.ActorID != w.ActorID || parent.ProjectID != w.ProjectID || parent.Request.TemplateID != w.Request.TemplateID {
		return app.ErrAuthorityChanged
	}
	if err = s.requirePreparationAuthorityTx(ctx, tx, parent); err != nil {
		return err
	}
	if w.Request.Build != nil {
		if w.Request.Build.RevisionID != parent.Request.Preparation.RevisionID {
			return app.ErrAuthorityChanged
		}
	} else if w.Request.Import != nil {
		if w.Request.Import.TargetID != parent.Request.Preparation.TargetID {
			return app.ErrAuthorityChanged
		}
		var linked int
		err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_extension_dependencies WHERE parent_id=? AND child_id=?`), parent.ID, w.Request.Import.BuildID).Scan(&linked)
		if err != nil {
			return err
		}
		if linked != 1 {
			return app.ErrAuthorityChanged
		}
	} else {
		return app.ErrAuthorityChanged
	}
	return nil
}
func (s *Store) linkPreparationTx(ctx context.Context, tx *sql.Tx, parent, child domain.OperationID) error {
	if parent == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_extension_dependencies(parent_id,child_id) VALUES(?,?) ON CONFLICT DO NOTHING`), parent, child)
	return err
}
func (s *Store) AuthorizePreparationWork(ctx context.Context, id domain.OperationID) (actor domain.UserID, err error) {
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		w, e := s.preparationTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if w.Request.Kind == domain.WorkPreparation {
			actor = w.ActorID
			return s.requirePreparationAuthorityTx(ctx, tx, w)
		}
		rows, e := tx.QueryContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_dependencies d JOIN labcentral_extension_work w ON w.operation_id=d.parent_id JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE d.child_id=? AND j.state IN ('queued','running') AND w.cancel_requested=0 ORDER BY w.operation_id LIMIT 1025`), id)
		if e != nil {
			return e
		}
		parents := []app.ExtensionWork{}
		for rows.Next() {
			p, e := scanExtensionWork(rows)
			if e != nil {
				rows.Close()
				return e
			}
			parents = append(parents, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(parents) > 1024 {
			return fmt.Errorf("preparation waiter limit")
		}
		for _, p := range parents {
			if p.ProjectID != w.ProjectID || p.Request.Preparation == nil || p.Request.Preparation.Version != 2 {
				continue
			}
			e = s.requirePreparationAuthorityTx(ctx, tx, p)
			if e == nil {
				actor = p.ActorID
				return nil
			}
			if !errors.Is(e, app.ErrAuthorityChanged) {
				return e
			}
		}
		return app.ErrAuthorityChanged
	})
	return
}

func (s *Store) CanReadPreparationWork(ctx context.Context, id domain.OperationID, actor domain.UserID) (bool, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id WHERE w.actor_id=? AND (w.operation_id=? OR EXISTS(SELECT 1 FROM labcentral_extension_dependencies d WHERE d.parent_id=w.operation_id AND d.child_id=?)) LIMIT 1025`), actor, id, id)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		w, e := scanExtensionWork(rows)
		if e != nil {
			return false, e
		}
		if w.Request.Preparation != nil && w.Request.Preparation.Version == 2 {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) requirePreparationAdmissionTx(ctx context.Context, tx *sql.Tx, r app.CreateInstanceRequest) error {
	w, err := s.preparationTx(ctx, tx, r.WorkFence.OperationID)
	if err != nil {
		return err
	}
	if w.Request.Version != 2 {
		return nil
	}
	if err = s.requirePreparationAuthorityTx(ctx, tx, w); err != nil {
		return err
	}
	p := w.Request.Preparation
	owner := p.EffectiveOwner(w.ActorID)
	if r.ProjectID != w.ProjectID || r.CreatedBy != w.ActorID || r.SubjectID != w.ActorID || r.RevisionID != p.RevisionID || r.ProviderTargetID != p.TargetID || r.OwnerKind != owner.Kind || r.OwnerID != owner.ID {
		return app.ErrAuthorityChanged
	}
	return nil
}
