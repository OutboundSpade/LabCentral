package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type fakeMutationAPI struct {
	mu                       sync.Mutex
	configs                  map[uint32]VMConfig
	statuses                 map[uint32]string
	calls                    []string
	cloneCalls               int
	configureCalls           int
	deleteCalls              int
	lifecycleCalls           map[string]int
	cloneErrorBefore         bool
	cloneErrorAfter          bool
	configureError           bool
	configureErrorAfterApply bool
	deleteErrorAfter         bool
	deleteErrorBefore        bool
	taskFailureType          string
	taskFailuresRemaining    map[string]int
	runningTaskType          string
	foreignOnClone           bool
	cloneWithoutObject       bool
	missingPermission        bool
	missingPropagation       bool
	lifecycleErrorAfter      map[string]bool
	malformedUPIDType        string
	agentError               bool
	missingConfigStatusCode  int
	completeTaskOnPoll       bool
	deferLifecycleState      bool
	closed                   bool
}

func newFakeMutationAPI() *fakeMutationAPI {
	return &fakeMutationAPI{
		configs:  map[uint32]VMConfig{9000: {Digest: strings40("c"), Fields: map[string]string{"digest": strings40("c"), "agent": "1", "scsi0": "local-lvm:base-9000-disk-0,size=8G", "ide2": "local-lvm:cloudinit"}}},
		statuses: map[uint32]string{9000: "stopped"}, lifecycleCalls: make(map[string]int), lifecycleErrorAfter: make(map[string]bool), taskFailuresRemaining: make(map[string]int),
	}
}

func strings40(value string) string {
	result := ""
	for len(result) < 40 {
		result += value
	}
	return result[:40]
}

func (fake *fakeMutationAPI) Discover(context.Context) (Inventory, error) {
	return mutationTestInventory(), nil
}
func (fake *fakeMutationAPI) CloseIdleConnections() { fake.closed = true }
func (fake *fakeMutationAPI) EffectivePermissions(context.Context) (EffectivePermissions, error) {
	paths := make(map[string][]string)
	propagating := make(map[string][]string)
	for _, scope := range mutationTestPermissionScopes() {
		paths[scope.Path] = append([]string(nil), scope.Privileges...)
		propagating[scope.Path] = append([]string(nil), scope.Privileges...)
	}
	if fake.missingPermission {
		paths["/vms/9000"] = paths["/vms/9000"][1:]
	}
	if fake.missingPropagation {
		delete(propagating, "/sdn/zones/localnetwork/vmbr1")
	}
	return EffectivePermissions{Paths: paths, PropagatingPaths: propagating}, nil
}
func (fake *fakeMutationAPI) AdvisoryVMIDAvailable(_ context.Context, vmid uint32) (bool, error) {
	fake.record(fmt.Sprintf("GET nextid %d", vmid))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	_, exists := fake.configs[vmid]
	return !exists, nil
}
func (fake *fakeMutationAPI) VMConfig(_ context.Context, _ string, vmid uint32) (VMConfig, error) {
	fake.record(fmt.Sprintf("GET config %d", vmid))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	config, exists := fake.configs[vmid]
	if !exists {
		statusCode := fake.missingConfigStatusCode
		if statusCode == 0 {
			statusCode = 404
		}
		return VMConfig{}, &APIStatusError{Method: "GET", Path: fmt.Sprintf("/qemu/%d/config", vmid), StatusCode: statusCode}
	}
	return cloneVMConfig(config), nil
}

