package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"time"

	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
)

func (s *Service) CreateLibrary(ctx context.Context, actor domain.UserID, project domain.ProjectID, binding, reference, key string, interval uint32) (app.TemplateLibrary, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return app.TemplateLibrary{}, err
	}
	b, ok := s.sources[binding]
	if !ok || !b.Collections || !slices.Contains(b.Projects, project) {
		return app.TemplateLibrary{}, ErrAccessUnavailable
	}
	if interval == 0 {
		interval = 900
	}
	if interval < 60 || interval > 86400 || len(reference) == 0 || len(reference) > 1024 {
		return app.TemplateLibrary{}, fmt.Errorf("invalid schedule or reference")
	}
	store, ok := s.store.(app.LibraryStore)
	if !ok {
		return app.TemplateLibrary{}, ErrAccessUnavailable
	}
	id, err := s.ids.New("library")
	if err != nil {
		return app.TemplateLibrary{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return app.TemplateLibrary{}, err
	}
	library := app.TemplateLibrary{ID: id, ProjectID: project, AuthorID: actor, Binding: binding, Reference: reference, IntervalSeconds: interval}
	raw, _ := json.Marshal(struct {
		Project            domain.ProjectID
		Binding, Reference string
		Interval           uint32
	}{project, binding, reference, interval})
	return store.CreateLibrary(ctx, app.CreateLibrary{Library: library, GovernanceRecordIDs: ids, IdempotencyKey: key, RequestDigest: digestBytes("library.create/v1", raw)})
}
func (s *Service) ListLibraries(ctx context.Context, actor domain.UserID, project domain.ProjectID) ([]app.TemplateLibrary, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRead); err != nil {
		return nil, err
	}
	store, ok := s.store.(app.LibraryStore)
	if !ok {
		return nil, ErrAccessUnavailable
	}
	return store.ListLibraries(ctx, project)
}
func (s *Service) loadLibrary(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string, action app.PermissionAction) (app.TemplateLibrary, error) {
	if err := s.authorizeExtension(ctx, actor, project, action); err != nil {
		return app.TemplateLibrary{}, err
	}
	store, ok := s.store.(app.LibraryStore)
	if !ok {
		return app.TemplateLibrary{}, ErrAccessUnavailable
	}
	library, err := store.GetLibrary(ctx, id)
	if err != nil || library.ProjectID != project {
		return app.TemplateLibrary{}, app.ErrNotFound
	}
	return library, nil
}
func (s *Service) LibraryEntries(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string) ([]app.LibraryEntry, error) {
	if _, err := s.loadLibrary(ctx, actor, project, id, app.ActionTemplateRead); err != nil {
		return nil, err
	}
	return s.store.(app.LibraryStore).LibraryEntries(ctx, id)
}
func (s *Service) LibraryHistory(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string) ([]app.ExtensionWork, error) {
	if _, err := s.loadLibrary(ctx, actor, project, id, app.ActionTemplateRevisionPublish); err != nil {
		return nil, err
	}
	return s.store.(app.LibraryStore).LibraryHistory(ctx, id)
}
func (s *Service) SetLibraryPaused(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string, paused bool) error {
	if _, err := s.loadLibrary(ctx, actor, project, id, app.ActionTemplateRevisionPublish); err != nil {
		return err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return err
	}
	reason := ""
	if paused {
		reason = "operator"
	}
	return s.store.(app.LibraryStore).SetLibraryPaused(ctx, id, actor, paused, reason, ids)
}
func (s *Service) SyncLibrary(ctx context.Context, actor domain.UserID, project domain.ProjectID, id, key string) (app.ExtensionWork, error) {
	library, err := s.loadLibrary(ctx, actor, project, id, app.ActionTemplateRevisionPublish)
	if err != nil {
		return app.ExtensionWork{}, err
	}
	return s.queueLibrary(ctx, library, key)
}
func (s *Service) queueLibrary(ctx context.Context, l app.TemplateLibrary, key string) (app.ExtensionWork, error) {
	if err := s.authorizeExtension(ctx, l.AuthorID, l.ProjectID, app.ActionTemplateRevisionPublish); err != nil {
		ids, idErr := s.newGovernanceRecordIDs()
		if idErr == nil {
			_ = s.store.(app.LibraryStore).SetLibraryPaused(ctx, l.ID, l.AuthorID, true, "authority_revoked", ids)
		}
		return app.ExtensionWork{}, err
	}
	b, ok := s.sources[l.Binding]
	if !ok || !b.Collections || !slices.Contains(b.Projects, l.ProjectID) || b.Available != nil && !b.Available() {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	return s.newExtensionWork(ctx, l.AuthorID, l.ProjectID, app.ExtensionWorkRequest{Kind: domain.WorkSource, LibraryID: l.ID, PluginInstance: b.InstanceID, PluginDigest: b.PluginDigest, BindingID: b.ID, ConfigurationDigest: digestBytes("extension.configuration/v1", b.Configuration), Reference: l.Reference}, key)
}
func (s *Service) RunLibrarySchedules(ctx context.Context, report func(error)) {
	store, ok := s.store.(app.LibraryStore)
	if !ok {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		libraries, err := store.DueLibraries(ctx)
		if err == nil {
			for _, library := range libraries {
				_, err = s.queueLibrary(ctx, library, fmt.Sprintf("schedule.%s.%d", library.ID, library.NextSyncAt.UnixMicro()))
				if err != nil && report != nil {
					report(err)
				}
			}
		} else if report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) publishSourceCollection(ctx context.Context, b SourceBinding, c app.ExtensionWorkClaim, result app.ExtensionSourceResult) error {
	if result.Collection == nil || len(result.Collection.Entries) > 32 {
		return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "invalid collection"}}}
	}
	store, ok := s.store.(app.LibraryStore)
	if !ok {
		return ErrAccessUnavailable
	}
	type preparedEntry struct {
		entry          app.SourceCollectionEntry
		source         []byte
		representation render.TemplateRepresentation
	}
	prepared := []preparedEntry{}
	seen := map[string]bool{}
	total := 0
	// No publication or artifact staging occurs until every source is validated.
	for _, entry := range result.Collection.Entries {
		if !sourceCollectionKey.MatchString(entry.Key) || seen[entry.Key] {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "duplicate collection key"}}}
		}
		seen[entry.Key] = true
		if err := validateTemplateMetadata(entry.Name, entry.Description); err != nil {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "invalid template metadata"}}}
		}
		reader, err := b.Source.ReadArtifact(ctx, workCall(c), entry.Bundle)
		if err != nil {
			return err
		}
		source, err := io.ReadAll(io.LimitReader(reader, labbundle.MaxBytes+1))
		reader.Close()
		if err != nil {
			return err
		}
		hash := sha256.Sum256(source)
		if uint64(len(source)) != entry.Bundle.Size || hex.EncodeToString(hash[:]) != entry.Bundle.Digest.Hex() {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "artifact identity mismatch"}}}
		}
		total += len(source)
		if total > 64<<20 {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "collection exceeds limit"}}}
		}
		bundle, err := labbundle.Read(source)
		if err != nil || bundle.Manifest.Package == nil {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: "portable source package required"}}}
		}
		representation, err := s.renderer.PrepareTemplate(ctx, source)
		if err != nil {
			return &ValidationError{Issues: []Issue{{Code: "collection_invalid", Message: err.Error()}}}
		}
		prepared = append(prepared, preparedEntry{entry: entry, source: source, representation: representation})
	}
	if err := s.authorizeExtension(ctx, c.Work.ActorID, c.Work.ProjectID, app.ActionTemplateRevisionPublish); err != nil {
		ids, e := s.newGovernanceRecordIDs()
		if e == nil {
			_ = store.SetLibraryPaused(ctx, c.Work.Request.LibraryID, c.Work.ActorID, true, "authority_revoked", ids)
		}
		return err
	}
	publication := app.LibraryPublishRequest{Claim: c, Commit: result.ResolvedIdentity}
	for _, p := range prepared {
		artifact, digest, err := s.commitExtensionArtifact(ctx, p.source)
		if err != nil {
			return err
		}
		if err = s.store.(app.BuildStore).ReferenceExtensionArtifact(ctx, c.Work.ID, artifact); err != nil {
			return err
		}
		tid := digestBytes("library.template/v1", []byte(c.Work.Request.LibraryID), []byte(p.entry.Key)).Hex()
		rid := digestBytes("library.revision/v1", []byte(c.Work.ID), []byte(p.entry.Key)).Hex()
		createIDs, err := s.newGovernanceRecordIDs()
		if err != nil {
			return err
		}
		revisionIDs, err := s.newGovernanceRecordIDs()
		if err != nil {
			return err
		}
		publication.Entries = append(publication.Entries, app.LibraryPublication{Key: p.entry.Key,
			Template: app.CreateLabTemplateRequest{ID: domain.TemplateID("template." + tid), Name: p.entry.Name, Description: p.entry.Description, ProjectID: c.Work.ProjectID, CreatedBy: c.Work.ActorID, IdempotencyKey: "library." + tid, RequestDigest: digestBytes("library.template.create/v1", []byte(tid)), GovernanceRecordIDs: createIDs},
			Revision: app.PublishTemplateRevisionRequest{ID: domain.RevisionID("revision." + rid), BundleArtifact: artifact, BundleDigest: digest, BundleSizeBytes: uint64(len(p.source)), AuthorAPIVersion: p.representation.AuthorVersion, RendererVersion: render.PackageRendererVersion, CanonicalVersion: render.TemplateFormatVersion, CanonicalDigest: p.representation.Digest, RepresentationJSON: string(p.representation.JSON), PublishedBy: c.Work.ActorID, IdempotencyKey: "library." + rid, RequestDigest: digestBytes("library.revision.publish/v1", []byte(tid), p.source), GovernanceRecordIDs: revisionIDs},
		})
	}
	return store.PublishLibrary(ctx, publication)
}

var sourceCollectionKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
