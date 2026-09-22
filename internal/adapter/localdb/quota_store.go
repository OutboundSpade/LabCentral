package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const (
	putQuotaLimitRouteAction     = "put_quota_limit"
	putLifetimePolicyRouteAction = "put_lifetime_policy"
	renewInstanceTTLRouteAction  = "renew_instance_ttl"
	defaultQuotaReservationHold  = 24 * time.Hour
	maximumExpirationSweepBatch  = uint32(1000)
)

func (s *Store) quotaMigrationStatements() []string {
	integerType := s.integerType()
	statements := []string{
		`CREATE TABLE labcentral_governance_idempotency_results (
			subject_id TEXT NOT NULL,
			route_action TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			result_payload ` + s.blobType() + ` NOT NULL,
			PRIMARY KEY (subject_id, route_action, idempotency_key),
			FOREIGN KEY (subject_id, route_action, idempotency_key)
				REFERENCES labcentral_idempotency_records(subject_id, route_action, idempotency_key) ON DELETE CASCADE
		)`,
		`CREATE TABLE labcentral_quota_limits (
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('platform', 'organization', 'project', 'team', 'user', 'target')),
			scope_id TEXT NOT NULL CHECK (length(scope_id) BETWEEN 1 AND 128),
			organization_id TEXT NOT NULL DEFAULT '' CHECK (length(organization_id) <= 128),
			dimension TEXT NOT NULL CHECK (dimension IN ('instances', 'vms', 'containers', 'cpu_millis', 'memory_bytes', 'storage_bytes', 'public_ips', 'published_ports', 'snapshots')),
			limit_value ` + integerType + ` NOT NULL CHECK (limit_value >= 0),
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			updated_by TEXT NOT NULL,
			created_at_us ` + integerType + ` NOT NULL CHECK (created_at_us > 0),
			updated_at_us ` + integerType + ` NOT NULL CHECK (updated_at_us > 0),
			PRIMARY KEY (scope_kind, scope_id, organization_id, dimension)
		)`,
		`CREATE INDEX labcentral_quota_limits_scope_idx
			ON labcentral_quota_limits(organization_id, scope_kind, scope_id, dimension)`,
		`CREATE TABLE labcentral_quota_usage (
			scope_kind TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			organization_id TEXT NOT NULL DEFAULT '',
			dimension TEXT NOT NULL,
			reserved_value ` + integerType + ` NOT NULL DEFAULT 0 CHECK (reserved_value >= 0),
			committed_value ` + integerType + ` NOT NULL DEFAULT 0 CHECK (committed_value >= 0),
			retained_value ` + integerType + ` NOT NULL DEFAULT 0 CHECK (retained_value >= 0),
			updated_at_us ` + integerType + ` NOT NULL CHECK (updated_at_us > 0),
			PRIMARY KEY (scope_kind, scope_id, organization_id, dimension),
			FOREIGN KEY (scope_kind, scope_id, organization_id, dimension)
				REFERENCES labcentral_quota_limits(scope_kind, scope_id, organization_id, dimension)
		)`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN owner_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN target_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN created_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_quota_reservations ADD COLUMN updated_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`CREATE UNIQUE INDEX labcentral_quota_reservations_instance_idx
			ON labcentral_quota_reservations(instance_id) WHERE instance_id <> ''`,
		`CREATE INDEX labcentral_quota_reservations_reconcile_idx
			ON labcentral_quota_reservations(reconciliation_required, state, expires_at_us, reservation_id)`,
		`CREATE TABLE labcentral_quota_reservation_lines (
			reservation_id TEXT NOT NULL REFERENCES labcentral_quota_reservations(reservation_id) ON DELETE CASCADE,
			scope_kind TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			organization_id TEXT NOT NULL DEFAULT '',
			dimension TEXT NOT NULL,
			amount ` + integerType + ` NOT NULL CHECK (amount > 0),
			PRIMARY KEY (reservation_id, scope_kind, scope_id, organization_id, dimension),
			FOREIGN KEY (scope_kind, scope_id, organization_id, dimension)
				REFERENCES labcentral_quota_limits(scope_kind, scope_id, organization_id, dimension)
		)`,
		`CREATE INDEX labcentral_quota_reservation_lines_scope_idx
			ON labcentral_quota_reservation_lines(scope_kind, scope_id, organization_id, dimension, reservation_id)`,
		`CREATE TABLE labcentral_lifetime_policies (
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('platform', 'organization', 'project', 'team', 'user', 'target')),
			scope_id TEXT NOT NULL CHECK (length(scope_id) BETWEEN 1 AND 128),
			organization_id TEXT NOT NULL DEFAULT '' CHECK (length(organization_id) <= 128),
			default_ttl_seconds ` + integerType + ` NOT NULL CHECK (default_ttl_seconds > 0),
			maximum_ttl_seconds ` + integerType + ` NOT NULL CHECK (maximum_ttl_seconds > 0),
			renewal_window_seconds ` + integerType + ` NOT NULL CHECK (renewal_window_seconds >= 0),
			warning_before_seconds ` + integerType + ` NOT NULL CHECK (warning_before_seconds >= 0),
			destruction_grace_seconds ` + integerType + ` NOT NULL CHECK (destruction_grace_seconds >= 0),
			renewal_allowed ` + integerType + ` NOT NULL CHECK (renewal_allowed IN (0, 1)),
			persistent_allowed ` + integerType + ` NOT NULL CHECK (persistent_allowed IN (0, 1)),
			resource_version ` + integerType + ` NOT NULL DEFAULT 1 CHECK (resource_version > 0),
			updated_by TEXT NOT NULL,
			created_at_us ` + integerType + ` NOT NULL CHECK (created_at_us > 0),
			updated_at_us ` + integerType + ` NOT NULL CHECK (updated_at_us > 0),
			PRIMARY KEY (scope_kind, scope_id, organization_id)
		)`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN expires_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN maximum_expires_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN expiration_warning_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN expiration_warning_emitted_at_us ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN expiration_grace_seconds ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN lifetime_renewal_allowed ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN lifetime_renewal_window_seconds ` + integerType + ` NOT NULL DEFAULT 0`,
		`ALTER TABLE labcentral_local_instances ADD COLUMN lifetime_policy_digest TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX labcentral_local_instances_expiration_idx
			ON labcentral_local_instances(desired_lifecycle, phase, expires_at_us, instance_id)`,
		`CREATE INDEX labcentral_local_instances_warning_idx
			ON labcentral_local_instances(expiration_warning_emitted_at_us, expiration_warning_at_us, instance_id)`,
	}
	for _, quota := range []struct {
		dimension application.QuotaDimension
		limit     uint64
	}{
		{application.QuotaInstances, 100},
		{application.QuotaVMs, 100},
		{application.QuotaContainers, 100},
		{application.QuotaCPUMillis, 512_000},
		{application.QuotaMemoryBytes, 2 << 40},
		{application.QuotaStorageBytes, 20 << 40},
		{application.QuotaPublicIPs, 100},
		{application.QuotaPublishedPorts, 1_000},
		{application.QuotaSnapshots, 500},
	} {
		statements = append(statements, fmt.Sprintf(`INSERT INTO labcentral_quota_limits
			(scope_kind, scope_id, organization_id, dimension, limit_value, resource_version, updated_by, created_at_us, updated_at_us)
			VALUES ('platform', 'platform', '', '%s', %d, 1, 'system.migration', %s, %s)
			ON CONFLICT (scope_kind, scope_id, organization_id, dimension) DO NOTHING`,
			quota.dimension, quota.limit, s.nowMicros(), s.nowMicros()))
	}
	statements = append(statements,
		`INSERT INTO labcentral_lifetime_policies
			(scope_kind, scope_id, organization_id, default_ttl_seconds, maximum_ttl_seconds,
			 renewal_window_seconds, warning_before_seconds, destruction_grace_seconds, renewal_allowed, persistent_allowed,
			 resource_version, updated_by, created_at_us, updated_at_us)
			VALUES ('platform', 'platform', '', 28800, 86400, 3600, 900, 300, 1, 0, 1, 'system.migration', `+s.nowMicros()+`, `+s.nowMicros()+`)
			ON CONFLICT (scope_kind, scope_id, organization_id) DO NOTHING`,
	)
	return statements
}

type legacyQuotaInstance struct {
	id        domain.InstanceID
	operation domain.OperationID
	phase     domain.InstancePhase
	createdAt int64
}

// backfillLegacyInstanceQuota is deliberately run after every migration check.
// This makes an interrupted upgrade self-healing while the migration lock is
// still held, and prevents pre-0009 operations from reaching a terminal write
// without a quota ledger.
func (s *Store) backfillLegacyInstanceQuota(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin legacy quota backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT instance.instance_id, instance.phase, instance.created_at_us,
		COALESCE((SELECT operation.operation_id FROM labcentral_local_operations operation
			WHERE operation.instance_id = instance.instance_id
			ORDER BY operation.created_at_us DESC, operation.operation_id DESC LIMIT 1), '')
		FROM labcentral_local_instances instance
		WHERE NOT EXISTS (SELECT 1 FROM labcentral_quota_reservations reservation
			WHERE reservation.instance_id = instance.instance_id)
		ORDER BY instance.instance_id`))
	if err != nil {
		return fmt.Errorf("list legacy instances for quota backfill: %w", err)
	}
	instances := make([]legacyQuotaInstance, 0)
	for rows.Next() {
		var instance legacyQuotaInstance
		if err := rows.Scan(&instance.id, &instance.phase, &instance.createdAt, &instance.operation); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy instance for quota backfill: %w", err)
		}
		if instance.createdAt <= 0 {
			_ = rows.Close()
			return fmt.Errorf("legacy instance %s has invalid creation time", instance.id)
		}
		if instance.operation == "" {
			digest := sha256.Sum256([]byte(instance.id))
			instance.operation = domain.OperationID("op_legacy_" + hex.EncodeToString(digest[:16]))
		}
		instances = append(instances, instance)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, instance := range instances {
		amounts, err := s.legacyInstanceQuotaAmountsTx(ctx, tx, instance.id)
		if err != nil {
			return err
		}
		state, reconciliationRequired, err := s.legacyQuotaStateTx(ctx, tx, instance.id, instance.phase)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(instance.id))
		reservationID := domain.QuotaReservationID("qres_legacy_" + hex.EncodeToString(digest[:16]))
		expiresAt := int64(math.MaxInt64)
		if state == "reserved" {
			expiresAt = time.UnixMicro(instance.createdAt).Add(defaultQuotaReservationHold).UnixMicro()
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservations
			(reservation_id, operation_id, state, expires_at_us, reconciliation_required, instance_id,
			 project_id, owner_kind, owner_id, target_id, created_at_us, updated_at_us)
			SELECT ?, ?, ?, ?, ?, instance_id, project_id, owner_kind, owner_id, provider_target_id, created_at_us, `+s.nowMicros()+`
			FROM labcentral_local_instances WHERE instance_id = ?`), reservationID, instance.operation, state,
			expiresAt, quotaBoolean(reconciliationRequired), instance.id); err != nil {
			return fmt.Errorf("backfill legacy quota reservation for %s: %w", instance.id, err)
		}
		for _, amount := range amounts {
			scope := application.QuotaScope{Kind: application.QuotaScopePlatform, ID: "platform"}
			if _, err := getQuotaLimitTx(ctx, tx, s, scope, amount.Dimension, true); errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_usage
				(scope_kind, scope_id, organization_id, dimension, reserved_value, committed_value, retained_value, updated_at_us)
				VALUES ('platform', 'platform', '', ?, 0, 0, 0, `+s.nowMicros()+`)
				ON CONFLICT (scope_kind, scope_id, organization_id, dimension) DO NOTHING`), amount.Dimension); err != nil {
				return fmt.Errorf("initialize legacy platform quota usage: %w", err)
			}
			if state != "released" {
				column := quotaUsageStateColumn(state)
				if column == "" {
					return fmt.Errorf("legacy quota state %q is invalid", state)
				}
				if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_usage SET `+column+` = `+column+` + ?, updated_at_us = `+s.nowMicros()+`
					WHERE scope_kind = 'platform' AND scope_id = 'platform' AND organization_id = '' AND dimension = ?`),
					amount.Value, amount.Dimension); err != nil {
					return fmt.Errorf("backfill legacy platform quota usage: %w", err)
				}
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservation_lines
				(reservation_id, scope_kind, scope_id, organization_id, dimension, amount)
				VALUES (?, 'platform', 'platform', '', ?, ?)`), reservationID, amount.Dimension, amount.Value); err != nil {
				return fmt.Errorf("backfill legacy quota line: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy quota backfill: %w", err)
	}
	return nil
}

