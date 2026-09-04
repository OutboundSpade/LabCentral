package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const createInstanceBatchRouteAction = "create_instance_batch"

func instanceBatchMigrationStatements(integerType string) []string {
	return []string{
		`CREATE TABLE labcentral_instance_batches (
			batch_id TEXT PRIMARY KEY,
			revision_id TEXT NOT NULL REFERENCES labcentral_template_revisions(revision_id),
			project_id TEXT NOT NULL,
			created_by TEXT NOT NULL,
			provider_target_id TEXT NOT NULL,
			item_count ` + integerType + ` NOT NULL CHECK (item_count BETWEEN 1 AND 100),
			created_at_us ` + integerType + ` NOT NULL CHECK (created_at_us > 0)
		)`,
		`CREATE INDEX labcentral_instance_batches_scope_idx
			ON labcentral_instance_batches(project_id, created_at_us, batch_id)`,
		`CREATE TABLE labcentral_instance_batch_items (
			batch_id TEXT NOT NULL REFERENCES labcentral_instance_batches(batch_id),
			ordinal ` + integerType + ` NOT NULL CHECK (ordinal BETWEEN 0 AND 99),
			owner_kind TEXT NOT NULL CHECK (owner_kind IN ('user', 'team')),
			owner_id TEXT NOT NULL,
			instance_id TEXT NOT NULL UNIQUE REFERENCES labcentral_local_instances(instance_id),
			operation_id TEXT NOT NULL UNIQUE REFERENCES labcentral_local_operations(operation_id),
			PRIMARY KEY (batch_id, ordinal),
			UNIQUE (batch_id, owner_kind, owner_id)
		)`,
		`CREATE INDEX labcentral_instance_batch_items_owner_idx
			ON labcentral_instance_batch_items(owner_kind, owner_id, batch_id)`,
	}
}

func (s *Store) CreateInstanceBatch(ctx context.Context, request controlplane.CreateInstanceBatchRequest) (controlplane.InstanceBatchRecord, error) {
	if err := validateCreateInstanceBatch(request); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.createInstanceBatch(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.InstanceBatchRecord{}, err
		}
	}
}

