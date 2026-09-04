package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

type RecordQueryInput struct {
	OrganizationID string `query:"organizationId" maxLength:"128"`
	ProjectID      string `query:"projectId" maxLength:"128"`
	ActorID        string `query:"actorId" maxLength:"128"`
	AggregateType  string `query:"aggregateType" maxLength:"128"`
	AggregateID    string `query:"aggregateId" maxLength:"128"`
	OccurredFrom   string `query:"occurredFrom" maxLength:"64"`
	OccurredBefore string `query:"occurredBefore" maxLength:"64"`
	Cursor         string `query:"cursor" maxLength:"64"`
	Limit          uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

func registerAuditAndEvents(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type auditInput struct {
		RecordQueryInput
		Category application.AuditCategory `query:"category" enum:"authentication,authorization,target,template,operation,access,administrator"`
	}
	huma.Get(api, "/audit-records", func(ctx context.Context, input *auditInput) (*struct {
		Body struct {
			Items      []AuditRecordDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		filter := application.AuditRecordFilter{
			Scope: scope, ActorID: input.ActorID, Category: input.Category, AggregateType: input.AggregateType,
			AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: input.Limit,
		}
		if err := application.ValidateAuditRecordFilter(filter); err != nil {
			return nil, huma.Error400BadRequest("Audit record filter is invalid")
		}
		page, err := service.ListAuditRecords(ctx, filter)
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []AuditRecordDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]AuditRecordDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, auditRecordDTO(record))
		}
		output.Body.NextCursor = encodeRecordCursor(page.NextCursor)
		return output, nil
	})

	type eventInput struct {
		RecordQueryInput
		Type string `query:"type" maxLength:"128"`
	}
	huma.Get(api, "/events", func(ctx context.Context, input *eventInput) (*struct {
		Body struct {
			Items      []EventRecordDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		filter := application.EventRecordFilter{
			Scope: scope, ActorID: input.ActorID, Type: input.Type, AggregateType: input.AggregateType,
			AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: input.Limit,
		}
		if err := application.ValidateEventRecordFilter(filter); err != nil {
			return nil, huma.Error400BadRequest("Event record filter is invalid")
		}
		page, err := service.ListEventRecords(ctx, filter)
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []EventRecordDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]EventRecordDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, eventRecordDTO(record))
		}
		output.Body.NextCursor = encodeRecordCursor(page.NextCursor)
		return output, nil
	})

	type exportInput struct {
		RecordQueryInput
		Category   application.AuditCategory `query:"category" enum:"authentication,authorization,target,template,operation,access,administrator"`
		MaxRecords uint32                    `query:"maxRecords" default:"1000" minimum:"1" maximum:"10000"`
		MaxBytes   uint64                    `query:"maxBytes" default:"8388608" minimum:"1" maximum:"67108864"`
	}
	type exportOutput struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		Truncated          string `header:"X-LabCentral-Export-Truncated"`
		NextCursor         string `header:"X-LabCentral-Next-Cursor"`
		Body               []byte
	}
	huma.Get(api, "/audit-records/export", func(ctx context.Context, input *exportInput) (*exportOutput, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		request := application.AuditExportRequest{
			Filter: application.AuditRecordFilter{
				Scope: scope, ActorID: input.ActorID, Category: input.Category, AggregateType: input.AggregateType,
				AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: 1,
			},
			MaxRecords: input.MaxRecords, MaxBytes: input.MaxBytes,
		}
		if err := application.ValidateAuditExportRequest(request); err != nil {
			return nil, huma.Error400BadRequest("Audit export request is invalid")
		}
		result, body, err := service.ExportAuditRecords(ctx, request)
		if err != nil {
			return nil, apiError(err)
		}
		return &exportOutput{
			ContentType: "application/x-ndjson", ContentDisposition: `attachment; filename="labcentral-audit.ndjson"`,
			Truncated: strconv.FormatBool(result.Truncated), NextCursor: encodeRecordCursor(result.Last), Body: body,
		}, nil
	})
}

func authorizeRecordQuery(ctx context.Context, security *requestSecurity, input RecordQueryInput) (application.RecordScope, time.Time, time.Time, *application.RecordCursor, error) {
	var scope application.RecordScope
	authorizationScope := application.AuthorizationRequest{Action: application.ActionAuditRead, ScopeKind: application.ScopePlatform, ScopeID: "platform"}
	if input.OrganizationID != "" {
		organizationID, err := domain.NewOrganizationID(input.OrganizationID)
		if err != nil {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Organization ID is invalid")
		}
		scope.OrganizationID = organizationID
		authorizationScope.ScopeKind, authorizationScope.ScopeID = application.ScopeOrganization, string(organizationID)
	}
	if input.ProjectID != "" {
		if scope.OrganizationID == "" {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Project-scoped record queries require organizationId")
		}
		projectID, err := domain.NewProjectID(input.ProjectID)
		if err != nil {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Project ID is invalid")
		}
		scope.ProjectID = projectID
		authorizationScope.ScopeKind, authorizationScope.ScopeID = application.ScopeProject, string(projectID)
	}
	if err := authorize(ctx, security, authorizationScope); err != nil {
		return scope, time.Time{}, time.Time{}, nil, err
	}
	from, err := parseRecordTime(input.OccurredFrom)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("occurredFrom must be an RFC3339 timestamp")
	}
	before, err := parseRecordTime(input.OccurredBefore)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("occurredBefore must be an RFC3339 timestamp")
	}
	after, err := decodeRecordCursor(input.Cursor)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Record cursor is invalid")
	}
	return scope, from, before, after, nil
}

func parseRecordTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func encodeRecordCursor(cursor *application.RecordCursor) string {
	if cursor == nil || cursor.StreamPosition == 0 {
		return ""
	}
	var bytes [9]byte
	bytes[0] = 1
	binary.BigEndian.PutUint64(bytes[1:], cursor.StreamPosition)
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

func decodeRecordCursor(value string) (*application.RecordCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 9 || decoded[0] != 1 {
		return nil, fmt.Errorf("invalid record cursor")
	}
	position := binary.BigEndian.Uint64(decoded[1:])
	if position == 0 {
		return nil, fmt.Errorf("invalid record cursor")
	}
	return &application.RecordCursor{StreamPosition: position}, nil
}

const (
	templateCursorKind byte = 1
	revisionCursorKind byte = 2
)

func auditRecordDTO(source application.AuditRecord) AuditRecordDTO {
	return AuditRecordDTO{
		ID: source.ID, StreamPosition: source.StreamPosition, SchemaVersion: source.SchemaVersion,
		Category: source.Category, Action: source.Action, Outcome: source.Outcome, Scope: source.Scope,
		Actor: source.Actor, AggregateType: source.AggregateType, AggregateID: source.AggregateID,
		Links: source.Links, Payload: json.RawMessage(source.RedactedPayload.Bytes()), OccurredAt: source.OccurredAt,
	}
}

func eventRecordDTO(source application.EventRecord) EventRecordDTO {
	return EventRecordDTO{
		ID: source.ID, StreamPosition: source.StreamPosition, AggregateSequence: source.AggregateSequence,
		SchemaVersion: source.SchemaVersion, Type: source.Type, Scope: source.Scope, Actor: source.Actor,
		AggregateType: source.AggregateType, AggregateID: source.AggregateID, Links: source.Links,
		Payload: json.RawMessage(source.SafePayload.Bytes()), OccurredAt: source.OccurredAt,
	}
}
