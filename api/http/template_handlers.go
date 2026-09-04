package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

func registerTemplates(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type projectInput struct {
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
		Cursor    string `query:"cursor" maxLength:"256"`
		Limit     uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}
	projectScope := func(value string) (domain.ProjectID, error) {
		projectID, err := domain.NewProjectID(value)
		if err != nil {
			return "", huma.Error400BadRequest("Project ID is invalid")
		}
		return projectID, nil
	}
	huma.Get(api, "/lab-templates", func(ctx context.Context, input *projectInput) (*struct {
		Body struct {
			Items      []LabTemplateDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		projectID, err := projectScope(input.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateRead, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
			return nil, err
		}
		after, err := decodeLabTemplateCursor(input.Cursor)
		if err != nil {
			return nil, huma.Error400BadRequest("Lab Template cursor is invalid")
		}
		page, err := service.ListLabTemplates(ctx, controlplane.LabTemplateListRequest{ProjectID: projectID, After: after, Limit: input.Limit})
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []LabTemplateDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]LabTemplateDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, labTemplateDTO(record))
		}
		output.Body.NextCursor = encodeLabTemplateCursor(page.NextCursor)
		return output, nil
	})

	type createTemplateInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID   string `json:"projectId" minLength:"1" maxLength:"128"`
			Name        string `json:"name" minLength:"1" maxLength:"63"`
			Description string `json:"description,omitempty" maxLength:"4096"`
		}
	}
	huma.Post(api, "/lab-templates", func(ctx context.Context, input *createTemplateInput) (*versionedOutput[LabTemplateDTO], error) {
		projectID, err := projectScope(input.Body.ProjectID)
		if err != nil {
			return nil, err
		}
		actor, err := authorizeActor(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateCreate, ScopeKind: application.ScopeProject, ScopeID: string(projectID)})
		if err != nil {
			return nil, err
		}
		record, err := service.CreateLabTemplate(ctx, actor.user.ID, projectID, input.Body.Name, input.Body.Description, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type templateInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	loadTemplate := func(ctx context.Context, id, project string, action application.PermissionAction) (controlplane.LabTemplateRecord, error) {
		projectID, err := projectScope(project)
		if err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: action, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
		templateID, err := domain.NewTemplateID(id)
		if err != nil {
			return controlplane.LabTemplateRecord{}, huma.Error404NotFound("Lab template was not found")
		}
		record, err := service.GetLabTemplate(ctx, templateID)
		if err != nil || record.ProjectID != projectID {
			return controlplane.LabTemplateRecord{}, huma.Error404NotFound("Lab template was not found")
		}
		return record, nil
	}
	huma.Get(api, "/lab-templates/{id}", func(ctx context.Context, input *templateInput) (*versionedOutput[LabTemplateDTO], error) {
		record, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type updateTemplateInput struct {
		ID             string `path:"id" maxLength:"128"`
		ProjectID      string `query:"projectId" required:"true" maxLength:"128"`
		IfMatch        string `header:"If-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Name        string `json:"name" minLength:"1" maxLength:"63"`
			Description string `json:"description,omitempty" maxLength:"4096"`
		}
	}
	huma.Put(api, "/lab-templates/{id}", func(ctx context.Context, input *updateTemplateInput) (*versionedOutput[LabTemplateDTO], error) {
		record, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateUpdate)
		if err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		record, err = service.UpdateLabTemplate(ctx, actor.user.ID, record.ID, input.Body.Name, input.Body.Description, expected, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type revisionListInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
		Cursor    string `query:"cursor" maxLength:"256"`
		Limit     uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}
	huma.Get(api, "/lab-templates/{id}/revisions", func(ctx context.Context, input *revisionListInput) (*struct {
		Body struct {
			Items      []TemplateRevisionDTO `json:"items"`
			NextCursor string                `json:"nextCursor,omitempty"`
		}
	}, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		after, err := decodeTemplateRevisionCursor(input.Cursor)
		if err != nil {
			return nil, huma.Error400BadRequest("Template Revision cursor is invalid")
		}
		page, err := service.ListTemplateRevisions(ctx, controlplane.TemplateRevisionListRequest{TemplateID: template.ID, After: after, Limit: input.Limit})
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []TemplateRevisionDTO `json:"items"`
				NextCursor string                `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]TemplateRevisionDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, templateRevisionDTO(record))
		}
		output.Body.NextCursor = encodeTemplateRevisionCursor(page.NextCursor)
		return output, nil
	})

	type publishRevisionInput struct {
		ID             string `path:"id" maxLength:"128"`
		ProjectID      string `query:"projectId" required:"true" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Source string `json:"source" minLength:"1" maxLength:"8388608"`
		}
	}
	huma.Post(api, "/lab-templates/{id}/revisions", func(ctx context.Context, input *publishRevisionInput) (*struct{ Body TemplateRevisionDTO }, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRevisionPublish)
		if err != nil {
			return nil, err
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		revision, err := service.PublishTemplateRevision(ctx, actor.user.ID, template.ID, []byte(input.Body.Source), input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body TemplateRevisionDTO }{Body: templateRevisionDTO(revision)}, nil
	})

	type revisionInput struct {
		ID         string `path:"id" maxLength:"128"`
		RevisionID string `path:"revisionId" maxLength:"128"`
		ProjectID  string `query:"projectId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/lab-templates/{id}/revisions/{revisionId}", func(ctx context.Context, input *revisionInput) (*struct{ Body TemplateRevisionDTO }, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		revisionID, err := domain.NewRevisionID(input.RevisionID)
		if err != nil {
			return nil, huma.Error404NotFound("Template revision was not found")
		}
		revision, err := service.GetTemplateRevision(ctx, revisionID)
		if err != nil || revision.TemplateID != template.ID {
			return nil, huma.Error404NotFound("Template revision was not found")
		}
		return &struct{ Body TemplateRevisionDTO }{Body: templateRevisionDTO(revision)}, nil
	})
}

