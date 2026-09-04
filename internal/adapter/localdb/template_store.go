package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maximumTemplateDescriptionBytes = 4096
	maximumTemplateBundleBytes      = 8 << 20
	maximumRevisionMetadataBytes    = 128
	createTemplateRouteAction       = "create_lab_template"
	updateTemplateRouteAction       = "update_lab_template"
	publishRevisionRouteAction      = "publish_template_revision"
)

var (
	errTemplateCreateUniqueConflict   = errors.New("Lab Template uniqueness conflict")
	errTemplateRevisionNumberConflict = errors.New("Template Revision number conflict")
)

func (s *Store) CreateLabTemplate(ctx context.Context, request controlplane.CreateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	if err := validateCreateLabTemplate(request); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.createLabTemplate(ctx, request)
		if errors.Is(err, errTemplateCreateUniqueConflict) {
			if attempt > 0 {
				return controlplane.LabTemplateRecord{}, controlplane.ErrAlreadyExists
			}
			if err := waitTransactionRetry(ctx, attempt); err != nil {
				return controlplane.LabTemplateRecord{}, err
			}
			continue
		}
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
	}
}

func (s *Store) createLabTemplate(ctx context.Context, request controlplane.CreateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("begin Lab Template creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.CreatedBy, createTemplateRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.CreatedBy, createTemplateRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.LabTemplateRecord{}, err
	} else if found {
		record, err := scanLabTemplate(tx.QueryRowContext(ctx, s.bind(`SELECT template_id, project_id, name, description,
			resource_version, created_by, created_at_us, updated_at_us
			FROM labcentral_lab_templates WHERE template_id = ?`), existing))
		if err != nil {
			return controlplane.LabTemplateRecord{}, fmt.Errorf("load replayed Lab Template: %w", err)
		}
		return record, tx.Commit()
	}
	if err := s.requireTemplateActor(ctx, tx, request.ProjectID, request.CreatedBy); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	row := tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_lab_templates
		(template_id, project_id, name, description, resource_version, created_by, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, 1, ?, `+s.nowMicros()+`, `+s.nowMicros()+`)
		RETURNING template_id, project_id, name, description, resource_version, created_by, created_at_us, updated_at_us`),
		request.ID, request.ProjectID, request.Name, request.Description, request.CreatedBy)
	record, err := scanLabTemplate(row)
	if err != nil {
		if isUniqueViolation(err) {
			return controlplane.LabTemplateRecord{}, errTemplateCreateUniqueConflict
		}
		if isForeignKeyViolation(err) {
			return controlplane.LabTemplateRecord{}, controlplane.ErrNotFound
		}
		return controlplane.LabTemplateRecord{}, fmt.Errorf("create Lab Template: %w", err)
	}
	if err := s.appendTemplateGovernance(ctx, tx, request.ProjectID, request.CreatedBy, request.ID, "", record.ResourceVersion,
		request.GovernanceRecordIDs, "template.create", "lab-template.created"); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.CreatedBy, createTemplateRouteAction, request.IdempotencyKey, request.RequestDigest, string(record.ID)); err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("bind Lab Template idempotency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("commit Lab Template creation: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateLabTemplate(ctx context.Context, request controlplane.UpdateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	if err := validateUpdateLabTemplate(request); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.updateLabTemplate(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
	}
}

func (s *Store) updateLabTemplate(ctx context.Context, request controlplane.UpdateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("begin Lab Template update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.UpdatedBy, updateTemplateRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.UpdatedBy, updateTemplateRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.LabTemplateRecord{}, err
	} else if found {
		record, err := scanLabTemplate(tx.QueryRowContext(ctx, s.bind(`SELECT template_id, project_id, name, description,
			resource_version, created_by, created_at_us, updated_at_us
			FROM labcentral_lab_templates WHERE template_id = ?`), existing))
		if err != nil {
			return controlplane.LabTemplateRecord{}, fmt.Errorf("load replayed Lab Template update: %w", err)
		}
		return record, tx.Commit()
	}
	parentQuery := `SELECT project_id FROM labcentral_lab_templates WHERE template_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		parentQuery += ` FOR UPDATE`
	}
	var projectID domain.ProjectID
	if err := tx.QueryRowContext(ctx, s.bind(parentQuery), request.ID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return controlplane.LabTemplateRecord{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("lock Lab Template for update: %w", err)
	}
	if err := s.requireTemplateActor(ctx, tx, projectID, request.UpdatedBy); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	row := tx.QueryRowContext(ctx, s.bind(`UPDATE labcentral_lab_templates
		SET name = ?, description = ?, resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE template_id = ? AND resource_version = ?
		RETURNING template_id, project_id, name, description, resource_version, created_by, created_at_us, updated_at_us`),
		request.Name, request.Description, request.ID, request.ExpectedResourceVersion)
	record, err := scanLabTemplate(row)
	if err != nil {
		if isUniqueViolation(err) {
			return controlplane.LabTemplateRecord{}, controlplane.ErrAlreadyExists
		}
		if errors.Is(err, sql.ErrNoRows) {
			return controlplane.LabTemplateRecord{}, controlplane.ErrVersionConflict
		}
		return controlplane.LabTemplateRecord{}, fmt.Errorf("update Lab Template: %w", err)
	}
	if err := s.appendTemplateGovernance(ctx, tx, projectID, request.UpdatedBy, request.ID, "", record.ResourceVersion,
		request.GovernanceRecordIDs, "template.update", "lab-template.updated"); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.UpdatedBy, updateTemplateRouteAction, request.IdempotencyKey, request.RequestDigest, string(record.ID)); err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("bind Lab Template update idempotency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("commit Lab Template update: %w", err)
	}
	return record, nil
}

