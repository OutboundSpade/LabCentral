package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func egressTestConfig(t *testing.T) (TargetConfig, *wire.TargetContext) {
	t.Helper()
	target := mutationTestTarget(t)
	var config TargetConfig
	if err := json.Unmarshal(target.ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = targetConfigV15
	config.Networks = append(config.Networks, NetworkProfile{Alias: "internet", Bridge: "vmbr1", VLANRanges: []IntegerRange{{Start: 211, End: 220}}, Egress: &NetworkEgressProfile{Mode: "allow", IPv4CIDR: "10.20.0.0/24", Gateway: "10.20.0.1", PolicyDigest: sha("a"), ProbeDigest: sha("b"), InternetReachable: true}})
	config.Qualification.InventoryDigest, _ = mutationTestInventory().MutationDigestForConfig(config)
	if err := config.validateMutation(); err != nil {
		t.Fatal(err)
	}
	target.ConfigEnvelope, _ = json.Marshal(config)
	return config, target
}

func TestEgressProfileRequiresQualifiedDisjointGateway(t *testing.T) {
	for name, change := range map[string]func(*TargetConfig){
		"old schema":               func(c *TargetConfig) { c.SchemaVersion = targetConfigV14 },
		"overlapping VLAN":         func(c *TargetConfig) { c.Networks[1].VLANRanges[0].Start = 210 },
		"unreachable Internet":     func(c *TargetConfig) { c.Networks[1].Egress.InternetReachable = false },
		"management reachable":     func(c *TargetConfig) { c.Networks[1].Egress.ManagementReachable = true },
		"cross instance reachable": func(c *TargetConfig) { c.Networks[1].Egress.CrossInstanceReachable = true },
		"missing policy":           func(c *TargetConfig) { c.Networks[1].Egress.PolicyDigest = "" },
		"missing probe":            func(c *TargetConfig) { c.Networks[1].Egress.ProbeDigest = "" },
		"public CIDR": func(c *TargetConfig) {
			c.Networks[1].Egress.IPv4CIDR = "8.8.8.0/24"
			c.Networks[1].Egress.Gateway = "8.8.8.1"
		},
		"outside gateway":   func(c *TargetConfig) { c.Networks[1].Egress.Gateway = "10.21.0.1" },
		"broadcast gateway": func(c *TargetConfig) { c.Networks[1].Egress.Gateway = "10.20.0.255" },
		"network gateway":   func(c *TargetConfig) { c.Networks[1].Egress.Gateway = "10.20.0.0" },
		"unmasked CIDR":     func(c *TargetConfig) { c.Networks[1].Egress.IPv4CIDR = "10.20.0.5/24" },
	} {
		t.Run(name, func(t *testing.T) {
			config, _ := egressTestConfig(t)
			change(&config)
			if err := config.validateMutation(); err == nil {
				t.Fatal("invalid egress accepted")
			}
		})
	}
}

func TestEgressSelectionPreservesDeniedNetworks(t *testing.T) {
	_, target := egressTestConfig(t)
	for _, tc := range []struct {
		name, lab, override, alias string
		reject                     bool
	}{
		{name: "default deny", lab: string(canonicalMutationLab()), alias: "isolated"},
		{name: "automatic egress", lab: strings.Replace(string(canonicalMutationLab()), `"mode":"deny"`, `"mode":"allow"`, 1), alias: "internet"},
		{name: "deny cannot use gateway", lab: string(canonicalMutationLab()), override: `{"networkAlias":"internet"}`, reject: true},
		{name: "allow cannot use deny", lab: strings.Replace(string(canonicalMutationLab()), `"mode":"deny"`, `"mode":"allow"`, 1), override: `{"networkAlias":"isolated"}`, reject: true},
		{name: "CIDR mismatch", lab: strings.ReplaceAll(strings.Replace(string(canonicalMutationLab()), `"mode":"deny"`, `"mode":"allow"`, 1), "10.20.0.", "10.21.0."), reject: true},
		{name: "restricted unimplemented", lab: strings.Replace(string(canonicalMutationLab()), `"mode":"deny"`, `"mode":"restricted"`, 1), reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewService().ValidateLab(context.Background(), &wire.ValidateLabRequest{Target: target, CanonicalLabJson: []byte(tc.lab), ProviderOverrideJson: []byte(tc.override)})
			if err != nil {
				t.Fatal(err)
			}
			if tc.reject {
				if len(r.Errors) == 0 {
					t.Fatal("unsafe profile selected")
				}
				return
			}
			p, err := decodeValidationPlan(r.ProviderPlan)
			if err != nil || len(r.Errors) != 0 || p.NetworkAlias != tc.alias {
				t.Fatalf("selection %+v errors %+v err %v", p, r.Errors, err)
			}
		})
	}
}

