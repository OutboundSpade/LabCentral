package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// auditMigrationStatements is intentionally separate from Store.Migrate so
// TASK-PROD-003 can wire the additive migration without coupling this storage
// slice to other governance work in progress.
func auditMigrationStatements(integerType, blobType string) []string {
	statements := []string{
		`CREATE TABLE labcentral_record_stream_sequences (
			stream_name TEXT PRIMARY KEY CHECK (stream_name IN ('audit', 'event')),
			last_position ` + integerType + ` NOT NULL CHECK (last_position > 0)
		)`,
		`CREATE TABLE labcentral_event_aggregate_sequences (
			aggregate_type TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			last_sequence ` + integerType + ` NOT NULL CHECK (last_sequence > 0),
			PRIMARY KEY (aggregate_type, aggregate_id)
		)`,
		`CREATE TABLE labcentral_audit_records (
			audit_id TEXT PRIMARY KEY CHECK (length(audit_id) BETWEEN 1 AND 128),
			stream_position ` + integerType + ` NOT NULL UNIQUE CHECK (stream_position > 0),
			schema_version TEXT NOT NULL CHECK (length(schema_version) BETWEEN 1 AND 128),
			category TEXT NOT NULL CHECK (category IN ('authentication', 'authorization', 'target', 'template', 'operation', 'access', 'administrator')),
			action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 128),
			outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'succeeded', 'denied', 'failed')),
			organization_id TEXT NOT NULL DEFAULT '' CHECK (length(organization_id) <= 128),
			project_id TEXT NOT NULL DEFAULT '' CHECK (length(project_id) <= 128),
			actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'system', 'anonymous')),
			actor_id TEXT NOT NULL DEFAULT '' CHECK (length(actor_id) <= 128),
			aggregate_type TEXT NOT NULL CHECK (length(aggregate_type) BETWEEN 1 AND 128),
			aggregate_id TEXT NOT NULL CHECK (length(aggregate_id) BETWEEN 1 AND 128),
			correlation_id TEXT NOT NULL DEFAULT '' CHECK (length(correlation_id) <= 128),
			causation_id TEXT NOT NULL DEFAULT '' CHECK (length(causation_id) <= 128),
			operation_id TEXT NOT NULL DEFAULT '' CHECK (length(operation_id) <= 128),
			payload_json ` + blobType + ` NOT NULL CHECK (length(payload_json) BETWEEN 2 AND 32768),
			occurred_at_us ` + integerType + ` NOT NULL CHECK (occurred_at_us > 0),
			CHECK (project_id = '' OR organization_id <> ''),
			CHECK ((actor_kind = 'anonymous' AND actor_id = '') OR (actor_kind <> 'anonymous' AND actor_id <> ''))
		)`,
		`CREATE INDEX labcentral_audit_records_scope_idx
			ON labcentral_audit_records(organization_id, project_id, stream_position)`,
		`CREATE INDEX labcentral_audit_records_actor_idx
			ON labcentral_audit_records(actor_id, stream_position)`,
		`CREATE INDEX labcentral_audit_records_category_idx
			ON labcentral_audit_records(category, stream_position)`,
		`CREATE INDEX labcentral_audit_records_retention_idx
			ON labcentral_audit_records(occurred_at_us, stream_position)`,
		`CREATE TABLE labcentral_events (
			event_id TEXT PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 128),
			stream_position ` + integerType + ` NOT NULL UNIQUE CHECK (stream_position > 0),
			schema_version TEXT NOT NULL CHECK (length(schema_version) BETWEEN 1 AND 128),
			event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 128),
			organization_id TEXT NOT NULL DEFAULT '' CHECK (length(organization_id) <= 128),
			project_id TEXT NOT NULL DEFAULT '' CHECK (length(project_id) <= 128),
			actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'system', 'anonymous')),
			actor_id TEXT NOT NULL DEFAULT '' CHECK (length(actor_id) <= 128),
			aggregate_type TEXT NOT NULL CHECK (length(aggregate_type) BETWEEN 1 AND 128),
			aggregate_id TEXT NOT NULL CHECK (length(aggregate_id) BETWEEN 1 AND 128),
			aggregate_sequence ` + integerType + ` NOT NULL CHECK (aggregate_sequence > 0),
			correlation_id TEXT NOT NULL DEFAULT '' CHECK (length(correlation_id) <= 128),
			causation_id TEXT NOT NULL DEFAULT '' CHECK (length(causation_id) <= 128),
			operation_id TEXT NOT NULL DEFAULT '' CHECK (length(operation_id) <= 128),
			payload_json ` + blobType + ` NOT NULL CHECK (length(payload_json) BETWEEN 2 AND 65536),
			occurred_at_us ` + integerType + ` NOT NULL CHECK (occurred_at_us > 0),
			UNIQUE (aggregate_type, aggregate_id, aggregate_sequence),
			CHECK (project_id = '' OR organization_id <> ''),
			CHECK ((actor_kind = 'anonymous' AND actor_id = '') OR (actor_kind <> 'anonymous' AND actor_id <> ''))
		)`,
		`CREATE INDEX labcentral_events_scope_idx
			ON labcentral_events(organization_id, project_id, stream_position)`,
		`CREATE INDEX labcentral_events_aggregate_idx
			ON labcentral_events(aggregate_type, aggregate_id, aggregate_sequence)`,
		`CREATE INDEX labcentral_events_type_idx
			ON labcentral_events(event_type, stream_position)`,
		`CREATE INDEX labcentral_events_retention_idx
			ON labcentral_events(occurred_at_us, stream_position)`,
		`CREATE TABLE labcentral_event_outbox_deliveries (
			event_id TEXT NOT NULL REFERENCES labcentral_events(event_id) ON DELETE CASCADE,
			sink TEXT NOT NULL CHECK (length(sink) BETWEEN 1 AND 128),
			payload_json ` + blobType + ` NOT NULL CHECK (length(payload_json) BETWEEN 2 AND 65536),
			available_at_us ` + integerType + ` NOT NULL CHECK (available_at_us > 0),
			attempts ` + integerType + ` NOT NULL DEFAULT 0 CHECK (attempts >= 0),
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_us ` + integerType + ` NOT NULL DEFAULT 0,
			delivered_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			PRIMARY KEY (event_id, sink)
		)`,
		`CREATE INDEX labcentral_event_outbox_claim_idx
			ON labcentral_event_outbox_deliveries(delivered_at_us, available_at_us, lease_until_us, event_id, sink)`,
	}
	if strings.EqualFold(strings.TrimSpace(blobType), "BYTEA") {
		return append(statements,
			`CREATE FUNCTION labcentral_reject_audit_record_update() RETURNS trigger
				LANGUAGE plpgsql AS $$ BEGIN
					RAISE EXCEPTION 'audit records are immutable' USING ERRCODE = '55000';
				END $$`,
			`CREATE TRIGGER labcentral_audit_records_immutable
				BEFORE UPDATE ON labcentral_audit_records
				FOR EACH ROW EXECUTE FUNCTION labcentral_reject_audit_record_update()`,
			`CREATE FUNCTION labcentral_reject_event_record_update() RETURNS trigger
				LANGUAGE plpgsql AS $$ BEGIN
					RAISE EXCEPTION 'event records are immutable' USING ERRCODE = '55000';
				END $$`,
			`CREATE TRIGGER labcentral_events_immutable
				BEFORE UPDATE ON labcentral_events
				FOR EACH ROW EXECUTE FUNCTION labcentral_reject_event_record_update()`,
		)
	}
	return append(statements,
		`CREATE TRIGGER labcentral_audit_records_immutable
			BEFORE UPDATE ON labcentral_audit_records
			BEGIN SELECT RAISE(ABORT, 'audit records are immutable'); END`,
		`CREATE TRIGGER labcentral_events_immutable
			BEFORE UPDATE ON labcentral_events
			BEGIN SELECT RAISE(ABORT, 'event records are immutable'); END`,
	)
}