func (s *Store) GetLabTemplate(ctx context.Context, id domain.TemplateID) (controlplane.LabTemplateRecord, error) {
	if _, err := domain.NewTemplateID(string(id)); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	record, err := scanLabTemplate(s.db.QueryRowContext(ctx, s.bind(`SELECT template_id, project_id, name, description,
		resource_version, created_by, created_at_us, updated_at_us
		FROM labcentral_lab_templates WHERE template_id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.LabTemplateRecord{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("get Lab Template: %w", err)
	}
	return record, nil
}

func (s *Store) ListLabTemplates(ctx context.Context, request controlplane.LabTemplateListRequest) (controlplane.LabTemplatePage, error) {
	if err := validateLabTemplateListRequest(request); err != nil {
		return controlplane.LabTemplatePage{}, err
	}
	query := `SELECT template_id, project_id, name, description,
		resource_version, created_by, created_at_us, updated_at_us
		FROM labcentral_lab_templates WHERE project_id = ?`
	arguments := []any{request.ProjectID}
	if request.After != nil {
		query += ` AND (created_at_us > ? OR (created_at_us = ? AND template_id > ?))`
		createdAt := request.After.CreatedAt.UTC().UnixMicro()
		arguments = append(arguments, createdAt, createdAt, request.After.ID)
	}
	query += ` ORDER BY created_at_us, template_id LIMIT ?`
	arguments = append(arguments, uint64(request.Limit)+1)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return controlplane.LabTemplatePage{}, fmt.Errorf("list Lab Templates: %w", err)
	}
	defer rows.Close()
	records := make([]controlplane.LabTemplateRecord, 0, request.Limit+1)
	for rows.Next() {
		record, scanErr := scanLabTemplate(rows)
		if scanErr != nil {
			return controlplane.LabTemplatePage{}, fmt.Errorf("scan Lab Template: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return controlplane.LabTemplatePage{}, fmt.Errorf("list Lab Templates: %w", err)
	}
	page := controlplane.LabTemplatePage{Records: records}
	if len(records) > int(request.Limit) {
		page.Records = records[:request.Limit]
		last := page.Records[len(page.Records)-1]
		page.NextCursor = &controlplane.LabTemplateCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (s *Store) PublishTemplateRevision(ctx context.Context, request controlplane.PublishTemplateRevisionRequest) (controlplane.TemplateRevisionRecord, error) {
	if err := validatePublishTemplateRevision(request); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.publishTemplateRevision(ctx, request)
		if (!errors.Is(err, errTemplateRevisionNumberConflict) && !isRetryableTransactionConflict(err)) || attempt == 4 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.TemplateRevisionRecord{}, err
		}
	}
}

func (s *Store) publishTemplateRevision(ctx context.Context, request controlplane.PublishTemplateRevisionRequest) (controlplane.TemplateRevisionRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("begin Template Revision publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.PublishedBy, publishRevisionRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.PublishedBy, publishRevisionRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	} else if found {
		record, err := scanTemplateRevision(tx.QueryRowContext(ctx, s.bind(`SELECT revision_id, template_id, revision_number,
			bundle_artifact_ref, bundle_digest, bundle_size_bytes, author_api_version, renderer_version, canonical_version,
			canonical_digest, published_by, created_at_us
			FROM labcentral_template_revisions WHERE revision_id = ?`), existing))
		if err != nil {
			return controlplane.TemplateRevisionRecord{}, fmt.Errorf("load replayed Template Revision: %w", err)
		}
		return record, tx.Commit()
	}
	parentQuery := `SELECT project_id FROM labcentral_lab_templates WHERE template_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		parentQuery += ` FOR UPDATE`
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, s.bind(parentQuery), request.TemplateID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return controlplane.TemplateRevisionRecord{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("lock Lab Template for publication: %w", err)
	}
	if err := s.requireTemplateActor(ctx, tx, domain.ProjectID(projectID), request.PublishedBy); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	var revisionNumber int64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(MAX(revision_number), 0) + 1
		FROM labcentral_template_revisions WHERE template_id = ?`), request.TemplateID).Scan(&revisionNumber); err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("allocate Template Revision number: %w", err)
	}
	row := tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_template_revisions
		(revision_id, template_id, revision_number, bundle_artifact_ref, bundle_digest, bundle_size_bytes,
		 author_api_version, renderer_version, canonical_version, canonical_digest, published_by, created_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.nowMicros()+`)
		RETURNING revision_id, template_id, revision_number, bundle_artifact_ref, bundle_digest, bundle_size_bytes,
		 author_api_version, renderer_version, canonical_version, canonical_digest, published_by, created_at_us`),
		request.ID, request.TemplateID, revisionNumber, request.BundleArtifact, request.BundleDigest.Hex(), request.BundleSizeBytes,
		request.AuthorAPIVersion, request.RendererVersion, request.CanonicalVersion, request.CanonicalDigest.Hex(), request.PublishedBy)
	record, err := scanTemplateRevision(row)
	if err != nil {
		if isUniqueViolation(err) {
			if isTemplateRevisionNumberConflict(err) {
				return controlplane.TemplateRevisionRecord{}, errTemplateRevisionNumberConflict
			}
			return controlplane.TemplateRevisionRecord{}, controlplane.ErrAlreadyExists
		}
		if isForeignKeyViolation(err) {
			return controlplane.TemplateRevisionRecord{}, controlplane.ErrNotFound
		}
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("publish Template Revision: %w", err)
	}
	if err := s.appendTemplateGovernance(ctx, tx, domain.ProjectID(projectID), request.PublishedBy, request.TemplateID, request.ID,
		domain.ResourceVersion(record.RevisionNumber), request.GovernanceRecordIDs, "template.revision.publish", "template-revision.published"); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.PublishedBy, publishRevisionRouteAction, request.IdempotencyKey, request.RequestDigest, string(record.ID)); err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("bind Template Revision idempotency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("commit Template Revision publication: %w", err)
	}
	return record, nil
}

func (s *Store) GetTemplateRevision(ctx context.Context, id domain.RevisionID) (controlplane.TemplateRevisionRecord, error) {
	if _, err := domain.NewRevisionID(string(id)); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	record, err := scanTemplateRevision(s.db.QueryRowContext(ctx, s.bind(`SELECT revision_id, template_id, revision_number,
		bundle_artifact_ref, bundle_digest, bundle_size_bytes, author_api_version, renderer_version, canonical_version,
		canonical_digest, published_by, created_at_us
		FROM labcentral_template_revisions WHERE revision_id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.TemplateRevisionRecord{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("get Template Revision: %w", err)
	}
	return record, nil
}

func (s *Store) ListTemplateRevisions(ctx context.Context, request controlplane.TemplateRevisionListRequest) (controlplane.TemplateRevisionPage, error) {
	if err := validateTemplateRevisionListRequest(request); err != nil {
		return controlplane.TemplateRevisionPage{}, err
	}
	query := `SELECT revision_id, template_id, revision_number,
		bundle_artifact_ref, bundle_digest, bundle_size_bytes, author_api_version, renderer_version, canonical_version,
		canonical_digest, published_by, created_at_us
		FROM labcentral_template_revisions WHERE template_id = ?`
	arguments := []any{request.TemplateID}
	if request.After != nil {
		query += ` AND (revision_number > ? OR (revision_number = ? AND revision_id > ?))`
		arguments = append(arguments, request.After.RevisionNumber, request.After.RevisionNumber, request.After.ID)
	}
	query += ` ORDER BY revision_number, revision_id LIMIT ?`
	arguments = append(arguments, uint64(request.Limit)+1)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return controlplane.TemplateRevisionPage{}, fmt.Errorf("list Template Revisions: %w", err)
	}
	defer rows.Close()
	records := make([]controlplane.TemplateRevisionRecord, 0, request.Limit+1)
	for rows.Next() {
		record, scanErr := scanTemplateRevision(rows)
		if scanErr != nil {
			return controlplane.TemplateRevisionPage{}, fmt.Errorf("scan Template Revision: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return controlplane.TemplateRevisionPage{}, fmt.Errorf("list Template Revisions: %w", err)
	}
	page := controlplane.TemplateRevisionPage{Records: records}
	if len(records) > int(request.Limit) {
		page.Records = records[:request.Limit]
		last := page.Records[len(page.Records)-1]
		page.NextCursor = &controlplane.TemplateRevisionCursor{RevisionNumber: last.RevisionNumber, ID: last.ID}
	}
	return page, nil
}

func validateCreateLabTemplate(request controlplane.CreateLabTemplateRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewTemplateID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewProjectID(string(request.ProjectID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.CreatedBy)); err != nil {
		return err
	}
	if err := validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest); err != nil {
		return err
	}
	return validateLabTemplateMetadata(request.Name, request.Description)
}

func validateLabTemplateListRequest(request controlplane.LabTemplateListRequest) error {
	if _, err := domain.NewProjectID(string(request.ProjectID)); err != nil {
		return err
	}
	if request.Limit == 0 || request.Limit > controlplane.MaximumTemplatePageSize {
		return fmt.Errorf("Lab Template page limit must be 1-%d", controlplane.MaximumTemplatePageSize)
	}
	if request.After != nil {
		if request.After.CreatedAt.IsZero() || request.After.CreatedAt.UTC().UnixMicro() <= 0 {
			return fmt.Errorf("Lab Template cursor time is invalid")
		}
		if _, err := domain.NewTemplateID(string(request.After.ID)); err != nil {
			return err
		}
	}
	return nil
}

func validateUpdateLabTemplate(request controlplane.UpdateLabTemplateRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewTemplateID(string(request.ID)); err != nil {
		return err
	}
	if request.ExpectedResourceVersion == 0 {
		return fmt.Errorf("expected Lab Template resource version is required")
	}
	if _, err := domain.NewUserID(string(request.UpdatedBy)); err != nil {
		return err
	}
	if err := validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest); err != nil {
		return err
	}
	return validateLabTemplateMetadata(request.Name, request.Description)
}

func validateLabTemplateMetadata(name, description string) error {
	if _, err := domain.NewName(name); err != nil {
		return fmt.Errorf("Lab Template name is invalid: %w", err)
	}
	if !utf8.ValidString(description) || len(description) > maximumTemplateDescriptionBytes || strings.ContainsRune(description, '\x00') {
		return fmt.Errorf("Lab Template description must be valid UTF-8 without NUL and at most %d bytes", maximumTemplateDescriptionBytes)
	}
	return nil
}

func validatePublishTemplateRevision(request controlplane.PublishTemplateRevisionRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewRevisionID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewTemplateID(string(request.TemplateID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.PublishedBy)); err != nil {
		return err
	}
	if err := validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest); err != nil {
		return err
	}
	bundleDigest, err := domain.ParseSHA256Digest(request.BundleDigest.Hex())
	if err != nil {
		return fmt.Errorf("bundle digest is invalid: %w", err)
	}
	if _, err := domain.ParseSHA256Digest(request.CanonicalDigest.Hex()); err != nil {
		return fmt.Errorf("canonical digest is invalid: %w", err)
	}
	artifact, err := domain.NewArtifactRef(string(request.BundleArtifact))
	if err != nil {
		return err
	}
	if artifact != domain.ArtifactRef(bundleDigest.String()) {
		return fmt.Errorf("bundle artifact reference must match the content digest")
	}
	if request.BundleSizeBytes == 0 || request.BundleSizeBytes > maximumTemplateBundleBytes {
		return fmt.Errorf("bundle size must be between 1 and %d bytes", maximumTemplateBundleBytes)
	}
	for label, value := range map[string]string{
		"author API version": request.AuthorAPIVersion,
		"renderer version":   request.RendererVersion,
		"canonical version":  request.CanonicalVersion,
	} {
		if !validRevisionMetadata(value) {
			return fmt.Errorf("%s must be printable, trimmed, and 1-%d bytes", label, maximumRevisionMetadataBytes)
		}
	}
	return nil
}