func (s *Store) createInstanceBatch(ctx context.Context, request controlplane.CreateInstanceBatchRequest) (controlplane.InstanceBatchRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("begin instance batch creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.CreatedBy, createInstanceBatchRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.CreatedBy, createInstanceBatchRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	} else if found {
		record, err := s.getInstanceBatchTx(ctx, tx, domain.InstanceBatchID(existing))
		if err != nil {
			return controlplane.InstanceBatchRecord{}, fmt.Errorf("load replayed instance batch: %w", err)
		}
		record.Replay = true
		return record, tx.Commit()
	}

	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_instance_batches
		(batch_id, revision_id, project_id, created_by, provider_target_id, item_count, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), request.ID, request.RevisionID, request.ProjectID,
		request.CreatedBy, request.TargetID, len(request.Instances), now.UnixMicro()); err != nil {
		if isUniqueViolation(err) {
			return controlplane.InstanceBatchRecord{}, controlplane.ErrAlreadyExists
		}
		if isForeignKeyViolation(err) {
			return controlplane.InstanceBatchRecord{}, controlplane.ErrNotFound
		}
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("insert instance batch: %w", err)
	}

	// A deterministic owner order prevents two overlapping classroom batches
	// from acquiring owner-specific quota locks in opposite orders.
	order := make([]int, len(request.Instances))
	for index := range request.Instances {
		order[index] = index
	}
	sort.Slice(order, func(left, right int) bool {
		leftRequest, rightRequest := request.Instances[order[left]], request.Instances[order[right]]
		leftKey := string(leftRequest.OwnerKind) + "\x00" + leftRequest.OwnerID
		rightKey := string(rightRequest.OwnerKind) + "\x00" + rightRequest.OwnerID
		return leftKey < rightKey
	})
	for _, index := range order {
		instance := request.Instances[index]
		if err := s.requireBatchCreateAuthorizationTx(ctx, tx, instance, now); err != nil {
			return controlplane.InstanceBatchRecord{}, err
		}
		if err := s.insertInstanceTx(ctx, tx, instance); err != nil {
			return controlplane.InstanceBatchRecord{}, err
		}
	}

	items := make([]controlplane.InstanceBatchItem, 0, len(request.Instances))
	for index, instance := range request.Instances {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_instance_batch_items
			(batch_id, ordinal, owner_kind, owner_id, instance_id, operation_id)
			VALUES (?, ?, ?, ?, ?, ?)`), request.ID, index, instance.OwnerKind, instance.OwnerID,
			instance.InstanceID, instance.OperationID); err != nil {
			return controlplane.InstanceBatchRecord{}, fmt.Errorf("insert instance batch item: %w", err)
		}
		items = append(items, controlplane.InstanceBatchItem{
			Ordinal: uint32(index), OwnerKind: instance.OwnerKind, OwnerID: instance.OwnerID,
			InstanceID: instance.InstanceID, OperationID: instance.OperationID,
		})
	}
	if err := s.appendInstanceBatchGovernanceTx(ctx, tx, request); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.CreatedBy, createInstanceBatchRouteAction, request.IdempotencyKey, request.RequestDigest, string(request.ID)); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("commit instance batch creation: %w", err)
	}
	return controlplane.InstanceBatchRecord{
		ID: request.ID, RevisionID: request.RevisionID, ProjectID: request.ProjectID,
		CreatedBy: request.CreatedBy, ProviderTargetID: request.TargetID, Items: items, CreatedAt: now,
	}, nil
}

func (s *Store) GetInstanceBatch(ctx context.Context, id domain.InstanceBatchID) (controlplane.InstanceBatchRecord, error) {
	if _, err := domain.NewInstanceBatchID(string(id)); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	record, err := s.getInstanceBatch(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.InstanceBatchRecord{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("get instance batch: %w", err)
	}
	return record, nil
}

func (s *Store) getInstanceBatchTx(ctx context.Context, tx *sql.Tx, id domain.InstanceBatchID) (controlplane.InstanceBatchRecord, error) {
	return s.getInstanceBatch(ctx, tx, id)
}

type instanceBatchQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) getInstanceBatch(ctx context.Context, queryer instanceBatchQueryer, id domain.InstanceBatchID) (controlplane.InstanceBatchRecord, error) {
	var record controlplane.InstanceBatchRecord
	var createdAt int64
	var itemCount int
	err := queryer.QueryRowContext(ctx, s.bind(`SELECT batch_id, revision_id, project_id, created_by,
		provider_target_id, item_count, created_at_us FROM labcentral_instance_batches WHERE batch_id = ?`), id).Scan(
		&record.ID, &record.RevisionID, &record.ProjectID, &record.CreatedBy,
		&record.ProviderTargetID, &itemCount, &createdAt,
	)
	if err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if itemCount < 1 || itemCount > controlplane.MaximumInstanceBatchSize || createdAt <= 0 {
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("stored instance batch is invalid")
	}
	rows, err := queryer.QueryContext(ctx, s.bind(`SELECT ordinal, owner_kind, owner_id, instance_id, operation_id
		FROM labcentral_instance_batch_items WHERE batch_id = ? ORDER BY ordinal`), id)
	if err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	defer rows.Close()
	record.Items = make([]controlplane.InstanceBatchItem, 0, itemCount)
	for rows.Next() {
		var item controlplane.InstanceBatchItem
		if err := rows.Scan(&item.Ordinal, &item.OwnerKind, &item.OwnerID, &item.InstanceID, &item.OperationID); err != nil {
			return controlplane.InstanceBatchRecord{}, err
		}
		record.Items = append(record.Items, item)
	}
	if err := rows.Err(); err != nil {
		return controlplane.InstanceBatchRecord{}, err
	}
	if len(record.Items) != itemCount {
		return controlplane.InstanceBatchRecord{}, fmt.Errorf("stored instance batch item count is inconsistent")
	}
	record.CreatedAt = time.UnixMicro(createdAt).UTC()
	return record, nil
}

func (s *Store) InstanceBatchAccess(ctx context.Context, id domain.InstanceBatchID) (application.InstanceBatchAccessRecord, error) {
	if _, err := domain.NewInstanceBatchID(string(id)); err != nil {
		return application.InstanceBatchAccessRecord{}, application.ErrAuthRecordNotFound
	}
	access := application.InstanceBatchAccessRecord{BatchID: id}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT project_id, provider_target_id
		FROM labcentral_instance_batches WHERE batch_id = ?`), id).Scan(&access.ProjectID, &access.TargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return application.InstanceBatchAccessRecord{}, application.ErrAuthRecordNotFound
	}
	if err != nil {
		return application.InstanceBatchAccessRecord{}, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT owner_kind, owner_id
		FROM labcentral_instance_batch_items WHERE batch_id = ? ORDER BY ordinal`), id)
	if err != nil {
		return application.InstanceBatchAccessRecord{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var owner application.InstanceBatchOwnerAccess
		if err := rows.Scan(&owner.OwnerKind, &owner.OwnerID); err != nil {
			return application.InstanceBatchAccessRecord{}, err
		}
		access.Owners = append(access.Owners, owner)
	}
	if err := rows.Err(); err != nil {
		return application.InstanceBatchAccessRecord{}, err
	}
	if len(access.Owners) == 0 {
		return application.InstanceBatchAccessRecord{}, fmt.Errorf("stored instance batch has no access owners")
	}
	return access, nil
}

func validateCreateInstanceBatch(request controlplane.CreateInstanceBatchRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewInstanceBatchID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewRevisionID(string(request.RevisionID)); err != nil {
		return err
	}
	if _, err := domain.NewProjectID(string(request.ProjectID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.CreatedBy)); err != nil {
		return err
	}
	if _, err := domain.NewProviderTargetID(string(request.TargetID)); err != nil {
		return err
	}
	if err := validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest); err != nil {
		return err
	}
	if len(request.Instances) < 1 || len(request.Instances) > controlplane.MaximumInstanceBatchSize {
		return fmt.Errorf("instance batch must contain 1-%d items", controlplane.MaximumInstanceBatchSize)
	}
	owners, instanceIDs, operationIDs := map[string]bool{}, map[domain.InstanceID]bool{}, map[domain.OperationID]bool{}
	for _, instance := range request.Instances {
		if err := validateCreate(instance); err != nil {
			return err
		}
		if instance.RevisionID != request.RevisionID || instance.ProjectID != request.ProjectID ||
			instance.CreatedBy != request.CreatedBy || instance.SubjectID != request.CreatedBy ||
			instance.ProviderTargetID != request.TargetID {
			return fmt.Errorf("instance batch item does not match its batch binding")
		}
		if instance.OwnerKind != application.OwnerUser && instance.OwnerKind != application.OwnerTeam {
			return fmt.Errorf("instance batch owner must be a user or team")
		}
		ownerKey := string(instance.OwnerKind) + "\x00" + instance.OwnerID
		if owners[ownerKey] {
			return fmt.Errorf("instance batch contains a duplicate owner")
		}
		if instanceIDs[instance.InstanceID] || operationIDs[instance.OperationID] {
			return fmt.Errorf("instance batch contains a duplicate generated identity")
		}
		owners[ownerKey], instanceIDs[instance.InstanceID], operationIDs[instance.OperationID] = true, true, true
	}
	return nil
}

func (s *Store) requireBatchCreateAuthorizationTx(ctx context.Context, tx *sql.Tx, instance controlplane.CreateInstanceRequest, now time.Time) error {
	if instance.ProjectID == "project.local" && instance.CreatedBy == controlplane.LocalAdminID {
		return nil
	}
	nowMicros := now.UTC().UnixMicro()
	query := `WITH instance_scope AS (
		SELECT project.organization_id FROM labcentral_projects project WHERE project.project_id = ?
	), active_memberships AS (
		SELECT membership.organization_id, COALESCE(membership.team_id, '') AS team_id
		FROM labcentral_memberships membership
		WHERE membership.user_id = ? AND membership.valid_from_us <= ?
		AND (membership.valid_until_us = 0 OR membership.valid_until_us > ?)
	), eligible_roles AS (
		SELECT 1 FROM labcentral_role_assignments assignment CROSS JOIN instance_scope scope
		WHERE assignment.valid_from_us <= ? AND (assignment.valid_until_us = 0 OR assignment.valid_until_us > ?)
		AND assignment.role IN ('platform-administrator', 'instructor')
		AND ((assignment.subject_kind = 'user' AND assignment.subject_id = ?)
			OR (assignment.subject_kind = 'team' AND EXISTS (
				SELECT 1 FROM active_memberships membership WHERE membership.team_id = assignment.subject_id)))
		AND (assignment.subject_kind <> 'user' OR assignment.scope_kind = 'platform' OR EXISTS (
			SELECT 1 FROM active_memberships membership WHERE membership.organization_id = scope.organization_id))
		AND ((assignment.scope_kind = 'platform' AND assignment.scope_id = 'platform')
			OR (assignment.scope_kind = 'organization' AND assignment.scope_id = scope.organization_id)
			OR (assignment.scope_kind = 'project' AND assignment.scope_id = ?)
			OR (assignment.scope_kind = 'team' AND ? = 'team' AND assignment.scope_id = ?
				AND EXISTS (SELECT 1 FROM active_memberships membership WHERE membership.team_id = ?)))
		LIMIT 1
	), eligible_grants AS (
		SELECT 1 FROM labcentral_provider_target_grants grant_record CROSS JOIN instance_scope scope
		WHERE grant_record.target_id = ? AND grant_record.valid_from_us <= ?
		AND (grant_record.valid_until_us = 0 OR grant_record.valid_until_us > ?)
		AND ((grant_record.scope_kind = 'platform' AND grant_record.scope_id = 'platform')
			OR (grant_record.scope_kind = 'organization' AND grant_record.scope_id = scope.organization_id)
			OR (grant_record.scope_kind = 'project' AND grant_record.scope_id = ?)
			OR (grant_record.scope_kind = 'team' AND ? = 'team' AND grant_record.scope_id = ?
				AND EXISTS (SELECT 1 FROM active_memberships membership WHERE membership.team_id = ?)))
		LIMIT 1
	)
	SELECT CASE WHEN EXISTS (SELECT 1 FROM labcentral_users user_record
		WHERE user_record.user_id = ? AND user_record.disabled = 0)
		AND EXISTS (SELECT 1 FROM eligible_roles) AND EXISTS (SELECT 1 FROM eligible_grants)
		THEN 1 ELSE 0 END`
	var allowed int
	err := tx.QueryRowContext(ctx, s.bind(query),
		instance.ProjectID, instance.CreatedBy, nowMicros, nowMicros,
		nowMicros, nowMicros, instance.CreatedBy, instance.ProjectID,
		instance.OwnerKind, instance.OwnerID, instance.OwnerID,
		instance.ProviderTargetID, nowMicros, nowMicros, instance.ProjectID,
		instance.OwnerKind, instance.OwnerID, instance.OwnerID,
		instance.CreatedBy,
	).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("recheck instance batch authority: %w", err)
	}
	if allowed != 1 {
		return controlplane.ErrAuthorityChanged
	}
	return nil
}

type instanceBatchGovernancePayload struct {
	BatchID        domain.InstanceBatchID  `json:"batchId"`
	RevisionID     domain.RevisionID       `json:"revisionId"`
	ProviderTarget domain.ProviderTargetID `json:"providerTargetId"`
	InstanceCount  int                     `json:"instanceCount"`
}

func (s *Store) appendInstanceBatchGovernanceTx(ctx context.Context, tx *sql.Tx, request controlplane.CreateInstanceBatchRequest) error {
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), request.ProjectID).Scan(&organizationID); err != nil {
		return fmt.Errorf("load instance batch governance scope: %w", err)
	}
	encoded, err := json.Marshal(instanceBatchGovernancePayload{
		BatchID: request.ID, RevisionID: request.RevisionID, ProviderTarget: request.TargetID, InstanceCount: len(request.Instances),
	})
	if err != nil {
		return err
	}
	redacted, err := application.NewRedactedAuditPayload(encoded)
	if err != nil {
		return err
	}
	safe, err := application.NewSafeEventPayload(encoded)
	if err != nil {
		return err
	}
	scope := application.RecordScope{OrganizationID: organizationID, ProjectID: request.ProjectID}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: string(request.CreatedBy)}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: request.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditOperation,
		Action: "instance.batch.create", Outcome: application.AuditAccepted, Scope: scope, Actor: actor,
		AggregateType: "lab-instance-batch", AggregateID: string(request.ID), RedactedPayload: redacted,
	}); err != nil {
		return fmt.Errorf("append instance batch audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: request.EventID, SchemaVersion: "event.labcentral.io/v1", Type: "lab.instance-batch.accepted",
		Scope: scope, Actor: actor, AggregateType: "lab-instance-batch", AggregateID: string(request.ID), SafePayload: safe,
	}); err != nil {
		return fmt.Errorf("append instance batch event: %w", err)
	}
	return nil
}