func (s *Store) legacyInstanceQuotaAmountsTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID) ([]application.QuotaAmount, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT kind, canonical_resource_json
		FROM labcentral_local_resources WHERE instance_id = ? ORDER BY ordinal, resource_id`), instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	amounts := []application.QuotaAmount{{Dimension: application.QuotaInstances, Value: 1}}
	for rows.Next() {
		var kind domain.ResourceKind
		var encoded []byte
		if err := rows.Scan(&kind, &encoded); err != nil {
			return nil, err
		}
		var resource struct {
			Spec struct {
				Compute struct {
					CPUMillis   uint64 `json:"cpuMillis"`
					MemoryBytes uint64 `json:"memoryBytes"`
				} `json:"compute"`
				SizeBytes      uint64 `json:"sizeBytes"`
				PublishedPorts []struct {
					Exposure domain.PortExposure `json:"exposure"`
				} `json:"publishedPorts"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(encoded, &resource); err != nil {
			return nil, fmt.Errorf("decode legacy resource %s quota shape: %w", kind, err)
		}
		switch kind {
		case domain.ResourceVM:
			amounts = append(amounts,
				application.QuotaAmount{Dimension: application.QuotaVMs, Value: 1},
				application.QuotaAmount{Dimension: application.QuotaCPUMillis, Value: resource.Spec.Compute.CPUMillis},
				application.QuotaAmount{Dimension: application.QuotaMemoryBytes, Value: resource.Spec.Compute.MemoryBytes})
		case domain.ResourceContainer:
			publicAddress := false
			for _, port := range resource.Spec.PublishedPorts {
				if port.Exposure == domain.ExposurePublic {
					publicAddress = true
				}
			}
			amounts = append(amounts,
				application.QuotaAmount{Dimension: application.QuotaContainers, Value: 1},
				application.QuotaAmount{Dimension: application.QuotaCPUMillis, Value: resource.Spec.Compute.CPUMillis},
				application.QuotaAmount{Dimension: application.QuotaMemoryBytes, Value: resource.Spec.Compute.MemoryBytes},
				application.QuotaAmount{Dimension: application.QuotaPublishedPorts, Value: uint64(len(resource.Spec.PublishedPorts))})
			if publicAddress {
				amounts = append(amounts, application.QuotaAmount{Dimension: application.QuotaPublicIPs, Value: 1})
			}
		case domain.ResourceVolume:
			amounts = append(amounts, application.QuotaAmount{Dimension: application.QuotaStorageBytes, Value: resource.Spec.SizeBytes})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return application.NormalizeQuotaAmounts(amounts)
}

func (s *Store) legacyQuotaStateTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, phase domain.InstancePhase) (string, bool, error) {
	state, reconciliationRequired := legacyQuotaState(phase)
	if state != "released" {
		return state, reconciliationRequired, nil
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT CASE WHEN
		EXISTS (SELECT 1 FROM labcentral_provider_allocation_claims claim
			WHERE claim.instance_id = ? AND claim.state <> 'released')
		OR EXISTS (SELECT 1 FROM labcentral_local_resources resource
			WHERE resource.instance_id = ? AND (
				resource.phase IN (?, ?) OR
				(resource.phase <> ? AND resource.provider_state IS NOT NULL AND length(resource.provider_state) > 0)))
		OR EXISTS (SELECT 1 FROM labcentral_local_operations operation
			JOIN labcentral_jobs job ON job.operation_id = operation.operation_id
			WHERE operation.instance_id = ? AND job.state IN (?, ?, ?))
		THEN 1 ELSE 0 END`), instanceID, instanceID, domain.ResourceUnknown, domain.ResourceCleanupFailed,
		domain.ResourceDestroyed, instanceID, domain.OperationPartial, domain.OperationTimedOut, domain.OperationCanceled).Scan(&uncertain); err != nil {
		return "", false, fmt.Errorf("inspect legacy instance cleanup evidence: %w", err)
	}
	if uncertain != 0 {
		return "retained", true, nil
	}
	return state, reconciliationRequired, nil
}

func legacyQuotaState(phase domain.InstancePhase) (string, bool) {
	switch phase {
	case domain.InstancePending, domain.InstanceValidating, domain.InstancePlanned:
		return "reserved", false
	case domain.InstanceReady, domain.InstanceStopped, domain.InstancePaused, domain.InstanceDestroying:
		return "committed", false
	case domain.InstanceFailed, domain.InstanceDestroyed:
		return "released", false
	default:
		return "retained", true
	}
}

// The remaining methods in this file implement the production quota and
// expiration contract. They intentionally do not reuse jobsdb's earlier
// single-row prototype operations, whose compatibility table is migrated in
// place above.

func (s *Store) PutQuotaLimit(ctx context.Context, request application.PutQuotaLimitRequest) (application.QuotaLimitRecord, error) {
	if err := validatePutQuotaLimit(request); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.putQuotaLimit(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.QuotaLimitRecord{}, err
		}
	}
}

func (s *Store) putQuotaLimit(ctx context.Context, request application.PutQuotaLimitRequest) (application.QuotaLimitRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("begin quota limit mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	identity := quotaLimitIdentity(request.Scope, request.Dimension)
	if err := s.lockQuotaKey(ctx, tx, identity); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := s.lockIdempotency(ctx, tx, request.UpdatedBy, putQuotaLimitRouteAction, request.IdempotencyKey); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.UpdatedBy, putQuotaLimitRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return application.QuotaLimitRecord{}, err
	} else if found {
		if existing != identity {
			return application.QuotaLimitRecord{}, jobsdb.ErrIdempotencyConflict
		}
		var record application.QuotaLimitRecord
		if err := s.loadGovernanceIdempotencyResult(ctx, tx, request.UpdatedBy, putQuotaLimitRouteAction, request.IdempotencyKey, &record); err != nil {
			return application.QuotaLimitRecord{}, err
		}
		return record, tx.Commit()
	}
	if err := s.validateQuotaScopeReferenceTx(ctx, tx, request.Scope); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	existing, err := getQuotaLimitTx(ctx, tx, s, request.Scope, request.Dimension, true)
	switch {
	case errors.Is(err, sql.ErrNoRows) && request.ExpectedResourceVersion != 0:
		return application.QuotaLimitRecord{}, application.ErrNotFound
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return application.QuotaLimitRecord{}, err
	case err == nil && existing.ResourceVersion != request.ExpectedResourceVersion:
		return application.QuotaLimitRecord{}, application.ErrVersionConflict
	}
	var createdAt, updatedAt, version int64
	if existing.ResourceVersion == 0 {
		err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_quota_limits
			(scope_kind, scope_id, organization_id, dimension, limit_value, resource_version, updated_by, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, 1, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
			RETURNING resource_version, created_at_us, updated_at_us`),
			request.Scope.Kind, request.Scope.ID, request.Scope.OrganizationID, request.Dimension, request.Limit, request.UpdatedBy,
		).Scan(&version, &createdAt, &updatedAt)
		if err == nil {
			_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_usage
				(scope_kind, scope_id, organization_id, dimension, reserved_value, committed_value, retained_value, updated_at_us)
				VALUES (?, ?, ?, ?, 0, 0, 0, `+s.nowMicros()+`)`),
				request.Scope.Kind, request.Scope.ID, request.Scope.OrganizationID, request.Dimension)
		}
		if err == nil {
			err = s.backfillQuotaScopeDimensionTx(ctx, tx, request.Scope, request.Dimension)
		}
	} else {
		// Migration-created platform limits intentionally exist before any
		// usage is recorded. Materialize their zero-valued ledger row on the
		// first administrative update, just as admission does on first use.
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_usage
			(scope_kind, scope_id, organization_id, dimension, reserved_value, committed_value, retained_value, updated_at_us)
			VALUES (?, ?, ?, ?, 0, 0, 0, `+s.nowMicros()+`)
			ON CONFLICT (scope_kind, scope_id, organization_id, dimension) DO NOTHING`),
			request.Scope.Kind, request.Scope.ID, request.Scope.OrganizationID, request.Dimension); err != nil {
			return application.QuotaLimitRecord{}, fmt.Errorf("initialize updated quota usage: %w", err)
		}
		if err := s.backfillQuotaScopeDimensionTx(ctx, tx, request.Scope, request.Dimension); err != nil {
			return application.QuotaLimitRecord{}, err
		}
		usage, usageErr := getQuotaUsageTx(ctx, tx, s, request.Scope, request.Dimension, true)
		if usageErr != nil {
			return application.QuotaLimitRecord{}, usageErr
		}
		if usage.Total() > request.Limit {
			return application.QuotaLimitRecord{}, &application.QuotaExceededError{
				Scope: request.Scope, Dimension: request.Dimension, Limit: request.Limit, Current: usage.Total(),
			}
		}
		err = tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_quota_limits
			SET limit_value = ?, resource_version = resource_version + 1, updated_by = ?, updated_at_us = `+s.nowMicros()+`
			WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ? AND resource_version = ?
			RETURNING resource_version, created_at_us, updated_at_us`),
			request.Limit, request.UpdatedBy, request.Scope.Kind, request.Scope.ID, request.Scope.OrganizationID,
			request.Dimension, request.ExpectedResourceVersion,
		).Scan(&version, &createdAt, &updatedAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return application.QuotaLimitRecord{}, application.ErrVersionConflict
	}
	if err != nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("persist quota limit: %w", err)
	}
	usage, err := getQuotaUsageTx(ctx, tx, s, request.Scope, request.Dimension, true)
	if err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if usage.Total() > request.Limit {
		return application.QuotaLimitRecord{}, &application.QuotaExceededError{
			Scope: request.Scope, Dimension: request.Dimension, Limit: request.Limit, Current: usage.Total(),
		}
	}
	record := application.QuotaLimitRecord{
		Scope: request.Scope, Dimension: request.Dimension, Limit: request.Limit,
		ResourceVersion: domain.ResourceVersion(version), UpdatedBy: request.UpdatedBy,
		CreatedAt: time.UnixMicro(createdAt).UTC(), UpdatedAt: time.UnixMicro(updatedAt).UTC(),
	}
	if err := s.appendQuotaGovernanceTx(ctx, tx, request.Scope, request.UpdatedBy, identity, request.GovernanceRecordIDs,
		"quota.limit.put", "quota-limit.updated", map[string]any{
			"scopeKind": request.Scope.Kind, "scopeId": request.Scope.ID, "organizationId": request.Scope.OrganizationID,
			"dimension": request.Dimension, "limit": request.Limit, "resourceVersion": record.ResourceVersion,
		}); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.UpdatedBy, putQuotaLimitRouteAction, request.IdempotencyKey, request.RequestDigest, identity); err != nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("bind quota limit idempotency: %w", err)
	}
	if err := s.saveGovernanceIdempotencyResult(ctx, tx, request.UpdatedBy, putQuotaLimitRouteAction, request.IdempotencyKey, record); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.QuotaLimitRecord{}, fmt.Errorf("commit quota limit mutation: %w", err)
	}
	return record, nil
}

func (s *Store) GetQuotaLimit(ctx context.Context, scope application.QuotaScope, dimension application.QuotaDimension) (application.QuotaLimitRecord, error) {
	if err := application.ValidateQuotaScope(scope); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if err := application.ValidateQuotaDimension(dimension); err != nil {
		return application.QuotaLimitRecord{}, err
	}
	record, err := getQuotaLimitTx(ctx, s.readDB(), s, scope, dimension, false)
	if errors.Is(err, sql.ErrNoRows) {
		return application.QuotaLimitRecord{}, application.ErrNotFound
	}
	return record, err
}

func (s *Store) ListQuotaUsage(ctx context.Context, scope application.QuotaScope) ([]application.QuotaUsageRecord, error) {
	if err := application.ValidateQuotaScope(scope); err != nil {
		return nil, err
	}
	rows, err := s.readDB().QueryContext(ctx, s.bind(`SELECT dimension, reserved_value, committed_value, retained_value, updated_at_us
		FROM labcentral_quota_usage WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? ORDER BY dimension`),
		scope.Kind, scope.ID, scope.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("list quota usage: %w", err)
	}
	defer rows.Close()
	records := make([]application.QuotaUsageRecord, 0)
	for rows.Next() {
		var dimension application.QuotaDimension
		var reserved, committed, retained, updatedAt int64
		if err := rows.Scan(&dimension, &reserved, &committed, &retained, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan quota usage: %w", err)
		}
		if reserved < 0 || committed < 0 || retained < 0 || updatedAt <= 0 {
			return nil, fmt.Errorf("stored quota usage is negative")
		}
		records = append(records, application.QuotaUsageRecord{
			Scope: scope, Dimension: dimension, Reserved: uint64(reserved), Committed: uint64(committed), Retained: uint64(retained),
			UpdatedAt: time.UnixMicro(updatedAt).UTC(),
		})
	}
	return records, rows.Err()
}

func validatePutQuotaLimit(request application.PutQuotaLimitRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if err := application.ValidateQuotaScope(request.Scope); err != nil {
		return err
	}
	if err := application.ValidateQuotaDimension(request.Dimension); err != nil {
		return err
	}
	if request.Limit > application.MaxQuotaValue {
		return fmt.Errorf("quota limit exceeds the supported range")
	}
	if _, err := domain.NewUserID(string(request.UpdatedBy)); err != nil {
		return err
	}
	return validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest)
}

type quotaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getQuotaLimitTx(ctx context.Context, queryer quotaQueryer, store *Store, scope application.QuotaScope, dimension application.QuotaDimension, lock bool) (application.QuotaLimitRecord, error) {
	query := `SELECT limit_value, resource_version, updated_by, created_at_us, updated_at_us
		FROM labcentral_quota_limits WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`
	if lock && store.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	var limit, version, createdAt, updatedAt int64
	var updatedBy string
	err := queryer.QueryRowContext(ctx, store.bind(query), scope.Kind, scope.ID, scope.OrganizationID, dimension).
		Scan(&limit, &version, &updatedBy, &createdAt, &updatedAt)
	if err != nil {
		return application.QuotaLimitRecord{}, err
	}
	if limit < 0 || version <= 0 || createdAt <= 0 || updatedAt <= 0 {
		return application.QuotaLimitRecord{}, fmt.Errorf("stored quota limit is invalid")
	}
	return application.QuotaLimitRecord{
		Scope: scope, Dimension: dimension, Limit: uint64(limit), ResourceVersion: domain.ResourceVersion(version),
		UpdatedBy: domain.UserID(updatedBy), CreatedAt: time.UnixMicro(createdAt).UTC(), UpdatedAt: time.UnixMicro(updatedAt).UTC(),
	}, nil
}

func getQuotaUsageTx(ctx context.Context, queryer quotaQueryer, store *Store, scope application.QuotaScope, dimension application.QuotaDimension, lock bool) (application.QuotaUsageRecord, error) {
	query := `SELECT reserved_value, committed_value, retained_value, updated_at_us FROM labcentral_quota_usage
		WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`
	if lock && store.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	var reserved, committed, retained, updatedAt int64
	err := queryer.QueryRowContext(ctx, store.bind(query), scope.Kind, scope.ID, scope.OrganizationID, dimension).
		Scan(&reserved, &committed, &retained, &updatedAt)
	if err != nil {
		return application.QuotaUsageRecord{}, err
	}
	if reserved < 0 || committed < 0 || retained < 0 || updatedAt <= 0 {
		return application.QuotaUsageRecord{}, fmt.Errorf("stored quota usage is invalid")
	}
	return application.QuotaUsageRecord{
		Scope: scope, Dimension: dimension, Reserved: uint64(reserved), Committed: uint64(committed), Retained: uint64(retained),
		UpdatedAt: time.UnixMicro(updatedAt).UTC(),
	}, nil
}

func (s *Store) backfillQuotaScopeDimensionTx(ctx context.Context, tx *sql.Tx, scope application.QuotaScope, dimension application.QuotaDimension) error {
	condition := "1 = 1"
	arguments := []any{dimension}
	switch scope.Kind {
	case application.QuotaScopePlatform:
	case application.QuotaScopeOrganization:
		condition = "project.organization_id = ?"
		arguments = append(arguments, scope.OrganizationID)
	case application.QuotaScopeProject:
		condition = "instance.project_id = ? AND project.organization_id = ?"
		arguments = append(arguments, scope.ID, scope.OrganizationID)
	case application.QuotaScopeTeam:
		condition = "instance.owner_kind = ? AND instance.owner_id = ? AND project.organization_id = ?"
		arguments = append(arguments, application.OwnerTeam, scope.ID, scope.OrganizationID)
	case application.QuotaScopeUser:
		condition = "instance.owner_kind = ? AND instance.owner_id = ? AND project.organization_id = ?"
		arguments = append(arguments, application.OwnerUser, scope.ID, scope.OrganizationID)
	case application.QuotaScopeTarget:
		condition = "instance.provider_target_id = ?"
		arguments = append(arguments, scope.ID)
	default:
		return fmt.Errorf("unsupported quota backfill scope %q", scope.Kind)
	}
	query := `SELECT reservation.reservation_id, reservation.state, platform_line.amount
		FROM labcentral_quota_reservations reservation
		JOIN labcentral_local_instances instance ON instance.instance_id = reservation.instance_id
		JOIN labcentral_projects project ON project.project_id = instance.project_id
		JOIN labcentral_quota_reservation_lines platform_line
			ON platform_line.reservation_id = reservation.reservation_id
			AND platform_line.scope_kind = 'platform' AND platform_line.scope_id = 'platform'
			AND platform_line.organization_id = '' AND platform_line.dimension = ?
		WHERE reservation.state <> 'released' AND ` + condition + `
		ORDER BY reservation.reservation_id`
	if s.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE OF reservation`
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return fmt.Errorf("list existing usage for new quota scope: %w", err)
	}
	type candidate struct {
		reservationID domain.QuotaReservationID
		state         string
		amount        uint64
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		var amount int64
		if err := rows.Scan(&item.reservationID, &item.state, &amount); err != nil {
			_ = rows.Close()
			return err
		}
		if amount <= 0 || quotaUsageStateColumn(item.state) == "" {
			_ = rows.Close()
			return fmt.Errorf("existing quota reservation ledger is invalid")
		}
		item.amount = uint64(amount)
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservation_lines
			(reservation_id, scope_kind, scope_id, organization_id, dimension, amount)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (reservation_id, scope_kind, scope_id, organization_id, dimension) DO NOTHING`),
			candidate.reservationID, scope.Kind, scope.ID, scope.OrganizationID, dimension, candidate.amount)
		if err != nil {
			return fmt.Errorf("backfill new quota reservation line: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			continue
		}
		column := quotaUsageStateColumn(candidate.state)
		result, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_usage SET `+column+` = `+column+` + ?, updated_at_us = `+s.nowMicros()+`
			WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`),
			candidate.amount, scope.Kind, scope.ID, scope.OrganizationID, dimension)
		if err != nil {
			return fmt.Errorf("backfill new quota usage: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("new quota usage row is unavailable")
		}
	}
	return nil
}