func TestEgressPlansFourMachinesAndRejectsGatewayTampering(t *testing.T) {
	config, target := egressTestConfig(t)
	lab := []byte(strings.Replace(string(canonicalMutationLab()), `"mode":"deny"`, `"mode":"allow"`, 1))
	var doc canonicalLabDocument
	_ = json.Unmarshal(lab, &doc)
	base := doc.Resources[1]
	doc.Resources = doc.Resources[:1]
	for i := 0; i < 4; i++ {
		resource := base
		resource.ID = fmt.Sprintf("vm.machine%d", i)
		resource.Spec = []byte(strings.Replace(string(base.Spec), "10.20.0.10", fmt.Sprintf("10.20.0.%d", 10+i), 1))
		doc.Resources = append(doc.Resources, resource)
	}
	lab, _ = json.Marshal(doc)
	service := NewService()
	validation, err := service.ValidateLab(context.Background(), &wire.ValidateLabRequest{Target: target, CanonicalLabJson: lab})
	if err != nil || len(validation.Errors) > 0 {
		t.Fatalf("validate %v %+v", err, validation.Errors)
	}
	networkJSON, _ := json.Marshal(doc.Resources[0])
	ninput := &wire.ResourceMutationInput{Context: completeCall("plan-network", "network.lab", target.TargetId), Target: target, CanonicalResourceJson: networkJSON, ProviderPlan: validation.ProviderPlan, OwnershipTokens: []string{"owned"}}
	net, err := service.PlanResource(context.Background(), &wire.PlanResourceRequest{Input: ninput})
	if err != nil || net.Error != nil {
		t.Fatalf("network %v %+v", err, net.Error)
	}
	var nplan QEMUPlan
	_ = json.Unmarshal(net.ProviderPlan, &nplan)
	if nplan.Networks[0].Gateway != "10.20.0.1" || nplan.Networks[0].VLAN != 211 {
		t.Fatalf("network binding %+v", nplan.Networks)
	}
	var excluded []*wire.AllocationClaim
	for i := 0; i < 4; i++ {
		resource := doc.Resources[i+1]
		vmJSON, _ := json.Marshal(resource)
		input := &wire.ResourceMutationInput{Context: completeCall(fmt.Sprintf("plan-vm%d", i), resource.ID, target.TargetId), Target: target, CanonicalResourceJson: vmJSON, ProviderPlan: validation.ProviderPlan, OwnershipTokens: []string{"owned"}, Dependencies: []*wire.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: net.ProviderPlan, ProviderPlanDigest: net.ProviderPlanDigest}}}
		r, err := service.PlanResource(context.Background(), &wire.PlanResourceRequest{Input: input, ExcludedAllocations: excluded})
		if err != nil || r.Error != nil {
			t.Fatalf("VM %d %v %+v", i, err, r.Error)
		}
		excluded = append(excluded, r.ProposedAllocations...)
		var plan QEMUPlan
		_ = json.Unmarshal(r.ProviderPlan, &plan)
		if plan.Networks[0].Gateway != "10.20.0.1" || validatePlanBindings(config, plan, vmJSON) != nil || bindingWithoutVMFields(plan.Networks[0]) != nplan.Networks[0] {
			t.Fatal("gateway not bound across network dependency and VM")
		}
		for _, address := range []string{"10.20.0.1", "10.20.0.0", "10.20.0.255"} {
			changed := strings.Replace(string(vmJSON), fmt.Sprintf("10.20.0.%d", 10+i), address, 1)
			input.CanonicalResourceJson = []byte(changed)
			bad, _ := service.PlanResource(context.Background(), &wire.PlanResourceRequest{Input: input})
			if bad.Error == nil {
				t.Fatalf("reserved address %s accepted", address)
			}
		}
		plan.Networks[0].Gateway = "10.20.0.2"
		if validatePlanBindings(config, plan, vmJSON) == nil {
			t.Fatal("tampered gateway accepted")
		}
	}
	nplan.Networks[0].Gateway = ""
	if validatePlanBindings(config, nplan, networkJSON) == nil {
		t.Fatal("network gateway removal accepted")
	}
}

func TestEgressCapabilityRequiresActiveMutationQualification(t *testing.T) {
	_, target := egressTestConfig(t)
	fake := newFakeMutationAPI()
	for _, tc := range []struct {
		name    string
		service *Service
		want    wire.Capability_Support
	}{
		{"closed", NewServiceWithFactory(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fake, nil })), wire.Capability_SUPPORT_UNSUPPORTED},
		{"qualified", newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fake, nil })), wire.Capability_SUPPORT_NATIVE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.service.DiscoverTarget(context.Background(), &wire.DiscoverTargetRequest{Target: target})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range r.Capabilities {
				if c.FeatureId == "network.egress.enforced" {
					if c.Support != tc.want {
						t.Fatalf("support %v, want %v", c.Support, tc.want)
					}
					return
				}
			}
			t.Fatal("missing egress capability")
		})
	}
}

func TestEgressGatewaySurvivesApplyReadAndOwnedDestroy(t *testing.T) {
	f := newExecutionFixture(t, true)
	r := applyVMFixture(t, f)
	if r.GetError() != nil {
		t.Fatalf("apply: %+v", r.GetError())
	}
	state, err := decodeQEMUState(r.ProviderState)
	if err != nil || state.Networks[0].Gateway != "10.20.0.1" {
		t.Fatalf("state %+v %v", state, err)
	}
	if f.fake.configs[state.VMID].Fields["ipconfig0"] != "ip=10.20.0.10/24,gw=10.20.0.1" {
		t.Fatal("gateway not sent to native Cloud-Init configuration")
	}
	f.vmInput.PriorState = r.ProviderState
	read, err := f.service.ReadResource(context.Background(), &wire.ReadResourceRequest{Input: f.vmInput})
	if err != nil || read.GetResult().GetError() != nil {
		t.Fatalf("read %v %+v", err, read)
	}
	f.vmInput.Context.InvocationId = "destroy-egress"
	stream := &destroyCapture{}
	if err := f.service.DestroyResource(&wire.DestroyResourceRequest{Input: f.vmInput}, stream); err != nil {
		t.Fatal(err)
	}
	last := stream.events[len(stream.events)-1].GetEvent().GetResult()
	if last.GetError() != nil || f.fake.configs[state.VMID].Digest != "" {
		t.Fatalf("destroy %+v", last.GetError())
	}
}