func TestReadOptionalVMConfigConfirmsPVE500AbsenceWithExactNextID(t *testing.T) {
	fake := newFakeMutationAPI()
	fake.missingConfigStatusCode = http.StatusInternalServerError
	config, exists, err := readOptionalVMConfig(context.Background(), fake, "pve-a", 101)
	if err != nil || exists || config.Fields != nil {
		t.Fatalf("missing PVE 9.2 config = %#v, exists %t, err %v", config, exists, err)
	}
	fake.configs[101] = VMConfig{Digest: strings40("a"), Fields: map[string]string{"digest": strings40("a")}}
	config, exists, err = readOptionalVMConfig(context.Background(), fake, "pve-a", 101)
	if err != nil || !exists || config.Digest != strings40("a") {
		t.Fatalf("existing PVE config = %#v, exists %t, err %v", config, exists, err)
	}
}
func (fake *fakeMutationAPI) CloneVM(_ context.Context, request CloneRequest) (UPID, error) {
	fake.record("POST clone")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.cloneCalls++
	if fake.cloneErrorBefore {
		return UPID{}, errors.New("clone canary-secret response lost")
	}
	if fake.cloneWithoutObject {
		return fakeUPID(request.SourceNode, "qmclone", request.SourceVMID), nil
	}
	base := cloneVMConfig(fake.configs[request.SourceVMID])
	base.Digest = strings40("d")
	base.Description = request.Description
	base.Fields["digest"] = base.Digest
	base.Fields["description"] = request.Description
	base.Fields["scsi0"] = fmt.Sprintf("%s:vm-%d-disk-0,size=8G", request.StorageID, request.NewVMID)
	if fake.foreignOnClone {
		base.Description = "foreign"
		base.Fields["description"] = "foreign"
	}
	fake.configs[request.NewVMID] = base
	fake.statuses[request.NewVMID] = "stopped"
	if fake.cloneErrorAfter {
		return UPID{}, errors.New("clone canary-secret response lost")
	}
	upid := fakeUPID(request.SourceNode, "qmclone", request.SourceVMID)
	if fake.malformedUPIDType == "clone" {
		upid.Raw = "UPID:wrong-node:00000001:00000002:00000003:qmclone:100:root@pam:"
	}
	return upid, nil
}
func (fake *fakeMutationAPI) ConfigureVM(_ context.Context, request ConfigureRequest) (UPID, error) {
	fake.record("POST config")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.configureCalls++
	if fake.configureError {
		return UPID{}, errors.New("config canary-secret")
	}
	config := fake.configs[request.VMID]
	if config.Digest != request.Digest {
		return UPID{}, errors.New("digest conflict")
	}
	config.Description = request.Description
	config.Tags = request.Tags
	config.Digest = strings40("e")
	config.Fields["digest"] = config.Digest
	config.Fields["description"] = request.Description
	config.Fields["tags"] = request.Tags
	config.Fields["sockets"] = fmt.Sprint(request.Sockets)
	config.Fields["cores"] = fmt.Sprint(request.Cores)
	config.Fields["cpulimit"] = request.CPULimit
	config.Fields["memory"] = fmt.Sprint(request.MemoryMiB)
	config.Fields["ciupgrade"] = "0"
	config.Fields["onboot"] = "0"
	for index, binding := range request.Networks {
		config.Fields[fmt.Sprintf("net%d", index)] = fmt.Sprintf("virtio=%s,bridge=%s,tag=%d", strings.ToUpper(binding.MAC), binding.Bridge, binding.VLAN)
		config.Fields[fmt.Sprintf("ipconfig%d", index)] = "ip=" + binding.IPv4Address + "/24"
	}
	fake.configs[request.VMID] = config
	if fake.configureErrorAfterApply {
		return UPID{}, errors.New("config canary-secret response lost")
	}
	upid := fakeUPID(request.Node, "qmconfig", request.VMID)
	if fake.malformedUPIDType == "config" {
		upid.Raw = "UPID:wrong-node:00000001:00000002:00000003:qmconfig:100:root@pam:"
	}
	return upid, nil
}
func (fake *fakeMutationAPI) VMStatus(_ context.Context, _ string, vmid uint32) (VMStatus, error) {
	fake.record(fmt.Sprintf("GET status %d", vmid))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	status, exists := fake.statuses[vmid]
	if !exists {
		return VMStatus{}, errors.New("status not found")
	}
	return VMStatus{Status: status, VMID: vmid}, nil
}
func (fake *fakeMutationAPI) LifecycleVM(_ context.Context, request LifecycleRequest) (UPID, error) {
	fake.record("POST " + request.Action)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.lifecycleCalls[request.Action]++
	taskType := map[string]string{"start": "qmstart", "stop": "qmshutdown", "pause": "qmpause", "resume": "qmresume"}[request.Action]
	if !fake.deferLifecycleState {
		fake.statuses[request.VMID] = map[string]string{"start": "running", "stop": "stopped", "pause": "paused", "resume": "running"}[request.Action]
	}
	upid := fakeUPID(request.Node, taskType, request.VMID)
	if fake.malformedUPIDType == request.Action {
		upid.Raw = "UPID:wrong-node:00000001:00000002:00000003:" + taskType + ":100:root@pam:"
	}
	if fake.lifecycleErrorAfter[request.Action] {
		return UPID{}, errors.New("lifecycle canary-secret response lost")
	}
	return upid, nil
}
func (fake *fakeMutationAPI) GuestAgentPing(_ context.Context, _ string, vmid uint32) error {
	fake.record(fmt.Sprintf("POST agent %d", vmid))
	if fake.agentError {
		return errors.New("agent unavailable")
	}
	return nil
}
func (fake *fakeMutationAPI) TaskStatus(_ context.Context, _ string, upid UPID) (TaskStatus, error) {
	fake.record("GET task " + upid.Type)
	if fake.runningTaskType == upid.Type {
		return TaskStatus{Status: "running"}, nil
	}
	fake.mu.Lock()
	if fake.taskFailuresRemaining[upid.Type] > 0 {
		fake.taskFailuresRemaining[upid.Type]--
		fake.mu.Unlock()
		return TaskStatus{Status: "stopped", ExitStatus: "ERROR canary-secret"}, nil
	}
	fake.mu.Unlock()
	if fake.completeTaskOnPoll {
		vmid, _ := strconv.ParseUint(upid.ObjectID, 10, 32)
		if status := map[string]string{"qmstart": "running", "qmshutdown": "stopped", "qmpause": "paused", "qmresume": "running"}[upid.Type]; status != "" {
			fake.mu.Lock()
			fake.statuses[uint32(vmid)] = status
			fake.mu.Unlock()
		}
	}
	if fake.taskFailureType == upid.Type {
		return TaskStatus{Status: "stopped", ExitStatus: "ERROR canary-secret"}, nil
	}
	return TaskStatus{Status: "stopped", ExitStatus: "OK"}, nil
}
func (fake *fakeMutationAPI) DeleteVM(_ context.Context, node string, vmid uint32) (UPID, error) {
	fake.record("DELETE vm")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deleteCalls++
	if fake.deleteErrorBefore {
		return UPID{}, errors.New("delete canary-secret response lost")
	}
	delete(fake.configs, vmid)
	delete(fake.statuses, vmid)
	if fake.deleteErrorAfter {
		return UPID{}, errors.New("delete canary-secret response lost")
	}
	return fakeUPID(node, "qmdestroy", vmid), nil
}
func (fake *fakeMutationAPI) record(call string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, call)
}
func cloneVMConfig(config VMConfig) VMConfig {
	result := config
	result.Fields = make(map[string]string, len(config.Fields))
	for key, value := range config.Fields {
		result.Fields[key] = value
	}
	return result
}
func fakeUPID(node, taskType string, vmid uint32) UPID {
	value := fmt.Sprintf("UPID:%s:00000001:00000002:00000003:%s:%d:root@pam:", node, taskType, vmid)
	upid, err := ParseUPID(value)
	if err != nil {
		panic(err)
	}
	return upid
}

