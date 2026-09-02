package pluginv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestProtocolV11AllocationAndDependencyFieldsRoundTrip(t *testing.T) {
	request := &PlanResourceRequest{
		Input: &ResourceMutationInput{
			Context: &CallContext{LabInstanceId: "instance-1", LogicalResourceId: "vm.router"},
			Dependencies: []*ResourceDependency{{
				LogicalResourceId: "network.red",
				ProviderPlan:      []byte(`{"bridgeAlias":"lab"}`),
				ProviderPlanDigest: "digest-network",
				ProviderState:      &ProviderState{Opaque: []byte(`{"vlan":201}`)},
				ObservedStateJson:  []byte(`{"ready":true}`),
				CommittedAllocations: []*AllocationClaim{{
					Namespace: "proxmox.network.vlan", Value: "201",
				}},
			}},
			CommittedAllocations: []*AllocationClaim{{Namespace: "proxmox.qemu.vmid", Value: "401"}},
		},
		ExcludedAllocations: []*AllocationClaim{{Namespace: "proxmox.qemu.vmid", Value: "400"}},
	}
	encoded, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PlanResourceRequest
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(request, &decoded) {
		t.Fatalf("protocol v1.1 round trip changed request:\nwant %v\ngot  %v", request, &decoded)
	}
	response := &PlanResourceResponse{ProposedAllocations: []*AllocationClaim{
		{Namespace: "proxmox.qemu.vmid", Value: "401"},
	}}
	encoded, err = proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decodedResponse PlanResourceResponse
	if err := proto.Unmarshal(encoded, &decodedResponse); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(response, &decodedResponse) {
		t.Fatalf("protocol v1.1 round trip changed response: %v", &decodedResponse)
	}
}
