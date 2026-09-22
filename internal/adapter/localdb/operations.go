package localdb

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func (s *Store) ReadOperations(ctx context.Context, q application.OperationQuery) (application.OperationPage, error) {
	page := application.OperationPage{Items: []application.OperationRecord{}}
	if q.Limit == 0 || q.Limit > 200 {
		return page, fmt.Errorf("invalid operation page size")
	}
	where := ` WHERE (? = '' OR instance.project_id = ?)`
	base := ` FROM labcentral_local_operations operation JOIN labcentral_jobs job ON job.operation_id = operation.operation_id JOIN labcentral_local_instances instance ON instance.instance_id = operation.instance_id`
	if err := s.readDB().QueryRowContext(ctx, s.bind(`SELECT COALESCE(SUM(CASE WHEN job.state='queued' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN job.state='running' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN job.state IN ('failed','partial','timed_out') THEN 1 ELSE 0 END),0)`+base+where), string(q.ProjectID), string(q.ProjectID)).Scan(&page.Queued, &page.Running, &page.Failed); err != nil {
		return page, err
	}
	switch q.Filter {
	case "active":
		where += ` AND job.state IN ('queued','running')`
	case "failed":
		where += ` AND job.state IN ('failed','partial','timed_out')`
	case "all":
	default:
		return page, fmt.Errorf("invalid operation filter")
	}
	args := []any{string(q.ProjectID), string(q.ProjectID)}
	if !q.AfterTime.IsZero() {
		where += ` AND (operation.created_at_us > ? OR (operation.created_at_us = ? AND operation.operation_id > ?))`
		args = append(args, q.AfterTime.UnixMicro(), q.AfterTime.UnixMicro(), string(q.AfterID))
	}
	args = append(args, int(q.Limit)+1)
	rows, err := s.readDB().QueryContext(ctx, s.bind(`SELECT operation.operation_id, operation.instance_id, operation.actor_id, operation.kind, job.state, job.desired_generation, job.attempt, operation.message, operation.error_code, operation.created_at_us, operation.updated_at_us`+base+where+` ORDER BY operation.created_at_us, operation.operation_id LIMIT ?`), args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var r application.OperationRecord
		var gen, attempt, created, updated int64
		if err := rows.Scan(&r.ID, &r.InstanceID, &r.ActorID, &r.Kind, &r.State, &gen, &attempt, &r.Message, &r.ErrorCode, &created, &updated); err != nil {
			return page, err
		}
		r.Generation = domain.Generation(gen)
		r.Attempt = uint32(attempt)
		r.CreatedAt = time.UnixMicro(created).UTC()
		r.UpdatedAt = time.UnixMicro(updated).UTC()
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > int(q.Limit) {
		page.HasMore = true
		page.Items = page.Items[:q.Limit]
	}
	return page, nil
}