type applyCapture struct {
	grpcServerStream
	events []*pluginv1.ApplyResourceResponse
}

func (capture *applyCapture) Send(value *pluginv1.ApplyResourceResponse) error {
	capture.events = append(capture.events, value)
	return nil
}

type destroyCapture struct {
	grpcServerStream
	events []*pluginv1.DestroyResourceResponse
}

func (capture *destroyCapture) Send(value *pluginv1.DestroyResourceResponse) error {
	capture.events = append(capture.events, value)
	return nil
}

type lifecycleCapture struct {
	grpcServerStream
	events []*pluginv1.LifecycleResponse
}

func (capture *lifecycleCapture) Send(value *pluginv1.LifecycleResponse) error {
	capture.events = append(capture.events, value)
	return nil
}

type grpcServerStream struct{ ctx context.Context }

func (stream grpcServerStream) SetHeader(metadata.MD) error  { return nil }
func (stream grpcServerStream) SendHeader(metadata.MD) error { return nil }
func (stream grpcServerStream) SetTrailer(metadata.MD)       {}
func (stream grpcServerStream) Context() context.Context {
	if stream.ctx == nil {
		return context.Background()
	}
	return stream.ctx
}
func (stream grpcServerStream) SendMsg(any) error { return nil }
func (stream grpcServerStream) RecvMsg(any) error { return nil }

type executionFixture struct {
	service      *Service
	fake         *fakeMutationAPI
	target       *pluginv1.TargetContext
	networkInput *pluginv1.ResourceMutationInput
	networkPlan  *pluginv1.PlanResourceResponse
	networkState *pluginv1.ProviderState
	vmInput      *pluginv1.ResourceMutationInput
	vmPlan       *pluginv1.PlanResourceResponse
}

func newExecutionFixture(t *testing.T) *executionFixture {
	t.Helper()
	fake := newFakeMutationAPI()
	service := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fake, nil }))
	target := mutationTestTarget(t)
	validation := validateMutationLab(t, service, target)
	networkResource := []byte(`{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}}`)
	networkInput := &pluginv1.ResourceMutationInput{Context: completeCall("inv-network", "network.lab", target.GetTargetId()), Target: target, CanonicalResourceJson: networkResource, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"network-token"}}
	networkPlan, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: networkInput})
	if err != nil || networkPlan.GetError() != nil {
		t.Fatalf("network plan: %v %#v", err, networkPlan.GetError())
	}
	networkInput.ProviderPlan = networkPlan.GetProviderPlan()
	networkInput.CommittedAllocations = cloneClaims(networkPlan.GetProposedAllocations())
	networkStream := &applyCapture{}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: networkInput}, networkStream); err != nil {
		t.Fatal(err)
	}
	networkResult := networkStream.events[len(networkStream.events)-1].GetEvent().GetResult()
	if networkResult.GetError() != nil {
		t.Fatalf("network apply: %#v", networkResult.GetError())
	}
	vmResource := []byte(`{"id":"vm.lab","kind":"VM","dependsOn":["network.lab"],"spec":{"image":{"repository":"labcentral/proxmox/debian","digest":"` + testTemplateDigest + `"},"osFamily":"linux","compute":{"cpuMillis":2500,"memoryBytes":2148000000,"architecture":"amd64"},"interfaces":[{"networkRef":"network.lab","ipv4":"10.20.0.10"}]}}`)
	vmPlanningInput := &pluginv1.ResourceMutationInput{Context: completeCall("plan-vm", "vm.lab", target.GetTargetId()), Target: target, CanonicalResourceJson: vmResource, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"vm-token"}, Dependencies: []*pluginv1.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: networkPlan.GetProviderPlan(), ProviderPlanDigest: networkPlan.GetProviderPlanDigest()}}}
	vmPlan, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: vmPlanningInput})
	if err != nil || vmPlan.GetError() != nil {
		t.Fatalf("VM plan: %v %#v", err, vmPlan.GetError())
	}
	vmInput := &pluginv1.ResourceMutationInput{Context: completeCall("inv-vm", "vm.lab", target.GetTargetId()), Target: target, CanonicalResourceJson: vmResource, ProviderPlan: vmPlan.GetProviderPlan(), OwnershipTokens: []string{"vm-token"}, CommittedAllocations: cloneClaims(vmPlan.GetProposedAllocations()), Dependencies: []*pluginv1.ResourceDependency{{LogicalResourceId: "network.lab", ProviderPlan: networkPlan.GetProviderPlan(), ProviderPlanDigest: networkPlan.GetProviderPlanDigest(), ProviderState: networkResult.GetProviderState(), CommittedAllocations: cloneClaims(networkPlan.GetProposedAllocations())}}}
	return &executionFixture{service: service, fake: fake, target: target, networkInput: networkInput, networkPlan: networkPlan, networkState: networkResult.GetProviderState(), vmInput: vmInput, vmPlan: vmPlan}
}

