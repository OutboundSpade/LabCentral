package jobsdb

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

// EnqueueWorkTx joins the caller's authorization, idempotency, audit and outbox
// transaction. Every child external invocation has its own operation row.
func (s *Store) EnqueueWorkTx(ctx context.Context, tx *sql.Tx, operation domain.Operation, payload []byte) error {
	if tx == nil || operation.Subject.Kind == domain.WorkInstance || operation.InstanceID != "" || operation.Generation != 1 || len(payload) == 0 || len(payload) > 1<<20 {
		return fmt.Errorf("invalid non-instance operation")
	}
	if err := operation.Subject.Validate(); err != nil {
		return err
	}
	if _, err := domain.NewOperationID(string(operation.ID)); err != nil {
		return err
	}
	aggregate := string(operation.Subject.Kind) + ":" + operation.Subject.ID
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_job_aggregates (aggregate_id,phase,resource_version,operation_state,instance_phase) VALUES (?,'queued',0,'queued','') ON CONFLICT (aggregate_id) DO NOTHING`), aggregate); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_jobs (id,operation_id,instance_id,aggregate_id,state,desired_generation,payload_version,payload,available_at_us,subject_kind,subject_id) VALUES (?,?,'',?,'queued',1,1,?,`+s.nowMicros()+`,?,?)`), operation.ID, operation.ID, aggregate, payload, operation.Subject.Kind, operation.Subject.ID)
	return err
}
func (s *Store) ClaimWorkTx(ctx context.Context, tx *sql.Tx, owner string, duration time.Duration, kind domain.WorkSubjectKind, operations []string) (ClaimedJob, error) {
	if tx == nil || kind == domain.WorkInstance || len(operations) == 0 || len(operations) > 1024 {
		return ClaimedJob{}, ErrNoClaimableJob
	}
	return s.claimJob(ctx, tx, owner, duration, operations, true, kind)
}
