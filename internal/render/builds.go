package render

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
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