func completeCall(invocation, resource, target string) *pluginv1.CallContext {
	return &pluginv1.CallContext{RequestId: "request", OperationId: "operation.test", InvocationId: invocation, LabInstanceId: "instance.test", LogicalResourceId: resource, ProviderTargetId: target, IdempotencyKey: "idempotency.test", DesiredGeneration: 1, FencingToken: 1}
}
func cloneClaims(claims []*pluginv1.AllocationClaim) []*pluginv1.AllocationClaim {
	result := make([]*pluginv1.AllocationClaim, len(claims))
	for index, claim := range claims {
		result[index] = &pluginv1.AllocationClaim{Namespace: claim.GetNamespace(), Value: claim.GetValue()}
	}
	return result
}

func applyVMFixture(t *testing.T, fixture *executionFixture) *pluginv1.ResourceResult {
	t.Helper()
	stream := &applyCapture{}
	if err := fixture.service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: fixture.vmInput}, stream); err != nil {
		t.Fatal(err)
	}
	result := stream.events[len(stream.events)-1].GetEvent().GetResult()
	return result
}

func TestT0ApplyReadLifecycleDestroyAndRestartReplay(t *testing.T) {
	fixture := newExecutionFixture(t)
	result := applyVMFixture(t, fixture)
	if result.GetError() != nil {
		t.Fatalf("apply failed: %#v", result.GetError())
	}
	if fixture.fake.cloneCalls != 1 || fixture.fake.configureCalls != 1 || fixture.fake.lifecycleCalls["start"] != 1 {
		t.Fatalf("calls: %#v", fixture.fake)
	}
	readInput := cloneMutationInput(fixture.vmInput)
	readInput.PriorState = result.GetProviderState()
	read, err := fixture.service.ReadResource(context.Background(), &pluginv1.ReadResourceRequest{Input: readInput})
	if err != nil || read.GetResult().GetError() != nil {
		t.Fatalf("read: %v %#v", err, read.GetResult().GetError())
	}
	lifecycle := &lifecycleCapture{}
	if err := fixture.service.Lifecycle(&pluginv1.LifecycleRequest{Context: completeCall("inv-stop", "vm.lab", fixture.target.GetTargetId()), Target: fixture.target, Action: pluginv1.LifecycleAction_LIFECYCLE_ACTION_STOP, PriorState: result.GetProviderState(), ActionParametersJson: []byte(`{}`)}, lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle.events[0].GetEvent().GetResult().GetError() != nil || fixture.fake.lifecycleCalls["stop"] != 1 {
		t.Fatalf("stop failed: %#v", lifecycle.events)
	}
	restarted := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fixture.fake, nil }))
	replay := &applyCapture{}
	if err := restarted.ApplyResource(&pluginv1.ApplyResourceRequest{Input: fixture.vmInput}, replay); err != nil {
		t.Fatal(err)
	}
	if replay.events[len(replay.events)-1].GetEvent().GetResult().GetError() != nil || fixture.fake.cloneCalls != 1 {
		t.Fatal("durable replay repeated clone")
	}
	lookup, err := restarted.GetInvocation(context.Background(), &pluginv1.GetInvocationRequest{Target: fixture.target, InvocationId: "inv-vm"})
	if err != nil || lookup.GetResult().GetError() != nil {
		t.Fatalf("GetInvocation: %v %#v", err, lookup.GetResult().GetError())
	}
	destroyInput := cloneMutationInput(fixture.vmInput)
	destroyInput.Context = completeCall("inv-destroy", "vm.lab", fixture.target.GetTargetId())
	destroyInput.PriorState = result.GetProviderState()
	destroy := &destroyCapture{}
	if err := restarted.DestroyResource(&pluginv1.DestroyResourceRequest{Input: destroyInput}, destroy); err != nil {
		t.Fatal(err)
	}
	destroyResult := destroy.events[len(destroy.events)-1].GetEvent().GetResult()
	if destroyResult.GetError() != nil || fixture.fake.deleteCalls != 1 {
		t.Fatalf("destroy: %#v, calls=%d", destroyResult.GetError(), fixture.fake.deleteCalls)
	}
}

func TestProductionConstructorKeepsMutationGateClosed(t *testing.T) {
	fixture := newExecutionFixture(t)
	production := NewServiceWithFactory(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fixture.fake, nil }))
	discovery, err := production.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: fixture.target})
	if err != nil || findCapability(t, discovery, "workload.vm").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED || findCapability(t, discovery, "lifecycle.start").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED {
		t.Fatalf("production mutation capability opened: %v %#v", err, discovery)
	}
	stream := &applyCapture{}
	if err := production.ApplyResource(&pluginv1.ApplyResourceRequest{Input: fixture.vmInput}, stream); err != nil {
		t.Fatal(err)
	}
	result := stream.events[0].GetEvent().GetResult()
	if result.GetError().GetCode() != "mutation_gate_closed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClosedMutationStateAndCommittedClaimBoundaries(t *testing.T) {
	fixture := newExecutionFixture(t)

	t.Run("apply requires the exact committed claim set", func(t *testing.T) {
		input := cloneMutationInput(fixture.vmInput)
		input.Context = completeCall("wrong-claims", "vm.lab", fixture.target.GetTargetId())
		input.CommittedAllocations = append(input.CommittedAllocations, &pluginv1.AllocationClaim{Namespace: vmidClaimNamespace, Value: "109"})
		stream := &applyCapture{}
		if err := fixture.service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[0].GetEvent().GetResult()
		if result.GetError().GetCode() != "allocation_mismatch" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v cloneCalls=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("plan and provider state reject unknown fields", func(t *testing.T) {
		invalidPlan := append([]byte(nil), fixture.vmPlan.GetProviderPlan()...)
		invalidPlan = append(invalidPlan[:len(invalidPlan)-1], []byte(`,"args":"host"}`)...)
		var plan QEMUPlan
		if err := decodeEnvelope(invalidPlan, maximumConfigBytes, &plan); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown plan field accepted: %v", err)
		}
		state := proto.Clone(fixture.networkState).(*pluginv1.ProviderState)
		state.Opaque = append(state.Opaque[:len(state.Opaque)-1], []byte(`,"rawBridge":"vmbr0"}`)...)
		digest := sha256.Sum256(state.Opaque)
		state.Digest = digest[:]
		if _, err := decodeQEMUState(state); err == nil {
			t.Fatal("unknown provider-state field accepted")
		}
	})
}

