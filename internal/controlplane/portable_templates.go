package controlplane

import (
	"context"
	"encoding/json"
	"slices"

	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
)

type PortablePreview struct {
	APIVersion           string                         `json:"apiVersion"`
	Entrypoint           string                         `json:"entrypoint"`
	FileCount            int                            `json:"fileCount"`
	SourceBytes          uint64                         `json:"sourceBytes"`
	Dependencies         []labbundle.SourceDependency   `json:"dependencies"`
	Builders             []labbundle.BuilderRequirement `json:"builders"`
	UnresolvedBuilders   []string                       `json:"unresolvedBuilders"`
	RequiredCapabilities []string                       `json:"requiredCapabilities"`
}

func (s *Service) PreviewPortableTemplate(ctx context.Context, actor domain.UserID, project domain.ProjectID, archive []byte) (PortablePreview, error) {
	if err := s.authorizeExtension(ctx, actor, project, app.ActionTemplateRevisionPublish); err != nil {
		return PortablePreview{}, err
	}
	bundle, err := labbundle.Read(archive)
	if err != nil {
		return PortablePreview{}, &ValidationError{Issues: []Issue{{Code: "package_invalid", Message: err.Error()}}}
	}
	if bundle.Manifest.Package == nil {
		return PortablePreview{}, &ValidationError{Issues: []Issue{{Code: "package_invalid", Message: "import preview requires a portable source package"}}}
	}
	representation, err := s.renderer.PrepareTemplate(ctx, archive)
	if err != nil {
		return PortablePreview{}, &ValidationError{Issues: []Issue{{Code: "template_invalid", Message: err.Error()}}}
	}
	preview := PortablePreview{APIVersion: bundle.Manifest.APIVersion, Entrypoint: bundle.Manifest.Entrypoint, FileCount: len(bundle.Files), SourceBytes: bundle.Manifest.TotalBytes,
		Dependencies: bundle.Manifest.Package.Dependencies, Builders: bundle.Manifest.Package.Builders, UnresolvedBuilders: []string{}, RequiredCapabilities: []string{}}
	// Explicit declaration requirements are informational until instance-time
	// rendering discovers the full resource/capability requirements.
	caps, err := portableCapabilities(representation.JSON)
	if err != nil {
		return PortablePreview{}, err
	}
	preview.RequiredCapabilities = caps
	for _, req := range preview.Builders {
		_, resolved := s.resolveRecipeBuilder(bundle, labbundle.BuildRecipe{Builder: req.ID, Schema: req.Schema}, project)
		if !resolved {
			preview.UnresolvedBuilders = append(preview.UnresolvedBuilders, req.ID)
		}
	}
	for _, req := range preview.Builders {
		if req.Kind != "dockerfile" {
			continue
		}
		available := false
		for _, binding := range s.builders {
			if binding.ContainerRuntime != nil && slices.Contains(binding.Projects, project) {
				available = true
			}
		}
		if !available {
			preview.UnresolvedBuilders = append(preview.UnresolvedBuilders, "core:container-runtime")
		}
		break
	}
	return preview, nil
}

func portableCapabilities(representation []byte) ([]string, error) {
	var source struct {
		Spec struct {
			Requirements struct {
				Capabilities []string `json:"capabilities"`
			} `json:"requirements"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(representation, &source); err != nil {
		return nil, err
	}
	return append([]string{}, source.Spec.Requirements.Capabilities...), nil
}

// TemplateRecipeSummary is presentation metadata, never a build authorization.
type TemplateRecipeSummary struct {
	ID           string   `json:"id"`
	Builder      string   `json:"builder"`
	Architecture string   `json:"architecture"`
	DependsOn    []string `json:"dependsOn"`
	Available    bool     `json:"available"`
}

func (s *Service) TemplateRecipeSummaries(data []byte, project domain.ProjectID) ([]TemplateRecipeSummary, error) {
	out := []TemplateRecipeSummary{}
	if !labbundle.Archive(data) {
		return out, nil
	}
	b, err := labbundle.Read(data)
	if err != nil {
		return nil, err
	}
	if b.Builds() == nil {
		return out, nil
	}
	for _, r := range b.Builds().Recipes {
		binding, ok := s.resolveRecipeBuilder(b, r, project)
		out = append(out, TemplateRecipeSummary{ID: r.ID, Builder: r.Builder, Architecture: r.Architecture, DependsOn: r.DependsOn, Available: ok && (binding.Available == nil || binding.Available())})
	}
	return out, nil
}
