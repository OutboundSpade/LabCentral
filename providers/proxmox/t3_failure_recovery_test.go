package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/protobuf/proto"
)

const t3RawDiagnosticCanary = "T3-RAW-DIAGNOSTIC-MUST-NOT-LEAK"

type t3FailureActivation struct {
	profilePath     string
	configPath      string
	credentialPath  string
	confirmedTarget string
}

type t3FaultControl struct {
	templateDrift         bool
	foreignDestination    bool
	ownershipMismatchRead bool
	syntheticCloneFailure bool
	missingPermissionPath string
	missingPermission     string
	loseCloneResponse     bool
	rejectConfiguration   bool
	loseConfigureResponse bool
	rejectCompensation    bool
	loseStartResponse     bool
	loseDestroyResponse   bool
	templateVMID          uint32
	destinationVMID       uint32

	mu              sync.Mutex
	cloneCalls      int
	configureCalls  int
	lifecycleCalls  map[string]int
	deleteCalls     int
	taskReads       int
	syntheticUPID   string
	successfulUPIDs map[string]bool
}

type t3FaultCounts struct {
	clone     int
	configure int
	lifecycle map[string]int
	delete    int
	taskReads int
}

type t3FaultAPI struct {
	MutationAPI
	control *t3FaultControl
}

type t3FailureFixture struct {
	profile              t3SmokeProfile
	config               TargetConfig
	target               *pluginv1.TargetContext
	configBytes          []byte
	credentialBytes      []byte
	normalService        *Service
	networkPlanningInput *pluginv1.ResourceMutationInput
	networkPlan          *pluginv1.PlanResourceResponse
	networkInput         *pluginv1.ResourceMutationInput
	networkState         *pluginv1.ProviderState
	vmInput              *pluginv1.ResourceMutationInput
	vmPlan               QEMUPlan
	vmState              *pluginv1.ProviderState
	vmDestroyed          bool
	networkDestroyed     bool
}

func TestContractCredentialedProxmoxT3FailureRecoveryRequiresExplicitTargetConfirmation(t *testing.T) {
	empty, err := readT3FailureActivation(func(string) string { return "" })
	if err != nil || empty != (t3FailureActivation{}) {
		t.Fatalf("empty activation = %#v, %v", empty, err)
	}
	partial := map[string]string{"LABCENTRAL_PROXMOX_T3_FAILURE_EXECUTE": t3ExecuteSentinel}
	if _, err := readT3FailureActivation(func(name string) string { return partial[name] }); err == nil {
		t.Fatal("partial failure/recovery activation was accepted")
	}
	complete := map[string]string{
		"LABCENTRAL_PROXMOX_T3_FAILURE_EXECUTE":         t3ExecuteSentinel,
		"LABCENTRAL_PROXMOX_T3_FAILURE_PROFILE":         "/qualification/profile.json",
		"LABCENTRAL_PROXMOX_T3_FAILURE_TARGET_CONFIG":   "/qualification/target.json",
		"LABCENTRAL_PROXMOX_T3_FAILURE_CREDENTIAL_FILE": "/qualification/credential.json",
		"LABCENTRAL_PROXMOX_T3_FAILURE_CONFIRM_TARGET":  "target.proxmox-t3",
	}
	activation, err := readT3FailureActivation(func(name string) string { return complete[name] })
	if err != nil || activation.confirmedTarget != "target.proxmox-t3" {
		t.Fatalf("complete activation = %#v, %v", activation, err)
	}
	complete["LABCENTRAL_PROXMOX_T3_FAILURE_EXECUTE"] = "yes"
	if _, err := readT3FailureActivation(func(name string) string { return complete[name] }); err == nil {
		t.Fatal("inexact failure/recovery sentinel was accepted")
	}
}