func TestContractTESTPROXMOX011RestartsAtEveryPersistedMutationCheckpoint(t *testing.T) {
	for _, phase := range []string{"prepared", "clone-submitted", "clone-complete", "config-submitted", "configured", "lifecycle-submitted"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			prepared, failure := fixture.service.prepareExecution(fixture.vmInput, "apply")
			if failure != nil {
				t.Fatal(failure.GetError())
			}
			prepared.client.CloseIdleConnections()
			var plan QEMUPlan
			if err := decodeEnvelope(fixture.vmPlan.GetProviderPlan(), maximumConfigBytes, &plan); err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				materializeOwnedClone(t, fixture.fake, plan)
			}
			if phase == "config-submitted" || phase == "configured" || phase == "lifecycle-submitted" {
				config := fixture.fake.configs[100]
				marker, _ := encodeOwnershipMarker(*plan.Ownership)
				if _, err := fixture.fake.ConfigureVM(context.Background(), ConfigureRequest{Node: plan.Destination.Node, VMID: 100, Digest: config.Digest, Sockets: plan.Hardware.Sockets, Cores: plan.Hardware.Cores, CPULimit: plan.Hardware.CPULimit, MemoryMiB: plan.Hardware.MemoryMiB, Description: marker, Tags: ownershipTag(*plan.Ownership), Networks: plan.Networks}); err != nil {
					t.Fatal(err)
				}
			}
			prepared.record.Phase = phase
			if phase == "clone-submitted" {
				prepared.record.TaskUPID = fakeUPID(plan.Template.SourceNode, "qmclone", plan.Template.VMID).Raw
			}
			if phase == "config-submitted" {
				prepared.record.TaskUPID = fakeUPID(plan.Destination.Node, "qmconfig", 100).Raw
			}
			if phase == "lifecycle-submitted" {
				prepared.record.TaskUPID = fakeUPID(plan.Destination.Node, "qmstart", 100).Raw
				fixture.fake.statuses[100] = "stopped"
				fixture.fake.completeTaskOnPoll = true
			}
			if err := prepared.journal.Write(prepared.record); err != nil {
				t.Fatal(err)
			}
			baselineClone, baselineConfig, baselineStart := fixture.fake.cloneCalls, fixture.fake.configureCalls, fixture.fake.lifecycleCalls["start"]
			restarted := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fixture.fake, nil }))
			fixture.service = restarted
			result := applyVMFixture(t, fixture)
			if result.GetError() != nil {
				t.Fatalf("resume failed: %#v", result.GetError())
			}
			if phase != "prepared" && fixture.fake.cloneCalls != baselineClone {
				t.Fatalf("phase %s repeated clone", phase)
			}
			if (phase == "config-submitted" || phase == "configured" || phase == "lifecycle-submitted") && fixture.fake.configureCalls != baselineConfig {
				t.Fatalf("phase %s repeated configuration", phase)
			}
			if phase == "lifecycle-submitted" && fixture.fake.lifecycleCalls["start"] != baselineStart {
				t.Fatal("lifecycle-submitted recovery repeated start")
			}
		})
	}

	t.Run("destroy-submitted", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		input := cloneMutationInput(fixture.vmInput)
		input.Context = completeCall("restart-destroy", "vm.lab", fixture.target.GetTargetId())
		input.PriorState = applied.GetProviderState()
		prepared, failure := fixture.service.prepareExecution(input, "destroy")
		if failure != nil {
			t.Fatal(failure.GetError())
		}
		prepared.client.CloseIdleConnections()
		delete(fixture.fake.configs, 100)
		delete(fixture.fake.statuses, 100)
		prepared.record.Phase = "destroy-submitted"
		prepared.record.TaskUPID = fakeUPID("pve-a", "qmdestroy", 100).Raw
		if err := prepared.journal.Write(prepared.record); err != nil {
			t.Fatal(err)
		}
		restarted := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fixture.fake, nil }))
		stream := &destroyCapture{}
		if err := restarted.DestroyResource(&pluginv1.DestroyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		if result := stream.events[len(stream.events)-1].GetEvent().GetResult(); result.GetError() != nil || fixture.fake.deleteCalls != 0 {
			t.Fatalf("destroy recovery=%#v deleteCalls=%d", result.GetError(), fixture.fake.deleteCalls)
		}
	})
}

