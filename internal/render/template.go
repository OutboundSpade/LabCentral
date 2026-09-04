package render

import (
	"context"
	"fmt"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const TemplateFormatVersion = "template.labcentral.io/v1"

type TemplateRepresentation struct {
	JSON          []byte
	Digest        domain.SHA256Digest
	AuthorVersion string
}

func (r *Renderer) PrepareTemplate(ctx context.Context, source []byte) (TemplateRepresentation, error) {
	if err := ctx.Err(); err != nil {
		return TemplateRepresentation{}, err
	}
	document, err := ParseYAML(source, r.limits.Parse)
	if err != nil {
		return TemplateRepresentation{}, err
	}
	if err = r.schema.Validate(document); err != nil {
		return TemplateRepresentation{}, err
	}
	lab, err := decodeAuthorLab(document)
	if err != nil {
		return TemplateRepresentation{}, err
	}
	if err = validateProviderOverrides(lab.Spec.ProviderOverrides); err != nil {
		return TemplateRepresentation{}, err
	}
	// Resolve declarations/defaults without inventing values for required inputs.
	// Expression evaluation and graph expansion are instance-time operations.
	parameters := append([]AuthorParameter(nil), lab.Spec.Parameters...)
	for i := range parameters {
		parameters[i].Required = false
	}
	if _, err := resolveInputs(parameters, nil); err != nil {
		return TemplateRepresentation{}, err
	}
	b, err := Canonicalize(document)
	if err != nil {
		return TemplateRepresentation{}, err
	}
	digest, err := CanonicalDigest(TemplateFormatVersion, b)
	if err != nil {
		return TemplateRepresentation{}, err
	}
	return TemplateRepresentation{JSON: b, Digest: digest, AuthorVersion: lab.APIVersion}, nil
}

// RendererVersion is a semantics identifier, not the product build version.
// A future semantic change must retain this implementation for old revisions.
func (r *Renderer) RenderVersion(ctx context.Context, version string, revision domain.RevisionContent, inputs domain.TypedInputs, policy domain.RenderPolicy) (domain.CanonicalLab, domain.RenderReport, error) {
	if version != "" && version != RendererVersion {
		return domain.CanonicalLab{}, domain.RenderReport{}, fmt.Errorf("unsupported renderer semantics %q", version)
	}
	return r.Render(ctx, revision, inputs, policy)
}
