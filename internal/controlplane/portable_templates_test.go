package controlplane

import (
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"strings"
	"testing"
)

type unusedPortableBuilder struct{ app.Builder }

func TestPortableBuilderRebindingPreservesSourceAndProjectIsolation(t *testing.T) {
	bundle := labbundle.Bundle{Manifest: labbundle.Manifest{Package: &labbundle.SourcePackage{Builders: []labbundle.BuilderRequirement{{ID: "linux", Kind: "ansible", Schema: "ansible-v1"}}}}}
	recipe := labbundle.BuildRecipe{Builder: "linux", Schema: "ansible-v1"}
	binding := func(instance string, project domain.ProjectID) BuilderBinding {
		return BuilderBinding{InstanceID: instance, ID: "local", Requirement: "linux", Kind: "ansible", Projects: []domain.ProjectID{project}, Builder: unusedPortableBuilder{}, Limits: app.BuildLimits{TimeoutSeconds: 60, InputBytes: 1024, OutputBytes: 1024, Egress: []string{}}}
	}
	first, second := &Service{}, &Service{}
	if err := first.SetBuilders([]BuilderBinding{binding("installation-a", "project.a")}); err != nil {
		t.Fatal(err)
	}
	if err := second.SetBuilders([]BuilderBinding{binding("installation-b", "project.b")}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		s        *Service
		project  domain.ProjectID
		instance string
	}{{first, "project.a", "installation-a"}, {second, "project.b", "installation-b"}} {
		got, ok := test.s.resolveRecipeBuilder(bundle, recipe, test.project)
		if !ok || got.InstanceID != test.instance {
			t.Fatal("logical rebinding failed")
		}
		if _, ok := test.s.resolveRecipeBuilder(bundle, recipe, "project.foreign"); ok {
			t.Fatal("cross-project builder selected")
		}
	}
	if recipe.Builder != "linux" || bundle.Manifest.Package.Builders[0].ID != "linux" {
		t.Fatal("source identity modified")
	}
	if err := first.SetBuilders([]BuilderBinding{binding("a", "project.a"), binding("b", "project.a")}); err == nil {
		t.Fatal("ambiguous project binding accepted")
	}
	if err := first.SetBuilders([]BuilderBinding{binding("a", "project.a")}); err != nil {
		t.Fatal(err)
	}
	legacy := labbundle.Bundle{}
	if _, ok := first.resolveRecipeBuilder(legacy, recipe, "project.a"); ok {
		t.Fatal("legacy identity silently reinterpreted")
	}
	recipe.Builder = "a/local"
	if _, ok := first.resolveRecipeBuilder(legacy, recipe, "project.a"); !ok {
		t.Fatal("legacy physical binding lost")
	}
}

func TestRuntimeEvidenceDomainAndPluginContractAgree(t *testing.T) {
	valid := containervm.PackagedImage{SchemaVersion: containervm.PackagedImageVersion, ManifestDigest: "sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64), BaseSHA256: strings.Repeat("c", 64), AgentSHA256: strings.Repeat("d", 64)}
	cases := []containervm.PackagedImage{valid}
	for _, mutate := range []func(*containervm.PackagedImage){func(p *containervm.PackagedImage) { p.ManifestDigest = "sha256:" + p.ManifestDigest }, func(p *containervm.PackagedImage) { p.ConfigDigest = "latest" }, func(p *containervm.PackagedImage) { p.AgentSHA256 = strings.ToUpper(p.AgentSHA256) }, func(p *containervm.PackagedImage) { p.BaseSHA256 = "sha256:" + p.BaseSHA256 }, func(p *containervm.PackagedImage) { p.SchemaVersion = "v0" }} {
		c := valid
		mutate(&c)
		cases = append(cases, c)
	}
	for _, wire := range cases {
		portable := domain.ContainerRuntimeImage(wire)
		if (wire.Validate() == nil) != (portable.Validate() == nil) {
			t.Fatal("runtime evidence adapter changed validation")
		}
	}
}
