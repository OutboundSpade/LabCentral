package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const (
	MaxAuditPayloadBytes = 32 << 10
	MaxEventPayloadBytes = 64 << 10
	MaxAuditPageSize     = 200
	MaxAuditExportRows   = 10_000
	MaxAuditExportBytes  = 64 << 20
	MaxRetentionBatch    = 10_000

	maxRecordPayloadDepth   = 8
	maxRecordPayloadEntries = 1_024
	maxRecordContainerItems = 128
	maxRecordStringBytes    = 2_048
)

var (
	recordFieldPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	recordIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type AuditCategory string

const (
	AuditAuthentication AuditCategory = "authentication"
	AuditAuthorization  AuditCategory = "authorization"
	AuditTarget         AuditCategory = "target"
	AuditTemplate       AuditCategory = "template"
	AuditOperation      AuditCategory = "operation"
	AuditAccess         AuditCategory = "access"
	AuditAdministrator  AuditCategory = "administrator"
)

type AuditOutcome string

const (
	AuditAccepted  AuditOutcome = "accepted"
	AuditSucceeded AuditOutcome = "succeeded"
	AuditDenied    AuditOutcome = "denied"
	AuditFailed    AuditOutcome = "failed"
)

type RecordActorKind string

const (
	RecordActorUser      RecordActorKind = "user"
	RecordActorSystem    RecordActorKind = "system"
	RecordActorAnonymous RecordActorKind = "anonymous"
)

// RedactedAuditPayload is a bounded JSON object whose keys have passed the
// audit sink's secret-bearing-key checks. Producers must still construct it
// from a typed, deliberately redacted DTO; this type is not a secret scrubber.
type RedactedAuditPayload struct{ raw []byte }

func NewRedactedAuditPayload(raw []byte) (RedactedAuditPayload, error) {
	validated, err := validateRecordPayload(raw, MaxAuditPayloadBytes)
	if err != nil {
		return RedactedAuditPayload{}, fmt.Errorf("redacted audit payload: %w", err)
	}
	return RedactedAuditPayload{raw: validated}, nil
}

func (p RedactedAuditPayload) Bytes() []byte { return bytes.Clone(p.raw) }

func (p RedactedAuditPayload) MarshalJSON() ([]byte, error) {
	if _, err := validateRecordPayload(p.raw, MaxAuditPayloadBytes); err != nil {
		return nil, err
	}
	return bytes.Clone(p.raw), nil
}

func (p *RedactedAuditPayload) UnmarshalJSON(raw []byte) error {
	value, err := NewRedactedAuditPayload(raw)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// SafeEventPayload is the event counterpart to RedactedAuditPayload. Event
// producers must use a versioned typed DTO containing references, safe
// metadata, and digests rather than credentials, tokens, or guest data.
type SafeEventPayload struct{ raw []byte }

func NewSafeEventPayload(raw []byte) (SafeEventPayload, error) {
	validated, err := validateRecordPayload(raw, MaxEventPayloadBytes)
	if err != nil {
		return SafeEventPayload{}, fmt.Errorf("safe event payload: %w", err)
	}
	return SafeEventPayload{raw: validated}, nil
}

func (p SafeEventPayload) Bytes() []byte { return bytes.Clone(p.raw) }

func (p SafeEventPayload) MarshalJSON() ([]byte, error) {
	if _, err := validateRecordPayload(p.raw, MaxEventPayloadBytes); err != nil {
		return nil, err
	}
	return bytes.Clone(p.raw), nil
}

func (p *SafeEventPayload) UnmarshalJSON(raw []byte) error {
	value, err := NewSafeEventPayload(raw)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

type RecordScope struct {
	OrganizationID domain.OrganizationID `json:"organizationId,omitempty"`
	ProjectID      domain.ProjectID      `json:"projectId,omitempty"`
}

type RecordActor struct {
	Kind RecordActorKind `json:"kind"`
	ID   string          `json:"id,omitempty"`
}

type RecordLinks struct {
	CorrelationID string             `json:"correlationId,omitempty"`
	CausationID   string             `json:"causationId,omitempty"`
	OperationID   domain.OperationID `json:"operationId,omitempty"`
}

type AppendAuditRecord struct {
	ID              string               `json:"id"`
	SchemaVersion   string               `json:"schemaVersion"`
	Category        AuditCategory        `json:"category"`
	Action          string               `json:"action"`
	Outcome         AuditOutcome         `json:"outcome"`
	Scope           RecordScope          `json:"scope"`
	Actor           RecordActor          `json:"actor"`
	AggregateType   string               `json:"aggregateType"`
	AggregateID     string               `json:"aggregateId"`
	Links           RecordLinks          `json:"links"`
	RedactedPayload RedactedAuditPayload `json:"payload"`
}

type AuditRecord struct {
	AppendAuditRecord
	StreamPosition uint64    `json:"streamPosition"`
	OccurredAt     time.Time `json:"occurredAt"`
}

type AppendEventRecord struct {
	ID            string           `json:"id"`
	SchemaVersion string           `json:"schemaVersion"`
	Type          string           `json:"type"`
	Scope         RecordScope      `json:"scope"`
	Actor         RecordActor      `json:"actor"`
	AggregateType string           `json:"aggregateType"`
	AggregateID   string           `json:"aggregateId"`
	Links         RecordLinks      `json:"links"`
	SafePayload   SafeEventPayload `json:"payload"`
}

type EventRecord struct {
	AppendEventRecord
	AggregateSequence domain.EventSequence `json:"aggregateSequence"`
	StreamPosition    uint64               `json:"streamPosition"`
	OccurredAt        time.Time            `json:"occurredAt"`
}

type RecordCursor struct {
	StreamPosition uint64 `json:"streamPosition"`
}

type AuditRecordFilter struct {
	Scope          RecordScope
	ActorID        string
	Category       AuditCategory
	AggregateType  string
	AggregateID    string
	OccurredFrom   time.Time
	OccurredBefore time.Time
	After          *RecordCursor
	Limit          uint32
}

type EventRecordFilter struct {
	Scope          RecordScope
	ActorID        string
	Type           string
	AggregateType  string
	AggregateID    string
	OccurredFrom   time.Time
	OccurredBefore time.Time
	After          *RecordCursor
	Limit          uint32
}

type AuditRecordPage struct {
	Records    []AuditRecord
	NextCursor *RecordCursor
}

type EventRecordPage struct {
	Records    []EventRecord
	NextCursor *RecordCursor
}

type AuditExportRequest struct {
	Filter     AuditRecordFilter
	MaxRecords uint32
	MaxBytes   uint64
}

type AuditExportResult struct {
	Records   uint32
	Bytes     uint64
	Last      *RecordCursor
	Truncated bool
}

type RetentionRequest struct {
	OccurredBefore time.Time
	BatchSize      uint32
}

// AuditEventStore is the read/export boundary for immutable audit and event
// records. Append calls use database time. Deletion is exposed only as a
// bounded retention operation; no update operation exists.
type AuditEventStore interface {
	AppendAuditRecord(context.Context, AppendAuditRecord) (AuditRecord, error)
	AppendEventRecord(context.Context, AppendEventRecord) (EventRecord, error)
	GetAuditRecord(context.Context, string) (AuditRecord, error)
	GetEventRecord(context.Context, string) (EventRecord, error)
	ListAuditRecords(context.Context, AuditRecordFilter) (AuditRecordPage, error)
	ListEventRecords(context.Context, EventRecordFilter) (EventRecordPage, error)
	ExportAuditRecords(context.Context, AuditExportRequest, io.Writer) (AuditExportResult, error)
	ApplyAuditRetention(context.Context, RetentionRequest) (int64, error)
	ApplyEventRetention(context.Context, RetentionRequest) (int64, error)
}

func ValidateAppendAuditRecord(record AppendAuditRecord) error {
	if err := validateRecordID("audit ID", record.ID); err != nil {
		return err
	}
	if err := validateRecordField("audit schema version", record.SchemaVersion); err != nil {
		return err
	}
	switch record.Category {
	case AuditAuthentication, AuditAuthorization, AuditTarget, AuditTemplate, AuditOperation, AuditAccess, AuditAdministrator:
	default:
		return fmt.Errorf("unsupported audit category %q", record.Category)
	}
	if err := validateRecordField("audit action", record.Action); err != nil {
		return err
	}
	switch record.Outcome {
	case AuditAccepted, AuditSucceeded, AuditDenied, AuditFailed:
	default:
		return fmt.Errorf("unsupported audit outcome %q", record.Outcome)
	}
	if err := validateRecordEnvelope(record.Scope, record.Actor, record.AggregateType, record.AggregateID, record.Links); err != nil {
		return err
	}
	_, err := validateRecordPayload(record.RedactedPayload.raw, MaxAuditPayloadBytes)
	return err
}

func ValidateAppendEventRecord(record AppendEventRecord) error {
	if err := validateRecordID("event ID", record.ID); err != nil {
		return err
	}
	if err := validateRecordField("event schema version", record.SchemaVersion); err != nil {
		return err
	}
	if err := validateRecordField("event type", record.Type); err != nil {
		return err
	}
	if err := validateRecordEnvelope(record.Scope, record.Actor, record.AggregateType, record.AggregateID, record.Links); err != nil {
		return err
	}
	_, err := validateRecordPayload(record.SafePayload.raw, MaxEventPayloadBytes)
	return err
}

func ValidateAuditRecordFilter(filter AuditRecordFilter) error {
	if err := validateRecordFilter(filter.Scope, filter.ActorID, filter.AggregateType, filter.AggregateID, filter.OccurredFrom, filter.OccurredBefore, filter.After, filter.Limit); err != nil {
		return err
	}
	if filter.Category != "" {
		switch filter.Category {
		case AuditAuthentication, AuditAuthorization, AuditTarget, AuditTemplate, AuditOperation, AuditAccess, AuditAdministrator:
		default:
			return fmt.Errorf("unsupported audit category %q", filter.Category)
		}
	}
	return nil
}

func ValidateEventRecordFilter(filter EventRecordFilter) error {
	if err := validateRecordFilter(filter.Scope, filter.ActorID, filter.AggregateType, filter.AggregateID, filter.OccurredFrom, filter.OccurredBefore, filter.After, filter.Limit); err != nil {
		return err
	}
	if filter.Type != "" {
		return validateRecordField("event type", filter.Type)
	}
	return nil
}

func ValidateAuditExportRequest(request AuditExportRequest) error {
	if request.MaxRecords == 0 || request.MaxRecords > MaxAuditExportRows {
		return fmt.Errorf("audit export max records must be 1-%d", MaxAuditExportRows)
	}
	if request.MaxBytes == 0 || request.MaxBytes > MaxAuditExportBytes {
		return fmt.Errorf("audit export max bytes must be 1-%d", MaxAuditExportBytes)
	}
	filter := request.Filter
	if filter.Limit == 0 {
		filter.Limit = 1
	}
	return ValidateAuditRecordFilter(filter)
}

func ValidateRetentionRequest(request RetentionRequest) error {
	if request.OccurredBefore.IsZero() || request.OccurredBefore.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("retention cutoff must be after the Unix epoch")
	}
	if request.BatchSize == 0 || request.BatchSize > MaxRetentionBatch {
		return fmt.Errorf("retention batch size must be 1-%d", MaxRetentionBatch)
	}
	return nil
}

func validateRecordFilter(scope RecordScope, actorID, aggregateType, aggregateID string, from, before time.Time, after *RecordCursor, limit uint32) error {
	if scope.OrganizationID != "" {
		if _, err := domain.NewOrganizationID(string(scope.OrganizationID)); err != nil {
			return err
		}
	}
	if scope.ProjectID != "" {
		if scope.OrganizationID == "" {
			return fmt.Errorf("project-scoped record filter requires organization ID")
		}
		if _, err := domain.NewProjectID(string(scope.ProjectID)); err != nil {
			return err
		}
	}
	if actorID != "" {
		if err := validateRecordID("actor ID", actorID); err != nil {
			return err
		}
	}
	if aggregateType != "" {
		if err := validateRecordField("aggregate type", aggregateType); err != nil {
			return err
		}
	}
	if aggregateID != "" {
		if aggregateType == "" {
			return fmt.Errorf("aggregate ID filter requires aggregate type")
		}
		if err := validateRecordID("aggregate ID", aggregateID); err != nil {
			return err
		}
	}
	if !from.IsZero() && from.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("record start time must be after the Unix epoch")
	}
	if !before.IsZero() && before.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("record end time must be after the Unix epoch")
	}
	if !from.IsZero() && !before.IsZero() && !from.Before(before) {
		return fmt.Errorf("record start time must precede end time")
	}
	if after != nil && after.StreamPosition == 0 {
		return fmt.Errorf("record cursor position must be positive")
	}
	if limit == 0 || limit > MaxAuditPageSize {
		return fmt.Errorf("record page size must be 1-%d", MaxAuditPageSize)
	}
	return nil
}