func (s *Store) PutLifetimePolicy(ctx context.Context, request application.PutLifetimePolicyRequest) (application.LifetimePolicyRecord, error) {
	if err := validatePutLifetimePolicy(request); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.putLifetimePolicy(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.LifetimePolicyRecord{}, err
		}
	}
}

func (s *Store) putLifetimePolicy(ctx context.Context, request application.PutLifetimePolicyRequest) (application.LifetimePolicyRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("begin lifetime policy mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	identity := lifetimePolicyIdentity(request.Policy.Scope)
	if err := s.lockQuotaKey(ctx, tx, identity); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if err := s.lockIdempotency(ctx, tx, request.UpdatedBy, putLifetimePolicyRouteAction, request.IdempotencyKey); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if existingID, found, err := s.findIdempotency(ctx, tx, request.UpdatedBy, putLifetimePolicyRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return application.LifetimePolicyRecord{}, err
	} else if found {
		if existingID != identity {
			return application.LifetimePolicyRecord{}, jobsdb.ErrIdempotencyConflict
		}
		var record application.LifetimePolicyRecord
		if err := s.loadGovernanceIdempotencyResult(ctx, tx, request.UpdatedBy, putLifetimePolicyRouteAction, request.IdempotencyKey, &record); err != nil {
			return application.LifetimePolicyRecord{}, err
		}
		return record, tx.Commit()
	}
	if err := s.validateQuotaScopeReferenceTx(ctx, tx, request.Policy.Scope); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	existing, err := getLifetimePolicyTx(ctx, tx, s, request.Policy.Scope, true)
	switch {
	case errors.Is(err, sql.ErrNoRows) && request.ExpectedResourceVersion != 0:
		return application.LifetimePolicyRecord{}, application.ErrNotFound
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return application.LifetimePolicyRecord{}, err
	case err == nil && existing.ResourceVersion != request.ExpectedResourceVersion:
		return application.LifetimePolicyRecord{}, application.ErrVersionConflict
	}
	renewalAllowed, persistentAllowed := 0, 0
	if request.Policy.RenewalAllowed {
		renewalAllowed = 1
	}
	if request.Policy.PersistentAllowed {
		persistentAllowed = 1
	}
	var version, createdAt, updatedAt int64
	if existing.ResourceVersion == 0 {
		err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_lifetime_policies
			(scope_kind, scope_id, organization_id, default_ttl_seconds, maximum_ttl_seconds,
			 renewal_window_seconds, warning_before_seconds, destruction_grace_seconds, renewal_allowed,
			 persistent_allowed, resource_version, updated_by, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
			RETURNING resource_version, created_at_us, updated_at_us`),
			request.Policy.Scope.Kind, request.Policy.Scope.ID, request.Policy.Scope.OrganizationID,
			request.Policy.DefaultTTLSeconds, request.Policy.MaximumTTLSeconds, request.Policy.RenewalWindowSeconds,
			request.Policy.WarningBeforeSeconds, request.Policy.DestructionGraceSecs, renewalAllowed, persistentAllowed, request.UpdatedBy,
		).Scan(&version, &createdAt, &updatedAt)
	} else {
		err = tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_lifetime_policies SET
			default_ttl_seconds = ?, maximum_ttl_seconds = ?, renewal_window_seconds = ?, warning_before_seconds = ?,
			destruction_grace_seconds = ?, renewal_allowed = ?, persistent_allowed = ?, resource_version = resource_version + 1,
			updated_by = ?, updated_at_us = `+s.nowMicros()+`
			WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND resource_version = ?
			RETURNING resource_version, created_at_us, updated_at_us`),
			request.Policy.DefaultTTLSeconds, request.Policy.MaximumTTLSeconds, request.Policy.RenewalWindowSeconds,
			request.Policy.WarningBeforeSeconds, request.Policy.DestructionGraceSecs, renewalAllowed, persistentAllowed,
			request.UpdatedBy, request.Policy.Scope.Kind, request.Policy.Scope.ID, request.Policy.Scope.OrganizationID,
			request.ExpectedResourceVersion,
		).Scan(&version, &createdAt, &updatedAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return application.LifetimePolicyRecord{}, application.ErrVersionConflict
	}
	if err != nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("persist lifetime policy: %w", err)
	}
	record := request.Policy
	record.ResourceVersion = domain.ResourceVersion(version)
	record.UpdatedBy = request.UpdatedBy
	record.CreatedAt, record.UpdatedAt = time.UnixMicro(createdAt).UTC(), time.UnixMicro(updatedAt).UTC()
	if err := s.appendQuotaGovernanceTx(ctx, tx, request.Policy.Scope, request.UpdatedBy, identity, request.GovernanceRecordIDs,
		"lifetime.policy.put", "lifetime-policy.updated", map[string]any{
			"scopeKind": request.Policy.Scope.Kind, "scopeId": request.Policy.Scope.ID,
			"organizationId": request.Policy.Scope.OrganizationID, "defaultTtlSeconds": request.Policy.DefaultTTLSeconds,
			"maximumTtlSeconds":       request.Policy.MaximumTTLSeconds,
			"renewalWindowSeconds":    request.Policy.RenewalWindowSeconds,
			"warningBeforeSeconds":    request.Policy.WarningBeforeSeconds,
			"destructionGraceSeconds": request.Policy.DestructionGraceSecs,
			"renewalAllowed":          request.Policy.RenewalAllowed, "persistentAllowed": request.Policy.PersistentAllowed,
			"resourceVersion": record.ResourceVersion,
		}); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.UpdatedBy, putLifetimePolicyRouteAction, request.IdempotencyKey, request.RequestDigest, identity); err != nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("bind lifetime policy idempotency: %w", err)
	}
	if err := s.saveGovernanceIdempotencyResult(ctx, tx, request.UpdatedBy, putLifetimePolicyRouteAction, request.IdempotencyKey, record); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.LifetimePolicyRecord{}, fmt.Errorf("commit lifetime policy mutation: %w", err)
	}
	return record, nil
}

