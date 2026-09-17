package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"io"
	"slices"
	"strings"
	"time"
)

type SourceBinding struct {
	InstanceID, ID, PluginDigest string
	Projects                     []domain.ProjectID
	Configuration                []byte
	Credential                   func(context.Context) ([]byte, error)
	Source                       app.ExtensionSource
	Available                    func() bool
}

func (s *Service) SetSources(bindings []SourceBinding) error {
	s.sources = map[string]SourceBinding{}
	for _, b := range bindings {
		key := b.InstanceID + "/" + b.ID
		if b.InstanceID == "" || b.ID == "" || b.Source == nil || len(b.Projects) == 0 || s.sources[key].Source != nil {
			return fmt.Errorf("invalid or duplicate source binding")
		}
		b.Configuration = slices.Clone(b.Configuration)
		b.Projects = slices.Clone(b.Projects)
		s.sources[key] = b
	}
	return nil
}
func (s *Service) authorizeExtension(ctx context.Context, actor domain.UserID, project domain.ProjectID, action app.PermissionAction) error {
	if s.operationsAuthorizer == nil {
		return ErrAccessUnavailable
	}
	return s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: action, ScopeKind: app.ScopeProject, ScopeID: string(project)})
}
func (s *Service) ResolveSource(ctx context.Context, actor domain.UserID, project domain.ProjectID, template domain.TemplateID, binding, reference, key string) (app.ExtensionWork, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return app.ExtensionWork{}, err
	}
	b, ok := s.sources[binding]
	if !ok || !slices.Contains(b.Projects, project) || b.Available != nil && !b.Available() {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	if len(reference) == 0 || len(reference) > 1024 || strings.ContainsAny(reference, "\x00\r\n") {
		return app.ExtensionWork{}, fmt.Errorf("source reference is invalid")
	}
	t, err := s.GetLabTemplate(ctx, template)
	if err != nil {
		return app.ExtensionWork{}, err
	}
	if t.ProjectID != project {
		return app.ExtensionWork{}, app.ErrNotFound
	}
	store, ok := s.store.(app.ExtensionWorkStore)
	if !ok {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	request := app.ExtensionWorkRequest{Version: 1, Kind: domain.WorkSource, PluginInstance: b.InstanceID, PluginDigest: b.PluginDigest, BindingID: b.ID, ConfigurationDigest: digestBytes("extension.configuration/v1", b.Configuration), TemplateID: template, Reference: reference}
	raw, err := json.Marshal(request)
	if err != nil {
		return app.ExtensionWork{}, err
	}
	op, err := s.ids.New("op")
	if err != nil {
		return app.ExtensionWork{}, err
	}
	inv, err := s.ids.New("inv")
	if err != nil {
		return app.ExtensionWork{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return app.ExtensionWork{}, err
	}
	return store.CreateExtensionWork(ctx, app.CreateExtensionWork{Work: app.ExtensionWork{ID: domain.OperationID(op), ProjectID: project, ActorID: actor, InvocationID: domain.InvocationID(inv), Request: request}, IdempotencyKey: key, RequestDigest: digestBytes("extension.resolve/v1", []byte(project), raw), GovernanceRecordIDs: ids})
}
func (s *Service) ListExtensionWork(ctx context.Context, actor domain.UserID, project domain.ProjectID) ([]app.ExtensionWork, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return nil, err
	}
	store, ok := s.store.(app.ExtensionWorkStore)
	if !ok {
		return nil, ErrAccessUnavailable
	}
	return store.ListExtensionWork(ctx, project)
}
func (s *Service) GetExtensionWork(ctx context.Context, actor domain.UserID, project domain.ProjectID, id domain.OperationID) (app.ExtensionWork, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return app.ExtensionWork{}, err
	}
	store, ok := s.store.(app.ExtensionWorkStore)
	if !ok {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	w, e := store.GetExtensionWork(ctx, id)
	if e == nil && w.ProjectID != project {
		return app.ExtensionWork{}, app.ErrNotFound
	}
	return w, e
}
func (s *Service) CancelExtensionWork(ctx context.Context, actor domain.UserID, project domain.ProjectID, id domain.OperationID) error {
	if _, err := s.GetExtensionWork(ctx, actor, project, id); err != nil {
		return err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return err
	}
	return s.store.(app.ExtensionWorkStore).CancelExtensionWork(ctx, id, actor, ids)
}

// RunSourceWork never interprets a transport error as absence. On recovery the
// same invocation is inspected before any redispatch; only explicit unknown
// permits an idempotent Resolve with the original invocation ID.
func (s *Service) RunSourceWork(ctx context.Context, owner string, report func(error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for key, b := range s.sources {
			if ctx.Err() != nil {
				return
			}
			if b.Available != nil && !b.Available() {
				continue
			}
			store, ok := s.store.(app.ExtensionWorkStore)
			if !ok {
				return
			}
			claim, err := store.ClaimExtensionWork(ctx, owner, key, domain.WorkSource, 30*time.Second)
			if err != nil {
				continue
			}
			if err = s.executeSource(ctx, store, b, claim); err != nil && report != nil {
				report(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) executeSource(parent context.Context, store app.ExtensionWorkStore, b SourceBinding, c app.ExtensionWorkClaim) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	leaseDone := make(chan struct{})
	defer func() { cancel(); <-leaseDone }()
	go func() {
		defer close(leaseDone)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := store.RenewExtensionWork(ctx, c, 30*time.Second); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	finish := func(state domain.OperationState, result json.RawMessage, code string) error {
		ids, e := s.newGovernanceRecordIDs()
		if e != nil {
			return e
		}
		return store.FinishExtensionWork(ctx, c, state, result, code, ids)
	}
	call := app.ProviderCall{RequestID: string(c.Work.ID), OperationID: string(c.Work.ID), InvocationID: string(c.Work.InvocationID), IdempotencyKey: string(c.Work.InvocationID), Generation: 1, Fence: uint64(c.Operation.FencingToken)}
	authorized := s.authorizeExtension(ctx, c.Work.ActorID, c.Work.ProjectID, app.ActionTemplateRevisionPublish) == nil
	current, e := store.GetExtensionWork(ctx, c.Work.ID)
	if e != nil {
		return e
	}
	if current.CancelRequested || !authorized {
		if err := b.Source.Cancel(ctx, call); err != nil {
			return err
		}
		return finish(domain.OperationCanceled, nil, "")
	}
	if c.Work.Request.PluginDigest != b.PluginDigest || c.Work.Request.ConfigurationDigest != digestBytes("extension.configuration/v1", b.Configuration) || !slices.Contains(b.Projects, c.Work.ProjectID) {
		return fmt.Errorf("source binding changed; restore the original binding to recover the invocation")
	}
	var result app.ExtensionSourceResult
	dispatch := !c.Recover
	if c.Recover {
		state, r, err := b.Source.Recover(ctx, call)
		if err != nil {
			return err
		}
		switch state {
		case "unknown":
			dispatch = true
		case "succeeded":
			result = r
		case "failed":
			return finish(domain.OperationFailed, nil, "source_failed")
		case "cancelled":
			return finish(domain.OperationCanceled, nil, "")
		default:
			return fmt.Errorf("source invocation is still unresolved")
		}
	}
	if dispatch {
		var credential []byte
		if b.Credential != nil {
			credential, e = b.Credential(ctx)
			if e != nil {
				return e
			}
		}
		result, e = b.Source.Resolve(ctx, app.ExtensionSourceRequest{Call: call, BindingID: b.ID, Reference: c.Work.Request.Reference, Configuration: b.Configuration, Credential: credential})
		clear(credential)
		if e != nil {
			return e
		}
	}
	reader, e := b.Source.ReadArtifact(ctx, call, result.Bundle)
	if e != nil {
		return e
	}
	data, e := io.ReadAll(io.LimitReader(reader, int64(labbundle.MaxBytes)+1))
	_ = reader.Close()
	if e != nil {
		return e
	}
	sum := sha256.Sum256(data)
	if uint64(len(data)) != result.Bundle.Size || hex.EncodeToString(sum[:]) != result.Bundle.Digest.Hex() {
		return finish(domain.OperationFailed, nil, "source_artifact_invalid")
	}
	if !labbundle.Archive(data) {
		return finish(domain.OperationFailed, nil, "source_bundle_required")
	}
	if _, e = labbundle.Read(data); e != nil {
		return finish(domain.OperationFailed, nil, "source_bundle_invalid")
	}
	if e = s.authorizeExtension(ctx, c.Work.ActorID, c.Work.ProjectID, app.ActionTemplateRevisionPublish); e != nil {
		return finish(domain.OperationFailed, nil, "authority_changed")
	}
	current, e = store.GetExtensionWork(ctx, c.Work.ID)
	if e != nil {
		return e
	}
	if current.CancelRequested {
		return finish(domain.OperationCanceled, nil, "")
	}
	// Publication is idempotent by the durable operation, including a crash after
	// publication but before completion. The original author remains authoritative.
	revision, e := s.publishTemplateRevision(ctx, c.Work.ActorID, c.Work.Request.TemplateID, data, "source."+string(c.Work.ID), &app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt})
	if e != nil {
		return e
	}
	output, e := json.Marshal(struct {
		Source     app.ExtensionSourceResult `json:"source"`
		RevisionID domain.RevisionID         `json:"revisionId"`
	}{result, revision.ID})
	if e != nil {
		return e
	}
	return finish(domain.OperationSucceeded, output, "")
}

type SourceBindingView struct {
	Binding   string `json:"binding"`
	Available bool   `json:"available"`
}

func (s *Service) SourceBindings(ctx context.Context, actor domain.UserID, project domain.ProjectID) ([]SourceBindingView, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return nil, err
	}
	out := []SourceBindingView{}
	for key, b := range s.sources {
		if slices.Contains(b.Projects, project) {
			out = append(out, SourceBindingView{Binding: key, Available: b.Available == nil || b.Available()})
		}
	}
	slices.SortFunc(out, func(a, b SourceBindingView) int { return strings.Compare(a.Binding, b.Binding) })
	return out, nil
}
