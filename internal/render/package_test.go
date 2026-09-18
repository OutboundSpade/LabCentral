package render

import (
	"bytes"
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"testing"
)

func TestPortableComponentCompositionIsPinnedAndVersioned(t *testing.T) {
	r := testRenderer(t, DefaultLimits())
	files := map[string][]byte{"lab.yaml": []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: composed}
spec:
  uses: [{name: primary, componentRef: shared-network}]
  resources: {}
`), "deps/base/network.yaml": []byte(`name: original
resources:
  networks: [{name: private, kind: isolated, ipam: providerManaged, egress: {mode: deny}}]
`)}
	m := labbundle.Manifest{APIVersion: labbundle.PortableVersion, Kind: "LabBundle", Entrypoint: "lab.yaml", Package: &labbundle.SourcePackage{APIVersion: labbundle.SourcePackageVersion, Dependencies: []labbundle.SourceDependency{{ID: "base", Root: "deps/base"}}, Components: []labbundle.SourceImport{{Name: "shared-network", Path: "deps/base/network.yaml"}}}}
	m, err := labbundle.LockSources(m, files)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := labbundle.Pack(m, files)
	if err != nil {
		t.Fatal(err)
	}
	rev := domain.RevisionContent{VerifiedBundle: archive}
	prepared, err := r.PrepareTemplate(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prepared.JSON, []byte(`"name":"shared-network"`)) {
		t.Fatal("import alias missing")
	}
	lab, _, err := r.RenderVersion(context.Background(), PackageRendererVersion, rev, nil, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lab.Resources) != 1 || lab.Resources[0].ID() != "network.primary-private" || lab.RendererVersion != PackageRendererVersion {
		t.Fatalf("incorrect expansion: %+v", lab)
	}
	automatic, _, err := r.RenderVersion(context.Background(), "", rev, nil, domain.RenderPolicy{})
	if err != nil || automatic.RendererVersion != PackageRendererVersion {
		t.Fatal("package semantics lost")
	}
	if _, _, err = r.RenderVersion(context.Background(), RendererVersion, rev, nil, domain.RenderPolicy{}); err == nil {
		t.Fatal("old semantics accepted new composition")
	}
	m.Package.Components = append(m.Package.Components, m.Package.Components[0])
	if _, err = labbundle.Pack(m, files); err == nil {
		t.Fatal("duplicate aliases accepted")
	}
}
