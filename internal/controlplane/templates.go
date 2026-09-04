package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
)

const (
	maximumTemplateDescriptionBytes = 4096
	maximumTemplateBundleBytes      = 8 << 20
)

func (s *Service) CreateLabTemplate(ctx context.Context, actor domain.UserID, projectID domain.ProjectID, name, description, idempotencyKey string) (LabTemplateRecord, error) {
	if s == nil || s.store == nil || s.ids == nil {
		return LabTemplateRecord{}, fmt.Errorf("control-plane template service is unavailable")
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return LabTemplateRecord{}, err
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return LabTemplateRecord{}, err
	}
	if err := validateTemplateMetadata(name, description); err != nil {
		return LabTemplateRecord{}, err
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return LabTemplateRecord{}, err
	}
	id, err := s.ids.New("tpl")
	if err != nil {
		return LabTemplateRecord{}, err
	}
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return LabTemplateRecord{}, err
	}
	return s.store.CreateLabTemplate(ctx, CreateLabTemplateRequest{
		GovernanceRecordIDs: recordIDs, ID: domain.TemplateID(id), ProjectID: projectID, Name: name, Description: description, CreatedBy: actor,
		IdempotencyKey: idempotencyKey,
		RequestDigest:  digestBytes("labcentral.http.create-lab-template/v1", []byte(projectID), []byte(name), []byte(description)),
	})
}

func (s *Service) UpdateLabTemplate(ctx context.Context, actor domain.UserID, templateID domain.TemplateID, name, description string, expected domain.ResourceVersion, idempotencyKey string) (LabTemplateRecord, error) {
	if s == nil || s.store == nil {
		return LabTemplateRecord{}, fmt.Errorf("control-plane template service is unavailable")
	}
	if _, err := domain.NewTemplateID(string(templateID)); err != nil {
		return LabTemplateRecord{}, err
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return LabTemplateRecord{}, err
	}
	if expected == 0 {
		return LabTemplateRecord{}, fmt.Errorf("expected template resource version is required")
	}
	if err := validateTemplateMetadata(name, description); err != nil {
		return LabTemplateRecord{}, err
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return LabTemplateRecord{}, err
	}
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return LabTemplateRecord{}, err
	}
	return s.store.UpdateLabTemplate(ctx, UpdateLabTemplateRequest{
		GovernanceRecordIDs: recordIDs, ID: templateID, UpdatedBy: actor, Name: name, Description: description, ExpectedResourceVersion: expected,
		IdempotencyKey: idempotencyKey,
		RequestDigest:  digestBytes("labcentral.http.update-lab-template/v1", []byte(templateID), []byte(name), []byte(description), []byte(fmt.Sprint(expected))),
	})
}

func (s *Service) GetLabTemplate(ctx context.Context, templateID domain.TemplateID) (LabTemplateRecord, error) {
	if s == nil || s.store == nil {
		return LabTemplateRecord{}, fmt.Errorf("control-plane template service is unavailable")
	}
	if _, err := domain.NewTemplateID(string(templateID)); err != nil {
		return LabTemplateRecord{}, err
	}
	return s.store.GetLabTemplate(ctx, templateID)
}

func (s *Service) ListLabTemplates(ctx context.Context, request LabTemplateListRequest) (LabTemplatePage, error) {
	if s == nil || s.store == nil {
		return LabTemplatePage{}, fmt.Errorf("control-plane template service is unavailable")
	}
	return s.store.ListLabTemplates(ctx, request)
}

func (s *Service) PublishTemplateRevision(ctx context.Context, actor domain.UserID, templateID domain.TemplateID, source []byte, idempotencyKey string) (TemplateRevisionRecord, error) {
	if s == nil || s.store == nil || s.artifacts == nil || s.renderer == nil || s.ids == nil {
		return TemplateRevisionRecord{}, fmt.Errorf("template publication is unavailable")
	}
	if _, err := domain.NewUserID(string(actor)); err != nil {
		return TemplateRevisionRecord{}, err
	}
	if _, err := domain.NewTemplateID(string(templateID)); err != nil {
		return TemplateRevisionRecord{}, err
	}
	if len(source) == 0 || len(source) > maximumTemplateBundleBytes {
		return TemplateRevisionRecord{}, fmt.Errorf("template bundle must contain 1-%d bytes", maximumTemplateBundleBytes)
	}
	if err := validateTemplateIdempotencyKey(idempotencyKey); err != nil {
		return TemplateRevisionRecord{}, err
	}
	canonical, _, err := s.renderer.Render(ctx, domain.RevisionContent{VerifiedBundle: source}, nil, domain.RenderPolicy{})
	if err != nil {
		return TemplateRevisionRecord{}, &ValidationError{Issues: []Issue{{Code: "render_failed", Message: err.Error()}}}
	}
	bundleHash := sha256.Sum256(source)
	bundleDigest, err := domain.ParseSHA256Digest(hex.EncodeToString(bundleHash[:]))
	if err != nil {
		return TemplateRevisionRecord{}, err
	}
	manifest := application.VerifiedManifest{Digest: bundleDigest, FileCount: 1, ByteCount: uint64(len(source))}
	uploadID, err := s.artifacts.Stage(ctx, manifest)
	if err != nil {
		return TemplateRevisionRecord{}, fmt.Errorf("stage template bundle: %w", err)
	}
	measured, err := s.artifacts.Write(ctx, uploadID, bytes.NewReader(source))
	if err != nil {
		return TemplateRevisionRecord{}, fmt.Errorf("write template bundle: %w", err)
	}
	if measured.Digest != bundleDigest || measured.Size != uint64(len(source)) {
		return TemplateRevisionRecord{}, fmt.Errorf("template bundle measurement mismatch")
	}
	artifact, err := s.artifacts.Commit(ctx, uploadID, manifest)
	if err != nil {
		return TemplateRevisionRecord{}, fmt.Errorf("commit template bundle: %w", err)
	}
	revisionID, err := s.ids.New("rev")
	if err != nil {
		return TemplateRevisionRecord{}, err
	}
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return TemplateRevisionRecord{}, err
	}
	return s.store.PublishTemplateRevision(ctx, PublishTemplateRevisionRequest{
		GovernanceRecordIDs: recordIDs, ID: domain.RevisionID(revisionID), TemplateID: templateID, BundleArtifact: artifact,
		BundleDigest: bundleDigest, BundleSizeBytes: uint64(len(source)), AuthorAPIVersion: canonical.APIVersion,
		RendererVersion: render.RendererVersion, CanonicalVersion: render.CanonicalFormatVersion,
		CanonicalDigest: canonical.Digest, PublishedBy: actor, IdempotencyKey: idempotencyKey,
		RequestDigest: digestBytes("labcentral.http.publish-template-revision/v1", []byte(templateID), source),
	})
}

