package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"time"
)

func providerIdentity(p app.Provider) string {
	if v, ok := p.(interface{ ExtensionIdentity() string }); ok {
		return v.ExtensionIdentity()
	}
	return ""
}
func (s *Service) RequestImport(ctx context.Context, actor domain.UserID, project domain.ProjectID, buildID domain.OperationID, targetID domain.ProviderTargetID, key string) (app.ExtensionWork, error) {
	build, e := s.GetExtensionWork(ctx, actor, project, buildID)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	if build.State != domain.OperationSucceeded || build.Request.Build == nil {
		return app.ExtensionWork{}, fmt.Errorf("completed build required")
	}
	var result app.BuildResult
	if e = json.Unmarshal(build.Result, &result); e != nil {
		return app.ExtensionWork{}, e
	}
	target, e := s.targets.AcquireForSubject(ctx, targetID, actor, app.InvocationScope{}, false)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	defer target.Close()
	if !target.Supports(0, "image-import-v1", "artifact-transfer-v1") || providerIdentity(target.Provider) == "" {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	// Import is an administrator-authorized provider mutation, separately from
	// deployment. It never receives an external URL or implicitly approves images.
	if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform", TargetID: targetID}); e != nil {
		return app.ExtensionWork{}, e
	}
	identity := providerIdentity(target.Provider)
	d := digestBytes("image.import/v1", []byte(result.Image.Digest), []byte(targetID), []byte(target.Context.ConfigFingerprint), []byte(identity), []byte(build.Request.Build.Recipe.Architecture))
	return s.newExtensionWork(ctx, actor, project, app.ExtensionWorkRequest{Kind: domain.WorkImageImport, PluginInstance: "core-import", PluginDigest: identity, BindingID: string(targetID), ConfigurationDigest: digestBytes("extension.configuration/v1", target.Context.Config), TemplateID: build.Request.TemplateID, DedupKey: d.Hex(), Import: &app.ImportWork{BuildID: buildID, TargetID: targetID, Architecture: build.Request.Build.Recipe.Architecture, TargetFingerprint: target.Context.ConfigFingerprint}}, key)
}
func (s *Service) executeImport(parent context.Context, c app.ExtensionWorkClaim) error {
	ctx, done := s.workContext(parent, c, 15*time.Minute)
	defer done()
	r := c.Work.Request.Import
	if r == nil {
		return fmt.Errorf("invalid import work")
	}
	store := s.store.(app.BuildStore)
	build, e := store.GetExtensionWork(ctx, r.BuildID)
	if e != nil {
		return e
	}
	var result app.BuildResult
	if e = json.Unmarshal(build.Result, &result); e != nil {
		return e
	}
	session, e := s.targets.AcquireForSubject(ctx, r.TargetID, c.Work.ActorID, app.InvocationScope{InvocationID: c.Work.InvocationID, ExpiresAt: time.Now().Add(15 * time.Minute)}, true)
	if e != nil {
		return e
	}
	defer session.Close()
	if session.Context.ConfigFingerprint != r.TargetFingerprint || providerIdentity(session.Provider) != c.Work.Request.PluginDigest || !session.Supports(0, "image-import-v1", "artifact-transfer-v1") {
		return fmt.Errorf("original provider identity and target required to recover image import")
	}
	provider, ok := session.Provider.(app.ImageImporter)
	if !ok {
		return ErrAccessUnavailable
	}
	call := workCall(c)
	call.TargetID = string(r.TargetID)
	request := app.ImportRequest{Call: call, Target: session.Context, Artifact: result.Image, Architecture: r.Architecture, ProvenanceDigest: result.ProvenanceDigest}
	state := "unknown"
	var binding app.ImageBinding
	if c.Recover {
		state, binding, e = provider.RecoverImport(ctx, request)
		if e != nil {
			return e
		}
	}
	if state == "unknown" {
		current, e := store.GetExtensionWork(ctx, c.Work.ID)
		if e != nil {
			return e
		}
		if current.CancelRequested {
			return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
		}
		if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform", TargetID: r.TargetID}); e != nil {
			return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
		}
		reader, e := s.artifacts.Open(ctx, app.AuthorizedArtifactRef{Reference: result.ArtifactRef, SubjectID: c.Work.ActorID})
		if e != nil {
			return e
		}
		request.Artifact, e = provider.WriteArtifact(ctx, call, result.Image, reader)
		_ = reader.Close()
		if e != nil {
			return e
		}
		state, binding, e = provider.ImportImage(ctx, request)
		if e != nil {
			return e
		}
	}
	if state == "failed" {
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "image_import_failed")
	}
	if state != "succeeded" {
		return fmt.Errorf("image import remains uncertain")
	}
	if binding.ArtifactDigest != result.Image.Digest || binding.Architecture != r.Architecture || binding.TargetConfigurationDigest != r.TargetFingerprint {
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "image_binding_invalid")
	}
	if e = provider.ValidateImageBinding(ctx, request, binding); e != nil {
		return e
	}
	record := app.StoredImageBinding{ID: string(c.Work.ID), ProjectID: c.Work.ProjectID, TargetID: r.TargetID, ProviderDigest: c.Work.Request.PluginDigest, ImportOperationID: c.Work.ID, Binding: binding}
	// Even a canceled waiter cannot discard an already imported image. Retain it
	// unapproved for explicit operator retirement; never delete it with a lab.
	if e = store.PutImageBinding(ctx, c, record); e != nil {
		return e
	}
	if e = store.ReferenceExtensionArtifact(ctx, c.Work.ID, result.ArtifactRef); e != nil {
		return e
	}
	return s.finishWork(ctx, c, domain.OperationSucceeded, record, "")
}
func (s *Service) ImageBinding(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string) (app.StoredImageBinding, error) {
	if e := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRead); e != nil {
		return app.StoredImageBinding{}, e
	}
	b, e := s.store.(app.BuildStore).GetImageBinding(ctx, id)
	if e == nil && b.ProjectID != project {
		return app.StoredImageBinding{}, app.ErrNotFound
	}
	return b, e
}
func (s *Service) SetImageApproval(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string, approved, retired bool) error {
	if _, e := s.ImageBinding(ctx, actor, project, id); e != nil {
		return e
	}
	if e := s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform"}); e != nil {
		return e
	}
	ids, e := s.newGovernanceRecordIDs()
	if e != nil {
		return e
	}
	return s.store.(app.BuildStore).SetImageBindingApproval(ctx, id, actor, approved, retired, ids)
}
func (s *Service) RequestPreparation(ctx context.Context, actor domain.UserID, project domain.ProjectID, r app.PreparationWork, key string) (app.ExtensionWork, error) {
	if e := s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: string(project), TargetID: r.TargetID, OwnerKind: app.OwnerUser, OwnerID: string(actor)}); e != nil {
		return app.ExtensionWork{}, e
	}
	rev, _, e := s.OpenTemplateRevision(ctx, actor, r.RevisionID)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	tpl, e := s.GetLabTemplate(ctx, rev.TemplateID)
	if e != nil || tpl.ProjectID != project {
		return app.ExtensionWork{}, app.ErrNotFound
	}
	return s.newExtensionWork(ctx, actor, project, app.ExtensionWorkRequest{Kind: domain.WorkPreparation, PluginInstance: "core-preparation", PluginDigest: "core.preparation/v1", BindingID: "default", TemplateID: rev.TemplateID, Preparation: &r}, key)
}
func (s *Service) executePreparation(parent context.Context, c app.ExtensionWorkClaim) error {
	ctx, done := s.workContext(parent, c, 2*time.Minute)
	defer done()
	store := s.store.(app.BuildStore)
	r := c.Work.Request.Preparation
	if r == nil {
		return fmt.Errorf("invalid preparation")
	}
	current, e := store.GetExtensionWork(ctx, c.Work.ID)
	if e != nil {
		return e
	}
	if current.CancelRequested {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
	}
	if e = s.authorizeExtension(ctx, c.Work.ActorID, c.Work.ProjectID, app.ActionTemplateRevisionPublish); e != nil {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
	}
	rev, data, e := s.OpenTemplateRevision(ctx, c.Work.ActorID, r.RevisionID)
	if e != nil {
		return e
	}
	result := app.PreparationResult{}
	images := []domain.ResolvedBuildImage{}
	bindingIDs := []string{}
	if rev.RendererVersion == render.BuildRendererVersion {
		bundle, e := labbundle.Read(data)
		if e != nil {
			return e
		}
		for _, recipe := range bundle.Builds().Recipes {
			build, e := s.RequestBuild(ctx, c.Work.ActorID, c.Work.ProjectID, r.RevisionID, recipe.ID, string(c.Work.ID)+".build."+recipe.ID)
			if e != nil {
				return e
			}
			if e = store.LinkExtensionWork(ctx, c.Work.ID, build.ID); e != nil {
				return e
			}
			result.BuildIDs = append(result.BuildIDs, build.ID)
			if build.State == domain.OperationFailed || build.State == domain.OperationCanceled {
				return s.finishWork(ctx, c, domain.OperationFailed, result, "build_failed")
			}
			if build.State != domain.OperationSucceeded {
				return store.YieldExtensionWork(ctx, c)
			}
			imp, e := s.RequestImport(ctx, c.Work.ActorID, c.Work.ProjectID, build.ID, r.TargetID, string(c.Work.ID)+".import."+recipe.ID)
			if e != nil {
				return e
			}
			if e = store.LinkExtensionWork(ctx, c.Work.ID, imp.ID); e != nil {
				return e
			}
			result.ImportIDs = append(result.ImportIDs, imp.ID)
			if imp.State == domain.OperationFailed || imp.State == domain.OperationCanceled {
				return s.finishWork(ctx, c, domain.OperationFailed, result, "import_failed")
			}
			if imp.State != domain.OperationSucceeded {
				return store.YieldExtensionWork(ctx, c)
			}
			binding, e := store.GetImageBinding(ctx, string(imp.ID))
			if e != nil {
				return e
			}
			if !binding.Approved || binding.Retired {
				return store.YieldExtensionWork(ctx, c)
			}
			session, e := s.targets.AcquireForSubject(ctx, r.TargetID, c.Work.ActorID, app.InvocationScope{InvocationID: c.Work.InvocationID, ExpiresAt: time.Now().Add(time.Minute)}, true)
			if e != nil {
				return e
			}
			provider, ok := session.Provider.(app.ImageImporter)
			valid := ok && binding.ProviderDigest == providerIdentity(session.Provider) && binding.Binding.TargetConfigurationDigest == session.Context.ConfigFingerprint
			if valid {
				e = provider.ValidateImageBinding(ctx, app.ImportRequest{Call: workCall(c), Target: session.Context}, binding.Binding)
			}
			session.Close()
			if !valid || e != nil {
				return fmt.Errorf("approved target image binding no longer valid")
			}
			image, e := domain.NewImageRef(binding.Binding.ImageRef)
			if e != nil {
				return e
			}
			bindingIDs = append(bindingIDs, binding.ID)
			images = append(images, domain.ResolvedBuildImage{RecipeID: recipe.ID, ImageRef: image})
		}
	}
	// Persisted image bindings already exist before entering the normal planner.
	// Admission still performs current authority, quota and exact-target checks.
	if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: string(c.Work.ProjectID), TargetID: r.TargetID, OwnerKind: app.OwnerUser, OwnerID: string(c.Work.ActorID)}); e != nil {
		return s.finishWork(ctx, c, domain.OperationCanceled, result, "authority_changed")
	}
	current, e = store.GetExtensionWork(ctx, c.Work.ID)
	if e != nil {
		return e
	}
	if current.CancelRequested {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
	}
	option := RenderOptions{Inputs: r.Inputs, BuildImages: images, ImageBindingIDs: bindingIDs, WorkFence: &app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Attempt: c.Operation.Attempt, Generation: 1}}
	if len(r.Owners) > 0 {
		if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionInstanceBatchCreate, ScopeKind: app.ScopeProject, ScopeID: string(c.Work.ProjectID), TargetID: r.TargetID}); e != nil {
			return s.finishWork(ctx, c, domain.OperationCanceled, result, "authority_changed")
		}
		deployed, err := s.CreateInstanceBatchRevisionTargetForLifetime(ctx, c.Work.ActorID, c.Work.ProjectID, r.Owners, r.RevisionID, data, r.Lifetime, r.Acknowledged, "prepare."+string(c.Work.ID), r.TargetID, nil, option)
		e = err
		if e == nil {
			result.Deployment, _ = json.Marshal(deployed)
		}
	} else {
		deployed, err := s.CreateInstanceRevisionTargetForLifetime(ctx, c.Work.ActorID, c.Work.ProjectID, app.OwnerUser, string(c.Work.ActorID), r.RevisionID, data, r.Lifetime, r.Acknowledged, "prepare."+string(c.Work.ID), r.TargetID, nil, option)
		e = err
		if e == nil {
			result.Deployment, _ = json.Marshal(deployed)
		}
	}
	if e != nil {
		return s.finishWork(ctx, c, domain.OperationFailed, result, "admission_failed")
	}
	return s.finishWork(ctx, c, domain.OperationSucceeded, result, "")
}