func encodeLabTemplateCursor(cursor *controlplane.LabTemplateCursor) string {
	if cursor == nil || cursor.CreatedAt.IsZero() || cursor.CreatedAt.UTC().UnixMicro() <= 0 || cursor.ID == "" {
		return ""
	}
	return encodeCollectionCursor(templateCursorKind, uint64(cursor.CreatedAt.UTC().UnixMicro()), string(cursor.ID))
}

func decodeLabTemplateCursor(value string) (*controlplane.LabTemplateCursor, error) {
	if value == "" {
		return nil, nil
	}
	position, id, err := decodeCollectionCursor(value, templateCursorKind)
	if err != nil || position > math.MaxInt64 {
		return nil, fmt.Errorf("invalid Lab Template cursor")
	}
	templateID, err := domain.NewTemplateID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid Lab Template cursor")
	}
	return &controlplane.LabTemplateCursor{CreatedAt: time.UnixMicro(int64(position)).UTC(), ID: templateID}, nil
}

func encodeTemplateRevisionCursor(cursor *controlplane.TemplateRevisionCursor) string {
	if cursor == nil || cursor.RevisionNumber == 0 || cursor.ID == "" {
		return ""
	}
	return encodeCollectionCursor(revisionCursorKind, cursor.RevisionNumber, string(cursor.ID))
}

func decodeTemplateRevisionCursor(value string) (*controlplane.TemplateRevisionCursor, error) {
	if value == "" {
		return nil, nil
	}
	position, id, err := decodeCollectionCursor(value, revisionCursorKind)
	if err != nil {
		return nil, fmt.Errorf("invalid Template Revision cursor")
	}
	revisionID, err := domain.NewRevisionID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid Template Revision cursor")
	}
	return &controlplane.TemplateRevisionCursor{RevisionNumber: position, ID: revisionID}, nil
}

func encodeCollectionCursor(kind byte, position uint64, id string) string {
	if position == 0 || len(id) == 0 || len(id) > 128 {
		return ""
	}
	encoded := make([]byte, 10+len(id))
	encoded[0], encoded[1] = 1, kind
	binary.BigEndian.PutUint64(encoded[2:10], position)
	copy(encoded[10:], id)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCollectionCursor(value string, expectedKind byte) (uint64, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < 11 || len(decoded) > 138 || decoded[0] != 1 || decoded[1] != expectedKind {
		return 0, "", fmt.Errorf("invalid collection cursor")
	}
	position := binary.BigEndian.Uint64(decoded[2:10])
	if position == 0 {
		return 0, "", fmt.Errorf("invalid collection cursor")
	}
	return position, string(decoded[10:]), nil
}

func labTemplateDTO(source controlplane.LabTemplateRecord) LabTemplateDTO {
	return LabTemplateDTO{
		ID: string(source.ID), ProjectID: string(source.ProjectID), Name: source.Name, Description: source.Description,
		ResourceVersion: uint64(source.ResourceVersion), CreatedBy: string(source.CreatedBy), CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func templateRevisionDTO(source controlplane.TemplateRevisionRecord) TemplateRevisionDTO {
	return TemplateRevisionDTO{
		ID: string(source.ID), TemplateID: string(source.TemplateID), RevisionNumber: source.RevisionNumber,
		BundleArtifact: string(source.BundleArtifact), BundleDigest: source.BundleDigest.String(), BundleSizeBytes: source.BundleSizeBytes,
		AuthorAPIVersion: source.AuthorAPIVersion, RendererVersion: source.RendererVersion, CanonicalVersion: source.CanonicalVersion,
		CanonicalDigest: source.CanonicalDigest.String(), PublishedBy: string(source.PublishedBy), CreatedAt: source.CreatedAt,
	}
}