func validateRecordEnvelope(scope RecordScope, actor RecordActor, aggregateType, aggregateID string, links RecordLinks) error {
	if scope.OrganizationID != "" {
		if _, err := domain.NewOrganizationID(string(scope.OrganizationID)); err != nil {
			return err
		}
	}
	if scope.ProjectID != "" {
		if scope.OrganizationID == "" {
			return fmt.Errorf("project-scoped record requires organization ID")
		}
		if _, err := domain.NewProjectID(string(scope.ProjectID)); err != nil {
			return err
		}
	}
	if err := validateRecordActor(actor); err != nil {
		return err
	}
	if err := validateRecordField("aggregate type", aggregateType); err != nil {
		return err
	}
	if err := validateRecordID("aggregate ID", aggregateID); err != nil {
		return err
	}
	if links.CorrelationID != "" {
		if err := validateRecordID("correlation ID", links.CorrelationID); err != nil {
			return err
		}
	}
	if links.CausationID != "" {
		if err := validateRecordID("causation ID", links.CausationID); err != nil {
			return err
		}
	}
	if links.OperationID != "" {
		if _, err := domain.NewOperationID(string(links.OperationID)); err != nil {
			return err
		}
	}
	return nil
}

func validateRecordActor(actor RecordActor) error {
	switch actor.Kind {
	case RecordActorUser:
		if _, err := domain.NewUserID(actor.ID); err != nil {
			return fmt.Errorf("user actor: %w", err)
		}
	case RecordActorSystem:
		if err := validateRecordID("system actor ID", actor.ID); err != nil {
			return err
		}
	case RecordActorAnonymous:
		if actor.ID != "" {
			return fmt.Errorf("anonymous actor must not have an ID")
		}
	default:
		return fmt.Errorf("unsupported record actor kind %q", actor.Kind)
	}
	return nil
}