func (s *Store) GetLifetimePolicy(ctx context.Context, scope application.QuotaScope) (application.LifetimePolicyRecord, error) {
	if err := application.ValidateQuotaScope(scope); err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	record, err := getLifetimePolicyTx(ctx, s.readDB(), s, scope, false)
	if errors.Is(err, sql.ErrNoRows) {
		return application.LifetimePolicyRecord{}, application.ErrNotFound
	}
	return record, err
}

func validatePutLifetimePolicy(request application.PutLifetimePolicyRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if err := application.ValidateLifetimePolicy(request.Policy); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.UpdatedBy)); err != nil {
		return err
	}
	return validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest)
}

func getLifetimePolicyTx(ctx context.Context, queryer quotaQueryer, store *Store, scope application.QuotaScope, lock bool) (application.LifetimePolicyRecord, error) {
	query := `SELECT default_ttl_seconds, maximum_ttl_seconds, renewal_window_seconds, warning_before_seconds,
		destruction_grace_seconds, renewal_allowed, persistent_allowed, resource_version, updated_by, created_at_us, updated_at_us
		FROM labcentral_lifetime_policies WHERE scope_kind = ? AND scope_id = ? AND organization_id = ?`
	if lock && store.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	var defaultTTL, maximumTTL, renewalWindow, warningBefore, grace, renewalAllowed, persistentAllowed, version, createdAt, updatedAt int64
	var updatedBy string
	err := queryer.QueryRowContext(ctx, store.bind(query), scope.Kind, scope.ID, scope.OrganizationID).Scan(
		&defaultTTL, &maximumTTL, &renewalWindow, &warningBefore, &grace, &renewalAllowed, &persistentAllowed,
		&version, &updatedBy, &createdAt, &updatedAt,
	)
	if err != nil {
		return application.LifetimePolicyRecord{}, err
	}
	if defaultTTL <= 0 || maximumTTL <= 0 || renewalWindow < 0 || warningBefore < 0 || grace < 0 || version <= 0 || createdAt <= 0 || updatedAt <= 0 {
		return application.LifetimePolicyRecord{}, fmt.Errorf("stored lifetime policy is invalid")
	}
	return application.LifetimePolicyRecord{
		Scope: scope, DefaultTTLSeconds: uint64(defaultTTL), MaximumTTLSeconds: uint64(maximumTTL),
		RenewalWindowSeconds: uint64(renewalWindow), WarningBeforeSeconds: uint64(warningBefore),
		DestructionGraceSecs: uint64(grace), RenewalAllowed: renewalAllowed != 0, PersistentAllowed: persistentAllowed != 0,
		ResourceVersion: domain.ResourceVersion(version), UpdatedBy: domain.UserID(updatedBy),
		CreatedAt: time.UnixMicro(createdAt).UTC(), UpdatedAt: time.UnixMicro(updatedAt).UTC(),
	}, nil
}

func (s *Store) reserveInstanceQuotaTx(ctx context.Context, tx *sql.Tx, request application.CreateInstanceRequest) (application.ResolvedInstanceLifetime, error) {
	if _, err := domain.NewQuotaReservationID(string(request.Quota.ReservationID)); err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	amounts, err := application.NormalizeQuotaAmounts(request.Quota.Amounts)
	if err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	if len(amounts) == 0 {
		return application.ResolvedInstanceLifetime{}, fmt.Errorf("instance quota request has no non-zero amounts")
	}
	if err := application.ValidateInstanceLifetimeRequest(request.Quota.Lifetime); err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	scopes, err := s.instanceQuotaScopesTx(ctx, tx, request.ProjectID, request.OwnerKind, request.OwnerID, request.ProviderTargetID)
	if err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	if err := s.lockApplicableQuotaPolicies(ctx, tx, scopes, amounts); err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	lifetime, err := s.resolveInstanceLifetimeTx(ctx, tx, scopes, request.Quota.Lifetime, now)
	if err != nil {
		return application.ResolvedInstanceLifetime{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservations
		(reservation_id, operation_id, state, expires_at_us, reconciliation_required, instance_id, project_id,
		 owner_kind, owner_id, target_id, created_at_us, updated_at_us)
		VALUES (?, ?, 'reserved', ?, 0, ?, ?, ?, ?, ?, ?, ?)`), request.Quota.ReservationID, request.OperationID,
		now.Add(defaultQuotaReservationHold).UnixMicro(), request.InstanceID, request.ProjectID, request.OwnerKind,
		request.OwnerID, request.ProviderTargetID, now.UnixMicro(), now.UnixMicro()); err != nil {
		return application.ResolvedInstanceLifetime{}, fmt.Errorf("create instance quota reservation: %w", err)
	}
	for _, scope := range scopes {
		for _, amount := range amounts {
			limit, err := getQuotaLimitTx(ctx, tx, s, scope, amount.Dimension, true)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return application.ResolvedInstanceLifetime{}, err
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_usage
				(scope_kind, scope_id, organization_id, dimension, reserved_value, committed_value, retained_value, updated_at_us)
				VALUES (?, ?, ?, ?, 0, 0, 0, `+s.nowMicros()+`)
				ON CONFLICT (scope_kind, scope_id, organization_id, dimension) DO NOTHING`),
				scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension); err != nil {
				return application.ResolvedInstanceLifetime{}, fmt.Errorf("initialize reservation usage: %w", err)
			}
			usage, err := getQuotaUsageTx(ctx, tx, s, scope, amount.Dimension, true)
			if err != nil {
				return application.ResolvedInstanceLifetime{}, err
			}
			current := usage.Total()
			if current > application.MaxQuotaValue-amount.Value || current+amount.Value > limit.Limit {
				return application.ResolvedInstanceLifetime{}, &application.QuotaExceededError{
					Scope: scope, Dimension: amount.Dimension, Limit: limit.Limit, Current: current, Requested: amount.Value,
				}
			}
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_usage
				SET reserved_value = reserved_value + ?, updated_at_us = `+s.nowMicros()+`
				WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`),
				amount.Value, scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension); err != nil {
				return application.ResolvedInstanceLifetime{}, fmt.Errorf("reserve quota usage: %w", err)
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservation_lines
				(reservation_id, scope_kind, scope_id, organization_id, dimension, amount)
				VALUES (?, ?, ?, ?, ?, ?)`), request.Quota.ReservationID, scope.Kind, scope.ID,
				scope.OrganizationID, amount.Dimension, amount.Value); err != nil {
				return application.ResolvedInstanceLifetime{}, fmt.Errorf("record quota reservation line: %w", err)
			}
		}
	}
	return lifetime, nil
}

func (s *Store) instanceQuotaScopesTx(ctx context.Context, tx *sql.Tx, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, targetID domain.ProviderTargetID) ([]application.QuotaScope, error) {
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), projectID).Scan(&organizationID); err != nil {
		return nil, fmt.Errorf("resolve instance quota organization: %w", err)
	}
	scopes := []application.QuotaScope{
		{Kind: application.QuotaScopePlatform, ID: "platform"},
		{Kind: application.QuotaScopeOrganization, ID: string(organizationID), OrganizationID: organizationID},
		{Kind: application.QuotaScopeProject, ID: string(projectID), OrganizationID: organizationID},
		{Kind: application.QuotaScopeTarget, ID: string(targetID)},
	}
	switch ownerKind {
	case application.OwnerUser:
		scopes = append(scopes, application.QuotaScope{Kind: application.QuotaScopeUser, ID: ownerID, OrganizationID: organizationID})
	case application.OwnerTeam:
		scopes = append(scopes, application.QuotaScope{Kind: application.QuotaScopeTeam, ID: ownerID, OrganizationID: organizationID})
	case application.OwnerProject:
	default:
		return nil, fmt.Errorf("instance owner kind is invalid for quota evaluation")
	}
	sort.Slice(scopes, func(i, j int) bool {
		return quotaScopeIdentity(scopes[i]) < quotaScopeIdentity(scopes[j])
	})
	return scopes, nil
}

func (s *Store) resolveInstanceLifetimeTx(ctx context.Context, tx *sql.Tx, scopes []application.QuotaScope, request application.InstanceLifetimeRequest, now time.Time) (application.ResolvedInstanceLifetime, error) {
	policies := make([]application.LifetimePolicyRecord, 0, len(scopes))
	for _, scope := range scopes {
		policy, err := getLifetimePolicyTx(ctx, tx, s, scope, false)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return application.ResolvedInstanceLifetime{}, err
		}
		policies = append(policies, policy)
	}
	if len(policies) == 0 {
		return application.ResolvedInstanceLifetime{}, fmt.Errorf("no applicable lifetime policy exists")
	}
	defaultTTL, maximumTTL := uint64(math.MaxInt64), uint64(math.MaxInt64)
	renewalWindow, warningBefore, destructionGrace := uint64(math.MaxInt64), uint64(0), uint64(math.MaxInt64)
	renewalAllowed, persistentAllowed := true, true
	for _, policy := range policies {
		defaultTTL = min(defaultTTL, policy.DefaultTTLSeconds)
		maximumTTL = min(maximumTTL, policy.MaximumTTLSeconds)
		if policy.RenewalWindowSeconds != 0 {
			renewalWindow = min(renewalWindow, policy.RenewalWindowSeconds)
		}
		warningBefore = max(warningBefore, policy.WarningBeforeSeconds)
		destructionGrace = min(destructionGrace, policy.DestructionGraceSecs)
		renewalAllowed = renewalAllowed && policy.RenewalAllowed
		persistentAllowed = persistentAllowed && policy.PersistentAllowed
	}
	if renewalWindow == uint64(math.MaxInt64) {
		renewalWindow = 0
	}
	if destructionGrace == uint64(math.MaxInt64) {
		destructionGrace = 0
	}
	digest := lifetimePoliciesDigest(policies)
	if request.Persistent {
		if !persistentAllowed {
			return application.ResolvedInstanceLifetime{}, &domain.DomainError{
				Code: "persistent_instance_not_allowed", Class: domain.ErrorQuota,
				Message: "Persistent instances are not allowed by an applicable lifetime policy",
			}
		}
		return application.ResolvedInstanceLifetime{
			RenewalAllowed: renewalAllowed, RenewalWindowSeconds: renewalWindow,
			DestructionGraceSeconds: destructionGrace, MaximumTTLSeconds: maximumTTL, PolicyDigest: digest,
		}, nil
	}
	ttl := request.TTLSeconds
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl == 0 || ttl > maximumTTL {
		return application.ResolvedInstanceLifetime{}, &domain.DomainError{
			Code: "instance_lifetime_exceeded", Class: domain.ErrorQuota,
			Message: "The requested instance lifetime exceeds an applicable policy",
		}
	}
	expiresAt := now.Add(time.Duration(ttl) * time.Second)
	maximumExpiresAt := now.Add(time.Duration(maximumTTL) * time.Second)
	warningAt := expiresAt.Add(-time.Duration(min(warningBefore, ttl)) * time.Second)
	return application.ResolvedInstanceLifetime{
		ExpiresAt: expiresAt, MaximumExpiresAt: maximumExpiresAt, ExpirationWarningAt: warningAt,
		DestructionGraceSeconds: destructionGrace, RenewalAllowed: renewalAllowed,
		RenewalWindowSeconds: renewalWindow, MaximumTTLSeconds: maximumTTL, PolicyDigest: digest,
	}, nil
}