func materializeOwnedClone(t *testing.T, fake *fakeMutationAPI, plan QEMUPlan) {
	t.Helper()
	marker, err := encodeOwnershipMarker(*plan.Ownership)
	if err != nil {
		t.Fatal(err)
	}
	base := cloneVMConfig(fake.configs[plan.Template.VMID])
	base.Digest = strings40("d")
	base.Description = marker
	base.Fields["digest"] = base.Digest
	base.Fields["description"] = marker
	base.Fields["scsi0"] = "local-lvm:vm-100-disk-0,size=8G"
	fake.configs[100] = base
	fake.statuses[100] = "stopped"
}

func TestProxmoxFailureWindowContract(t *testing.T) {
	t.Run("TEST-PROXMOX-002 stale plan", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		target := proto.Clone(fixture.target).(*pluginv1.TargetContext)
		target.ConfigFingerprint = sha("f")
		fixture.vmInput.Target = target
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "stale_plan" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v cloneCalls=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-002 plan fields remain bound to configured profiles", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		var plan QEMUPlan
		if err := decodeEnvelope(fixture.vmInput.GetProviderPlan(), maximumConfigBytes, &plan); err != nil {
			t.Fatal(err)
		}
		plan.Destination.StorageID = "attacker-storage"
		for index := range plan.Disks {
			plan.Disks[index].StorageID = "attacker-storage"
		}
		contents, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		fixture.vmInput.ProviderPlan = contents
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "invalid_provider_plan" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v cloneCalls=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-003 template drift", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		config := fixture.fake.configs[9000]
		config.Fields["hostpci0"] = "0000:01:00.0"
		fixture.fake.configs[9000] = config
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "template_drift" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v cloneCalls=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-004 foreign VMID", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.configs[100] = VMConfig{Digest: strings40("f"), Description: "foreign", Fields: map[string]string{"digest": strings40("f"), "description": "foreign"}}
		fixture.fake.statuses[100] = "stopped"
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "ownership_mismatch" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v cloneCalls=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-005 lost clone response after commit", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.cloneErrorAfter = true
		result := applyVMFixture(t, fixture)
		if result.GetError() != nil || fixture.fake.cloneCalls != 1 || fixture.fake.configureCalls != 1 {
			t.Fatalf("result=%#v calls=%d/%d", result.GetError(), fixture.fake.cloneCalls, fixture.fake.configureCalls)
		}
	})

	t.Run("TEST-PROXMOX-006 lost clone response before commit retries", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.cloneErrorBefore = true
		first := applyVMFixture(t, fixture)
		if first.GetError().GetCode() != "clone_submission_failed" || !first.GetError().GetRetryable() {
			t.Fatalf("first=%#v", first.GetError())
		}
		fixture.fake.cloneErrorBefore = false
		second := applyVMFixture(t, fixture)
		if second.GetError() != nil || fixture.fake.cloneCalls != 2 {
			t.Fatalf("second=%#v calls=%d", second.GetError(), fixture.fake.cloneCalls)
		}
		if _, exists := fixture.fake.configs[100]; !exists {
			t.Fatal("retry did not produce exactly addressed VM")
		}
	})

	t.Run("TEST-PROXMOX-007 clone task failure before object", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.cloneWithoutObject = true
		fixture.fake.taskFailureType = "qmclone"
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "provider_task_failed" || !result.GetCompensationComplete() || fixture.fake.deleteCalls != 0 {
			t.Fatalf("result=%#v compensation=%t delete=%d", result.GetError(), result.GetCompensationComplete(), fixture.fake.deleteCalls)
		}
	})

	t.Run("TEST-PROXMOX-008 configuration failure compensates", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.configureError = true
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "provider_config_failed" || !result.GetCompensationAttempted() || !result.GetCompensationComplete() || fixture.fake.deleteCalls != 1 {
			t.Fatalf("result=%#v attempted=%t complete=%t delete=%d", result.GetError(), result.GetCompensationAttempted(), result.GetCompensationComplete(), fixture.fake.deleteCalls)
		}
	})

	t.Run("TEST-PROXMOX-009 compensation failure retains leftover", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.configureError = true
		fixture.fake.deleteErrorBefore = true
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "provider_config_failed" || result.GetCompensationComplete() || fixture.fake.deleteCalls != 1 || !strings.Contains(string(result.GetObservedStateJson()), "qemu/100") {
			t.Fatalf("result=%#v complete=%t observed=%s", result.GetError(), result.GetCompensationComplete(), result.GetObservedStateJson())
		}
	})

	t.Run("TEST-PROXMOX-010 lost config response uses desired readback", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.configureErrorAfterApply = true
		result := applyVMFixture(t, fixture)
		if result.GetError() != nil || fixture.fake.configureCalls != 1 {
			t.Fatalf("result=%#v configureCalls=%d", result.GetError(), fixture.fake.configureCalls)
		}
	})

	t.Run("TEST-PROXMOX-010 configuration task failure compensates", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.taskFailureType = "qmconfig"
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "provider_task_failed" || !result.GetCompensationAttempted() || !result.GetCompensationComplete() {
			t.Fatalf("result=%#v attempted=%t complete=%t", result.GetError(), result.GetCompensationAttempted(), result.GetCompensationComplete())
		}
	})

	t.Run("TEST-PROXMOX-012 lost start response reconciles state", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.lifecycleErrorAfter["start"] = true
		result := applyVMFixture(t, fixture)
		if result.GetError() != nil || fixture.fake.lifecycleCalls["start"] != 1 {
			t.Fatalf("result=%#v startCalls=%d", result.GetError(), fixture.fake.lifecycleCalls["start"])
		}
	})

	t.Run("TEST-PROXMOX-015 definitively failed lifecycle task retries from unchanged state", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		fixture.fake.taskFailuresRemaining["qmpause"] = 2
		fixture.fake.deferLifecycleState = true
		fixture.fake.completeTaskOnPoll = true
		stream := &lifecycleCapture{}
		request := &pluginv1.LifecycleRequest{
			Context: completeCall("pause-retry", "vm.lab", fixture.target.GetTargetId()), Target: fixture.target,
			Action: pluginv1.LifecycleAction_LIFECYCLE_ACTION_PAUSE, PriorState: applied.GetProviderState(), ActionParametersJson: []byte(`{}`),
		}
		if err := fixture.service.Lifecycle(request, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[0].GetEvent().GetResult()
		if result.GetError() != nil || fixture.fake.lifecycleCalls["pause"] != 3 {
			t.Fatalf("result=%#v pauseCalls=%d", result.GetError(), fixture.fake.lifecycleCalls["pause"])
		}
	})

	t.Run("TEST-PROXMOX-013 destroy refuses marker mismatch", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		if applied.GetError() != nil {
			t.Fatal(applied.GetError())
		}
		config := fixture.fake.configs[100]
		config.Description = "foreign"
		config.Fields["description"] = "foreign"
		fixture.fake.configs[100] = config
		input := cloneMutationInput(fixture.vmInput)
		input.Context = completeCall("destroy-foreign", "vm.lab", fixture.target.GetTargetId())
		input.PriorState = applied.GetProviderState()
		stream := &destroyCapture{}
		if err := fixture.service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[len(stream.events)-1].GetEvent().GetResult()
		if result.GetError().GetCode() != "ownership_mismatch" || fixture.fake.deleteCalls != 0 {
			t.Fatalf("result=%#v delete=%d", result.GetError(), fixture.fake.deleteCalls)
		}
	})

	t.Run("TEST-PROXMOX-013 destroy refuses provider-state address substitution", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		state, err := decodeQEMUState(applied.GetProviderState())
		if err != nil {
			t.Fatal(err)
		}
		state.VMID = 101
		contents, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		prior := proto.Clone(applied.GetProviderState()).(*pluginv1.ProviderState)
		prior.Opaque = contents
		digest := sha256.Sum256(contents)
		prior.Digest = digest[:]
		input := cloneMutationInput(fixture.vmInput)
		input.Context = completeCall("destroy-substituted", "vm.lab", fixture.target.GetTargetId())
		input.PriorState = prior
		stream := &destroyCapture{}
		if err := fixture.service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[len(stream.events)-1].GetEvent().GetResult()
		if result.GetError().GetCode() != "invalid_provider_state" || fixture.fake.deleteCalls != 0 || countCalls(fixture.fake.calls, "GET config 101") != 0 {
			t.Fatalf("result=%#v delete=%d calls=%#v", result.GetError(), fixture.fake.deleteCalls, fixture.fake.calls)
		}
	})

	t.Run("TEST-PROXMOX-014 lost destroy response confirms absence", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		fixture.fake.deleteErrorAfter = true
		input := cloneMutationInput(fixture.vmInput)
		input.Context = completeCall("destroy-lost", "vm.lab", fixture.target.GetTargetId())
		input.PriorState = applied.GetProviderState()
		stream := &destroyCapture{}
		if err := fixture.service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[len(stream.events)-1].GetEvent().GetResult()
		if result.GetError() != nil || fixture.fake.deleteCalls != 1 {
			t.Fatalf("result=%#v delete=%d", result.GetError(), fixture.fake.deleteCalls)
		}
	})

	t.Run("TEST-PROXMOX-015 lifecycle is read-before-action", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		before := fixture.fake.lifecycleCalls["start"]
		stream := &lifecycleCapture{}
		if err := fixture.service.Lifecycle(&pluginv1.LifecycleRequest{Context: completeCall("already-running", "vm.lab", fixture.target.GetTargetId()), Target: fixture.target, Action: pluginv1.LifecycleAction_LIFECYCLE_ACTION_START, PriorState: applied.GetProviderState(), ActionParametersJson: []byte(`{}`)}, stream); err != nil {
			t.Fatal(err)
		}
		if stream.events[0].GetEvent().GetResult().GetError() != nil || fixture.fake.lifecycleCalls["start"] != before {
			t.Fatalf("duplicate lifecycle action: %d -> %d", before, fixture.fake.lifecycleCalls["start"])
		}
	})

	t.Run("TEST-PROXMOX-020 lifecycle rejects null parameters", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		applied := applyVMFixture(t, fixture)
		before := fixture.fake.lifecycleCalls["stop"]
		stream := &lifecycleCapture{}
		if err := fixture.service.Lifecycle(&pluginv1.LifecycleRequest{Context: completeCall("null-parameters", "vm.lab", fixture.target.GetTargetId()), Target: fixture.target, Action: pluginv1.LifecycleAction_LIFECYCLE_ACTION_STOP, PriorState: applied.GetProviderState(), ActionParametersJson: []byte(`null`)}, stream); err != nil {
			t.Fatal(err)
		}
		result := stream.events[0].GetEvent().GetResult()
		if result.GetError().GetCode() != "invalid_lifecycle_input" || fixture.fake.lifecycleCalls["stop"] != before {
			t.Fatalf("result=%#v stopCalls=%d", result.GetError(), fixture.fake.lifecycleCalls["stop"])
		}
	})

	t.Run("TEST-PROXMOX-016 canaries are redacted", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.cloneErrorBefore = true
		result := applyVMFixture(t, fixture)
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "canary-secret") {
			t.Fatalf("result leaked canary: %s", encoded)
		}
		for _, contents := range journalContents(t, fixture.target) {
			if strings.Contains(string(contents), "canary-secret") {
				t.Fatalf("journal leaked canary: %s", contents)
			}
		}
	})

	t.Run("TEST-PROXMOX-019 missing privilege blocks mutation", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.missingPermission = true
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "permission_gate_failed" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v clone=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-019 non-propagating VLAN scope blocks mutation", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.missingPropagation = true
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "permission_gate_failed" || fixture.fake.cloneCalls != 0 {
			t.Fatalf("result=%#v clone=%d", result.GetError(), fixture.fake.cloneCalls)
		}
	})

	t.Run("TEST-PROXMOX-021 malformed UPID", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.malformedUPIDType = "clone"
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "invalid_task_identity" || fixture.fake.configureCalls != 0 || fixture.fake.lifecycleCalls["start"] != 0 {
			t.Fatalf("result=%#v configure=%d start=%d", result.GetError(), fixture.fake.configureCalls, fixture.fake.lifecycleCalls["start"])
		}
	})

	t.Run("TEST-PROXMOX-022 failed task is bounded and redacted", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.cloneWithoutObject = true
		fixture.fake.taskFailureType = "qmclone"
		result := applyVMFixture(t, fixture)
		encoded, _ := json.Marshal(result)
		if result.GetError().GetCode() != "provider_task_failed" || strings.Contains(string(encoded), "canary-secret") || countCalls(fixture.fake.calls, "GET task") > maximumTaskPolls {
			t.Fatalf("result=%s calls=%#v", encoded, fixture.fake.calls)
		}
	})

	t.Run("TEST-PROXMOX-022 running clone timeout does not race compensation", func(t *testing.T) {
		fixture := newExecutionFixture(t)
		fixture.fake.runningTaskType = "qmclone"
		result := applyVMFixture(t, fixture)
		if result.GetError().GetCode() != "unknown_clone_outcome" || result.GetCompensationAttempted() || fixture.fake.deleteCalls != 0 || countCalls(fixture.fake.calls, "GET task") != maximumTaskPolls {
			t.Fatalf("result=%#v attempted=%t delete=%d calls=%#v", result.GetError(), result.GetCompensationAttempted(), fixture.fake.deleteCalls, fixture.fake.calls)
		}
	})
}