func (s *Store) AppendAuditRecord(ctx context.Context, appendRecord application.AppendAuditRecord) (application.AuditRecord, error) {
	if err := application.ValidateAppendAuditRecord(appendRecord); err != nil {
		return application.AuditRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.appendAuditRecord(ctx, appendRecord)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.AuditRecord{}, err
		}
	}
}

func (s *Store) appendAuditRecord(ctx context.Context, appendRecord application.AppendAuditRecord) (application.AuditRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.AuditRecord{}, fmt.Errorf("begin audit append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := s.appendAuditRecordTx(ctx, tx, appendRecord)
	if err != nil {
		return application.AuditRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.AuditRecord{}, fmt.Errorf("commit audit append: %w", err)
	}
	return record, nil
}

func (s *Store) appendAuditRecordTx(ctx context.Context, tx *sql.Tx, appendRecord application.AppendAuditRecord) (application.AuditRecord, error) {
	if err := application.ValidateAppendAuditRecord(appendRecord); err != nil {
		return application.AuditRecord{}, err
	}
	position, err := s.nextRecordStreamPosition(ctx, tx, "audit")
	if err != nil {
		return application.AuditRecord{}, err
	}
	var occurredAt int64
	err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_audit_records
		(audit_id, stream_position, schema_version, category, action, outcome,
		 organization_id, project_id, actor_kind, actor_id, aggregate_type, aggregate_id,
		 correlation_id, causation_id, operation_id, payload_json, occurred_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)
		RETURNING occurred_at_us`),
		appendRecord.ID, position, appendRecord.SchemaVersion, appendRecord.Category, appendRecord.Action, appendRecord.Outcome,
		appendRecord.Scope.OrganizationID, appendRecord.Scope.ProjectID, appendRecord.Actor.Kind, appendRecord.Actor.ID,
		appendRecord.AggregateType, appendRecord.AggregateID, appendRecord.Links.CorrelationID,
		appendRecord.Links.CausationID, appendRecord.Links.OperationID, appendRecord.RedactedPayload.Bytes(),
	).Scan(&occurredAt)
	if err != nil {
		return application.AuditRecord{}, fmt.Errorf("append audit record: %w", err)
	}
	if occurredAt <= 0 {
		return application.AuditRecord{}, fmt.Errorf("database returned invalid audit timestamp")
	}
	return application.AuditRecord{AppendAuditRecord: appendRecord, StreamPosition: position, OccurredAt: time.UnixMicro(occurredAt).UTC()}, nil
}

func (s *Store) AppendEventRecord(ctx context.Context, appendRecord application.AppendEventRecord) (application.EventRecord, error) {
	if err := application.ValidateAppendEventRecord(appendRecord); err != nil {
		return application.EventRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.appendEventRecord(ctx, appendRecord)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return application.EventRecord{}, err
		}
	}
}

func (s *Store) appendEventRecord(ctx context.Context, appendRecord application.AppendEventRecord) (application.EventRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return application.EventRecord{}, fmt.Errorf("begin event append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := s.appendEventRecordTx(ctx, tx, appendRecord)
	if err != nil {
		return application.EventRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.EventRecord{}, fmt.Errorf("commit event append: %w", err)
	}
	return record, nil
}

func (s *Store) appendEventRecordTx(ctx context.Context, tx *sql.Tx, appendRecord application.AppendEventRecord) (application.EventRecord, error) {
	if err := application.ValidateAppendEventRecord(appendRecord); err != nil {
		return application.EventRecord{}, err
	}
	position, err := s.nextRecordStreamPosition(ctx, tx, "event")
	if err != nil {
		return application.EventRecord{}, err
	}
	sequence, err := s.nextAggregateEventSequence(ctx, tx, appendRecord.AggregateType, appendRecord.AggregateID)
	if err != nil {
		return application.EventRecord{}, err
	}
	var occurredAt int64
	err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_events
		(event_id, stream_position, schema_version, event_type, organization_id, project_id,
		 actor_kind, actor_id, aggregate_type, aggregate_id, aggregate_sequence,
		 correlation_id, causation_id, operation_id, payload_json, occurred_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)
		RETURNING occurred_at_us`),
		appendRecord.ID, position, appendRecord.SchemaVersion, appendRecord.Type,
		appendRecord.Scope.OrganizationID, appendRecord.Scope.ProjectID, appendRecord.Actor.Kind,
		appendRecord.Actor.ID, appendRecord.AggregateType, appendRecord.AggregateID, sequence,
		appendRecord.Links.CorrelationID, appendRecord.Links.CausationID,
		appendRecord.Links.OperationID, appendRecord.SafePayload.Bytes(),
	).Scan(&occurredAt)
	if err != nil {
		return application.EventRecord{}, fmt.Errorf("append event record: %w", err)
	}
	if occurredAt <= 0 {
		return application.EventRecord{}, fmt.Errorf("database returned invalid event timestamp")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_event_outbox_deliveries
		(event_id, sink, payload_json, available_at_us)
		VALUES (?, 'local-events', ?, `+s.nowMicros()+`)`), appendRecord.ID, appendRecord.SafePayload.Bytes()); err != nil {
		return application.EventRecord{}, fmt.Errorf("append event outbox delivery: %w", err)
	}
	return application.EventRecord{
		AppendEventRecord: appendRecord, AggregateSequence: domain.EventSequence(sequence),
		StreamPosition: position, OccurredAt: time.UnixMicro(occurredAt).UTC(),
	}, nil
}

func (s *Store) nextRecordStreamPosition(ctx context.Context, tx *sql.Tx, stream string) (uint64, error) {
	var position int64
	err := tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_record_stream_sequences (stream_name, last_position)
		VALUES (?, 1)
		ON CONFLICT(stream_name) DO UPDATE SET last_position = labcentral_record_stream_sequences.last_position + 1
		RETURNING last_position`), stream).Scan(&position)
	if err != nil {
		return 0, fmt.Errorf("allocate %s stream position: %w", stream, err)
	}
	if position <= 0 {
		return 0, fmt.Errorf("database returned invalid %s stream position", stream)
	}
	return uint64(position), nil
}