func (s *Store) RenewInstanceLifetime(ctx context.Context, request application.RenewInstanceLifetimeRequest) (application.InstanceRecord, error) {
	if err := validateRenewInstanceLifetime(request); err != nil {
		return application.InstanceRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.renewInstanceLifetime(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.InstanceRecord{}, err
		}
	}
}

type instanceLifetimeMutationResult struct {
	InstanceID                   domain.InstanceID      `json:"instanceId"`
	ExpiresAt                    time.Time              `json:"expiresAt"`
	MaximumExpiresAt             time.Time              `json:"maximumExpiresAt"`
	ExpirationWarningAt          time.Time              `json:"expirationWarningAt"`
	ExpirationGraceSeconds       uint64                 `json:"expirationGraceSeconds"`
	LifetimeRenewalAllowed       bool                   `json:"lifetimeRenewalAllowed"`
	LifetimeRenewalWindowSeconds uint64                 `json:"lifetimeRenewalWindowSeconds"`
	LifetimePolicyDigest         domain.SHA256Digest    `json:"lifetimePolicyDigest"`
	ResourceVersion              domain.ResourceVersion `json:"resourceVersion"`
	UpdatedAt                    time.Time              `json:"updatedAt"`
}

func lifetimeMutationResult(instance application.InstanceRecord) instanceLifetimeMutationResult {
	return instanceLifetimeMutationResult{
		InstanceID: instance.ID, ExpiresAt: instance.ExpiresAt, MaximumExpiresAt: instance.MaximumExpiresAt,
		ExpirationWarningAt: instance.ExpirationWarningAt, ExpirationGraceSeconds: instance.ExpirationGraceSeconds,
		LifetimeRenewalAllowed:       instance.LifetimeRenewalAllowed,
		LifetimeRenewalWindowSeconds: instance.LifetimeRenewalWindowSeconds,
		LifetimePolicyDigest:         instance.LifetimePolicyDigest, ResourceVersion: instance.ResourceVersion, UpdatedAt: instance.UpdatedAt,
	}
}

func applyLifetimeMutationResult(instance *application.InstanceRecord, result instanceLifetimeMutationResult) error {
	if instance == nil || result.InstanceID == "" || instance.ID != result.InstanceID || result.ExpiresAt.IsZero() ||
		result.MaximumExpiresAt.IsZero() || result.ResourceVersion == 0 || result.UpdatedAt.IsZero() {
		return fmt.Errorf("stored lifetime mutation result is invalid")
	}
	instance.ExpiresAt, instance.MaximumExpiresAt = result.ExpiresAt, result.MaximumExpiresAt
	instance.ExpirationWarningAt, instance.ExpirationGraceSeconds = result.ExpirationWarningAt, result.ExpirationGraceSeconds
	instance.LifetimeRenewalAllowed = result.LifetimeRenewalAllowed
	instance.LifetimeRenewalWindowSeconds = result.LifetimeRenewalWindowSeconds
	instance.LifetimePolicyDigest = result.LifetimePolicyDigest
	instance.ResourceVersion, instance.UpdatedAt = result.ResourceVersion, result.UpdatedAt
	return nil
}

func (s *Store) renewInstanceLifetime(ctx context.Context, request application.RenewInstanceLifetimeRequest) (application.InstanceRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.InstanceRecord{}, fmt.Errorf("begin instance lifetime renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	action := renewInstanceTTLRouteAction + ":" + string(request.InstanceID)
	if err := s.lockIdempotency(ctx, tx, request.ActorID, action, request.IdempotencyKey); err != nil {
		return application.InstanceRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.ActorID, action, request.IdempotencyKey, request.RequestDigest); err != nil {
		return application.InstanceRecord{}, err
	} else if found {
		if existing != string(request.InstanceID) {
			return application.InstanceRecord{}, jobsdb.ErrIdempotencyConflict
		}
		record, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceRecordSelect+` WHERE instance_id = ?`), request.InstanceID))
		if err != nil {
			return application.InstanceRecord{}, fmt.Errorf("load replayed instance lifetime: %w", err)
		}
		var snapshot instanceLifetimeMutationResult
		if err := s.loadGovernanceIdempotencyResult(ctx, tx, request.ActorID, action, request.IdempotencyKey, &snapshot); err != nil {
			return application.InstanceRecord{}, err
		}
		if err := applyLifetimeMutationResult(&record, snapshot); err != nil {
			return application.InstanceRecord{}, err
		}
		return record, tx.Commit()
	}
	instanceQuery := instanceRecordSelect + ` WHERE instance_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		instanceQuery += ` FOR UPDATE`
	}
	instance, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceQuery), request.InstanceID))
	if err != nil {
		return application.InstanceRecord{}, err
	}
	if instance.ResourceVersion != request.ExpectedResourceVersion {
		return application.InstanceRecord{}, application.ErrVersionConflict
	}
	if instance.ExpiresAt.IsZero() || instance.MaximumExpiresAt.IsZero() || !instance.LifetimeRenewalAllowed {
		return application.InstanceRecord{}, &domain.DomainError{
			Code: "instance_lifetime_renewal_denied", Class: domain.ErrorQuota,
			Message: "The instance lifetime policy does not permit renewal",
		}
	}
	if instance.DesiredLifecycle == domain.LifecycleDestroyed || instance.Phase == domain.InstanceDestroying || instance.Phase == domain.InstanceDestroyed {
		return application.InstanceRecord{}, application.ErrVersionConflict
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return application.InstanceRecord{}, err
	}
	if err := s.requireInstanceTTLRenewalAuthorizationTx(ctx, tx, instance, request.ActorID, now); err != nil {
		return application.InstanceRecord{}, err
	}
	if !now.Before(instance.ExpiresAt) {
		return application.InstanceRecord{}, &domain.DomainError{
			Code: "instance_already_expired", Class: domain.ErrorQuota, Message: "The instance has already expired",
		}
	}
	if instance.LifetimeRenewalWindowSeconds > 0 && now.Before(instance.ExpiresAt.Add(-time.Duration(instance.LifetimeRenewalWindowSeconds)*time.Second)) {
		return application.InstanceRecord{}, &domain.DomainError{
			Code: "instance_renewal_window_not_open", Class: domain.ErrorQuota,
			Message: "The instance renewal window is not open",
		}
	}
	scopes, err := s.instanceQuotaScopesTx(ctx, tx, instance.ProjectID, instance.OwnerKind, instance.OwnerID, instance.ProviderTargetID)
	if err != nil {
		return application.InstanceRecord{}, err
	}
	if err := s.lockApplicableLifetimePolicies(ctx, tx, scopes); err != nil {
		return application.InstanceRecord{}, err
	}
	resolved, err := s.resolveInstanceLifetimeTx(ctx, tx, scopes, application.InstanceLifetimeRequest{TTLSeconds: request.TTLSeconds}, now)
	if err != nil {
		return application.InstanceRecord{}, err
	}
	if !resolved.RenewalAllowed {
		return application.InstanceRecord{}, &domain.DomainError{
			Code: "instance_lifetime_renewal_denied", Class: domain.ErrorQuota,
			Message: "An applicable lifetime policy no longer permits renewal",
		}
	}
	maximumAllowed := instance.MaximumExpiresAt
	currentPolicyMaximum := instance.CreatedAt.Add(time.Duration(resolved.MaximumTTLSeconds) * time.Second)
	if currentPolicyMaximum.Before(maximumAllowed) {
		maximumAllowed = currentPolicyMaximum
	}
	if !resolved.ExpiresAt.After(instance.ExpiresAt) || resolved.ExpiresAt.After(maximumAllowed) {
		return application.InstanceRecord{}, &domain.DomainError{
			Code: "instance_lifetime_exceeded", Class: domain.ErrorQuota,
			Message: "The requested renewal exceeds the instance maximum lifetime",
		}
	}
	if err := s.recheckAndExtendReservationScopesTx(ctx, tx, instance, scopes); err != nil {
		return application.InstanceRecord{}, err
	}
	warningAt := resolved.ExpirationWarningAt
	grace := min(instance.ExpirationGraceSeconds, resolved.DestructionGraceSeconds)
	renewalWindow := instance.LifetimeRenewalWindowSeconds
	if resolved.RenewalWindowSeconds != 0 && (renewalWindow == 0 || resolved.RenewalWindowSeconds < renewalWindow) {
		renewalWindow = resolved.RenewalWindowSeconds
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET
		expires_at_us = ?, maximum_expires_at_us = ?, expiration_warning_at_us = ?, expiration_warning_emitted_at_us = 0,
		expiration_grace_seconds = ?, lifetime_renewal_allowed = ?, lifetime_renewal_window_seconds = ?,
		lifetime_policy_digest = ?, resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE instance_id = ? AND resource_version = ? AND expires_at_us > `+s.nowMicros()),
		resolved.ExpiresAt.UnixMicro(), maximumAllowed.UnixMicro(), warningAt.UnixMicro(), grace,
		quotaBoolean(instance.LifetimeRenewalAllowed && resolved.RenewalAllowed), renewalWindow,
		resolved.PolicyDigest.Hex(), request.InstanceID, request.ExpectedResourceVersion)
	if err != nil {
		return application.InstanceRecord{}, fmt.Errorf("renew instance lifetime: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return application.InstanceRecord{}, application.ErrVersionConflict
	}
	updated, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceRecordSelect+` WHERE instance_id = ?`), request.InstanceID))
	if err != nil {
		return application.InstanceRecord{}, err
	}
	if err := s.appendInstanceLifetimeGovernanceTx(ctx, tx, updated, request.ActorID, request.GovernanceRecordIDs,
		"instance.ttl.extend", "lab-instance.ttl-extended", application.RecordActorUser); err != nil {
		return application.InstanceRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.ActorID, action, request.IdempotencyKey, request.RequestDigest, string(request.InstanceID)); err != nil {
		return application.InstanceRecord{}, fmt.Errorf("bind instance lifetime renewal idempotency: %w", err)
	}
	if err := s.saveGovernanceIdempotencyResult(ctx, tx, request.ActorID, action, request.IdempotencyKey, lifetimeMutationResult(updated)); err != nil {
		return application.InstanceRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.InstanceRecord{}, fmt.Errorf("commit instance lifetime renewal: %w", err)
	}
	return updated, nil
}

