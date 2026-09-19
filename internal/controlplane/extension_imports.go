package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"github.com/OutboundSpade/LabCentral/pkg/ocilayout"
	"strings"
	"time"
)

func providerIdentity(p app.Provider) string {
	if v, ok := p.(interface{ ExtensionIdentity() string }); ok {
		return v.ExtensionIdentity()
	}
	return ""
}
func (s *Service) RequestImport(ctx context.Context, actor domain.UserID, project domain.ProjectID, buildID domain.OperationID, targetID domain.ProviderTargetID, key string) (app.ExtensionWork, error) {
	return s.requestImport(ctx, actor, project, buildID, targetID, key, "")
}
func (s *Service) requestImport(ctx context.Context, actor domain.UserID, project domain.ProjectID, buildID domain.OperationID, targetID domain.ProviderTargetID, key string, preparation domain.OperationID) (app.ExtensionWork, error) {
	var build app.ExtensionWork
	var e error
	if preparation == "" {
		build, e = s.GetExtensionWork(ctx, actor, project, buildID)
	} else {
		if e = s.authorizePreparation(ctx, actor, project, preparation); e != nil {
			return app.ExtensionWork{}, e
		}
		build, e = s.store.(app.BuildStore).GetExtensionWork(ctx, buildID)
		if e == nil && build.ProjectID != project {
			e = app.ErrNotFound
		}
	}
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
	if result.ContainerRuntime != nil && !target.Supports(0, "container-image-import-v1") {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	// Explicit imports need image-administrator authority. A v2 preparation
	// derives only this import from current launch authority; approval stays separate.
	if preparation == "" {
		if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform", TargetID: targetID}); e != nil {
			return app.ExtensionWork{}, e
		}
	}
	identity := providerIdentity(target.Provider)
	d := digestBytes("image.import/v1", []byte(result.Image.Digest), []byte(targetID), []byte(target.Context.ConfigFingerprint), []byte(identity), []byte(build.Request.Build.Recipe.Architecture))
	r := app.ExtensionWorkRequest{Kind: domain.WorkImageImport, PluginInstance: "core-import", PluginDigest: identity, BindingID: string(targetID), ConfigurationDigest: digestBytes("extension.configuration/v1", target.Context.Config), TemplateID: build.Request.TemplateID, DedupKey: d.Hex(), Import: &app.ImportWork{BuildID: buildID, TargetID: targetID, Architecture: build.Request.Build.Recipe.Architecture, TargetFingerprint: target.Context.ConfigFingerprint}}
	if preparation != "" {
		parent, err := s.store.(app.BuildStore).GetExtensionWork(ctx, preparation)
		if err != nil {
			return app.ExtensionWork{}, err
		}
		r.Version = 2
		r.PreparationID = preparation
		r.TemplateID = parent.Request.TemplateID
	}
	return s.newExtensionWork(ctx, actor, project, r, key)
}
func (s *Service) executeImport(parent context.Context, c app.ExtensionWorkClaim) error {
	ctx, done := s.workContext(parent, c, 15*time.Minute)
	defer done()
	r := c.Work.Request.Import
	if r == nil {
		return fmt.Errorf("invalid import work")
	}
	if r.RetireBindingID != "" {
		return s.executeImageRetirement(ctx, c)
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
	actor := c.Work.ActorID
	if c.Work.Request.PreparationID != "" {
		authorized, err := s.preparationWorkActor(ctx, c.Work.ID)
		if err == nil {
			actor = authorized
		} else if !errors.Is(err, app.ErrAuthorityChanged) {
			return err
		} else if !c.Recover {
			return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
		}
	}
	session, e := s.targets.AcquireForSubject(ctx, r.TargetID, actor, app.InvocationScope{WorkSubject: &domain.WorkSubject{Kind: c.Work.Request.Kind, ID: string(c.Work.ID)}, InvocationID: c.Work.InvocationID, ExpiresAt: time.Now().Add(15 * time.Minute)}, true)
	if e != nil {
		return e
	}
	defer session.Close()
	if session.Context.ConfigFingerprint != r.TargetFingerprint || providerIdentity(session.Provider) != c.Work.Request.PluginDigest || !session.Supports(0, "image-import-v1", "artifact-transfer-v1") {
		return fmt.Errorf("original provider identity and target required to recover image import")
	}
	if result.ContainerRuntime != nil && !session.Supports(0, "container-image-import-v1") {
		return ErrAccessUnavailable
	}
	provider, ok := session.Provider.(app.ImageImporter)
	if !ok {
		return ErrAccessUnavailable
	}
	call := workCall(c)
	call.TargetID = string(r.TargetID)
	request := app.ImportRequest{ContainerRuntime: result.ContainerRuntime, Call: call, Target: session.Context, Artifact: result.Image, Architecture: r.Architecture, ProvenanceDigest: result.ProvenanceDigest}

	if session.Supports(0, "image-import-reservations-v1") {
		reservations, ok := s.store.(app.ImageImportReservationStore)
		planner, supported := session.Provider.(app.ImageImportPlanner)
		if !ok || !supported {
			return ErrAccessUnavailable
		}
		plan, err := reservations.GetImageImportPlan(ctx, c.Work.ID)
		if errors.Is(err, app.ErrNotFound) {
			excluded, snapshotErr := s.store.ListTargetAllocationKeys(ctx, r.TargetID)
			if snapshotErr != nil {
				return snapshotErr
			}
			for attempt := 0; attempt < app.MaximumAllocationPlanAttempts; attempt++ {
				plan, err = planner.PlanImageImport(ctx, request, excluded)
				if err != nil {
					return err
				}
				err = reservations.ReserveImageImportPlan(ctx, c, plan)
				if err == nil {
					break
				}
				var conflict *app.AllocationConflictError
				if !errors.As(err, &conflict) || len(conflict.Conflicts) == 0 {
					return err
				}
				excluded = append(excluded, conflict.Conflicts...)
			}
		}
		if err != nil {
			return err
		}
		request.ReservedPlan = &plan
	}
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
		if c.Work.Request.PreparationID != "" {
			_, e = s.preparationWorkActor(ctx, c.Work.ID)
		} else {
			e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform", TargetID: r.TargetID})
		}
		if e != nil {
			return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
		}
		reader, e := s.artifacts.Open(ctx, app.AuthorizedArtifactRef{Reference: result.ArtifactRef, SubjectID: actor})
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
	if result.ContainerRuntime != nil && !strings.HasSuffix(binding.ImageRef, "@"+result.ContainerRuntime.ManifestDigest) {
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

	if retired {
		queued, err := s.requestImageRetirement(ctx, actor, project, id)
		if err != nil || queued {
			return err
		}
	}
	return s.store.(app.BuildStore).SetImageBindingApproval(ctx, id, actor, approved, retired, ids)
}
func (s *Service) RequestPreparation(ctx context.Context, actor domain.UserID, project domain.ProjectID, r app.PreparationWork, key string) (app.ExtensionWork, error) {
	if e := r.ValidateVersion(); e != nil {
		return app.ExtensionWork{}, e
	}
	owner := r.EffectiveOwner(actor)
	if e := s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: actor, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: string(project), TargetID: r.TargetID, OwnerKind: owner.Kind, OwnerID: owner.ID}); e != nil {
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
	if e = s.authorizeExtension(ctx, actor, project, app.ActionTemplateRead); e != nil {
		return app.ExtensionWork{}, e
	}
	request := app.ExtensionWorkRequest{Kind: domain.WorkPreparation, PluginInstance: "core-preparation", PluginDigest: "core.preparation/v1", BindingID: "default", TemplateID: rev.TemplateID, Preparation: &r}
	if r.Version == 2 {
		request.Version = 2
		request.PluginDigest = "core.preparation/v2"
	}
	return s.newExtensionWork(ctx, actor, project, request, key)
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
	if r.Version == 2 {
		e = s.authorizePreparation(ctx, c.Work.ActorID, c.Work.ProjectID, c.Work.ID)
	} else {
		e = s.authorizeExtension(ctx, c.Work.ActorID, c.Work.ProjectID, app.ActionTemplateRevisionPublish)
	}
	if e != nil {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
	}
	rev, data, e := s.OpenTemplateRevision(ctx, c.Work.ActorID, r.RevisionID)
	if e != nil {
		return e
	}
	preparation := domain.OperationID("")
	if r.Version == 2 {
		preparation = c.Work.ID
	}
	owner := r.EffectiveOwner(c.Work.ActorID)
	result := app.PreparationResult{}
	images := []domain.ResolvedBuildImage{}
	bindingIDs := []string{}
	if rev.RendererVersion == render.BuildRendererVersion || rev.RendererVersion == render.PackageRendererVersion {
		recipes, e := s.renderer.RequiredBuildRecipes(ctx, rev.RendererVersion, domain.RevisionContent{VerifiedBundle: data}, r.Inputs)
		if e != nil {
			return e
		}
		for _, recipeID := range recipes {
			build, e := s.requestBuild(ctx, c.Work.ActorID, c.Work.ProjectID, r.RevisionID, recipeID, string(c.Work.ID)+".build."+recipeID, map[string]app.ExtensionWork{}, preparation)
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
				result.Stage = "building"
				return s.yieldWorkProgress(ctx, c, result)
			}
			var built app.BuildResult
			if e = json.Unmarshal(build.Result, &built); e != nil {
				return e
			}
			if built.Image.MediaType == ocilayout.MediaType {
				packaged, err := s.requestContainerRuntime(ctx, c.Work, build, string(c.Work.ID)+".runtime."+recipeID)
				if err != nil {
					return err
				}
				result.BuildIDs = append(result.BuildIDs, packaged.ID)
				if packaged.State == domain.OperationFailed || packaged.State == domain.OperationCanceled {
					return s.finishWork(ctx, c, domain.OperationFailed, result, "runtime_packaging_failed")
				}
				if packaged.State != domain.OperationSucceeded {
					result.Stage = "packaging"
					return s.yieldWorkProgress(ctx, c, result)
				}
				build = packaged
			}
			imp, e := s.requestImport(ctx, c.Work.ActorID, c.Work.ProjectID, build.ID, r.TargetID, string(c.Work.ID)+".import."+recipeID, preparation)
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
				result.Stage = "importing"
				return s.yieldWorkProgress(ctx, c, result)
			}
			binding, e := store.GetImageBinding(ctx, string(imp.ID))
			if e != nil {
				return e
			}
			if binding.Retired {
				return s.finishWork(ctx, c, domain.OperationFailed, result, "image_retired")
			}
			if !binding.Approved {
				result.Stage = "awaiting-approval"
				return s.yieldWorkProgress(ctx, c, result)
			}
			session, e := s.targets.AcquireForSubject(ctx, r.TargetID, c.Work.ActorID, app.InvocationScope{WorkSubject: &domain.WorkSubject{Kind: c.Work.Request.Kind, ID: string(c.Work.ID)}, InvocationID: c.Work.InvocationID, ExpiresAt: time.Now().Add(time.Minute)}, true)
			if e != nil {
				return e
			}
			provider, ok := session.Provider.(app.ImageImporter)
			valid := ok && binding.ProviderDigest == providerIdentity(session.Provider) && binding.Binding.TargetConfigurationDigest == session.Context.ConfigFingerprint
			if valid {
				call := workCall(c)
				call.TargetID = string(r.TargetID)
				e = provider.ValidateImageBinding(ctx, app.ImportRequest{Call: call, Target: session.Context}, binding.Binding)
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
			images = append(images, domain.ResolvedBuildImage{RecipeID: recipeID, ImageRef: image})
		}
	}
	// Persisted image bindings already exist before entering the normal planner.
	// Admission still performs current authority, quota and exact-target checks.
	if e = s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionInstanceCreate, ScopeKind: app.ScopeProject, ScopeID: string(c.Work.ProjectID), TargetID: r.TargetID, OwnerKind: owner.Kind, OwnerID: owner.ID}); e != nil {
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
		deployed, err := s.CreateInstanceRevisionTargetForLifetime(ctx, c.Work.ActorID, c.Work.ProjectID, owner.Kind, owner.ID, r.RevisionID, data, r.Lifetime, r.Acknowledged, "prepare."+string(c.Work.ID), r.TargetID, nil, option)
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
