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
				LogicalResourceId:  "network.red",
				ProviderPlan:       []byte(`{"bridgeAlias":"lab"}`),
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

func TestProtocolV12IndependentAccessFieldsRoundTrip(t *testing.T) {
	registration := &RegisterPluginResponse{
		SelectedProviderProtocol: &ProtocolVersion{Major: 1, Minor: 1},
		SelectedAccessProtocol:   &ProtocolVersion{Major: 1, Minor: 2},
		AccessCallbackCredential: "launch-scoped-secret",
		AccessAudience:           "https://labs.example.test",
	}
	request := &CreateAccessRequest{
		Context:         &CallContext{LabInstanceId: "instance-a", LogicalResourceId: "vm-a", ProviderTargetId: "target-a"},
		AccessSessionId: "access-a", SubjectId: "user-a", Method: "vnc", OpaqueConnectionData: []byte("endpoint"),
		EndpointSchema: &SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 1}, EndpointDigest: "endpoint-digest",
		ProviderStateDigest: "provider-state-digest", Audience: registration.GetAccessAudience(),
	}
	for name, message := range map[string]proto.Message{"registration": registration, "create": request} {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		decoded := message.ProtoReflect().Type().New().Interface()
		if err := proto.Unmarshal(encoded, decoded); err != nil || !proto.Equal(message, decoded) {
			t.Fatalf("protocol v1.2 %s round trip changed message: %v, %v", name, decoded, err)
		}
	}
}
