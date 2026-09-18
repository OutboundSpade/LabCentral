package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func TestMultipleImagesBindPerResourceAndShareAllocationNamespace(t *testing.T) {
	service := NewService()
	target := mutationTestTarget(t)
	var config TargetConfig
	if err := json.Unmarshal(target.ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	second := config.Templates[0]
	second.Alias, second.VMID, second.ConfigDigest, second.ContentDigest = "second", 9001, strings.Repeat("d", 40), sha("5")
	config.Templates = append(config.Templates, second)
	config.PermissionScopes = nil
	for path, privileges := range expectedPermissionScopes(config) {
		scope := PermissionScope{Path: path}
		for name := range privileges {
			scope.Privileges = append(scope.Privileges, name)
		}
		config.PermissionScopes = append(config.PermissionScopes, scope)
	}
	target.ConfigEnvelope, _ = json.Marshal(config)
	var lab canonicalLabDocument
	if err := json.Unmarshal(canonicalMutationLab(), &lab); err != nil {
		t.Fatal(err)
	}
	other := lab.Resources[1]
	other.ID = "vm.second"
	other.Spec = bytes.ReplaceAll(other.Spec, []byte(testTemplateDigest), []byte(second.ContentDigest))
	other.Spec = bytes.ReplaceAll(other.Spec, []byte("10.20.0.10"), []byte("10.20.0.11"))
	lab.Resources = append(lab.Resources, other)
	raw, _ := json.Marshal(lab)
	validation, err := service.ValidateLab(context.Background(), &wire.ValidateLabRequest{Target: target, CanonicalLabJson: raw})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatal("multi-image validation rejected", validation, err)
	}
	selected, err := decodeValidationPlan(validation.ProviderPlan)
	if err != nil || selected.SchemaVersion != validationPlanV11 || len(selected.ResourceTemplates) != 2 {
		t.Fatal("missing versioned per-resource selection", err)
	}
	excluded := []*wire.AllocationClaim{{Namespace: vmidClaimNamespace, Value: "100"}}
	var dependencies []*wire.ResourceDependency
	for index, resource := range lab.Resources {
		resourceJSON, _ := json.Marshal(resource)
		input := &wire.ResourceMutationInput{Context: &wire.CallContext{LabInstanceId: "instance.multiple", LogicalResourceId: resource.ID, ProviderTargetId: target.TargetId, DesiredGeneration: 1}, Target: target, CanonicalResourceJson: resourceJSON, ProviderPlan: validation.ProviderPlan, OwnershipTokens: []string{"owned-" + resource.ID}, Dependencies: dependencies}
		planned, err := service.PlanResource(context.Background(), &wire.PlanResourceRequest{Input: input, ExcludedAllocations: excluded})
		if err != nil || planned.GetError() != nil {
			t.Fatal("resource plan rejected", resource.ID, planned, err)
		}
		var plan QEMUPlan
		if err := json.Unmarshal(planned.ProviderPlan, &plan); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			dependencies = []*wire.ResourceDependency{{LogicalResourceId: resource.ID, ProviderPlan: planned.ProviderPlan, ProviderPlanDigest: planned.ProviderPlanDigest}}
		} else {
			if plan.Template.VMID != uint32(8999+index) {
				t.Fatal("resource selected another VM's image", plan.Template)
			}
			if len(planned.ProposedAllocations) != 1 || planned.ProposedAllocations[0].Value != []string{"", "101", "102"}[index] {
				t.Fatal("shared VMID namespace collided", planned.ProposedAllocations)
			}
		}
		excluded = append(excluded, planned.ProposedAllocations...)
		if index == 2 {
			input.CanonicalResourceJson = bytes.ReplaceAll(resourceJSON, []byte(second.ContentDigest), []byte(testTemplateDigest))
			bad, err := service.PlanResource(context.Background(), &wire.PlanResourceRequest{Input: input, ExcludedAllocations: excluded})
			if err == nil && bad.GetError() == nil {
				t.Fatal("changed image escaped validated resource binding")
			}
		}
	}
	selected.ResourceTemplates = append(selected.ResourceTemplates, selected.ResourceTemplates[0])
	raw, _ = json.Marshal(selected)
	if _, err := decodeValidationPlan(raw); err == nil {
		t.Fatal("ambiguous resource bindings accepted")
	}
	selected.SchemaVersion = validationPlanV10
	selected.TemplateAlias = "debian"
	raw, _ = json.Marshal(selected)
	if _, err := decodeValidationPlan(raw); err == nil {
		t.Fatal("new selection silently changed old plan semantics")
	}
}
