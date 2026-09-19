package proxmox

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestInfrastructureQualificationSeparatesImportedImages(t *testing.T) {
	var config TargetConfig
	if err := json.Unmarshal(mutationTestTarget(t).ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	original := mutationTestInventory()
	legacy, err := original.MutationDigestForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	changed := mutationTestInventory()
	changed.Templates = append(changed.Templates, Template{Node: "pve-a", VMID: 9001, Name: "imported", Status: "stopped"})
	oldChanged, _ := changed.MutationDigestForConfig(config)
	if legacy == oldChanged {
		t.Fatal("changed legacy identity semantics")
	}
	config.SchemaVersion = targetConfigV14
	before, _ := original.MutationDigestForConfig(config)
	after, _ := changed.MutationDigestForConfig(config)
	if before != after || before == legacy {
		t.Fatal("v1.4 projection not isolated/versioned")
	}
	changed.Templates[0].Status = "running"
	after, _ = changed.MutationDigestForConfig(config)
	if before == after {
		t.Fatal("configured template drift ignored")
	}
	changed = mutationTestInventory()
	changed.Nodes[0].Status = "offline"
	after, _ = changed.MutationDigestForConfig(config)
	if before == after {
		t.Fatal("infrastructure drift ignored")
	}
	changed = mutationTestInventory()
	changed.Templates = nil
	after, _ = changed.MutationDigestForConfig(config)
	if before == after {
		t.Fatal("missing static template ignored")
	}
}

func TestImageImportRequiresStagingDeletionPermission(t *testing.T) {
	_, _, request, _ := imageFixture(t)
	var config TargetConfig
	if err := json.Unmarshal(request.Target.ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	if err := config.validateMutation(); err != nil {
		t.Fatal(err)
	}
	for i := range config.PermissionScopes {
		scope := &config.PermissionScopes[i]
		if scope.Path == "/storage/"+config.ImageImport.StagingStorage {
			scope.Privileges = slices.DeleteFunc(scope.Privileges, func(p string) bool { return p == "Datastore.Allocate" })
		}
	}
	if err := config.validateMutation(); err == nil {
		t.Fatal("upload-only permission permits import whose staging cannot be retired")
	}
}
