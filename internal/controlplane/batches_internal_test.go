package controlplane

import (
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestBatchPlanningExcludesAllocationsChosenByPriorItems(t *testing.T) {
	vmID := domain.LogicalResourceID("vm.student")
	networkID := domain.LogicalResourceID("network.student")
	external := application.AllocationKey{TargetID: "target.proxmox", Namespace: "proxmox.vmid", Value: "100"}
	priorItem := application.AllocationKey{TargetID: "target.proxmox", Namespace: "proxmox.vmid", Value: "101"}
	persistent := map[domain.LogicalResourceID][]application.AllocationKey{vmID: {external}}
	resources := []domain.LogicalResourceID{vmID, networkID}

	merged := mergeBatchExclusions(persistent, resources, []application.AllocationKey{priorItem, external})
	if len(merged[vmID]) != 2 || merged[vmID][0] != external || merged[vmID][1] != priorItem {
		t.Fatalf("VM exclusions = %#v", merged[vmID])
	}
	if len(merged[networkID]) != 2 || merged[networkID][0] != priorItem || merged[networkID][1] != external {
		t.Fatalf("network exclusions = %#v", merged[networkID])
	}
	merged[vmID][0].Value = "changed"
	if persistent[vmID][0] != external {
		t.Fatal("merged exclusions aliased planner state")
	}
}
