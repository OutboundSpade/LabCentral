package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"io"
	"slices"
	"strings"
	"time"
)

type BuilderBinding struct {
	Dependencies                 bool
	Requirement, Kind            string
	InstanceID, ID, PluginDigest string
	Projects                     []domain.ProjectID
	Configuration                []byte
	Limits                       app.BuildLimits
	Builder                      app.Builder
	Available                    func() bool
}

func (s *Service) SetBuilders(bindings []BuilderBinding) error {
	s.builders = map[string]BuilderBinding{}
	for _, b := range bindings {
		k := b.InstanceID + "/" + b.ID
		if b.InstanceID == "" || b.ID == "" || b.Builder == nil || s.builders[k].Builder != nil || len(b.Projects) == 0 || b.Limits.TimeoutSeconds == 0 || b.Limits.OutputBytes == 0 || b.Limits.InputBytes == 0 || b.Limits.Egress == nil {
			return fmt.Errorf("invalid builder binding")
		}
		b.Projects = slices.Clone(b.Projects)
		b.Configuration = slices.Clone(b.Configuration)
		b.Limits.Egress = slices.Clone(b.Limits.Egress)
		if b.Requirement != "" {
			if len(b.Requirement) > 64 || b.Kind == "" {
				return fmt.Errorf("logical builder requires identity and kind")
			}
			for _, existing := range s.builders {
				if existing.Requirement == b.Requirement {
					for _, project := range b.Projects {
						if slices.Contains(existing.Projects, project) {
							return fmt.Errorf("ambiguous logical builder requirement in project")
						}
					}
				}
			}
		}
		s.builders[k] = b
	}
	return nil
}
func (s *Service) newExtensionWork(ctx context.Context, actor domain.UserID, project domain.ProjectID, r app.ExtensionWorkRequest, key string) (app.ExtensionWork, error) {
	store, ok := s.store.(app.BuildStore)
	if !ok {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	if e := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); e != nil {
		return app.ExtensionWork{}, e
	}
	if r.Version == 0 {
		r.Version = 1
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	op, e := s.ids.New("op")
	if e != nil {
		return app.ExtensionWork{}, e
	}
	inv, e := s.ids.New("inv")
	if e != nil {
		return app.ExtensionWork{}, e
	}
	ids, e := s.newGovernanceRecordIDs()
	if e != nil {
		return app.ExtensionWork{}, e
	}
	return store.CreateExtensionWork(ctx, app.CreateExtensionWork{Work: app.ExtensionWork{ID: domain.OperationID(op), ProjectID: project, ActorID: actor, InvocationID: domain.InvocationID(inv), Request: r}, IdempotencyKey: key, RequestDigest: digestBytes("extension.work/v1", []byte(project), raw), GovernanceRecordIDs: ids})
}
func (s *Service) commitExtensionArtifact(ctx context.Context, data []byte) (domain.ArtifactRef, domain.SHA256Digest, error) {
	sum := sha256.Sum256(data)
	d, _ := domain.ParseSHA256Digest(hex.EncodeToString(sum[:]))
	m := app.VerifiedManifest{Digest: d, FileCount: 1, ByteCount: uint64(len(data))}
	u, e := s.artifacts.Stage(ctx, m)
	if e != nil {
		return "", d, e
	}
	v, e := s.artifacts.Write(ctx, u, bytes.NewReader(data))
	if e != nil {
		return "", d, e
	}
	if v.Digest != d || v.Size != uint64(len(data)) {
		return "", d, fmt.Errorf("artifact verification failed")
	}
	ref, e := s.artifacts.Commit(ctx, u, m)
	return ref, d, e
}
func (s *Service) RequestBuild(ctx context.Context, actor domain.UserID, project domain.ProjectID, revision domain.RevisionID, recipeID, key string) (app.ExtensionWork, error) {
	return s.requestBuild(ctx, actor, project, revision, recipeID, key, map[string]app.ExtensionWork{})
}
func (s *Service) requestBuild(ctx context.Context, actor domain.UserID, project domain.ProjectID, revision domain.RevisionID, recipeID, key string, memo map[string]app.ExtensionWork) (app.ExtensionWork, error) {
	if work, ok := memo[recipeID]; ok {
		return work, nil
	}

	if e := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); e != nil {
		return app.ExtensionWork{}, e
	}
	rev, data, e := s.OpenTemplateRevision(ctx, actor, revision)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	tpl, e := s.GetLabTemplate(ctx, rev.TemplateID)
	if e != nil || tpl.ProjectID != project {
		return app.ExtensionWork{}, app.ErrNotFound
	}
	bundle, e := labbundle.Read(data)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	var recipe *labbundle.BuildRecipe
	if bundle.Builds() != nil {
		for _, r := range bundle.Builds().Recipes {
			if r.ID == recipeID {
				r := r
				recipe = &r
				break
			}
		}
	}
	if recipe == nil {
		return app.ExtensionWork{}, fmt.Errorf("recipe not found")
	}
	b, ok := s.resolveRecipeBuilder(bundle, *recipe, project)
	if !ok {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}

	if len(recipe.DependsOn) > 0 && !b.Dependencies {
		return app.ExtensionWork{}, ErrAccessUnavailable
	}
	dependencies := []app.BuildDependencyWork{}
	for _, name := range recipe.DependsOn {
		childKey := "build.dependency." + digestBytes("build.dependency.request/v1", []byte(key), []byte(name)).Hex()
		child, err := s.requestBuild(ctx, actor, project, revision, name, childKey, memo)
		if err != nil {
			return app.ExtensionWork{}, err
		}
		dependencies = append(dependencies, app.BuildDependencyWork{RecipeID: name, BuildID: child.ID})
	}
	if bundle.Manifest.Package != nil {
		slices.SortFunc(dependencies, func(a, b app.BuildDependencyWork) int { return strings.Compare(a.RecipeID, b.RecipeID) })
	}
	// Only declared files and nonsecret declared inputs cross the builder boundary.
	type file struct {
		Path string `json:"path"`
		Data []byte `json:"data"`
	}
	files := []file{}
	for _, p := range recipe.Context {
		name := p
		if recipe.ContextRoot != "" && recipe.ContextRoot != "." {
			name = strings.TrimPrefix(p, recipe.ContextRoot+"/")
		}
		files = append(files, file{name, bundle.Files[p]})
	}
	slices.SortFunc(files, func(a, b file) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	declaredInputs := recipe.Inputs
	if bundle.Manifest.Package != nil {
		declaredInputs = slices.Clone(recipe.Inputs)
		slices.SortFunc(declaredInputs, func(a, b labbundle.BuildInput) int { return strings.Compare(a.Name, b.Name) })
	}
	input, e := json.Marshal(struct {
		Version uint32                 `json:"version"`
		Files   []file                 `json:"files"`
		Inputs  []labbundle.BuildInput `json:"inputs"`
	}{1, files, declaredInputs})
	if e != nil {
		return app.ExtensionWork{}, e
	}
	if uint64(len(input)) > b.Limits.InputBytes {
		return app.ExtensionWork{}, fmt.Errorf("build input exceeds binding limit")
	}
	ref, d, e := s.commitExtensionArtifact(ctx, input)
	if e != nil {
		return app.ExtensionWork{}, e
	}
	raw, _ := json.Marshal(recipe)
	limits, _ := json.Marshal(b.Limits)
	recipeDigest := digestBytes("build.recipe/v1", raw, []byte(d), []byte(b.PluginDigest), b.Configuration, limits)
	if bundle.Manifest.Package != nil {
		// Portable aliases and paths outside the transferred context do not
		// change a build. Bind only effective inputs and the installed toolchain.
		configuration, err := render.DecodeJSON([]byte(recipe.ConfigurationJSON))
		if err != nil {
			return app.ExtensionWork{}, err
		}
		canonical, err := render.Canonicalize(configuration)
		if err != nil {
			return app.ExtensionWork{}, err
		}
		baseDigests := slices.Clone(recipe.BaseImageDigests)
		slices.Sort(baseDigests)
		effective, _ := json.Marshal(struct {
			Schema, Architecture, Configuration string
			BaseImageDigests                    []string
		}{recipe.Schema, recipe.Architecture, string(canonical), baseDigests})
		recipeDigest = digestBytes("build.portable-effective/v1", effective, []byte(d), []byte(b.InstanceID), []byte(b.ID), []byte(b.PluginDigest), b.Configuration, limits)
	}
	if len(dependencies) > 0 {
		encoded, _ := json.Marshal(dependencies)
		recipeDigest = digestBytes("build.recipe-dependencies/v1", []byte(recipeDigest), encoded)
	}
	r := app.ExtensionWorkRequest{Kind: domain.WorkBuild, PluginInstance: b.InstanceID, PluginDigest: b.PluginDigest, BindingID: b.ID, ConfigurationDigest: digestBytes("extension.configuration/v1", b.Configuration), TemplateID: rev.TemplateID, DedupKey: recipeDigest.Hex(), Build: &app.BuildWork{Dependencies: dependencies, RevisionID: revision, Recipe: portableRecipe(*recipe), RecipeDigest: recipeDigest, ContextArtifact: ref, ContextDigest: d, ContextSize: uint64(len(input)), Limits: b.Limits}}
	w, e := s.newExtensionWork(ctx, actor, project, r, key)
	if e == nil {
		e = s.store.(app.BuildStore).ReferenceExtensionArtifact(ctx, w.ID, ref)
	}
	if e == nil {
		memo[recipeID] = w
	}
	return w, e
}

// A portable recipe resolves a logical requirement only within its project.
// Legacy bundles continue to select the exact installation/binding identity.
func (s *Service) resolveRecipeBuilder(bundle labbundle.Bundle, recipe labbundle.BuildRecipe, project domain.ProjectID) (BuilderBinding, bool) {
	b, ok := s.builders[recipe.Builder]
	if bundle.Manifest.Package != nil {
		ok = false
		for _, candidate := range s.builders {
			if candidate.Requirement != recipe.Builder || !slices.Contains(candidate.Projects, project) {
				continue
			}
			for _, requirement := range bundle.Manifest.Package.Builders {
				if requirement.ID == recipe.Builder && requirement.Kind == candidate.Kind && requirement.Schema == recipe.Schema {
					b, ok = candidate, true
				}
			}
		}
	}
	if !ok || !slices.Contains(b.Projects, project) || b.Available != nil && !b.Available() {
		return BuilderBinding{}, false
	}
	return b, true
}

func workCall(c app.ExtensionWorkClaim) app.ProviderCall {
	return app.ProviderCall{RequestID: string(c.Work.ID), OperationID: string(c.Work.ID), InvocationID: string(c.Work.InvocationID), IdempotencyKey: string(c.Work.InvocationID), Generation: 1, Fence: uint64(c.Operation.FencingToken)}
}
func (s *Service) finishWork(ctx context.Context, c app.ExtensionWorkClaim, state domain.OperationState, result any, code string) error {
	ids, e := s.newGovernanceRecordIDs()
	if e != nil {
		return e
	}
	var raw []byte
	if result != nil {
		raw, e = json.Marshal(result)
		if e != nil {
			return e
		}
	}
	return s.store.(app.BuildStore).FinishExtensionWork(ctx, c, state, raw, code, ids)
}
func (s *Service) workContext(parent context.Context, c app.ExtensionWorkClaim, timeout time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if c.Work.Request.Kind != domain.WorkImageImport {
					current, e := s.store.(app.BuildStore).GetExtensionWork(ctx, c.Work.ID)
					if e != nil || current.CancelRequested {
						cancel()
						return
					}
				}
				if s.store.(app.BuildStore).RenewExtensionWork(ctx, c, 30*time.Second) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}
func (s *Service) executeBuild(parent context.Context, b BuilderBinding, c app.ExtensionWorkClaim) error {
	ctx, done := s.workContext(parent, c, time.Duration(b.Limits.TimeoutSeconds)*time.Second)
	defer done()
	store := s.store.(app.BuildStore)
	call := workCall(c)
	r := c.Work.Request.Build
	if r == nil {
		return fmt.Errorf("invalid build work")
	}
	currentLimits, _ := json.Marshal(b.Limits)
	originalLimits, _ := json.Marshal(r.Limits)
	if !bytes.Equal(currentLimits, originalLimits) {
		return fmt.Errorf("original builder limits required for recovery")
	}
	if b.PluginDigest != c.Work.Request.PluginDigest || digestBytes("extension.configuration/v1", b.Configuration) != c.Work.Request.ConfigurationDigest || !slices.Contains(b.Projects, c.Work.ProjectID) {
		return fmt.Errorf("original builder binding required for recovery")
	}
	current, e := store.GetExtensionWork(ctx, c.Work.ID)
	if e != nil {
		return e
	}
	if current.CancelRequested {
		if e = b.Builder.CancelBuild(ctx, call); e != nil {
			return e
		}
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
	}
	// Child operation IDs bind immutable terminal outputs. Waiting never executes
	// author code or holds a builder binding lease while another recipe builds.
	dependencyResults := []struct {
		name   string
		result app.BuildResult
	}{}
	inputBytes := r.ContextSize
	if inputBytes > r.Limits.InputBytes {
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_input_limit")
	}
	for _, dependency := range r.Dependencies {
		child, err := store.GetExtensionWork(ctx, dependency.BuildID)
		if err != nil {
			return err
		}
		if child.ProjectID != c.Work.ProjectID || child.Request.Kind != domain.WorkBuild {
			return fmt.Errorf("invalid build dependency scope")
		}
		if child.State == domain.OperationFailed || child.State == domain.OperationCanceled {
			return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_dependency_failed")
		}
		if child.State != domain.OperationSucceeded {
			return store.YieldExtensionWork(ctx, c)
		}
		var result app.BuildResult
		if err = json.Unmarshal(child.Result, &result); err != nil {
			return err
		}
		if result.ArtifactRef == "" || result.Image.Size == 0 {
			return fmt.Errorf("dependency output is missing")
		}
		if result.Image.Size > r.Limits.InputBytes-inputBytes {
			return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_dependency_input_limit")
		}
		inputBytes += result.Image.Size
		dependencyResults = append(dependencyResults, struct {
			name   string
			result app.BuildResult
		}{dependency.RecipeID, result})
	}
	// Shared builds keep running when the original requester loses authority; no
	// publication or deployment is authorized by the build result itself.
	state := "unknown"
	var result app.BuildResult
	if c.Recover {
		state, result, e = b.Builder.RecoverBuild(ctx, call)
		if e != nil {
			return e
		}
	}
	if state == "unknown" {
		resolvedDependencies := []app.BuildDependency{}
		for _, dependency := range dependencyResults {
			reader, err := s.artifacts.Open(ctx, app.AuthorizedArtifactRef{Reference: dependency.result.ArtifactRef, SubjectID: c.Work.ActorID})
			if err != nil {
				return err
			}
			input, err := b.Builder.WriteArtifact(ctx, call, dependency.result.Image, reader)
			reader.Close()
			if err != nil {
				return err
			}
			resolvedDependencies = append(resolvedDependencies, app.BuildDependency{RecipeID: dependency.name, Artifact: input})
		}

		input := app.ExtensionArtifact{Digest: r.ContextDigest, Size: r.ContextSize, MediaType: "application/vnd.labcentral.build-context+json"}
		reader, e := s.artifacts.Open(ctx, app.AuthorizedArtifactRef{Reference: r.ContextArtifact, SubjectID: c.Work.ActorID})
		if e != nil {
			return e
		}
		input, e = b.Builder.WriteArtifact(ctx, call, input, reader)
		_ = reader.Close()
		if e != nil {
			return e
		}
		effectiveDigest := r.RecipeDigest
		if len(resolvedDependencies) > 0 {
			encoded, _ := json.Marshal(resolvedDependencies)
			effectiveDigest = digestBytes("build.effective-inputs/v1", []byte(r.RecipeDigest), encoded)
		}
		request := app.BuildRequest{Dependencies: resolvedDependencies, BindingConfiguration: b.Configuration, Call: call, BindingID: b.ID, Recipe: r.Recipe, Digest: effectiveDigest, Inputs: []app.ExtensionArtifact{input}, Limits: r.Limits}
		if e = b.Builder.ValidateBuild(ctx, request); e != nil {
			return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_validation_failed")
		}
		state, result, e = b.Builder.Build(ctx, request)
		if e != nil {
			return e
		}
	}
	switch state {
	case "failed":
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_failed")
	case "cancelled":
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
	case "succeeded":
	default:
		return fmt.Errorf("build requires recovery")
	}
	if result.Image.Size > r.Limits.OutputBytes || result.Image.Size > 32<<30 {
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_output_limit")
	}
	reader, e := b.Builder.ReadArtifact(ctx, call, result.Image)
	if e != nil {
		return e
	}
	defer reader.Close()
	m := app.VerifiedManifest{Digest: result.Image.Digest, FileCount: 1, ByteCount: result.Image.Size}
	upload, e := s.artifacts.Stage(ctx, m)
	if e != nil {
		return e
	}
	measured, e := s.artifacts.Write(ctx, upload, io.LimitReader(reader, int64(result.Image.Size)+1))
	if e != nil || measured.Digest != m.Digest || measured.Size != m.ByteCount {
		return s.finishWork(ctx, c, domain.OperationFailed, nil, "build_output_invalid")
	}
	result.ArtifactRef, e = s.artifacts.Commit(ctx, upload, m)
	if e != nil {
		return e
	}
	if e = store.ReferenceExtensionArtifact(ctx, c.Work.ID, result.ArtifactRef); e != nil {
		return e
	}
	return s.finishWork(ctx, c, domain.OperationSucceeded, result, "")
}
func (s *Service) RunBuildWork(ctx context.Context, owner string, report func(error)) {
	store, ok := s.store.(app.BuildStore)
	if !ok {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for key, b := range s.builders {
			if ctx.Err() != nil {
				return
			}
			if b.Available != nil && !b.Available() {
				continue
			}
			c, e := store.ClaimExtensionWork(ctx, owner, key, domain.WorkBuild, 30*time.Second)
			if e == nil {
				if e = s.executeBuild(ctx, b, c); e != nil && report != nil {
					report(e)
				}
			}
		}
		for _, target := range s.targets.RegisteredTargets() {
			if !slices.Contains(target.Features, "image-import-v1") {
				continue
			}
			c, e := store.ClaimExtensionWork(ctx, owner, "core-import/"+string(target.ID), domain.WorkImageImport, 30*time.Second)
			if e == nil {
				if e = s.executeImport(ctx, c); e != nil && report != nil {
					report(e)
				}
			}
		}
		c, e := store.ClaimExtensionWork(ctx, owner, "core-preparation/default", domain.WorkPreparation, 30*time.Second)
		if e == nil {
			if e = s.executePreparation(ctx, c); e != nil && report != nil {
				report(e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func portableRecipe(r labbundle.BuildRecipe) domain.BuildRecipe {
	inputs := []domain.BuildInput{}
	for _, i := range r.Inputs {
		inputs = append(inputs, domain.BuildInput{Name: i.Name, Value: i.Value})
	}
	return domain.BuildRecipe{DependsOn: slices.Clone(r.DependsOn), ID: r.ID, Builder: r.Builder, Schema: r.Schema, Architecture: r.Architecture, ConfigurationJSON: r.ConfigurationJSON, Context: slices.Clone(r.Context), BaseImageDigests: slices.Clone(r.BaseImageDigests), Inputs: inputs}
}
