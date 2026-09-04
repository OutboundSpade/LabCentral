package render

import (
	"context"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestRequiredParametersArePublishedBeforeResolution(t *testing.T) {
	r := testRenderer(t, DefaultLimits())
	ctx := context.Background()
	source := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: parameters}
spec:
  parameters:
    - name: lab_name
      type: string
      required: true
  resources:
    networks:
      - name: app
        kind: isolated
        ipam: providerManaged
        egress: {mode: deny}
`)
	prepared, err := r.PrepareTemplate(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.JSON) == 0 || prepared.Digest == "" {
		t.Fatal("missing stored representation")
	}
	revision := domain.RevisionContent{VerifiedBundle: source}
	if _, _, err := r.RenderVersion(ctx, RendererVersion, revision, nil, domain.RenderPolicy{}); err == nil {
		t.Fatal("missing required value accepted")
	}
	input := domain.TypedInputs{{Name: "lab_name", Kind: domain.InputString, String: "classroom"}}
	if _, _, err := r.RenderVersion(ctx, RendererVersion, revision, input, domain.RenderPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RenderVersion(ctx, "future-semantics", revision, input, domain.RenderPolicy{}); err == nil {
		t.Fatal("unknown semantics accepted")
	}
}