// TestCredentialedProxmoxT3FailureRecovery exercises target-specific failure
// windows against a disposable real PVE endpoint. Transport loss, permission
// removal, task failure, foreign ownership, and compensation failure are
// injected only at the package-private MutationAPI boundary; the test records
// which calls reached PVE and independently confirms absence after every case.
// It cannot enable the production constructor and is skipped by normal CI.
func TestCredentialedProxmoxT3FailureRecovery(t *testing.T) {
	activation, err := readT3FailureActivation(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if activation == (t3FailureActivation{}) {
		t.Skip("credentialed Proxmox T3 failure/recovery qualification is not explicitly enabled")
	}

	profileBytes, err := readT3File(activation.profilePath, 1<<20, false)
	if err != nil {
		t.Fatalf("read failure/recovery profile: %v", err)
	}
	var baseProfile t3SmokeProfile
	if err := decodeEnvelope(profileBytes, 1<<20, &baseProfile); err != nil {
		t.Fatalf("decode failure/recovery profile: %v", err)
	}
	configBytes, err := readT3File(activation.configPath, maximumConfigBytes, false)
	if err != nil {
		t.Fatalf("read failure/recovery target config: %v", err)
	}
	credentialBytes, err := readT3File(activation.credentialPath, maximumCredentialBytes, true)
	if err != nil {
		t.Fatalf("read failure/recovery credential: %v", err)
	}
	defer zeroBytesForT3(credentialBytes)
	var config TargetConfig
	if err := decodeEnvelope(configBytes, maximumConfigBytes, &config); err != nil {
		t.Fatalf("decode failure/recovery target config: %v", err)
	}
	if err := validateT3Profile(baseProfile, config, activation.confirmedTarget); err != nil {
		t.Fatal(err)
	}
	target := t3FailureTarget(baseProfile.TargetID, configBytes, credentialBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	baseline := newT3FaultService(nil)
	discovery, err := baseline.DiscoverTarget(ctx, &pluginv1.DiscoverTargetRequest{Target: target})
	if err != nil || capabilitySupport(discovery, "workload.vm") != pluginv1.Capability_SUPPORT_NATIVE {
		t.Fatalf("exact target failed failure/recovery preflight: %v", err)
	}
	assertT3VMAbsent(t, ctx, config, configBytes, credentialBytes, baseProfile.ExpectedVMID)

	t.Run("TEST-PROXMOX-003 template drift", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p003", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{templateDrift: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		if result.GetError().GetCode() != "template_drift" || control.counts().clone != 0 {
			t.Fatalf("result=%#v counts=%#v", result.GetError(), control.counts())
		}
	})

	t.Run("TEST-PROXMOX-004 foreign VMID", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p004", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{foreignDestination: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		if result.GetError().GetCode() != "ownership_mismatch" || control.counts().clone != 0 {
			t.Fatalf("result=%#v counts=%#v", result.GetError(), control.counts())
		}
	})

	t.Run("TEST-PROXMOX-007 and TEST-PROXMOX-022 failed clone task is clean bounded and redacted", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p007-p022", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{syntheticCloneFailure: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		encoded, _ := json.Marshal(result)
		counts := control.counts()
		if result.GetError().GetCode() != "provider_task_failed" || !result.GetCompensationComplete() || counts.clone != 1 || counts.delete != 0 || counts.taskReads > maximumTaskPolls || strings.Contains(string(encoded), t3RawDiagnosticCanary) {
			t.Fatalf("result=%s counts=%#v", encoded, counts)
		}
	})

	t.Run("TEST-PROXMOX-019 missing privilege", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p019", config, target, configBytes, credentialBytes)
		scope := config.PermissionScopes[0]
		control := &t3FaultControl{missingPermissionPath: scope.Path, missingPermission: scope.Privileges[0], templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		if result.GetError().GetCode() != "permission_gate_failed" || control.counts().clone != 0 {
			t.Fatalf("result=%#v counts=%#v", result.GetError(), control.counts())
		}
	})

	t.Run("TEST-PROXMOX-017 target claim exclusions remain unique", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p017", config, target, configBytes, credentialBytes)
		second, err := t3ReservedPlan(t, baseline, ctx, &pluginv1.PlanResourceRequest{Input: fixture.networkPlanningInput, ExcludedAllocations: cloneClaims(fixture.networkPlan.GetProposedAllocations())}, fixture.profile.ExpectedVMID, fixture.profile.ExpectedVLAN)
		if err != nil || second.GetError() != nil || len(second.GetProposedAllocations()) != 1 || second.GetProposedAllocations()[0].GetNamespace() != vlanClaimNamespace || second.GetProposedAllocations()[0].GetValue() == fixture.networkPlan.GetProposedAllocations()[0].GetValue() {
			t.Fatalf("excluded claim replan=%#v, %v", second, err)
		}
	})

	t.Run("TEST-PROXMOX-008 rejected configuration compensates", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p008", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{rejectConfiguration: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		counts := control.counts()
		if result.GetError().GetCode() != "provider_config_failed" || !result.GetCompensationAttempted() || !result.GetCompensationComplete() || counts.clone != 1 || counts.configure != 1 || counts.delete != 1 {
			t.Fatalf("result=%#v compensation=%t/%t counts=%#v", result.GetError(), result.GetCompensationAttempted(), result.GetCompensationComplete(), counts)
		}
		assertT3VMAbsent(t, ctx, config, configBytes, credentialBytes, fixture.profile.ExpectedVMID)
	})

	t.Run("TEST-PROXMOX-009 failed compensation retains an owned leftover", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p009", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{rejectConfiguration: true, rejectCompensation: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		counts := control.counts()
		if result.GetError().GetCode() != "provider_config_failed" || !result.GetCompensationAttempted() || result.GetCompensationComplete() || counts.delete != 1 || !strings.Contains(string(result.GetObservedStateJson()), fmt.Sprintf("qemu/%d", fixture.profile.ExpectedVMID)) {
			t.Fatalf("result=%#v compensation=%t/%t counts=%#v observed=%s", result.GetError(), result.GetCompensationAttempted(), result.GetCompensationComplete(), counts, result.GetObservedStateJson())
		}
	})

	t.Run("TEST-PROXMOX-005 010 012 013 014 and 015 recovery sequence", func(t *testing.T) {
		fixture := newT3FailureFixture(t, ctx, baseProfile, "p005-p010-p012-p013-p014-p015", config, target, configBytes, credentialBytes)
		control := &t3FaultControl{loseCloneResponse: true, loseConfigureResponse: true, loseStartResponse: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		result := fixture.applyVM(t, ctx, newT3FaultService(control))
		fixture.vmState = result.GetProviderState()
		counts := control.counts()
		if result.GetError() != nil || fixture.vmState == nil || counts.clone != 1 || counts.configure != 1 || counts.lifecycle["start"] != 1 {
			t.Fatalf("apply result=%#v counts=%#v", result.GetError(), counts)
		}

		idempotentControl := &t3FaultControl{templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		idempotent := newT3FaultService(idempotentControl)
		stream := &lifecycleCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
		request := &pluginv1.LifecycleRequest{Context: t3Call(fixture.profile, "already-running", fixture.profile.VMResourceID), Target: target, Action: pluginv1.LifecycleAction_LIFECYCLE_ACTION_START, PriorState: fixture.vmState, ActionParametersJson: []byte(`{}`)}
		if err := idempotent.Lifecycle(request, stream); err != nil {
			t.Fatal(err)
		}
		lifecycleResult := finalLifecycleResult(stream.events)
		if lifecycleResult == nil || lifecycleResult.GetError() != nil || idempotentControl.counts().lifecycle["start"] != 0 {
			t.Fatalf("idempotent lifecycle=%#v counts=%#v", lifecycleResult, idempotentControl.counts())
		}
		fixture.vmState = lifecycleResult.GetProviderState()

		ownershipControl := &t3FaultControl{ownershipMismatchRead: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		ownershipResult := fixture.destroyVM(t, ctx, newT3FaultService(ownershipControl), "ownership-mismatch")
		if ownershipResult.GetError().GetCode() != "ownership_mismatch" || ownershipControl.counts().delete != 0 {
			t.Fatalf("ownership result=%#v counts=%#v", ownershipResult.GetError(), ownershipControl.counts())
		}

		destroyControl := &t3FaultControl{loseDestroyResponse: true, templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
		destroyResult := fixture.destroyVM(t, ctx, newT3FaultService(destroyControl), "lost-destroy-response")
		if destroyResult.GetError() != nil || destroyControl.counts().delete != 1 {
			t.Fatalf("destroy result=%#v counts=%#v", destroyResult.GetError(), destroyControl.counts())
		}
		fixture.vmDestroyed = true
		assertT3VMAbsent(t, ctx, config, configBytes, credentialBytes, fixture.profile.ExpectedVMID)
	})

	t.Run("TEST-PROXMOX-011 restart from every durable checkpoint", func(t *testing.T) {
		runT3RestartCheckpoints(t, ctx, baseProfile, config, target, configBytes, credentialBytes)
	})

	assertT3VMAbsent(t, ctx, config, configBytes, credentialBytes, baseProfile.ExpectedVMID)
	t.Logf("credentialed T3 failure/recovery cases completed for target %s; injected boundary faults were distinguished from calls accepted by PVE", baseProfile.TargetID)
}

func readT3FailureActivation(getenv func(string) string) (t3FailureActivation, error) {
	names := []string{
		"LABCENTRAL_PROXMOX_T3_FAILURE_EXECUTE",
		"LABCENTRAL_PROXMOX_T3_FAILURE_PROFILE",
		"LABCENTRAL_PROXMOX_T3_FAILURE_TARGET_CONFIG",
		"LABCENTRAL_PROXMOX_T3_FAILURE_CREDENTIAL_FILE",
		"LABCENTRAL_PROXMOX_T3_FAILURE_CONFIRM_TARGET",
	}
	values := make(map[string]string, len(names))
	configured := false
	for _, name := range names {
		values[name] = strings.TrimSpace(getenv(name))
		configured = configured || values[name] != ""
	}
	if !configured {
		return t3FailureActivation{}, nil
	}
	if values[names[0]] != t3ExecuteSentinel {
		return t3FailureActivation{}, errors.New("credentialed T3 failure/recovery requires the exact destructive execution sentinel")
	}
	for _, name := range names[1:] {
		if values[name] == "" {
			return t3FailureActivation{}, fmt.Errorf("credentialed T3 failure/recovery requires %s", name)
		}
	}
	seen := make(map[string]bool, 3)
	for _, name := range names[1:4] {
		value := values[name]
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || seen[value] {
			return t3FailureActivation{}, fmt.Errorf("%s must be an absolute, clean, non-root, distinct path", name)
		}
		seen[value] = true
	}
	if !logicalIDPattern.MatchString(values[names[4]]) {
		return t3FailureActivation{}, errors.New("credentialed T3 failure/recovery target confirmation is invalid")
	}
	return t3FailureActivation{profilePath: values[names[1]], configPath: values[names[2]], credentialPath: values[names[3]], confirmedTarget: values[names[4]]}, nil
}

func t3FailureTarget(targetID string, configBytes, credentialBytes []byte) *pluginv1.TargetContext {
	fingerprint := digestDocument(configBytes)
	return &pluginv1.TargetContext{TargetId: targetID, ConfigEnvelope: configBytes, CredentialEnvelope: credentialBytes, ConfigFingerprint: fingerprint}
}

func newT3FaultService(control *t3FaultControl) *Service {
	service := newT0MutationService(ClientFactoryFunc(func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
		client, err := NewClientFromEnvelopes(configEnvelope, credentialEnvelope)
		if err != nil || control == nil {
			return client, err
		}
		return &t3FaultAPI{MutationAPI: client, control: control}, nil
	}))
	service.sleep = time.Sleep
	return service
}

func (control *t3FaultControl) counts() t3FaultCounts {
	control.mu.Lock()
	defer control.mu.Unlock()
	lifecycle := make(map[string]int, len(control.lifecycleCalls))
	for action, count := range control.lifecycleCalls {
		lifecycle[action] = count
	}
	return t3FaultCounts{clone: control.cloneCalls, configure: control.configureCalls, lifecycle: lifecycle, delete: control.deleteCalls, taskReads: control.taskReads}
}

func (api *t3FaultAPI) EffectivePermissions(ctx context.Context) (EffectivePermissions, error) {
	permissions, err := api.MutationAPI.EffectivePermissions(ctx)
	if err != nil || api.control.missingPermissionPath == "" {
		return permissions, err
	}
	permissions.Paths = cloneT3PermissionMap(permissions.Paths)
	permissions.PropagatingPaths = cloneT3PermissionMap(permissions.PropagatingPaths)
	permissions.Paths[api.control.missingPermissionPath] = removeT3String(permissions.Paths[api.control.missingPermissionPath], api.control.missingPermission)
	permissions.PropagatingPaths[api.control.missingPermissionPath] = removeT3String(permissions.PropagatingPaths[api.control.missingPermissionPath], api.control.missingPermission)
	return permissions, nil
}

func (api *t3FaultAPI) VMConfig(ctx context.Context, node string, vmid uint32) (VMConfig, error) {
	if api.control.foreignDestination && vmid == api.control.destinationVMID {
		digest := strings.Repeat("f", 40)
		return VMConfig{Digest: digest, Description: "foreign", Fields: map[string]string{"digest": digest, "description": "foreign"}}, nil
	}
	config, err := api.MutationAPI.VMConfig(ctx, node, vmid)
	if err != nil {
		return config, err
	}
	config = cloneVMConfig(config)
	if api.control.templateDrift && vmid == api.control.templateVMID {
		config.Fields["hostpci0"] = "0000:01:00.0"
	}
	if api.control.ownershipMismatchRead && vmid == api.control.destinationVMID {
		config.Description = "foreign"
		config.Fields["description"] = "foreign"
	}
	return config, nil
}

func (api *t3FaultAPI) CloneVM(ctx context.Context, request CloneRequest) (UPID, error) {
	api.control.mu.Lock()
	api.control.cloneCalls++
	api.control.mu.Unlock()
	if api.control.syntheticCloneFailure {
		upid := mustT3SyntheticUPID(request.SourceNode, "qmclone", request.SourceVMID)
		api.control.mu.Lock()
		api.control.syntheticUPID = upid.Raw
		api.control.mu.Unlock()
		return upid, nil
	}
	upid, err := api.MutationAPI.CloneVM(ctx, request)
	if err != nil || !api.control.loseCloneResponse {
		return upid, err
	}
	if err := waitT3VMOwnershipPresent(ctx, api.MutationAPI, request.TargetNode, request.NewVMID, request.Description); err != nil {
		return UPID{}, err
	}
	return UPID{}, errors.New("injected clone response loss after PVE accepted the request")
}

func (api *t3FaultAPI) ConfigureVM(ctx context.Context, request ConfigureRequest) (UPID, error) {
	api.control.mu.Lock()
	api.control.configureCalls++
	api.control.mu.Unlock()
	if api.control.rejectConfiguration {
		request.Digest = strings.Repeat("0", 40)
	}
	upid, err := api.MutationAPI.ConfigureVM(ctx, request)
	if err != nil || !api.control.loseConfigureResponse {
		return upid, err
	}
	return UPID{}, errors.New("injected configuration response loss after PVE accepted the request")
}

func (api *t3FaultAPI) LifecycleVM(ctx context.Context, request LifecycleRequest) (UPID, error) {
	api.control.mu.Lock()
	if api.control.lifecycleCalls == nil {
		api.control.lifecycleCalls = make(map[string]int)
	}
	api.control.lifecycleCalls[request.Action]++
	api.control.mu.Unlock()
	upid, err := api.MutationAPI.LifecycleVM(ctx, request)
	if request.Action != "start" || !api.control.loseStartResponse {
		return upid, err
	}
	if err == nil {
		if err := waitT3RealTask(ctx, api.MutationAPI, request.Node, upid); err != nil {
			return UPID{}, err
		}
	} else if waitErr := waitT3VMStatus(ctx, api.MutationAPI, request.Node, request.VMID, "running"); waitErr != nil {
		return UPID{}, waitErr
	}
	return UPID{}, errors.New("injected start response loss after PVE completed the request")
}

func (api *t3FaultAPI) TaskStatus(ctx context.Context, node string, upid UPID) (TaskStatus, error) {
	api.control.mu.Lock()
	api.control.taskReads++
	synthetic := upid.Raw == api.control.syntheticUPID && api.control.syntheticUPID != ""
	successful := api.control.successfulUPIDs[upid.Raw]
	api.control.mu.Unlock()
	if synthetic {
		return TaskStatus{Status: "stopped", ExitStatus: "ERROR " + t3RawDiagnosticCanary}, nil
	}
	if successful {
		return TaskStatus{Status: "stopped", ExitStatus: "OK"}, nil
	}
	return api.MutationAPI.TaskStatus(ctx, node, upid)
}

func (api *t3FaultAPI) DeleteVM(ctx context.Context, node string, vmid uint32) (UPID, error) {
	api.control.mu.Lock()
	api.control.deleteCalls++
	api.control.mu.Unlock()
	if api.control.rejectCompensation {
		return UPID{}, errors.New("injected compensation delete rejection before PVE request")
	}
	upid, err := api.MutationAPI.DeleteVM(ctx, node, vmid)
	if err != nil || !api.control.loseDestroyResponse {
		return upid, err
	}
	if err := waitT3RealTask(ctx, api.MutationAPI, node, upid); err != nil {
		return UPID{}, err
	}
	if err := waitT3VMAbsent(ctx, api.MutationAPI, node, vmid); err != nil {
		return UPID{}, err
	}
	return UPID{}, errors.New("injected destroy response loss after PVE confirmed absence")
}

func cloneT3PermissionMap(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for path, privileges := range source {
		result[path] = append([]string(nil), privileges...)
	}
	return result
}

func removeT3String(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

func mustT3SyntheticUPID(node, taskType string, vmid uint32) UPID {
	upid, err := ParseUPID(fmt.Sprintf("UPID:%s:00000001:00000002:00000003:%s:%d:root@pam:", node, taskType, vmid))
	if err != nil {
		panic(err)
	}
	return upid
}

func waitT3RealTask(ctx context.Context, client MutationAPI, node string, upid UPID) error {
	for {
		status, err := client.TaskStatus(ctx, node, upid)
		if err == nil && status.Status == "stopped" {
			if status.ExitStatus != "OK" {
				return fmt.Errorf("PVE task failed")
			}
			return nil
		}
		if err := t3Wait(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitT3VMOwnershipPresent(ctx context.Context, client MutationAPI, node string, vmid uint32, ownership string) error {
	stableReads := 0
	for {
		if config, err := client.VMConfig(ctx, node, vmid); err == nil && config.Description == ownership && config.Fields["lock"] == "" {
			stableReads++
			if stableReads == 2 {
				return nil
			}
		} else {
			stableReads = 0
		}
		if err := t3Wait(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitT3VMStatus(ctx context.Context, client MutationAPI, node string, vmid uint32, expected string) error {
	for {
		if status, err := client.VMStatus(ctx, node, vmid); err == nil && status.Status == expected {
			return nil
		}
		if err := t3Wait(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitT3GuestReady(ctx context.Context, client MutationAPI, node string, vmid uint32) error {
	for {
		if status, err := client.VMStatus(ctx, node, vmid); err == nil && status.Status == "running" && client.GuestAgentPing(ctx, node, vmid) == nil {
			return nil
		}
		if err := t3Wait(ctx, time.Second); err != nil {
			return err
		}
	}
}

func waitT3VMAbsent(ctx context.Context, client MutationAPI, node string, vmid uint32) error {
	for {
		_, exists, err := readOptionalVMConfig(ctx, client, node, vmid)
		if err == nil && !exists {
			return nil
		}
		if err := t3Wait(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}

func t3Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func t3FailureProfile(base t3SmokeProfile, suffix string) t3SmokeProfile {
	result := base
	result.RunID = base.RunID + "." + suffix
	result.InstanceID = base.InstanceID + "." + suffix
	result.NetworkResourceID = base.NetworkResourceID + "." + suffix
	result.VMResourceID = base.VMResourceID + "." + suffix
	return result
}

func newT3FailureFixture(t *testing.T, ctx context.Context, base t3SmokeProfile, suffix string, config TargetConfig, target *pluginv1.TargetContext, configBytes, credentialBytes []byte) *t3FailureFixture {
	t.Helper()
	profile := t3FailureProfile(base, suffix)
	if err := validateT3Profile(profile, config, target.GetTargetId()); err != nil {
		t.Fatal(err)
	}
	service := newT3FaultService(nil)
	labJSON, networkJSON, vmJSON, overrideJSON, err := t3Documents(profile)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := service.ValidateLab(ctx, &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: labJSON, ProviderOverrideJson: overrideJSON})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatalf("failure fixture validation: %v %#v", err, validation.GetErrors())
	}
	networkPlanningInput := &pluginv1.ResourceMutationInput{Context: t3Call(profile, "plan-network", profile.NetworkResourceID), Target: target, CanonicalResourceJson: networkJSON, ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{randomT3Token(t)}}
	networkPlan, err := t3ReservedPlan(t, service, ctx, &pluginv1.PlanResourceRequest{Input: networkPlanningInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
	if err != nil || networkPlan.GetError() != nil || !claimSetEquals(networkPlan.GetProposedAllocations(), vlanClaimNamespace, profile.ExpectedVLAN) {
		t.Fatalf("failure fixture network plan: %v %#v", err, networkPlan)
	}
	networkInput := proto.Clone(networkPlanningInput).(*pluginv1.ResourceMutationInput)
	networkInput.Context = t3Call(profile, "apply-network", profile.NetworkResourceID)
	networkInput.ProviderPlan = networkPlan.GetProviderPlan()
	networkInput.CommittedAllocations = cloneClaims(networkPlan.GetProposedAllocations())
	networkStream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: networkInput}, networkStream); err != nil {
		t.Fatal(err)
	}
	networkResult := finalApplyResult(networkStream.events)
	if networkResult == nil || networkResult.GetError() != nil {
		t.Fatalf("failure fixture network apply: %#v", networkResult)
	}
	networkState := networkResult.GetProviderState()
	vmPlanningInput := &pluginv1.ResourceMutationInput{
		Context: t3Call(profile, "plan-vm", profile.VMResourceID), Target: target, CanonicalResourceJson: vmJSON,
		ProviderPlan: validation.GetProviderPlan(), OwnershipTokens: []string{randomT3Token(t)},
		Dependencies: []*pluginv1.ResourceDependency{{LogicalResourceId: profile.NetworkResourceID, ProviderPlan: networkPlan.GetProviderPlan(), ProviderPlanDigest: networkPlan.GetProviderPlanDigest(), ProviderState: networkState, CommittedAllocations: cloneClaims(networkPlan.GetProposedAllocations())}},
	}
	vmPlanResponse, err := t3ReservedPlan(t, service, ctx, &pluginv1.PlanResourceRequest{Input: vmPlanningInput}, profile.ExpectedVMID, profile.ExpectedVLAN)
	if err != nil || vmPlanResponse.GetError() != nil || !claimSetEquals(vmPlanResponse.GetProposedAllocations(), vmidClaimNamespace, profile.ExpectedVMID) {
		t.Fatalf("failure fixture VM plan: %v %#v", err, vmPlanResponse)
	}
	vmInput := proto.Clone(vmPlanningInput).(*pluginv1.ResourceMutationInput)
	vmInput.Context = t3Call(profile, "apply-vm", profile.VMResourceID)
	vmInput.ProviderPlan = vmPlanResponse.GetProviderPlan()
	vmInput.CommittedAllocations = cloneClaims(vmPlanResponse.GetProposedAllocations())
	var vmPlan QEMUPlan
	if err := decodeEnvelope(vmInput.GetProviderPlan(), maximumConfigBytes, &vmPlan); err != nil {
		t.Fatal(err)
	}
	fixture := &t3FailureFixture{profile: profile, config: config, target: target, configBytes: configBytes, credentialBytes: credentialBytes, normalService: service, networkPlanningInput: networkPlanningInput, networkPlan: networkPlan, networkInput: networkInput, networkState: networkState, vmInput: vmInput, vmPlan: vmPlan}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

func (fixture *t3FailureFixture) applyVM(t *testing.T, ctx context.Context, service *Service) *pluginv1.ResourceResult {
	t.Helper()
	stream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: fixture.vmInput}, stream); err != nil {
		t.Fatal(err)
	}
	result := finalApplyResult(stream.events)
	if result == nil {
		t.Fatal("failure fixture apply returned no final result")
	}
	if result.GetProviderState() != nil {
		fixture.vmState = result.GetProviderState()
	}
	return result
}

func (fixture *t3FailureFixture) destroyVM(t *testing.T, ctx context.Context, service *Service, suffix string) *pluginv1.ResourceResult {
	t.Helper()
	input := proto.Clone(fixture.vmInput).(*pluginv1.ResourceMutationInput)
	input.Context = t3Call(fixture.profile, suffix, fixture.profile.VMResourceID)
	input.PriorState = fixture.vmState
	stream := &destroyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: input}, stream); err != nil {
		t.Fatal(err)
	}
	result := finalDestroyResult(stream.events)
	if result == nil {
		t.Fatal("failure fixture destroy returned no final result")
	}
	return result
}

func (fixture *t3FailureFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := NewClientFromEnvelopes(fixture.configBytes, fixture.credentialBytes)
	if err != nil {
		t.Errorf("failure fixture cleanup client: %v", err)
		return
	}
	config, exists, readErr := readOptionalVMConfig(ctx, client, fixture.config.DestinationNode, fixture.profile.ExpectedVMID)
	if readErr == nil && exists && config.Fields["lock"] == "clone" {
		config, readErr = fixture.normalService.waitCloneUnlocked(ctx, client, fixture.config.DestinationNode, fixture.profile.ExpectedVMID)
	}
	client.CloseIdleConnections()
	if readErr != nil {
		t.Errorf("failure fixture cleanup read: %v", readErr)
		return
	}
	if exists {
		if fixture.vmPlan.Ownership == nil || verifyOwnership(config.Description, *fixture.vmPlan.Ownership) != nil {
			t.Errorf("failure fixture cleanup refused non-owned VMID %d", fixture.profile.ExpectedVMID)
			return
		}
		for attempt := 0; attempt < 3; attempt++ {
			state := qemuVMState(fixture.vmPlan, config, "ready", "")
			observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, ConfigDigest: config.Digest, Networks: append([]NetworkBinding(nil), state.Networks...)}
			fixture.vmState = successResult(state, observed, fixture.vmInput.GetOwnershipTokens()).GetProviderState()
			result := fixture.destroyVM(t, ctx, fixture.normalService, fmt.Sprintf("cleanup-vm-%d", attempt))
			if result.GetError() == nil {
				fixture.vmDestroyed = true
				break
			}
			if err := t3Wait(ctx, 2*time.Second); err != nil {
				break
			}
			client, err = NewClientFromEnvelopes(fixture.configBytes, fixture.credentialBytes)
			if err != nil {
				break
			}
			config, exists, readErr = readOptionalVMConfig(ctx, client, fixture.config.DestinationNode, fixture.profile.ExpectedVMID)
			client.CloseIdleConnections()
			if readErr != nil || !exists {
				fixture.vmDestroyed = readErr == nil
				break
			}
			if verifyOwnership(config.Description, *fixture.vmPlan.Ownership) != nil {
				t.Errorf("failure fixture cleanup ownership changed for VMID %d", fixture.profile.ExpectedVMID)
				return
			}
		}
		if !fixture.vmDestroyed {
			t.Errorf("failure fixture owned VM cleanup did not confirm removal")
			return
		}
	}
	assertT3VMAbsent(t, ctx, fixture.config, fixture.configBytes, fixture.credentialBytes, fixture.profile.ExpectedVMID)
	if !fixture.networkDestroyed {
		if err := destroyT3Network(ctx, fixture.normalService, fixture.profile, fixture.target, fixture.networkInput, fixture.networkState); err != nil {
			t.Errorf("failure fixture network cleanup: %v", err)
		} else {
			fixture.networkDestroyed = true
		}
	}
}

func assertT3VMAbsent(t *testing.T, ctx context.Context, config TargetConfig, configBytes, credentialBytes []byte, vmid uint32) {
	t.Helper()
	client, err := NewClientFromEnvelopes(configBytes, credentialBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, exists, err := readOptionalVMConfig(ctx, client, config.DestinationNode, vmid)
	if err != nil || exists {
		t.Fatalf("provider absence was not confirmed for VMID %d: exists=%t err=%v", vmid, exists, err)
	}
}

func runT3RestartCheckpoints(t *testing.T, ctx context.Context, base t3SmokeProfile, config TargetConfig, target *pluginv1.TargetContext, configBytes, credentialBytes []byte) {
	t.Helper()
	fixture := newT3FailureFixture(t, ctx, base, "p011", config, target, configBytes, credentialBytes)
	client, err := NewClientFromEnvelopes(configBytes, credentialBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	marker, err := encodeOwnershipMarker(*fixture.vmPlan.Ownership)
	if err != nil {
		t.Fatal(err)
	}
	cloneUPID, err := client.CloneVM(ctx, CloneRequest{SourceNode: fixture.vmPlan.Template.SourceNode, SourceVMID: fixture.vmPlan.Template.VMID, NewVMID: fixture.profile.ExpectedVMID, Name: safeVMName(fixture.vmPlan.InstanceID, fixture.vmPlan.LogicalResourceID), TargetNode: fixture.vmPlan.Destination.Node, StorageID: fixture.vmPlan.Destination.StorageID, Format: fixture.vmPlan.Destination.StorageFormat, Pool: fixture.vmPlan.Destination.Pool, Description: marker})
	if err != nil || waitT3RealTask(ctx, client, fixture.vmPlan.Template.SourceNode, cloneUPID) != nil {
		t.Fatalf("materialize restart clone: %v", err)
	}
	cloned, err := fixture.normalService.waitCloneUnlocked(ctx, client, fixture.vmPlan.Destination.Node, fixture.profile.ExpectedVMID)
	if err != nil {
		t.Fatal(err)
	}
	configUPID, err := client.ConfigureVM(ctx, ConfigureRequest{Node: fixture.vmPlan.Destination.Node, VMID: fixture.profile.ExpectedVMID, Digest: cloned.Digest, Sockets: fixture.vmPlan.Hardware.Sockets, Cores: fixture.vmPlan.Hardware.Cores, CPULimit: fixture.vmPlan.Hardware.CPULimit, MemoryMiB: fixture.vmPlan.Hardware.MemoryMiB, Description: marker, Tags: ownershipTag(*fixture.vmPlan.Ownership), Networks: fixture.vmPlan.Networks})
	if err != nil || waitT3RealTask(ctx, client, fixture.vmPlan.Destination.Node, configUPID) != nil {
		t.Fatalf("materialize restart configuration: %v", err)
	}
	configured, err := fixture.normalService.waitManagedConfiguration(ctx, client, fixture.vmPlan)
	if err != nil {
		t.Fatal(err)
	}
	startUPID, startErr := client.LifecycleVM(ctx, LifecycleRequest{Node: fixture.vmPlan.Destination.Node, VMID: fixture.profile.ExpectedVMID, Action: "start"})
	if startErr == nil {
		if err := waitT3RealTask(ctx, client, fixture.vmPlan.Destination.Node, startUPID); err != nil {
			t.Fatalf("materialize restart lifecycle: %v", err)
		}
	} else {
		if err := waitT3VMStatus(ctx, client, fixture.vmPlan.Destination.Node, fixture.profile.ExpectedVMID, "running"); err != nil {
			t.Fatalf("materialize restart lifecycle after response loss: %v", err)
		}
		startUPID = mustT3SyntheticUPID(fixture.vmPlan.Destination.Node, "qmstart", fixture.profile.ExpectedVMID)
	}
	if err := waitT3GuestReady(ctx, client, fixture.vmPlan.Destination.Node, fixture.profile.ExpectedVMID); err != nil {
		t.Fatalf("materialize restart guest readiness: %v", err)
	}

	control := &t3FaultControl{templateVMID: config.Templates[0].VMID, destinationVMID: fixture.profile.ExpectedVMID}
	if startErr != nil {
		control.successfulUPIDs = map[string]bool{startUPID.Raw: true}
	}
	var recoveredState *pluginv1.ProviderState
	for _, phase := range []string{"prepared", "clone-submitted", "clone-complete", "config-submitted", "configured", "lifecycle-submitted"} {
		input := proto.Clone(fixture.vmInput).(*pluginv1.ResourceMutationInput)
		input.Context = t3Call(fixture.profile, "restart-"+phase, fixture.profile.VMResourceID)
		preparing := newT3FaultService(control)
		prepared, failure := preparing.prepareExecution(ctx, input, "apply")
		if failure != nil {
			t.Fatalf("prepare checkpoint %s: %#v", phase, failure.GetError())
		}
		prepared.client.CloseIdleConnections()
		prepared.record.Phase = phase
		prepared.record.LastSafeObservation = "credentialed restart checkpoint"
		prepared.record.TaskUPID = ""
		switch phase {
		case "clone-submitted":
			prepared.record.TaskUPID = cloneUPID.Raw
		case "config-submitted":
			prepared.record.TaskUPID = configUPID.Raw
		case "lifecycle-submitted":
			prepared.record.TaskUPID = startUPID.Raw
		}
		if err := prepared.journal.Write(prepared.record); err != nil {
			t.Fatal(err)
		}
		if err := prepared.journal.Close(); err != nil {
			t.Fatal(err)
		}
		before := control.counts()
		stream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
		if err := newT3FaultService(control).ApplyResource(&pluginv1.ApplyResourceRequest{Input: input}, stream); err != nil {
			t.Fatal(err)
		}
		result := finalApplyResult(stream.events)
		after := control.counts()
		if result == nil || result.GetError() != nil || before.clone != after.clone || before.configure != after.configure || before.lifecycle["start"] != after.lifecycle["start"] {
			t.Fatalf("checkpoint %s recovery result=%#v before=%#v after=%#v", phase, result, before, after)
		}
		recoveredState = result.GetProviderState()
	}
	fixture.vmState = recoveredState

	stopUPID, err := client.LifecycleVM(ctx, LifecycleRequest{Node: fixture.vmPlan.Destination.Node, VMID: fixture.profile.ExpectedVMID, Action: "stop"})
	if err != nil || waitT3RealTask(ctx, client, fixture.vmPlan.Destination.Node, stopUPID) != nil {
		t.Fatalf("prepare restart destroy stop: %v", err)
	}
	destroyInput := proto.Clone(fixture.vmInput).(*pluginv1.ResourceMutationInput)
	destroyInput.Context = t3Call(fixture.profile, "restart-destroy-submitted", fixture.profile.VMResourceID)
	destroyInput.PriorState = recoveredState
	preparing := newT3FaultService(control)
	prepared, failure := preparing.prepareExecution(ctx, destroyInput, "destroy")
	if failure != nil {
		t.Fatalf("prepare destroy checkpoint: %#v", failure.GetError())
	}
	prepared.client.CloseIdleConnections()
	destroyUPID, err := client.DeleteVM(ctx, fixture.vmPlan.Destination.Node, fixture.profile.ExpectedVMID)
	if err != nil {
		t.Fatal(err)
	}
	prepared.record.Phase = "destroy-submitted"
	prepared.record.LastSafeObservation = "credentialed destroy checkpoint"
	prepared.record.TaskUPID = destroyUPID.Raw
	if err := prepared.journal.Write(prepared.record); err != nil {
		t.Fatal(err)
	}
	if err := prepared.journal.Close(); err != nil {
		t.Fatal(err)
	}
	beforeDestroy := control.counts()
	destroyStream := &destroyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := newT3FaultService(control).DestroyResource(&pluginv1.DestroyResourceRequest{Input: destroyInput}, destroyStream); err != nil {
		t.Fatal(err)
	}
	destroyResult := finalDestroyResult(destroyStream.events)
	if destroyResult == nil || destroyResult.GetError() != nil || control.counts().delete != beforeDestroy.delete {
		t.Fatalf("destroy checkpoint recovery=%#v before=%#v after=%#v", destroyResult, beforeDestroy, control.counts())
	}
	fixture.vmDestroyed = true
	assertT3VMAbsent(t, ctx, config, configBytes, credentialBytes, fixture.profile.ExpectedVMID)

	corruptInput := proto.Clone(fixture.vmInput).(*pluginv1.ResourceMutationInput)
	corruptInput.Context = t3Call(fixture.profile, "restart-corrupt-version", fixture.profile.VMResourceID)
	corruptPrepared, failure := newT3FaultService(control).prepareExecution(ctx, corruptInput, "apply")
	if failure != nil {
		t.Fatalf("prepare corrupt checkpoint: %#v", failure.GetError())
	}
	corruptPrepared.client.CloseIdleConnections()
	corruptPrepared.record.SchemaVersion = "labcentral.proxmox-invocation-journal/v999"
	recordBytes, err := json.Marshal(corruptPrepared.record)
	if err != nil {
		t.Fatal(err)
	}
	envelopeBytes, err := json.Marshal(journalEnvelope{Record: corruptPrepared.record, Checksum: digestDocument(recordBytes)})
	if err != nil {
		t.Fatal(err)
	}
	corruptPath := corruptPrepared.journal.recordPath(corruptPrepared.record.InvocationID)
	if err := os.WriteFile(corruptPath, envelopeBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := corruptPrepared.journal.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(corruptPath) })
	beforeCorrupt := control.counts()
	corruptStream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := newT3FaultService(control).ApplyResource(&pluginv1.ApplyResourceRequest{Input: corruptInput}, corruptStream); err != nil {
		t.Fatal(err)
	}
	corruptResult := finalApplyResult(corruptStream.events)
	if corruptResult == nil || corruptResult.GetError().GetCode() != "journal_corrupt" || control.counts().clone != beforeCorrupt.clone {
		t.Fatalf("corrupt journal result=%#v before=%#v after=%#v", corruptResult, beforeCorrupt, control.counts())
	}
	_ = configured
}
