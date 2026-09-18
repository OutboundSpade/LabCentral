package render

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"sort"
	"strings"
)

const BuildRendererVersion = "labcentral.renderer/v1alpha1+builds/v1"

func BundleRendererVersion(source []byte) (string, error) {
	if !labbundle.Archive(source) {
		return RendererVersion, nil
	}
	b, e := labbundle.Read(source)
	if e != nil {
		return "", e
	}
	if b.Manifest.Package != nil {
		return PackageRendererVersion, nil
	}
	if b.Builds() != nil {
		return BuildRendererVersion, nil
	}
	return RendererVersion, nil
}
func resolveBuildImages(lab *AuthorLab, bindings []domain.ResolvedBuildImage) error {
	resolve := func(ref *string) error {
		if !strings.HasPrefix(*ref, "build://") {
			return nil
		}
		name := strings.TrimPrefix(*ref, "build://")
		for _, b := range bindings {
			if b.RecipeID == name {
				*ref = string(b.ImageRef)
				return nil
			}
		}
		return fmt.Errorf("build reference %q has no approved target image binding", name)
	}
	resources := func(r *AuthorResources) error {
		for i := range r.VMs {
			if e := resolve(&r.VMs[i].ImageRef); e != nil {
				return e
			}
		}
		for i := range r.Containers {
			if e := resolve(&r.Containers[i].Image); e != nil {
				return e
			}
		}
		return nil
	}
	if e := resources(&lab.Spec.Resources); e != nil {
		return e
	}
	for i := range lab.Spec.Components {
		if e := resources(&lab.Spec.Components[i].Resources); e != nil {
			return e
		}
	}
	return nil
}

// RequiredBuildRecipes renders composition and typed inputs before selecting
// build roots. Dependency recipes are queued by the build graph, not imported
// as unrelated images. Placeholder references never leave this local render.
func (r *Renderer) RequiredBuildRecipes(ctx context.Context, version string, revision domain.RevisionContent, inputs domain.TypedInputs) ([]string, error) {
	bundle, err := labbundle.Read(revision.VerifiedBundle)
	if err != nil {
		return nil, err
	}
	if bundle.Builds() == nil {
		return nil, nil
	}
	byImage := map[domain.ImageRef]string{}
	policy := domain.RenderPolicy{}
	for _, recipe := range bundle.Builds().Recipes {
		sum := sha256.Sum256([]byte(recipe.ID))
		ref := domain.ImageRef(fmt.Sprintf("labcentral.invalid/preparation@sha256:%x", sum))
		byImage[ref] = recipe.ID
		policy.BuildImages = append(policy.BuildImages, domain.ResolvedBuildImage{RecipeID: recipe.ID, ImageRef: ref})
	}
	canonical, _, err := r.RenderVersion(ctx, version, revision, inputs, policy)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, resource := range canonical.Resources {
		var image domain.ImageRef
		switch value := resource.(type) {
		case domain.VM:
			image = value.Spec.Image
		case domain.Container:
			image = value.Spec.Image
		}
		if name, found := byImage[image]; found {
			used[name] = true
		}
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