func TestProxmoxAllocationAndCapabilityEvidenceContract(t *testing.T) {
	t.Run("TEST-PROXMOX-017 excluded VLAN claims replan uniquely", func(t *testing.T) {
		service := NewService()
		target := mutationTestTarget(t)
		validation := validateMutationLab(t, service, target)
		resource := []byte(`{"id":"network.lab","kind":"Network","spec":{"kind":"isolated","ipv4Cidr":"10.20.0.0/24","ipam":{"mode":"static"},"egress":{"mode":"deny"}}}`)
		input := &pluginv1.ResourceMutationInput{Context: &pluginv1.CallContext{LabInstanceId: "instance.test", LogicalResourceId: "network.lab", ProviderTargetId: target.GetTargetId(), DesiredGeneration: 1}, Target: target, CanonicalResourceJson: resource, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{"token"}}
		first, _ := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: input})
		second, _ := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{Input: input, ExcludedAllocations: cloneClaims(first.GetProposedAllocations())})
		if first.GetProposedAllocations()[0].GetValue() != "200" || second.GetProposedAllocations()[0].GetValue() != "201" {
			t.Fatalf("claims=%#v %#v", first.GetProposedAllocations(), second.GetProposedAllocations())
		}
	})

	t.Run("TEST-PROXMOX-018 and TEST-PROXMOX-019 capability gate needs positive isolation and permissions", func(t *testing.T) {
		fake := newFakeMutationAPI()
		service := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fake, nil }))
		target := mutationTestTarget(t)
		response, err := service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: target})
		if err != nil || findCapability(t, response, "workload.vm").GetSupport() != pluginv1.Capability_SUPPORT_NATIVE {
			t.Fatalf("qualified capability unavailable: %v %#v", err, response)
		}
		fake.missingPermission = true
		response, err = service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: target})
		if err != nil || findCapability(t, response, "workload.vm").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED {
			t.Fatalf("mis-scoped capability advertised: %v %#v", err, response)
		}
		fake.missingPermission = false
		var config TargetConfig
		if err := json.Unmarshal(target.GetConfigEnvelope(), &config); err != nil {
			t.Fatal(err)
		}
		config.ManagementIsolation.ManagementReachable = true
		unsafeEnvelope, _ := json.Marshal(config)
		unsafeTarget := proto.Clone(target).(*pluginv1.TargetContext)
		unsafeTarget.ConfigEnvelope = unsafeEnvelope
		response, err = service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: unsafeTarget})
		if err != nil || findCapability(t, response, "workload.vm").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED {
			t.Fatalf("unsafe management reachability advertised: %v %#v", err, response)
		}
	})
}

func journalContents(t *testing.T, target *pluginv1.TargetContext) [][]byte {
	t.Helper()
	var config TargetConfig
	if err := json.Unmarshal(target.GetConfigEnvelope(), &config); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(config.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var result [][]byte
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(config.JournalDirectory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, contents)
	}
	return result
}

func countCalls(calls []string, prefix string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

func cloneMutationInput(input *pluginv1.ResourceMutationInput) *pluginv1.ResourceMutationInput {
	return proto.Clone(input).(*pluginv1.ResourceMutationInput)
}