func validateTemplateRevisionListRequest(request controlplane.TemplateRevisionListRequest) error {
	if _, err := domain.NewTemplateID(string(request.TemplateID)); err != nil {
		return err
	}
	if request.Limit == 0 || request.Limit > controlplane.MaximumTemplatePageSize {
		return fmt.Errorf("Template Revision page limit must be 1-%d", controlplane.MaximumTemplatePageSize)
	}
	if request.After != nil {
		if request.After.RevisionNumber == 0 {
			return fmt.Errorf("Template Revision cursor number is invalid")
		}
		if _, err := domain.NewRevisionID(string(request.After.ID)); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplateIdempotency(key string, digest domain.SHA256Digest) error {
	if len(key) < 8 || len(key) > 128 || strings.ContainsRune(key, '\x00') {
		return fmt.Errorf("Template idempotency key must contain 8-128 bytes without NUL")
	}
	if _, err := domain.ParseSHA256Digest(digest.Hex()); err != nil {
		return fmt.Errorf("Template request digest is invalid: %w", err)
	}
	return nil
}

func validateGovernanceRecordIDs(ids controlplane.GovernanceRecordIDs) error {
	if ids.AuditID == "" || ids.EventID == "" {
		return fmt.Errorf("governance audit and event IDs are required")
	}
	return nil
}

type templateGovernancePayload struct {
	TemplateID      domain.TemplateID      `json:"templateId"`
	RevisionID      domain.RevisionID      `json:"revisionId,omitempty"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
}

func (s *Store) appendTemplateGovernance(
	ctx context.Context,
	tx *sql.Tx,
	projectID domain.ProjectID,
	actorID domain.UserID,
	templateID domain.TemplateID,
	revisionID domain.RevisionID,
	resourceVersion domain.ResourceVersion,
	ids controlplane.GovernanceRecordIDs,
	action string,
	eventType string,
) error {
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), projectID).Scan(&organizationID); errors.Is(err, sql.ErrNoRows) {
		return controlplane.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("load Template governance scope: %w", err)
	}
	encoded, err := json.Marshal(templateGovernancePayload{TemplateID: templateID, RevisionID: revisionID, ResourceVersion: resourceVersion})
	if err != nil {
		return fmt.Errorf("encode Template governance payload: %w", err)
	}
	auditPayload, err := application.NewRedactedAuditPayload(encoded)
	if err != nil {
		return err
	}
	eventPayload, err := application.NewSafeEventPayload(encoded)
	if err != nil {
		return err
	}
	scope := application.RecordScope{OrganizationID: organizationID, ProjectID: projectID}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: string(actorID)}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditTemplate,
		Action: action, Outcome: application.AuditSucceeded, Scope: scope, Actor: actor,
		AggregateType: "lab-template", AggregateID: string(templateID), RedactedPayload: auditPayload,
	}); err != nil {
		return fmt.Errorf("append Template audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: eventType,
		Scope: scope, Actor: actor, AggregateType: "lab-template", AggregateID: string(templateID), SafePayload: eventPayload,
	}); err != nil {
		return fmt.Errorf("append Template event: %w", err)
	}
	return nil
}

func validRevisionMetadata(value string) bool {
	if value == "" || len(value) > maximumRevisionMetadataBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == '\x7f' {
			return false
		}
	}
	return true
}

type templateActorQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) requireTemplateActor(ctx context.Context, querier templateActorQuerier, projectID domain.ProjectID, actorID domain.UserID) error {
	if projectID == "project.local" && actorID == controlplane.LocalAdminID {
		return nil
	}
	var exists int
	if err := querier.QueryRowContext(ctx, s.bind(`SELECT 1 FROM labcentral_users WHERE user_id = ? AND disabled = 0`), actorID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return controlplane.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("validate Lab Template actor: %w", err)
	}
	return nil
}

func scanLabTemplate(row scanner) (controlplane.LabTemplateRecord, error) {
	var record controlplane.LabTemplateRecord
	var templateID, projectID, createdBy string
	var resourceVersion, createdAt, updatedAt int64
	if err := row.Scan(&templateID, &projectID, &record.Name, &record.Description, &resourceVersion, &createdBy, &createdAt, &updatedAt); err != nil {
		return controlplane.LabTemplateRecord{}, err
	}
	if resourceVersion <= 0 || createdAt <= 0 || updatedAt <= 0 {
		return controlplane.LabTemplateRecord{}, fmt.Errorf("Lab Template row contains invalid monotonic values")
	}
	record.ID = domain.TemplateID(templateID)
	record.ProjectID = domain.ProjectID(projectID)
	record.ResourceVersion = domain.ResourceVersion(resourceVersion)
	record.CreatedBy = domain.UserID(createdBy)
	record.CreatedAt = timeFromDatabaseMicros(createdAt)
	record.UpdatedAt = timeFromDatabaseMicros(updatedAt)
	return record, nil
}

func scanTemplateRevision(row scanner) (controlplane.TemplateRevisionRecord, error) {
	var record controlplane.TemplateRevisionRecord
	var revisionID, templateID, bundleArtifact, bundleDigest, canonicalDigest, publishedBy string
	var revisionNumber, bundleSizeBytes, createdAt int64
	if err := row.Scan(&revisionID, &templateID, &revisionNumber, &bundleArtifact, &bundleDigest, &bundleSizeBytes,
		&record.AuthorAPIVersion, &record.RendererVersion, &record.CanonicalVersion, &canonicalDigest, &publishedBy, &createdAt); err != nil {
		return controlplane.TemplateRevisionRecord{}, err
	}
	if revisionNumber <= 0 || bundleSizeBytes <= 0 || createdAt <= 0 {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("Template Revision row contains invalid monotonic values")
	}
	parsedBundleDigest, err := domain.ParseSHA256Digest(bundleDigest)
	if err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("decode Template Revision bundle digest: %w", err)
	}
	parsedCanonicalDigest, err := domain.ParseSHA256Digest(canonicalDigest)
	if err != nil {
		return controlplane.TemplateRevisionRecord{}, fmt.Errorf("decode Template Revision canonical digest: %w", err)
	}
	record.ID = domain.RevisionID(revisionID)
	record.TemplateID = domain.TemplateID(templateID)
	record.RevisionNumber = uint64(revisionNumber)
	record.BundleArtifact = domain.ArtifactRef(bundleArtifact)
	record.BundleDigest = parsedBundleDigest
	record.BundleSizeBytes = uint64(bundleSizeBytes)
	record.CanonicalDigest = parsedCanonicalDigest
	record.PublishedBy = domain.UserID(publishedBy)
	record.CreatedAt = timeFromDatabaseMicros(createdAt)
	return record, nil
}

func timeFromDatabaseMicros(value int64) time.Time {
	return time.UnixMicro(value).UTC()
}

func isForeignKeyViolation(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return true
	}
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

func isTemplateRevisionNumberConflict(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "23505" && postgresError.ConstraintName == "labcentral_template_revisions_template_id_revision_number_key"
	}
	return strings.Contains(err.Error(), "labcentral_template_revisions.template_id, labcentral_template_revisions.revision_number")
}
