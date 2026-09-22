package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Store) GetDeploymentSettings(ctx context.Context, id domain.ProviderTargetID) (application.DeploymentSettings, error) {
	var result application.DeploymentSettings
	err := s.readDB().QueryRowContext(ctx, s.bind("SELECT maximum,resource_version FROM labcentral_target_deployment_settings WHERE target_id=?"), id).Scan(&result.MaxConcurrentDeployments, &result.ResourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		err = application.ErrNotFound
	}
	return result, err
}
func (s *Store) PutDeploymentSettings(ctx context.Context, id domain.ProviderTargetID, input application.DeploymentSettings) (application.DeploymentSettings, error) {
	if _, err := domain.NewProviderTargetID(string(id)); err != nil {
		return application.DeploymentSettings{}, err
	}
	if input.MaxConcurrentDeployments < 1 || input.MaxConcurrentDeployments > 16 {
		return application.DeploymentSettings{}, fmt.Errorf("deployment limit must be 1–16")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.DeploymentSettings{}, err
	}
	defer tx.Rollback()
	if err := s.lockTargetAdmissionTx(ctx, tx, id); err != nil {
		return application.DeploymentSettings{}, err
	}
	var result sql.Result
	if input.ResourceVersion == 0 {
		result, err = tx.ExecContext(ctx, s.bind("INSERT INTO labcentral_target_deployment_settings (target_id,maximum,resource_version) VALUES (?,?,1) ON CONFLICT DO NOTHING"), id, input.MaxConcurrentDeployments)
	} else {
		result, err = tx.ExecContext(ctx, s.bind("UPDATE labcentral_target_deployment_settings SET maximum=?,resource_version=resource_version+1 WHERE target_id=? AND resource_version=?"), input.MaxConcurrentDeployments, id, input.ResourceVersion)
	}
	if err != nil {
		return application.DeploymentSettings{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return application.DeploymentSettings{}, err
	}
	if n != 1 {
		return application.DeploymentSettings{}, application.ErrVersionConflict
	}
	input.ResourceVersion++
	if err = s.appendAdministratorGovernanceTx(ctx, tx, application.ScopePlatform, "platform", "target.deployment_limit.updated", "target.deployment_limit.updated", "provider_target", string(id), input.ResourceVersion, fmt.Sprint(input.MaxConcurrentDeployments)); err != nil {
		return application.DeploymentSettings{}, err
	}
	return input, tx.Commit()
}

// Settings changes and claims share a lock so a reduced limit applies to the
// next admission across replicas. Existing deployments retain their slots.
func (s *Store) lockTargetAdmissionTx(ctx context.Context, tx *sql.Tx, id domain.ProviderTargetID) error {
	if s.dialect != jobsdb.DialectPostgreSQL {
		return nil
	}
	key := sha256.Sum256([]byte("labcentral.target-admission:" + string(id)))
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(key[:8])))
	return err
}