func (s *Service) newGovernanceRecordIDs() (GovernanceRecordIDs, error) {
	auditID, err := s.ids.New("audit")
	if err != nil {
		return GovernanceRecordIDs{}, err
	}
	eventID, err := s.ids.New("event")
	if err != nil {
		return GovernanceRecordIDs{}, err
	}
	return GovernanceRecordIDs{AuditID: auditID, EventID: eventID}, nil
}

func (s *Service) GetTemplateRevision(ctx context.Context, revisionID domain.RevisionID) (TemplateRevisionRecord, error) {
	if s == nil || s.store == nil {
		return TemplateRevisionRecord{}, fmt.Errorf("control-plane template service is unavailable")
	}
	if _, err := domain.NewRevisionID(string(revisionID)); err != nil {
		return TemplateRevisionRecord{}, err
	}
	return s.store.GetTemplateRevision(ctx, revisionID)
}

func (s *Service) ListTemplateRevisions(ctx context.Context, request TemplateRevisionListRequest) (TemplateRevisionPage, error) {
	if s == nil || s.store == nil {
		return TemplateRevisionPage{}, fmt.Errorf("control-plane template service is unavailable")
	}
	return s.store.ListTemplateRevisions(ctx, request)
}

func (s *Service) OpenTemplateRevision(ctx context.Context, actor domain.UserID, revisionID domain.RevisionID) (TemplateRevisionRecord, []byte, error) {
	if s == nil || s.artifacts == nil {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("template artifact store is unavailable")
	}
	revision, err := s.GetTemplateRevision(ctx, revisionID)
	if err != nil {
		return TemplateRevisionRecord{}, nil, err
	}
	reader, err := s.artifacts.Open(ctx, application.AuthorizedArtifactRef{Reference: revision.BundleArtifact, SubjectID: actor})
	if err != nil {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("open template bundle: %w", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(reader, int64(revision.BundleSizeBytes)+1))
	closeErr := reader.Close()
	if readErr != nil {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("read template bundle: %w", readErr)
	}
	if closeErr != nil {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("close template bundle: %w", closeErr)
	}
	if uint64(len(contents)) != revision.BundleSizeBytes {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("template bundle size verification failed")
	}
	measured := sha256.Sum256(contents)
	if hex.EncodeToString(measured[:]) != revision.BundleDigest.Hex() {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("template bundle digest verification failed")
	}
	canonical, _, err := s.renderer.Render(ctx, domain.RevisionContent{VerifiedBundle: contents}, nil, domain.RenderPolicy{})
	if err != nil || canonical.Digest != revision.CanonicalDigest || canonical.APIVersion != revision.AuthorAPIVersion ||
		canonical.RendererVersion != revision.RendererVersion || revision.CanonicalVersion != render.CanonicalFormatVersion {
		return TemplateRevisionRecord{}, nil, fmt.Errorf("template revision provenance verification failed")
	}
	return revision, contents, nil
}

func (s *Service) OpenTemplateRevisionForProject(ctx context.Context, actor domain.UserID, projectID domain.ProjectID, revisionID domain.RevisionID) (TemplateRevisionRecord, []byte, error) {
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return TemplateRevisionRecord{}, nil, err
	}
	revision, err := s.GetTemplateRevision(ctx, revisionID)
	if err != nil {
		return TemplateRevisionRecord{}, nil, err
	}
	template, err := s.GetLabTemplate(ctx, revision.TemplateID)
	if err != nil || template.ProjectID != projectID {
		return TemplateRevisionRecord{}, nil, ErrNotFound
	}
	return s.OpenTemplateRevision(ctx, actor, revisionID)
}

func validateTemplateMetadata(name, description string) error {
	if _, err := domain.NewName(name); err != nil {
		return err
	}
	if !utf8.ValidString(description) || len(description) > maximumTemplateDescriptionBytes || strings.ContainsRune(description, '\x00') {
		return fmt.Errorf("template description must be valid UTF-8 without NUL and at most %d bytes", maximumTemplateDescriptionBytes)
	}
	return nil
}

func validateTemplateIdempotencyKey(value string) error {
	if len(value) < 8 || len(value) > 128 || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("template idempotency key must contain 8-128 bytes without NUL")
	}
	return nil
}
