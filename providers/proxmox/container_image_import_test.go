package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"google.golang.org/protobuf/proto"
)

func TestContainerImageImportKeepsDiskAndOCIIdentitiesSeparate(t *testing.T) {
	ctx := context.Background()
	s, fake, r, data := imageFixture(t)
	policy := &ContainerImageImportPolicy{BaseSHA256: strings.Repeat("b", 64), AgentSHA256: strings.Repeat("c", 64)}
	fake.config.ImageImport.ContainerRuntime = policy
	fake.config.ContainerRuntime = &ContainerRuntimeProfile{SchemaVersion: containervm.Version, TemplateAlias: "debian", Images: []string{"docker.io/library/python@" + sha("a")}}
	fake.config.PermissionScopes = nil
	for path, privileges := range expectedPermissionScopes(fake.config) {
		scope := PermissionScope{Path: path}
		for name := range privileges {
			scope.Privileges = append(scope.Privileges, name)
		}
		fake.config.PermissionScopes = append(fake.config.PermissionScopes, scope)
	}
	if !runtimeVMPath(fake.config, "/vms/200") || runtimeVMPath(fake.config, "/vms/999999") || runtimeVMPath(fake.config, "/") {
		t.Fatal("runtime permissions exceeded explicit allocation policy")
	}
	r.Target.ConfigEnvelope, _ = json.Marshal(fake.config)
	evidence := containervm.PackagedImage{SchemaVersion: containervm.PackagedImageVersion, ManifestDigest: sha("d"), ConfigDigest: sha("e"), BaseSHA256: policy.BaseSHA256, AgentSHA256: policy.AgentSHA256}
	r.ConfigurationJson, _ = json.Marshal(evidence)
	plan, err := s.Plan(ctx, &wire.ImageImportServicePlanRequest{Input: r})
	if err != nil {
		t.Fatal(err)
	}
	r.ReservedPlan = plan.Plan
	var document importPlanDocument
	json.Unmarshal(plan.Plan.Opaque, &document)
	if document.Version != imageImportPlanV11 || document.ContainerRuntime == nil || *document.ContainerRuntime != evidence {
		t.Fatal("runtime evidence not bound to reserved plan")
	}
	changed := proto.Clone(r).(*wire.ImageImportServiceImportRequest)
	changed.ConfigurationJson = bytes.ReplaceAll(r.ConfigurationJson, []byte(evidence.ManifestDigest), []byte(sha("f")))
	if _, err = s.Import(ctx, changed); err == nil {
		t.Fatal("changed OCI identity reused reserved disk plan")
	}
	uploadImageFixture(t, s, r, data)
	imported, err := s.Import(ctx, r)
	if err != nil || imported.GetState() != "succeeded" {
		t.Fatal(imported, err)
	}
	if !strings.HasSuffix(imported.Binding.ImageRef, "@"+evidence.ManifestDigest) || imported.Binding.ArtifactDigest != r.Artifact.Digest {
		t.Fatal("OCI and disk identities conflated", imported.Binding)
	}
	repository := strings.Split(imported.Binding.ImageRef, "@")[0]
	network := `{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}}`
	container := `{"id":"container.web","kind":"Container","dependsOn":["network.lab"],"spec":{"image":{"repository":"` + repository + `","digest":"` + evidence.ManifestDigest + `"},"compute":{"cpuMillis":1000,"memoryBytes":536870912,"architecture":"amd64"},"isolation":"virtualMachine","realizationPreference":["viaVM"],"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}]}}`
	lab := []byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","metadata":{"name":"imported-container"},"resources":[` + network + `,` + container + `]}`)
	service := NewService()
	validation, err := service.ValidateLab(ctx, &wire.ValidateLabRequest{Target: r.Target, CanonicalLabJson: lab})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatal(validation, err)
	}
	input := &wire.ResourceMutationInput{Context: &wire.CallContext{LabInstanceId: "instance.container", LogicalResourceId: "network.lab", ProviderTargetId: r.Target.TargetId, DesiredGeneration: 1}, Target: r.Target, CanonicalResourceJson: []byte(network), ProviderPlan: validation.ProviderPlan, OwnershipTokens: []string{"owned"}}
	net, err := service.PlanResource(ctx, &wire.PlanResourceRequest{Input: input})
	if err != nil || net.GetError() != nil {
		t.Fatal(net, err)
	}
	input.Context.LogicalResourceId = "container.web"
	input.CanonicalResourceJson = []byte(container)
	input.Dependencies = []*wire.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: net.ProviderPlan, ProviderPlanDigest: net.ProviderPlanDigest}}
	resource, err := service.PlanResource(ctx, &wire.PlanResourceRequest{Input: input})
	if err != nil || resource.GetError() != nil {
		t.Fatal(resource, err)
	}
	var vm QEMUPlan
	json.Unmarshal(resource.ProviderPlan, &vm)
	if vm.Template.VMID != document.VMID || vm.Template.ContentDigest != "sha256:"+r.Artifact.Digest || vm.Container == nil || vm.Container.SchemaVersion != containervm.PackagedRuntimeVersion || vm.Container.ImageID != evidence.ConfigDigest || vm.Container.Image != imported.Binding.ImageRef {
		t.Fatalf("runtime plan identity mismatch: %+v", vm)
	}
	if err = validatePlanBindings(fake.config, vm, []byte(container)); err != nil {
		t.Fatal("runtime binding", err)
	}
	vm.Container.ImageID = sha("f")
	if validatePlanBindings(fake.config, vm, []byte(container)) == nil {
		t.Fatal("runtime image ID tampering accepted")
	}
}