// requireInstanceTTLRenewalAuthorizationTx repeats the accepted TTL permission,
// owner relationship, membership, and target-grant checks inside the same
// serializable transaction that changes expiry. This closes the handler-check /
// mutation race when an administrator revokes authority concurrently.
func (s *Store) requireInstanceTTLRenewalAuthorizationTx(ctx context.Context, tx *sql.Tx, instance application.InstanceRecord, actorID domain.UserID, now time.Time) error {
	return s.requireInstanceOwnerAuthorizationTx(ctx, tx, instance, actorID, now)
}

// requireInstanceOwnerAuthorizationTx is shared by TTL and access commands,
// whose accepted RBAC matrix is identical: administrators/instructors may act
// within scope, while participants remain restricted to their user/team owner.
func (s *Store) requireInstanceOwnerAuthorizationTx(ctx context.Context, tx *sql.Tx, instance application.InstanceRecord, actorID domain.UserID, now time.Time) error {
	return s.requireInstanceAuthorityTx(ctx, tx, instance, actorID, now, true)
}

func (s *Store) requireInstanceAuthorityTx(ctx context.Context, tx *sql.Tx, instance application.InstanceRecord, actorID domain.UserID, now time.Time, allowParticipant bool) error {
	participantAllowed := 0
	if allowParticipant {
		participantAllowed = 1
	}
	if instance.ProjectID == "project.local" && actorID == application.LocalAdminID {
		return nil
	}
	if instance.OwnerKind == application.OwnerTeam {
		var groupProject string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT project_id FROM labcentral_project_groups WHERE team_id=?`), instance.OwnerID).Scan(&groupProject)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if groupProject != "" && groupProject != string(instance.ProjectID) {
			return application.ErrAuthorityChanged
		}
	}
	nowMicros := now.UTC().UnixMicro()
	query := `WITH instance_scope AS (
		SELECT project.organization_id
		FROM labcentral_projects project WHERE project.project_id = ?
	), active_memberships AS (
		SELECT membership.organization_id, COALESCE(membership.team_id, '') AS team_id
		FROM labcentral_memberships membership
		WHERE membership.user_id = ? AND membership.valid_from_us <= ?
		AND (membership.valid_until_us = 0 OR membership.valid_until_us > ?)
	), owner_memberships AS (
        SELECT team_id FROM active_memberships
        UNION SELECT team_id FROM labcentral_project_enrollments WHERE project_id=? AND user_id=?
    ), eligible_roles AS (
		SELECT 1
		FROM labcentral_role_assignments assignment CROSS JOIN instance_scope scope
		WHERE assignment.valid_from_us <= ? AND (assignment.valid_until_us = 0 OR assignment.valid_until_us > ?)
		AND assignment.role IN ('platform-administrator', 'instructor', 'participant')
		AND (? = 1 OR assignment.role <> 'participant')
		AND ((assignment.subject_kind = 'user' AND assignment.subject_id = ?)
			OR (assignment.subject_kind = 'team' AND EXISTS (
				SELECT 1 FROM active_memberships membership
				WHERE membership.team_id = assignment.subject_id)))
		AND (assignment.subject_kind <> 'user' OR assignment.scope_kind = 'platform' OR EXISTS (
			SELECT 1 FROM active_memberships membership WHERE membership.organization_id = scope.organization_id))
		AND ((assignment.scope_kind = 'platform' AND assignment.scope_id = 'platform')
			OR (assignment.scope_kind = 'organization' AND assignment.scope_id = scope.organization_id)
			OR (assignment.scope_kind = 'project' AND assignment.scope_id = ?)
			OR (assignment.scope_kind = 'team' AND ? = 'team' AND assignment.scope_id = ?
				AND EXISTS (SELECT 1 FROM active_memberships membership WHERE membership.team_id = ?)))
		AND (assignment.role <> 'participant'
			OR (? = 'user' AND ? = ?)
			OR (? = 'team' AND EXISTS (
				SELECT 1 FROM owner_memberships membership WHERE membership.team_id = ?)))
		LIMIT 1
	), eligible_grants AS (
		SELECT 1
		FROM labcentral_provider_target_grants grant_record CROSS JOIN instance_scope scope
		WHERE grant_record.target_id = ? AND grant_record.valid_from_us <= ?
		AND (grant_record.valid_until_us = 0 OR grant_record.valid_until_us > ?)
		AND ((grant_record.scope_kind = 'platform' AND grant_record.scope_id = 'platform')
			OR (grant_record.scope_kind = 'organization' AND grant_record.scope_id = scope.organization_id)
			OR (grant_record.scope_kind = 'project' AND grant_record.scope_id = ?)
			OR (grant_record.scope_kind = 'team' AND ? = 'team' AND grant_record.scope_id = ?
				AND EXISTS (SELECT 1 FROM owner_memberships membership WHERE membership.team_id = ?)))
		LIMIT 1
	)
	SELECT CASE WHEN EXISTS (SELECT 1 FROM labcentral_users user_record
		WHERE user_record.user_id = ? AND user_record.disabled = 0)
		AND EXISTS (SELECT 1 FROM eligible_roles)
		AND EXISTS (SELECT 1 FROM eligible_grants)
		THEN 1 ELSE 0 END`
	var allowed int
	err := tx.QueryRowContext(ctx, s.bind(query),
		instance.ProjectID, actorID, nowMicros, nowMicros, instance.ProjectID, actorID,
		nowMicros, nowMicros, participantAllowed, actorID, instance.ProjectID,
		instance.OwnerKind, instance.OwnerID, instance.OwnerID,
		instance.OwnerKind, instance.OwnerID, actorID, instance.OwnerKind, instance.OwnerID,
		instance.ProviderTargetID, nowMicros, nowMicros, instance.ProjectID,
		instance.OwnerKind, instance.OwnerID, instance.OwnerID,
		actorID,
	).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("recheck instance authority: %w", err)
	}
	if allowed != 1 {
		return application.ErrAuthorityChanged
	}
	return nil
}

func validateRenewInstanceLifetime(request application.RenewInstanceLifetimeRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewInstanceID(string(request.InstanceID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.ActorID)); err != nil {
		return err
	}
	if request.TTLSeconds == 0 || request.TTLSeconds > application.MaxInstanceTTLSeconds || request.ExpectedResourceVersion == 0 {
		return fmt.Errorf("instance lifetime renewal fields are invalid")
	}
	return validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest)
}

func (s *Store) recheckAndExtendReservationScopesTx(ctx context.Context, tx *sql.Tx, instance application.InstanceRecord, scopes []application.QuotaScope) error {
	reservationQuery := `SELECT reservation_id, state FROM labcentral_quota_reservations WHERE instance_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		reservationQuery += ` FOR UPDATE`
	}
	var reservationID domain.QuotaReservationID
	var reservationState string
	if err := tx.QueryRowContext(ctx, s.bind(reservationQuery), instance.ID).Scan(&reservationID, &reservationState); err != nil {
		return fmt.Errorf("load renewal quota reservation: %w", err)
	}
	stateColumn := quotaUsageStateColumn(reservationState)
	if reservationState == "released" || stateColumn == "" {
		return &domain.DomainError{Code: "instance_quota_unavailable", Class: domain.ErrorQuota, Message: "The instance quota reservation is unavailable"}
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT dimension, amount FROM labcentral_quota_reservation_lines
		WHERE reservation_id = ? AND scope_kind = 'platform' AND scope_id = 'platform' AND organization_id = ''
		ORDER BY dimension`), reservationID)
	if err != nil {
		return err
	}
	var amounts []application.QuotaAmount
	for rows.Next() {
		var amount int64
		var dimension application.QuotaDimension
		if err := rows.Scan(&dimension, &amount); err != nil {
			_ = rows.Close()
			return err
		}
		if amount <= 0 {
			_ = rows.Close()
			return fmt.Errorf("stored renewal quota amount is invalid")
		}
		amounts = append(amounts, application.QuotaAmount{Dimension: dimension, Value: uint64(amount)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(amounts) == 0 {
		return fmt.Errorf("instance quota reservation has no platform amount ledger")
	}
	for _, scope := range scopes {
		for _, amount := range amounts {
			identity := quotaLimitIdentity(scope, amount.Dimension)
			if err := s.lockQuotaKey(ctx, tx, identity); err != nil {
				return err
			}
			limit, err := getQuotaLimitTx(ctx, tx, s, scope, amount.Dimension, true)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_usage
				(scope_kind, scope_id, organization_id, dimension, reserved_value, committed_value, retained_value, updated_at_us)
				VALUES (?, ?, ?, ?, 0, 0, 0, `+s.nowMicros()+`)
				ON CONFLICT (scope_kind, scope_id, organization_id, dimension) DO NOTHING`),
				scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension); err != nil {
				return err
			}
			usage, err := getQuotaUsageTx(ctx, tx, s, scope, amount.Dimension, true)
			if err != nil {
				return err
			}
			var lineExists int
			err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_quota_reservation_lines
				WHERE reservation_id = ? AND scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`),
				reservationID, scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension).Scan(&lineExists)
			hasLine := err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			current := usage.Total()
			requested := uint64(0)
			if !hasLine {
				requested = amount.Value
			}
			if current > application.MaxQuotaValue-requested || current+requested > limit.Limit {
				return &application.QuotaExceededError{Scope: scope, Dimension: amount.Dimension, Limit: limit.Limit, Current: current, Requested: requested}
			}
			if hasLine {
				continue
			}
			result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_usage SET `+stateColumn+` = `+stateColumn+` + ?, updated_at_us = `+s.nowMicros()+`
				WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ?`),
				amount.Value, scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension)
			if err != nil {
				return err
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return fmt.Errorf("renewal quota usage row is unavailable")
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_quota_reservation_lines
				(reservation_id, scope_kind, scope_id, organization_id, dimension, amount) VALUES (?, ?, ?, ?, ?, ?)`),
				reservationID, scope.Kind, scope.ID, scope.OrganizationID, amount.Dimension, amount.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ExpireStaleQuotaReservations(ctx context.Context, limit uint32) (uint32, error) {
	if limit == 0 || limit > maximumExpirationSweepBatch {
		return 0, fmt.Errorf("quota reservation expiration limit must be 1-%d", maximumExpirationSweepBatch)
	}
	for attempt := 0; ; attempt++ {
		expired, err := s.expireStaleQuotaReservations(ctx, limit)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return expired, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return 0, err
		}
	}
}

func (s *Store) expireStaleQuotaReservations(ctx context.Context, limit uint32) (uint32, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT reservation.reservation_id, reservation.instance_id, reservation.operation_id
		FROM labcentral_quota_reservations reservation
		JOIN labcentral_jobs job ON job.operation_id = reservation.operation_id
		JOIN labcentral_local_instances instance ON instance.instance_id = reservation.instance_id
		WHERE reservation.state = 'reserved'
		AND (reservation.expires_at_us <= ` + s.nowMicros() + ` OR
			(instance.expires_at_us > 0 AND instance.expires_at_us <= ` + s.nowMicros() + `))
		AND (job.state = ? OR (job.state = ? AND job.lease_until_us <= ` + s.nowMicros() + `))
		AND instance.phase IN (?, ?, ?, ?)
		AND NOT EXISTS (SELECT 1 FROM labcentral_runtime_invocations invocation
			WHERE invocation.operation_id = reservation.operation_id)
		ORDER BY reservation.expires_at_us, reservation.reservation_id LIMIT ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE OF reservation, job, instance SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), domain.OperationQueued, domain.OperationRunning,
		domain.InstancePending, domain.InstanceValidating, domain.InstancePlanned, domain.InstanceApplying, limit)
	if err != nil {
		return 0, fmt.Errorf("list stale quota reservations: %w", err)
	}
	type staleReservation struct {
		id          domain.QuotaReservationID
		instanceID  domain.InstanceID
		operationID domain.OperationID
	}
	candidates := make([]staleReservation, 0)
	for rows.Next() {
		var candidate staleReservation
		if err := rows.Scan(&candidate.id, &candidate.instanceID, &candidate.operationID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var expired uint32
	for _, candidate := range candidates {
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET state = ?, lease_owner = NULL, lease_until_us = 0
			WHERE operation_id = ?
			AND (state = ? OR (state = ? AND lease_until_us <= `+s.nowMicros()+`))
			AND NOT EXISTS (
				SELECT 1 FROM labcentral_runtime_invocations invocation WHERE invocation.operation_id = ?)`),
			domain.OperationTimedOut, candidate.operationID, domain.OperationQueued, domain.OperationRunning, candidate.operationID)
		if err != nil {
			return expired, fmt.Errorf("expire stale quota operation: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_operations
			SET message = ?, error_code = ?, updated_at_us = `+s.nowMicros()+` WHERE operation_id = ?`),
			"Quota reservation expired before provider work began", "quota_reservation_expired", candidate.operationID); err != nil {
			return expired, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances
			SET phase = ?, error_code = ?, error_message = ?, resource_version = resource_version + 1,
			updated_at_us = `+s.nowMicros()+` WHERE instance_id = ? AND phase IN (?, ?, ?, ?)`), domain.InstanceFailed,
			"quota_reservation_expired", "Quota reservation expired before provider work began", candidate.instanceID,
			domain.InstancePending, domain.InstanceValidating, domain.InstancePlanned, domain.InstanceApplying); err != nil {
			return expired, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_job_aggregates
			SET phase = 'complete', operation_state = ?, instance_phase = ?, resource_version = resource_version + 1
			WHERE aggregate_id = ?`), domain.OperationTimedOut, domain.InstanceFailed, candidate.instanceID); err != nil {
			return expired, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_provider_allocation_claims
			SET state = 'released', updated_at_us = `+s.nowMicros()+`
			WHERE instance_id = ? AND state <> 'released'`), candidate.instanceID); err != nil {
			return expired, fmt.Errorf("release allocation claims for expired reservation: %w", err)
		}
		if err := s.transitionInstanceQuotaTx(ctx, tx, candidate.instanceID, "released", false); err != nil {
			return expired, err
		}
		if err := s.appendQuotaReservationExpiredTx(ctx, tx, candidate); err != nil {
			return expired, err
		}
		expired++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit stale quota reservation expiration: %w", err)
	}
	return expired, nil
}