func validateRecordID(kind, value string) error {
	if len(value) == 0 || len(value) > 128 || !recordIDPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-128 bounded opaque safe characters", kind)
	}
	return nil
}

func validateRecordField(kind, value string) error {
	if len(value) == 0 || len(value) > 128 || !recordFieldPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-128 bounded safe characters", kind)
	}
	return nil
}

func validateRecordPayload(raw []byte, maximum int) ([]byte, error) {
	if len(raw) < 2 || len(raw) > maximum {
		return nil, fmt.Errorf("must contain 2-%d bytes", maximum)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("must be a JSON object")
	}
	entries := 0
	if err := validateRecordContainer(decoder, delim, 1, &entries); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("must contain one JSON value")
		}
		return nil, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("compact JSON: %w", err)
	}
	return compact.Bytes(), nil
}

func validateRecordContainer(decoder *json.Decoder, delim json.Delim, depth int, entries *int) error {
	if depth > maxRecordPayloadDepth {
		return fmt.Errorf("JSON nesting exceeds %d", maxRecordPayloadDepth)
	}
	items := 0
	seen := map[string]struct{}{}
	for decoder.More() {
		items++
		*entries++
		if items > maxRecordContainerItems || *entries > maxRecordPayloadEntries {
			return fmt.Errorf("JSON payload has too many entries")
		}
		if delim == '{' {
			token, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("read JSON object key: %w", err)
			}
			key, ok := token.(string)
			if !ok || key == "" || len(key) > 128 || strings.ContainsRune(key, '\x00') {
				return fmt.Errorf("JSON object keys must be 1-128 safe bytes")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if isSecretBearingKey(key) {
				return fmt.Errorf("secret-bearing JSON key %q is forbidden", key)
			}
		}
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read JSON value: %w", err)
		}
		if child, ok := token.(json.Delim); ok {
			if child != '{' && child != '[' {
				return fmt.Errorf("unexpected JSON delimiter %q", child)
			}
			if err := validateRecordContainer(decoder, child, depth+1, entries); err != nil {
				return err
			}
			continue
		}
		switch value := token.(type) {
		case string:
			if len(value) > maxRecordStringBytes || strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("JSON string exceeds safe bounds")
			}
		case json.Number:
			if len(value.String()) > 64 {
				return fmt.Errorf("JSON number exceeds safe bounds")
			}
		case bool, nil:
		default:
			return fmt.Errorf("unsupported JSON scalar")
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("close JSON container: %w", err)
	}
	if closing != matchingDelimiter(delim) {
		return fmt.Errorf("mismatched JSON delimiter")
	}
	return nil
}

func matchingDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}

func isSecretBearingKey(key string) bool {
	var normalized strings.Builder
	for _, character := range strings.ToLower(key) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	value := normalized.String()
	for _, safeSuffix := range []string{"id", "ref", "hash", "digest", "fingerprint"} {
		if strings.HasSuffix(value, safeSuffix) {
			return false
		}
	}
	for _, forbidden := range []string{
		"password", "passwd", "authorization", "cookie", "privatekey", "apikey",
		"accesstoken", "refreshtoken", "idtoken", "sessiontoken", "bearertoken",
		"secret", "credential", "credentials", "token",
	} {
		if strings.Contains(value, forbidden) {
			return true
		}
	}
	return false
}
