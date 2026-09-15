package proxmox

import (
	"context"
	"encoding/json"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"strings"
	"testing"
)

func TestContainerVMPlanningBindsClosedRuntime(t *testing.T) {
	target := mutationTestTarget(t)
	var config TargetConfig
	json.Unmarshal(target.ConfigEnvelope, &config)
	config.SchemaVersion = targetConfigV13
	image := "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
	config.ContainerRuntime = &ContainerRuntimeProfile{SchemaVersion: containervm.Version, TemplateAlias: "debian", Images: []string{image}}
	for i := range config.PermissionScopes {
		if config.PermissionScopes[i].Path == "/pool/lab" {
			config.PermissionScopes[i].Privileges = append(config.PermissionScopes[i].Privileges, "VM.GuestAgent.Unrestricted")
		}
	}
	if err := config.validateMutation(); err != nil {
		t.Fatal(err)
	}
	target.ConfigEnvelope, _ = json.Marshal(config)
	service := NewService()
	network := `{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}}`
	container := `{"id":"container.web","kind":"Container","dependsOn":["network.lab"],"spec":{"image":{"repository":"docker.io/library/python","digest":"sha256:` + strings.Repeat("a", 64) + `"},"compute":{"cpuMillis":1000,"memoryBytes":536870912,"architecture":"amd64"},"isolation":"virtualMachine","realizationPreference":["viaVM"],"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}],"command":["python3"],"args":["-m","http.server"]}}`
	lab := []byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","metadata":{"name":"containers"},"resources":[` + network + `,` + container + `]}`)
	validation, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: lab})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatal(err, validation.GetErrors())
	}
	input := &pluginv1.ResourceMutationInput{Context: &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "network.lab", ProviderTargetId: target.TargetId, DesiredGeneration: 1}, Target: target, CanonicalResourceJson: []byte(network), ProviderPlan: validation.ProviderPlan, OwnershipTokens: []string{"owned"}}
	net, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: input})
	if err != nil || net.Error != nil {
		t.Fatal(err, net.GetError())
	}
	input.Context.LogicalResourceId = "container.web"
	input.CanonicalResourceJson = []byte(container)
	input.Dependencies = []*pluginv1.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: net.ProviderPlan, ProviderPlanDigest: net.ProviderPlanDigest}}
	result, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: input})
	if err != nil || result.GetError() != nil {
		t.Fatal(err, result.GetError())
	}
	var plan QEMUPlan
	json.Unmarshal(result.ProviderPlan, &plan)
	if plan.ResourceKind != "VM" || plan.Container == nil || plan.Container.InstanceID != "instance.test" || plan.SchemaVersion != qemuPlanV11 {
		t.Fatal("unbound runtime", plan)
	}
	if err := validatePlanBindings(config, plan, []byte(container)); err != nil {
		t.Fatal("serialized runtime binding", err)
	}
	plan.Container.Args = []string{"different"}
	if validatePlanBindings(config, plan, []byte(container)) == nil {
		t.Fatal("modified bootstrap accepted")
	}
	var resource canonicalResource
	json.Unmarshal([]byte(container), &resource)
	for _, replacement := range []string{strings.Replace(container, `"viaVM"`, `"native"`, 1), strings.Replace(container, `"args":`, `"mounts":[],"args":`, 1), strings.Replace(container, `"virtualMachine"`, `"process"`, 1)} {
		json.Unmarshal([]byte(replacement), &resource)
		if _, _, err := containerVM(config, resource); err == nil {
			t.Fatal("unsupported resource accepted")
		}
	}
	config.ContainerRuntime = nil
	if config.validateMutation() == nil {
		t.Fatal("guest exec permission accepted without runtime")
	}
}
func TestShortVMNamesRemainDistinct(t *testing.T) {
	first := safeVMName("inst_"+strings.Repeat("a", 32), "vm.workstation")
	if len(first) > 21 || !strings.HasPrefix(first, "workstation-") {
		t.Fatal(first)
	}
	if first == safeVMName("inst_"+strings.Repeat("b", 32), "vm.workstation") {
		t.Fatal("names collide")
	}
}