func (s *Store) appendQuotaReservationExpiredTx(ctx context.Context, tx *sql.Tx, candidate struct {
	id          domain.QuotaReservationID
	instanceID  domain.InstanceID
	operationID domain.OperationID
}) error {
	var projectID domain.ProjectID
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT instance.project_id, project.organization_id
		FROM labcentral_local_instances instance JOIN labcentral_projects project ON project.project_id = instance.project_id
		WHERE instance.instance_id = ?`), candidate.instanceID).Scan(&projectID, &organizationID); err != nil {
		return err
	}
	payloadBytes, err := json.Marshal(map[string]any{
		"instanceId": candidate.instanceID, "operationId": candidate.operationID,
		"quotaReservationId": candidate.id, "reason": "expired_before_provider_work",
	})
	if err != nil {
		return err
	}
	redacted, err := application.NewRedactedAuditPayload(payloadBytes)
	if err != nil {
		return err
	}
	safe, err := application.NewSafeEventPayload(payloadBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(candidate.id))
	suffix := hex.EncodeToString(digest[:16])
	scope := application.RecordScope{OrganizationID: organizationID, ProjectID: projectID}
	actor := application.RecordActor{Kind: application.RecordActorSystem, ID: string(application.SystemExpirationActorID)}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: "audit.quota-expired." + suffix, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditOperation,
		Action: "instance.quota-reservation.expire", Outcome: application.AuditSucceeded, Scope: scope, Actor: actor,
		AggregateType: "lab-instance", AggregateID: string(candidate.instanceID), RedactedPayload: redacted,
	}); err != nil {
		return err
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: "event.quota-expired." + suffix, SchemaVersion: "event.labcentral.io/v1",
		Type: "lab-instance.quota-reservation-expired", Scope: scope, Actor: actor,
		AggregateType: "lab-instance", AggregateID: string(candidate.instanceID), SafePayload: safe,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) ListExpirationWarnings(ctx context.Context, limit uint32) ([]application.ExpirationCandidate, error) {
	return s.listExpirationCandidates(ctx, limit, false)
}

func (s *Store) ListExpiredInstances(ctx context.Context, limit uint32) ([]application.ExpirationCandidate, error) {
	return s.listExpirationCandidates(ctx, limit, true)
}

func (s *Store) listExpirationCandidates(ctx context.Context, limit uint32, expired bool) ([]application.ExpirationCandidate, error) {
	if limit == 0 || limit > maximumExpirationSweepBatch {
		return nil, fmt.Errorf("expiration candidate limit must be 1-%d", maximumExpirationSweepBatch)
	}
	where := `expires_at_us > 0 AND expiration_warning_at_us > 0 AND expiration_warning_emitted_at_us = 0
		AND expiration_warning_at_us <= ` + s.nowMicros()
	order := `expiration_warning_at_us`
	if expired {
		where = `expires_at_us > 0 AND expires_at_us + expiration_grace_seconds * 1000000 <= ` + s.nowMicros()
		order = `expires_at_us`
	}
	query := `SELECT instance_id, expires_at_us, expiration_warning_at_us, expiration_grace_seconds, resource_version
		FROM labcentral_local_instances WHERE desired_lifecycle <> ? AND phase <> ? AND ` + where + `
		ORDER BY ` + order + `, instance_id LIMIT ?`
	rows, err := s.readDB().QueryContext(ctx, s.bind(query), domain.LifecycleDestroyed, domain.InstanceDestroyed, limit)
	if err != nil {
		return nil, fmt.Errorf("list expiration candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]application.ExpirationCandidate, 0)
	for rows.Next() {
		var candidate application.ExpirationCandidate
		var expiresAt, warningAt, grace, version int64
		if err := rows.Scan(&candidate.InstanceID, &expiresAt, &warningAt, &grace, &version); err != nil {
			return nil, err
		}
		if expiresAt <= 0 || warningAt <= 0 || grace < 0 || version <= 0 {
			return nil, fmt.Errorf("stored expiration candidate is invalid")
		}
		candidate.ExpiresAt = time.UnixMicro(expiresAt).UTC()
		candidate.WarningAt = time.UnixMicro(warningAt).UTC()
		candidate.GraceSeconds = uint64(grace)
		candidate.ResourceVersion = domain.ResourceVersion(version)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s *Store) MarkExpirationWarning(ctx context.Context, request application.MarkExpirationWarningRequest) (bool, error) {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return false, err
	}
	if _, err := domain.NewInstanceID(string(request.Candidate.InstanceID)); err != nil || request.Candidate.ExpiresAt.IsZero() {
		return false, fmt.Errorf("expiration warning candidate is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET
		expiration_warning_emitted_at_us = `+s.nowMicros()+`, resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE instance_id = ? AND expires_at_us = ? AND expiration_warning_at_us > 0
		AND expiration_warning_at_us <= `+s.nowMicros()+` AND expiration_warning_emitted_at_us = 0
		AND desired_lifecycle <> ? AND phase <> ?`), request.Candidate.InstanceID, request.Candidate.ExpiresAt.UTC().UnixMicro(),
		domain.LifecycleDestroyed, domain.InstanceDestroyed)
	if err != nil {
		return false, fmt.Errorf("mark expiration warning: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return false, tx.Commit()
	}
	instance, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceRecordSelect+` WHERE instance_id = ?`), request.Candidate.InstanceID))
	if err != nil {
		return false, err
	}
	if err := s.appendInstanceLifetimeGovernanceTx(ctx, tx, instance, application.SystemExpirationActorID,
		request.GovernanceRecordIDs, "instance.expiration.warning", "lab.instance.expiring", application.RecordActorSystem); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) appendInstanceLifetimeGovernanceTx(ctx context.Context, tx *sql.Tx, instance application.InstanceRecord, actor domain.UserID, ids application.GovernanceRecordIDs, action, eventType string, actorKind application.RecordActorKind) error {
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), instance.ProjectID).Scan(&organizationID); err != nil {
		return err
	}
	payloadBytes, err := json.Marshal(struct {
		InstanceID      domain.InstanceID      `json:"instanceId"`
		ExpiresAt       time.Time              `json:"expiresAt"`
		MaximumExpires  time.Time              `json:"maximumExpiresAt"`
		ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	}{instance.ID, instance.ExpiresAt, instance.MaximumExpiresAt, instance.ResourceVersion})
	if err != nil {
		return err
	}
	redacted, err := application.NewRedactedAuditPayload(payloadBytes)
	if err != nil {
		return err
	}
	safe, err := application.NewSafeEventPayload(payloadBytes)
	if err != nil {
		return err
	}
	scope := application.RecordScope{OrganizationID: organizationID, ProjectID: instance.ProjectID}
	recordActor := application.RecordActor{Kind: actorKind, ID: string(actor)}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditOperation,
		Action: action, Outcome: application.AuditSucceeded, Scope: scope, Actor: recordActor,
		AggregateType: "lab-instance", AggregateID: string(instance.ID), RedactedPayload: redacted,
	}); err != nil {
		return err
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: eventType, Scope: scope, Actor: recordActor,
		AggregateType: "lab-instance", AggregateID: string(instance.ID), SafePayload: safe,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) RetainInstanceQuota(ctx context.Context, fence application.Fence) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("retain instance quota: %w", err)
		}
	}()
	for attempt := 0; ; attempt++ {
		err = s.retainInstanceQuota(ctx, fence)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return err
		}
		if err = waitTransactionRetry(ctx, attempt); err != nil {
			return err
		}
	}
}
func (s *Store) retainInstanceQuota(ctx context.Context, fence application.Fence) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, fence); err != nil {
		return err
	}
	var instanceID domain.InstanceID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT instance_id FROM labcentral_jobs WHERE operation_id = ?`), fence.OperationID).Scan(&instanceID); err != nil {
		return fmt.Errorf("load quota reservation instance: %w", err)
	}
	if err := s.transitionInstanceQuotaTx(ctx, tx, instanceID, "retained", true); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ApplyExecutionAllowed(ctx context.Context, fence application.Fence) (allowed bool, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("apply execution policy transaction: %w", err)
		}
	}()
	for attempt := 0; ; attempt++ {
		allowed, err = s.applyExecutionAllowed(ctx, fence)
		if !isRetryableTransactionConflict(err) || attempt+1 >= maximumTransactionAttempts {
			return allowed, err
		}
		if err = waitTransactionRetry(ctx, attempt); err != nil {
			return false, err
		}
	}
}
func (s *Store) applyExecutionAllowed(ctx context.Context, fence application.Fence) (bool, error) {
	// requireFence deliberately locks the running job on PostgreSQL so an
	// expiration transition cannot race this policy decision.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireFence(ctx, tx, fence); err != nil {
		return false, err
	}
	var allowed int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT CASE WHEN instance.desired_lifecycle = ?
		AND (instance.expires_at_us = 0 OR instance.expires_at_us > `+s.nowMicros()+`) THEN 1 ELSE 0 END
		FROM labcentral_local_instances instance JOIN labcentral_jobs job ON job.instance_id = instance.instance_id
		WHERE job.operation_id = ?`), domain.LifecycleRunning, fence.OperationID).Scan(&allowed); err != nil {
		return false, fmt.Errorf("check apply execution policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return allowed != 0, nil
}

func (s *Store) finalizeInstanceQuotaTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, kind application.OperationKind, state domain.OperationState, phase domain.InstancePhase) error {
	targetState := "retained"
	reconciliationRequired := true
	switch {
	case kind == application.OperationApply && state == domain.OperationSucceeded && phase == domain.InstanceReady:
		targetState, reconciliationRequired = "committed", false
	case kind == application.OperationApply && state == domain.OperationFailed && phase == domain.InstanceFailed:
		targetState, reconciliationRequired = "released", false
	case kind == application.OperationDestroy && state == domain.OperationSucceeded && phase == domain.InstanceDestroyed:
		targetState, reconciliationRequired = "released", false
	}
	return s.transitionInstanceQuotaTx(ctx, tx, instanceID, targetState, reconciliationRequired)
}