func (s *Store) nextAggregateEventSequence(ctx context.Context, tx *sql.Tx, aggregateType, aggregateID string) (uint64, error) {
	var sequence int64
	err := tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_event_aggregate_sequences (aggregate_type, aggregate_id, last_sequence)
		VALUES (?, ?, 1)
		ON CONFLICT(aggregate_type, aggregate_id) DO UPDATE SET last_sequence = labcentral_event_aggregate_sequences.last_sequence + 1
		RETURNING last_sequence`), aggregateType, aggregateID).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("allocate aggregate event sequence: %w", err)
	}
	if sequence <= 0 {
		return 0, fmt.Errorf("database returned invalid aggregate event sequence")
	}
	return uint64(sequence), nil
}

func (s *Store) GetAuditRecord(ctx context.Context, id string) (application.AuditRecord, error) {
	if len(id) == 0 || len(id) > 128 {
		return application.AuditRecord{}, fmt.Errorf("audit ID must be 1-128 bytes")
	}
	return scanAuditRecord(s.db.QueryRowContext(ctx, s.bind(auditRecordSelect+` WHERE audit_id = ?`), id))
}

func (s *Store) GetEventRecord(ctx context.Context, id string) (application.EventRecord, error) {
	if len(id) == 0 || len(id) > 128 {
		return application.EventRecord{}, fmt.Errorf("event ID must be 1-128 bytes")
	}
	return scanEventRecord(s.db.QueryRowContext(ctx, s.bind(eventRecordSelect+` WHERE event_id = ?`), id))
}

const auditRecordSelect = `SELECT audit_id, stream_position, schema_version, category, action, outcome,
	organization_id, project_id, actor_kind, actor_id, aggregate_type, aggregate_id,
	correlation_id, causation_id, operation_id, payload_json, occurred_at_us
	FROM labcentral_audit_records`

const eventRecordSelect = `SELECT event_id, stream_position, schema_version, event_type,
	organization_id, project_id, actor_kind, actor_id, aggregate_type, aggregate_id, aggregate_sequence,
	correlation_id, causation_id, operation_id, payload_json, occurred_at_us
	FROM labcentral_events`

func (s *Store) ListAuditRecords(ctx context.Context, filter application.AuditRecordFilter) (application.AuditRecordPage, error) {
	if err := application.ValidateAuditRecordFilter(filter); err != nil {
		return application.AuditRecordPage{}, err
	}
	query, arguments := buildRecordQuery(auditRecordSelect, "category", string(filter.Category), filter.Scope,
		filter.ActorID, filter.AggregateType, filter.AggregateID, filter.OccurredFrom,
		filter.OccurredBefore, filter.After, filter.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return application.AuditRecordPage{}, fmt.Errorf("list audit records: %w", err)
	}
	defer rows.Close()
	records := make([]application.AuditRecord, 0, filter.Limit+1)
	for rows.Next() {
		record, err := scanAuditRecord(rows)
		if err != nil {
			return application.AuditRecordPage{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return application.AuditRecordPage{}, fmt.Errorf("iterate audit records: %w", err)
	}
	page := application.AuditRecordPage{Records: records}
	if len(records) > int(filter.Limit) {
		page.Records = records[:filter.Limit]
		page.NextCursor = &application.RecordCursor{StreamPosition: page.Records[len(page.Records)-1].StreamPosition}
	}
	return page, nil
}

func (s *Store) ListEventRecords(ctx context.Context, filter application.EventRecordFilter) (application.EventRecordPage, error) {
	if err := application.ValidateEventRecordFilter(filter); err != nil {
		return application.EventRecordPage{}, err
	}
	query, arguments := buildRecordQuery(eventRecordSelect, "event_type", filter.Type, filter.Scope,
		filter.ActorID, filter.AggregateType, filter.AggregateID, filter.OccurredFrom,
		filter.OccurredBefore, filter.After, filter.Limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return application.EventRecordPage{}, fmt.Errorf("list event records: %w", err)
	}
	defer rows.Close()
	records := make([]application.EventRecord, 0, filter.Limit+1)
	for rows.Next() {
		record, err := scanEventRecord(rows)
		if err != nil {
			return application.EventRecordPage{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return application.EventRecordPage{}, fmt.Errorf("iterate event records: %w", err)
	}
	page := application.EventRecordPage{Records: records}
	if len(records) > int(filter.Limit) {
		page.Records = records[:filter.Limit]
		page.NextCursor = &application.RecordCursor{StreamPosition: page.Records[len(page.Records)-1].StreamPosition}
	}
	return page, nil
}

func buildRecordQuery(base, typeColumn, typeValue string, scope application.RecordScope, actorID, aggregateType, aggregateID string, from, before time.Time, after *application.RecordCursor, limit uint32) (string, []any) {
	var query strings.Builder
	query.WriteString(base)
	query.WriteString(" WHERE 1 = 1")
	arguments := make([]any, 0, 9)
	add := func(clause string, value any) {
		query.WriteString(" AND ")
		query.WriteString(clause)
		arguments = append(arguments, value)
	}
	if scope.OrganizationID != "" {
		add("organization_id = ?", scope.OrganizationID)
	}
	if scope.ProjectID != "" {
		add("project_id = ?", scope.ProjectID)
	}
	if actorID != "" {
		add("actor_id = ?", actorID)
	}
	if typeValue != "" {
		add(typeColumn+" = ?", typeValue)
	}
	if aggregateType != "" {
		add("aggregate_type = ?", aggregateType)
	}
	if aggregateID != "" {
		add("aggregate_id = ?", aggregateID)
	}
	if !from.IsZero() {
		add("occurred_at_us >= ?", from.UTC().UnixMicro())
	}
	if !before.IsZero() {
		add("occurred_at_us < ?", before.UTC().UnixMicro())
	}
	if after != nil {
		add("stream_position > ?", after.StreamPosition)
	}
	query.WriteString(" ORDER BY stream_position LIMIT ?")
	arguments = append(arguments, uint64(limit)+1)
	return query.String(), arguments
}

func scanAuditRecord(row scanner) (application.AuditRecord, error) {
	var (
		record                                 application.AuditRecord
		organizationID, projectID, operationID string
		streamPosition, occurredAt             int64
		payload                                []byte
	)
	err := row.Scan(&record.ID, &streamPosition, &record.SchemaVersion, &record.Category,
		&record.Action, &record.Outcome, &organizationID, &projectID, &record.Actor.Kind,
		&record.Actor.ID, &record.AggregateType, &record.AggregateID, &record.Links.CorrelationID,
		&record.Links.CausationID, &operationID, &payload, &occurredAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return application.AuditRecord{}, sql.ErrNoRows
		}
		return application.AuditRecord{}, fmt.Errorf("scan audit record: %w", err)
	}
	if streamPosition <= 0 || occurredAt <= 0 {
		return application.AuditRecord{}, fmt.Errorf("stored audit record has invalid sequence or timestamp")
	}
	redacted, err := application.NewRedactedAuditPayload(payload)
	if err != nil {
		return application.AuditRecord{}, fmt.Errorf("stored audit payload is invalid: %w", err)
	}
	record.Scope = application.RecordScope{OrganizationID: domain.OrganizationID(organizationID), ProjectID: domain.ProjectID(projectID)}
	record.Links.OperationID = domain.OperationID(operationID)
	record.RedactedPayload = redacted
	record.StreamPosition = uint64(streamPosition)
	record.OccurredAt = time.UnixMicro(occurredAt).UTC()
	if err := application.ValidateAppendAuditRecord(record.AppendAuditRecord); err != nil {
		return application.AuditRecord{}, fmt.Errorf("stored audit record is invalid: %w", err)
	}
	return record, nil
}

func scanEventRecord(row scanner) (application.EventRecord, error) {
	var (
		record                                        application.EventRecord
		organizationID, projectID, operationID        string
		streamPosition, aggregateSequence, occurredAt int64
		payload                                       []byte
	)
	err := row.Scan(&record.ID, &streamPosition, &record.SchemaVersion, &record.Type,
		&organizationID, &projectID, &record.Actor.Kind, &record.Actor.ID,
		&record.AggregateType, &record.AggregateID, &aggregateSequence,
		&record.Links.CorrelationID, &record.Links.CausationID, &operationID, &payload, &occurredAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return application.EventRecord{}, sql.ErrNoRows
		}
		return application.EventRecord{}, fmt.Errorf("scan event record: %w", err)
	}
	if streamPosition <= 0 || aggregateSequence <= 0 || occurredAt <= 0 {
		return application.EventRecord{}, fmt.Errorf("stored event record has invalid sequence or timestamp")
	}
	safe, err := application.NewSafeEventPayload(payload)
	if err != nil {
		return application.EventRecord{}, fmt.Errorf("stored event payload is invalid: %w", err)
	}
	record.Scope = application.RecordScope{OrganizationID: domain.OrganizationID(organizationID), ProjectID: domain.ProjectID(projectID)}
	record.Links.OperationID = domain.OperationID(operationID)
	record.SafePayload = safe
	record.StreamPosition = uint64(streamPosition)
	record.AggregateSequence = domain.EventSequence(aggregateSequence)
	record.OccurredAt = time.UnixMicro(occurredAt).UTC()
	if err := application.ValidateAppendEventRecord(record.AppendEventRecord); err != nil {
		return application.EventRecord{}, fmt.Errorf("stored event record is invalid: %w", err)
	}
	return record, nil
}

func (s *Store) ExportAuditRecords(ctx context.Context, request application.AuditExportRequest, writer io.Writer) (application.AuditExportResult, error) {
	if writer == nil {
		return application.AuditExportResult{}, fmt.Errorf("audit export writer is nil")
	}
	if err := application.ValidateAuditExportRequest(request); err != nil {
		return application.AuditExportResult{}, err
	}
	result := application.AuditExportResult{}
	filter := request.Filter
	for result.Records < request.MaxRecords {
		remaining := request.MaxRecords - result.Records
		filter.Limit = min(remaining, application.MaxAuditPageSize)
		page, err := s.ListAuditRecords(ctx, filter)
		if err != nil {
			return result, err
		}
		for _, record := range page.Records {
			line, err := json.Marshal(record)
			if err != nil {
				return result, fmt.Errorf("encode audit export: %w", err)
			}
			line = append(line, '\n')
			if result.Bytes+uint64(len(line)) > request.MaxBytes {
				result.Truncated = true
				return result, nil
			}
			if err := writeAuditExportLine(writer, line); err != nil {
				return result, err
			}
			result.Records++
			result.Bytes += uint64(len(line))
			result.Last = &application.RecordCursor{StreamPosition: record.StreamPosition}
			if result.Records == request.MaxRecords {
				result.Truncated = page.NextCursor != nil || record.StreamPosition != page.Records[len(page.Records)-1].StreamPosition
				return result, nil
			}
		}
		if page.NextCursor == nil {
			return result, nil
		}
		filter.After = page.NextCursor
	}
	return result, nil
}

func writeAuditExportLine(writer io.Writer, line []byte) error {
	for len(line) > 0 {
		written, err := writer.Write(line)
		if err != nil {
			return fmt.Errorf("write audit export: %w", err)
		}
		if written <= 0 || written > len(line) {
			return io.ErrShortWrite
		}
		line = line[written:]
	}
	return nil
}

func (s *Store) ApplyAuditRetention(ctx context.Context, request application.RetentionRequest) (int64, error) {
	return s.applyRecordRetention(ctx, request, "labcentral_audit_records", "audit_id")
}

func (s *Store) ApplyEventRetention(ctx context.Context, request application.RetentionRequest) (int64, error) {
	if err := application.ValidateRetentionRequest(request); err != nil {
		return 0, err
	}
	// An event remains the retry source of truth while any sink delivery is
	// outstanding. Deleting it would cascade-delete the outbox row and violate
	// the at-least-once delivery contract during a prolonged sink outage.
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM labcentral_events WHERE event_id IN (
		SELECT event.event_id FROM labcentral_events AS event
		WHERE event.occurred_at_us < ?
		AND NOT EXISTS (
			SELECT 1 FROM labcentral_event_outbox_deliveries AS delivery
			WHERE delivery.event_id = event.event_id AND delivery.delivered_at_us = 0
		)
		ORDER BY event.occurred_at_us, event.stream_position LIMIT ?
	)`), request.OccurredBefore.UTC().UnixMicro(), request.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("apply event retention: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read event retention count: %w", err)
	}
	return deleted, nil
}

func (s *Store) applyRecordRetention(ctx context.Context, request application.RetentionRequest, table, idColumn string) (int64, error) {
	if err := application.ValidateRetentionRequest(request); err != nil {
		return 0, err
	}
	query := `DELETE FROM ` + table + ` WHERE ` + idColumn + ` IN (
		SELECT ` + idColumn + ` FROM ` + table + `
		WHERE occurred_at_us < ? ORDER BY occurred_at_us, stream_position LIMIT ?
	)`
	result, err := s.db.ExecContext(ctx, s.bind(query), request.OccurredBefore.UTC().UnixMicro(), request.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("apply record retention: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read record retention count: %w", err)
	}
	return deleted, nil
}

var _ application.AuditEventStore = (*Store)(nil)