type quotaReservationLine struct {
	scope  application.QuotaScope
	dim    application.QuotaDimension
	amount uint64
}

func (s *Store) transitionInstanceQuotaTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, targetState string, reconciliationRequired bool) error {
	query := `SELECT reservation_id, state FROM labcentral_quota_reservations WHERE instance_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	var reservationID domain.QuotaReservationID
	var currentState string
	if err := tx.QueryRowContext(ctx, s.bind(query), instanceID).Scan(&reservationID, &currentState); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("instance quota reservation is missing")
	} else if err != nil {
		return fmt.Errorf("load instance quota reservation: %w", err)
	}
	if currentState == targetState {
		if reconciliationRequired {
			_, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_reservations
				SET reconciliation_required = 1, updated_at_us = `+s.nowMicros()+` WHERE reservation_id = ?`), reservationID)
			return err
		}
		return nil
	}
	if currentState == "released" {
		return fmt.Errorf("released quota reservation cannot transition to %s", targetState)
	}
	allowed := false
	switch currentState {
	case "reserved":
		allowed = targetState == "committed" || targetState == "retained" || targetState == "released"
	case "committed":
		allowed = targetState == "retained" || targetState == "released"
	case "retained":
		allowed = targetState == "committed" || targetState == "released"
	}
	if !allowed {
		return fmt.Errorf("unsupported quota reservation transition %s to %s", currentState, targetState)
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT scope_kind, scope_id, organization_id, dimension, amount
		FROM labcentral_quota_reservation_lines WHERE reservation_id = ?
		ORDER BY scope_kind, scope_id, organization_id, dimension`), reservationID)
	if err != nil {
		return fmt.Errorf("list quota reservation lines: %w", err)
	}
	var lines []quotaReservationLine
	for rows.Next() {
		var line quotaReservationLine
		var amount int64
		if err := rows.Scan(&line.scope.Kind, &line.scope.ID, &line.scope.OrganizationID, &line.dim, &amount); err != nil {
			_ = rows.Close()
			return err
		}
		if amount <= 0 {
			_ = rows.Close()
			return fmt.Errorf("stored quota reservation line is invalid")
		}
		line.amount = uint64(amount)
		lines = append(lines, line)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fromColumn := quotaUsageStateColumn(currentState)
	if fromColumn == "" {
		return fmt.Errorf("stored quota reservation state %q is invalid", currentState)
	}
	for _, line := range lines {
		statement := `UPDATE labcentral_quota_usage SET ` + fromColumn + ` = ` + fromColumn + ` - ?, updated_at_us = ` + s.nowMicros() + `
			WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ? AND ` + fromColumn + ` >= ?`
		arguments := []any{line.amount, line.scope.Kind, line.scope.ID, line.scope.OrganizationID, line.dim, line.amount}
		if targetState != "released" {
			toColumn := quotaUsageStateColumn(targetState)
			statement = `UPDATE labcentral_quota_usage SET ` + fromColumn + ` = ` + fromColumn + ` - ?, ` + toColumn + ` = ` + toColumn + ` + ?, updated_at_us = ` + s.nowMicros() + `
				WHERE scope_kind = ? AND scope_id = ? AND organization_id = ? AND dimension = ? AND ` + fromColumn + ` >= ?`
			arguments = []any{line.amount, line.amount, line.scope.Kind, line.scope.ID, line.scope.OrganizationID, line.dim, line.amount}
		}
		result, err := tx.ExecContext(ctx, s.bind(statement), arguments...)
		if err != nil {
			return fmt.Errorf("transition quota usage: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("quota usage does not match its reservation ledger")
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_quota_reservations
		SET state = ?, reconciliation_required = ?, updated_at_us = `+s.nowMicros()+` WHERE reservation_id = ?`),
		targetState, quotaBoolean(reconciliationRequired), reservationID); err != nil {
		return fmt.Errorf("transition quota reservation: %w", err)
	}
	return nil
}

func quotaUsageStateColumn(state string) string {
	switch state {
	case "reserved":
		return "reserved_value"
	case "committed":
		return "committed_value"
	case "retained":
		return "retained_value"
	default:
		return ""
	}
}

func lifetimePoliciesDigest(policies []application.LifetimePolicyRecord) domain.SHA256Digest {
	copyOf := append([]application.LifetimePolicyRecord(nil), policies...)
	sort.Slice(copyOf, func(i, j int) bool { return quotaScopeIdentity(copyOf[i].Scope) < quotaScopeIdentity(copyOf[j].Scope) })
	hash := sha256.New()
	for _, policy := range copyOf {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%t\x00%t\x00%d\x00",
			policy.Scope.Kind, policy.Scope.ID, policy.Scope.OrganizationID, policy.DefaultTTLSeconds,
			policy.MaximumTTLSeconds, policy.RenewalWindowSeconds, policy.WarningBeforeSeconds,
			policy.DestructionGraceSecs, policy.RenewalAllowed, policy.PersistentAllowed, policy.ResourceVersion)
	}
	return domain.SHA256Digest(hex.EncodeToString(hash.Sum(nil)))
}

func (s *Store) databaseNowTx(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var micros int64
	if err := tx.QueryRowContext(ctx, `SELECT `+s.nowMicros()).Scan(&micros); err != nil {
		return time.Time{}, fmt.Errorf("read database time: %w", err)
	}
	if micros <= 0 {
		return time.Time{}, fmt.Errorf("database returned invalid time")
	}
	return time.UnixMicro(micros).UTC(), nil
}

func quotaLimitIdentity(scope application.QuotaScope, dimension application.QuotaDimension) string {
	return hashedGovernanceIdentity("quota", quotaScopeIdentity(scope)+"\x00"+string(dimension))
}

func lifetimePolicyIdentity(scope application.QuotaScope) string {
	return hashedGovernanceIdentity("lifetime", quotaScopeIdentity(scope))
}

func quotaScopeIdentity(scope application.QuotaScope) string {
	return string(scope.Kind) + "\x00" + scope.ID + "\x00" + string(scope.OrganizationID)
}

func hashedGovernanceIdentity(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return prefix + "." + hex.EncodeToString(digest[:])
}

func (s *Store) lockQuotaKey(ctx context.Context, tx *sql.Tx, key string) error {
	if s.dialect != jobsdb.DialectPostgreSQL {
		return nil
	}
	digest := sha256.Sum256([]byte("labcentral.quota-lock/v1\x00" + key))
	lockID := int64(uint64(digest[0])<<56 | uint64(digest[1])<<48 | uint64(digest[2])<<40 | uint64(digest[3])<<32 |
		uint64(digest[4])<<24 | uint64(digest[5])<<16 | uint64(digest[6])<<8 | uint64(digest[7]))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID); err != nil {
		return fmt.Errorf("serialize quota mutation: %w", err)
	}
	return nil
}

func (s *Store) lockApplicableQuotaPolicies(ctx context.Context, tx *sql.Tx, scopes []application.QuotaScope, amounts []application.QuotaAmount) error {
	if err := s.lockApplicableLifetimePolicies(ctx, tx, scopes); err != nil {
		return err
	}
	keys := make([]string, 0, len(scopes)*len(amounts))
	for _, scope := range scopes {
		for _, amount := range amounts {
			keys = append(keys, quotaLimitIdentity(scope, amount.Dimension))
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := s.lockQuotaKey(ctx, tx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) lockApplicableLifetimePolicies(ctx context.Context, tx *sql.Tx, scopes []application.QuotaScope) error {
	keys := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		keys = append(keys, lifetimePolicyIdentity(scope))
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := s.lockQuotaKey(ctx, tx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validateQuotaScopeReferenceTx(ctx context.Context, tx *sql.Tx, scope application.QuotaScope) error {
	if err := application.ValidateQuotaScope(scope); err != nil {
		return err
	}
	var exists int
	var err error
	switch scope.Kind {
	case application.QuotaScopePlatform:
		return nil
	case application.QuotaScopeOrganization:
		err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_organizations WHERE organization_id = ?`), scope.ID).Scan(&exists)
	case application.QuotaScopeProject:
		err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_projects WHERE project_id = ? AND organization_id = ?`), scope.ID, scope.OrganizationID).Scan(&exists)
	case application.QuotaScopeTeam:
		err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_teams WHERE team_id = ? AND organization_id = ?`), scope.ID, scope.OrganizationID).Scan(&exists)
	case application.QuotaScopeUser:
		err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_users user_record
			WHERE user_record.user_id = ? AND EXISTS (
				SELECT 1 FROM labcentral_memberships membership
				WHERE membership.user_id = user_record.user_id AND membership.organization_id = ?
			)`), scope.ID, scope.OrganizationID).Scan(&exists)
	case application.QuotaScopeTarget:
		err = tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_local_provider_targets WHERE target_id = ?`), scope.ID).Scan(&exists)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return application.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("validate quota scope reference: %w", err)
	}
	return nil
}

func (s *Store) appendQuotaGovernanceTx(ctx context.Context, tx *sql.Tx, scope application.QuotaScope, actor domain.UserID, aggregateID string, ids application.GovernanceRecordIDs, action, eventType string, payloadValue any) error {
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return fmt.Errorf("encode quota governance payload: %w", err)
	}
	redacted, err := application.NewRedactedAuditPayload(payload)
	if err != nil {
		return err
	}
	safe, err := application.NewSafeEventPayload(payload)
	if err != nil {
		return err
	}
	recordScope := application.RecordScope{}
	switch scope.Kind {
	case application.QuotaScopeOrganization, application.QuotaScopeTeam, application.QuotaScopeUser:
		recordScope.OrganizationID = scope.OrganizationID
	case application.QuotaScopeProject:
		recordScope.OrganizationID, recordScope.ProjectID = scope.OrganizationID, domain.ProjectID(scope.ID)
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditAdministrator,
		Action: action, Outcome: application.AuditSucceeded, Scope: recordScope,
		Actor:         application.RecordActor{Kind: application.RecordActorUser, ID: string(actor)},
		AggregateType: "governance-policy", AggregateID: aggregateID, RedactedPayload: redacted,
	}); err != nil {
		return fmt.Errorf("append quota audit record: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: eventType, Scope: recordScope,
		Actor:         application.RecordActor{Kind: application.RecordActorUser, ID: string(actor)},
		AggregateType: "governance-policy", AggregateID: aggregateID, SafePayload: safe,
	}); err != nil {
		return fmt.Errorf("append quota event record: %w", err)
	}
	return nil
}

func quotaBoolean(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) saveGovernanceIdempotencyResult(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode governance idempotency result: %w", err)
	}
	if len(payload) == 0 || len(payload) > 1<<20 {
		return fmt.Errorf("governance idempotency result exceeds the supported size")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_governance_idempotency_results
		(subject_id, route_action, idempotency_key, result_payload) VALUES (?, ?, ?, ?)`),
		subject, action, key, payload); err != nil {
		return fmt.Errorf("persist governance idempotency result: %w", err)
	}
	return nil
}

func (s *Store) loadGovernanceIdempotencyResult(ctx context.Context, tx *sql.Tx, subject domain.UserID, action, key string, destination any) error {
	var payload []byte
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT result_payload FROM labcentral_governance_idempotency_results
		WHERE subject_id = ? AND route_action = ? AND idempotency_key = ?`), subject, action, key).Scan(&payload); err != nil {
		return fmt.Errorf("load governance idempotency result: %w", err)
	}
	if len(payload) == 0 || len(payload) > 1<<20 {
		return fmt.Errorf("stored governance idempotency result is invalid")
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return fmt.Errorf("decode governance idempotency result: %w", err)
	}
	return nil
}

func timeMicrosOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMicro()
}
