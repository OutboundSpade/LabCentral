package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maximumTaskPolls      = 12
	maximumReadinessPolls = 8
)

var diskFieldPattern = regexp.MustCompile(`^(scsi|virtio|sata|ide)[0-9]{1,2}$`)
var networkFieldPattern = regexp.MustCompile(`^(net|ipconfig)[0-9]{1,2}$`)

func (service *Service) ApplyResource(request *pluginv1.ApplyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResourceResponse]) error {
	if stream == nil {
		return fmt.Errorf("Proxmox apply stream is required")
	}
	service.executionMu.Lock()
	defer service.executionMu.Unlock()
	input := request.GetInput()
	prepared, failure := service.prepareExecution(input, "apply")
	if failure != nil {
		return stream.Send(&pluginv1.ApplyResourceResponse{Event: resultInvocationEvent(failure)})
	}
	defer prepared.client.CloseIdleConnections()
	if cached := prepared.cachedResult(); cached != nil {
		return stream.Send(&pluginv1.ApplyResourceResponse{Event: resultInvocationEvent(cached)})
	}
	if err := stream.Send(&pluginv1.ApplyResourceResponse{Event: progressInvocationEvent(service.now(), "preflight", "Checking Proxmox plan and ownership", "")}); err != nil {
		return err
	}
	var result *pluginv1.ResourceResult
	if prepared.plan.ResourceKind == "Network" {
		result = prepared.applyNetwork(stream.Context())
	} else {
		result = service.applyVM(stream.Context(), prepared)
	}
	return stream.Send(&pluginv1.ApplyResourceResponse{Event: resultInvocationEvent(result)})
}

func (service *Service) ReadResource(ctx context.Context, request *pluginv1.ReadResourceRequest) (*pluginv1.ReadResourceResponse, error) {
	input := request.GetInput()
	resourceID := ""
	if input != nil && input.GetContext() != nil {
		resourceID = input.GetContext().GetLogicalResourceId()
	}
	if input == nil || input.GetTarget() == nil || input.GetPriorState() == nil {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("invalid_read_input", "invalid_declaration", "Proxmox read input is incomplete", false, resourceID)}, nil
	}
	config, err := mutationConfig(input.GetTarget())
	state, stateErr := decodeQEMUState(input.GetPriorState())
	call := input.GetContext()
	if err != nil || stateErr != nil || call == nil || !stateMatchesTargetConfig(config, state) || state.TargetID != input.GetTarget().GetTargetId() || state.LogicalResourceID != resourceID || call.GetProviderTargetId() != state.TargetID || call.GetDesiredGeneration() != state.Generation || state.Ownership != nil && (call.GetLabInstanceId() != state.Ownership.InstanceID || len(input.GetPriorState().GetOwnershipIds()) != 1 || input.GetPriorState().GetOwnershipIds()[0] != state.Ownership.Token) {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("invalid_provider_state", "state_version_incompatible", "Proxmox provider state is invalid or mismatched", false, resourceID)}, nil
	}
	if state.ResourceKind == "Network" {
		observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "Network", Node: state.Node, Networks: append([]NetworkBinding(nil), state.Networks...)}
		return &pluginv1.ReadResourceResponse{Result: successResult(&state, observed, input.GetPriorState().GetOwnershipIds())}, nil
	}
	created, err := service.factory.New(input.GetTarget().GetConfigEnvelope(), input.GetTarget().GetCredentialEnvelope())
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("invalid_target_credentials", "provider_unavailable", "Proxmox target credentials are invalid", false, resourceID)}, nil
	}
	client, ok := created.(MutationAPI)
	if !ok {
		created.CloseIdleConnections()
		return &pluginv1.ReadResourceResponse{Result: executionFailure("mutation_client_unavailable", "provider_unavailable", "Proxmox mutation client is unavailable", false, resourceID)}, nil
	}
	defer client.CloseIdleConnections()
	current, exists, err := readOptionalVMConfig(ctx, client, state.Node, state.VMID)
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("provider_read_failed", "provider_unavailable", "Proxmox VM could not be read", true, resourceID)}, nil
	}
	if !exists {
		observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, Destroyed: true}
		return &pluginv1.ReadResourceResponse{Result: successResult(&state, observed, input.GetPriorState().GetOwnershipIds())}, nil
	}
	if state.Ownership == nil || verifyOwnership(current.Description, *state.Ownership) != nil {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("ownership_mismatch", "ownership_mismatch", "Proxmox VM ownership marker does not match recorded state", false, resourceID)}, nil
	}
	status, err := client.VMStatus(ctx, state.Node, state.VMID)
	if err != nil {
		return &pluginv1.ReadResourceResponse{Result: executionFailure("provider_read_failed", "provider_unavailable", "Proxmox VM status could not be read", true, resourceID)}, nil
	}
	state.ConfigDigest = current.Digest
	state.Phase = status.Status
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, PowerState: status.Status, ConfigDigest: current.Digest, Networks: append([]NetworkBinding(nil), state.Networks...)}
	return &pluginv1.ReadResourceResponse{Result: successResult(&state, observed, input.GetPriorState().GetOwnershipIds())}, nil
}

func (service *Service) DestroyResource(request *pluginv1.DestroyResourceRequest, stream grpc.ServerStreamingServer[pluginv1.DestroyResourceResponse]) error {
	if stream == nil {
		return fmt.Errorf("Proxmox destroy stream is required")
	}
	service.executionMu.Lock()
	defer service.executionMu.Unlock()
	input := request.GetInput()
	prepared, failure := service.prepareExecution(input, "destroy")
	if failure != nil {
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultInvocationEvent(failure)})
	}
	defer prepared.client.CloseIdleConnections()
	if cached := prepared.cachedResult(); cached != nil {
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultInvocationEvent(cached)})
	}
	if err := stream.Send(&pluginv1.DestroyResourceResponse{Event: progressInvocationEvent(service.now(), "preflight", "Checking Proxmox ownership before destroy", "")}); err != nil {
		return err
	}
	state, err := decodeQEMUState(input.GetPriorState())
	if err != nil || !stateMatchesPlan(prepared.config, state, prepared.plan, input.GetPriorState()) {
		result := prepared.fail("failed", "invalid_provider_state", "state_version_incompatible", "Destroy requires matching Proxmox provider state", false, false, false, nil)
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultInvocationEvent(result)})
	}
	if state.ResourceKind == "Network" {
		observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "Network", Node: state.Node, Destroyed: true}
		result := successResult(nil, observed, input.GetOwnershipTokens())
		if prepared.finish("destroyed", nil, observed, nil, false, true) != nil {
			result = executionFailure("journal_unavailable", "unknown_outcome", "Proxmox network destroy result could not be persisted", true, state.LogicalResourceID)
		}
		return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultInvocationEvent(result)})
	}
	result := service.destroyVM(stream.Context(), prepared, state)
	return stream.Send(&pluginv1.DestroyResourceResponse{Event: resultInvocationEvent(result)})
}

func (service *Service) destroyVM(ctx context.Context, prepared *preparedExecution, state QEMUProviderState) *pluginv1.ResourceResult {
	if prepared.existing != nil && prepared.existing.Phase == "destroy-submitted" {
		upid, parseErr := ParseUPID(prepared.existing.TaskUPID)
		if parseErr != nil || upid.validateObject(state.Node, state.VMID, "qmdestroy") != nil {
			return prepared.fail("partial", "invalid_task_identity", "provider_protocol_error", "Persisted Proxmox destroy task identity is invalid", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
		}
		if service.waitTask(ctx, prepared.client, state.Node, upid) != nil {
			return prepared.fail("partial", "provider_task_failed", "provider_failure", "Persisted Proxmox destroy task did not complete successfully", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
		}
		_, exists, err := readOptionalVMConfig(ctx, prepared.client, state.Node, state.VMID)
		if err != nil || exists {
			return prepared.fail("partial", "destroy_not_confirmed", "unknown_outcome", "Proxmox VM absence could not be confirmed", true, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
		}
		observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, Destroyed: true}
		if prepared.finish("destroyed", nil, observed, nil, false, true) != nil {
			return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, state.LogicalResourceID)
		}
		return successResult(nil, observed, prepared.input.GetOwnershipTokens())
	}
	current, exists, err := readOptionalVMConfig(ctx, prepared.client, state.Node, state.VMID)
	if err != nil {
		return prepared.fail("partial", "provider_read_failed", "provider_unavailable", "Proxmox VM could not be checked before destroy", true, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	if !exists {
		observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, Destroyed: true}
		if prepared.finish("destroyed", nil, observed, nil, false, true) != nil {
			return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, state.LogicalResourceID)
		}
		return successResult(nil, observed, prepared.input.GetOwnershipTokens())
	}
	if state.Ownership == nil || verifyOwnership(current.Description, *state.Ownership) != nil || prepared.plan.Ownership == nil || *state.Ownership != *prepared.plan.Ownership {
		return prepared.fail("partial", "ownership_mismatch", "ownership_mismatch", "Proxmox VM ownership marker does not match recorded state", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	status, err := prepared.client.VMStatus(ctx, state.Node, state.VMID)
	if err != nil {
		return prepared.fail("partial", "provider_read_failed", "provider_unavailable", "Proxmox VM status could not be read before destroy", true, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	if status.Status == "running" {
		upid, stopErr := prepared.client.LifecycleVM(ctx, LifecycleRequest{Node: state.Node, VMID: state.VMID, Action: "stop"})
		if stopErr != nil || upid.validateObject(state.Node, state.VMID, "qmshutdown") != nil || service.waitTask(ctx, prepared.client, state.Node, upid) != nil {
			return prepared.fail("partial", "provider_stop_failed", "provider_failure", "Proxmox VM did not stop cleanly before destroy", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
		}
	}
	upid, err := prepared.client.DeleteVM(ctx, state.Node, state.VMID)
	if err != nil {
		_, exists, readErr := readOptionalVMConfig(ctx, prepared.client, state.Node, state.VMID)
		if readErr == nil && !exists {
			observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, Destroyed: true}
			if prepared.finish("destroyed", nil, observed, nil, false, true) != nil {
				return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, state.LogicalResourceID)
			}
			return successResult(nil, observed, prepared.input.GetOwnershipTokens())
		}
		return prepared.fail("partial", "unknown_destroy_outcome", "unknown_outcome", "Proxmox destroy outcome is unknown", true, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	if upid.validateObject(state.Node, state.VMID, "qmdestroy") != nil {
		return prepared.fail("partial", "invalid_task_identity", "provider_protocol_error", "Proxmox destroy returned an invalid task identity", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	prepared.record.TaskUPID = upid.Raw
	if !prepared.checkpoint("destroy-submitted", "destroy task accepted") {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation journal could not checkpoint destroy", true, state.LogicalResourceID)
	}
	if service.waitTask(ctx, prepared.client, state.Node, upid) != nil {
		return prepared.fail("partial", "provider_task_failed", "provider_failure", "Proxmox destroy task did not complete successfully", false, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	_, exists, err = readOptionalVMConfig(ctx, prepared.client, state.Node, state.VMID)
	if err != nil || exists {
		return prepared.fail("partial", "destroy_not_confirmed", "unknown_outcome", "Proxmox VM absence could not be confirmed", true, false, false, []string{fmt.Sprintf("qemu/%d", state.VMID)})
	}
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, Destroyed: true}
	if prepared.finish("destroyed", nil, observed, nil, false, true) != nil {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, state.LogicalResourceID)
	}
	return successResult(nil, observed, prepared.input.GetOwnershipTokens())
}

func (service *Service) Lifecycle(request *pluginv1.LifecycleRequest, stream grpc.ServerStreamingServer[pluginv1.LifecycleResponse]) error {
	if stream == nil {
		return fmt.Errorf("Proxmox lifecycle stream is required")
	}
	service.executionMu.Lock()
	defer service.executionMu.Unlock()
	result := service.lifecycle(stream.Context(), request)
	return stream.Send(&pluginv1.LifecycleResponse{Event: resultInvocationEvent(result)})
}

func (service *Service) lifecycle(ctx context.Context, request *pluginv1.LifecycleRequest) *pluginv1.ResourceResult {
	resourceID := ""
	if request != nil && request.GetContext() != nil {
		resourceID = request.GetContext().GetLogicalResourceId()
	}
	if !service.allowT0Mutation {
		return executionFailure("mutation_gate_closed", "policy_denied", "Proxmox lifecycle execution is disabled pending exact-target qualification", false, resourceID)
	}
	if request == nil || request.GetContext() == nil || request.GetTarget() == nil || request.GetPriorState() == nil || !emptyActionParameters(request.GetActionParametersJson()) {
		return executionFailure("invalid_lifecycle_input", "invalid_declaration", "Proxmox lifecycle input is invalid", false, resourceID)
	}
	action, desired, allowedType := lifecycleNames(request.GetAction())
	if action == "" {
		return executionFailure("unsupported_lifecycle_action", "unsupported_capability", "Proxmox lifecycle action is unsupported", false, resourceID)
	}
	config, err := mutationConfig(request.GetTarget())
	state, stateErr := decodeQEMUState(request.GetPriorState())
	call := request.GetContext()
	if err != nil || stateErr != nil || state.ResourceKind != "VM" || !stateMatchesTargetConfig(config, state) || state.TargetID != request.GetTarget().GetTargetId() || state.LogicalResourceID != call.GetLogicalResourceId() || state.Ownership == nil || state.Ownership.InstanceID != call.GetLabInstanceId() || state.Generation != call.GetDesiredGeneration() || call.GetProviderTargetId() != state.TargetID || call.GetInvocationId() == "" || call.GetOperationId() == "" || call.GetIdempotencyKey() == "" || call.GetFencingToken() == 0 || len(request.GetPriorState().GetOwnershipIds()) != 1 || request.GetPriorState().GetOwnershipIds()[0] != state.Ownership.Token {
		return executionFailure("invalid_provider_state", "state_version_incompatible", "Proxmox lifecycle state or binding is invalid", false, resourceID)
	}
	journal, err := OpenInvocationJournal(config.JournalDirectory)
	if err != nil {
		return executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal is unavailable", true, resourceID)
	}
	marker, err := encodeOwnershipMarker(*state.Ownership)
	if err != nil {
		return executionFailure("ownership_mismatch", "ownership_mismatch", "Proxmox ownership marker is invalid", false, resourceID)
	}
	record := JournalRecord{SchemaVersion: journalV10, InvocationID: call.GetInvocationId(), TargetID: state.TargetID, Action: action, RequestDigest: digestDocument(append([]byte(action+"\x00"), request.GetPriorState().GetOpaque()...)), Node: state.Node, VMID: state.VMID, OwnershipDigest: digestDocument([]byte(marker)), Phase: "prepared"}
	if stored, readErr := journal.Read(record.InvocationID); readErr == nil {
		if stored.TargetID != record.TargetID || stored.Action != record.Action || stored.RequestDigest != record.RequestDigest || stored.OwnershipDigest != record.OwnershipDigest {
			return executionFailure("invocation_binding_mismatch", "conflict", "Invocation ID is already bound to a different request", false, resourceID)
		}
		if stored.Result != nil {
			return journalResultToProto(stored.Result, request.GetPriorState().GetOwnershipIds())
		}
		record = stored
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return executionFailure("journal_corrupt", "state_version_incompatible", "Proxmox invocation journal record is invalid", false, resourceID)
	} else if journal.Write(record) != nil {
		return executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal could not be persisted", true, resourceID)
	}
	created, err := service.factory.New(request.GetTarget().GetConfigEnvelope(), request.GetTarget().GetCredentialEnvelope())
	if err != nil {
		return executionFailure("invalid_target_credentials", "provider_unavailable", "Proxmox target credentials are invalid", false, resourceID)
	}
	client, ok := created.(MutationAPI)
	if !ok {
		created.CloseIdleConnections()
		return executionFailure("mutation_client_unavailable", "provider_unavailable", "Proxmox mutation client is unavailable", false, resourceID)
	}
	defer client.CloseIdleConnections()
	if record.Phase == "lifecycle-submitted" {
		upid, parseErr := ParseUPID(record.TaskUPID)
		if parseErr != nil || upid.validateObject(state.Node, state.VMID, allowedType) != nil {
			return lifecycleJournalFailure(journal, record, "invalid_task_identity", "provider_protocol_error", "Persisted Proxmox lifecycle task identity is invalid", false, resourceID)
		}
		if service.waitTask(ctx, client, state.Node, upid) != nil {
			return lifecycleJournalFailure(journal, record, "provider_task_failed", "provider_failure", "Persisted Proxmox lifecycle task did not complete successfully", false, resourceID)
		}
	}
	current, exists, err := readOptionalVMConfig(ctx, client, state.Node, state.VMID)
	if err != nil || !exists || verifyOwnership(current.Description, *state.Ownership) != nil {
		return lifecycleJournalFailure(journal, record, "ownership_mismatch", "ownership_mismatch", "Proxmox VM ownership could not be verified", false, resourceID)
	}
	status, err := client.VMStatus(ctx, state.Node, state.VMID)
	if err != nil {
		return lifecycleJournalFailure(journal, record, "provider_read_failed", "provider_unavailable", "Proxmox VM status could not be read", true, resourceID)
	}
	if status.Status != desired {
		if !validLifecycleSource(action, status.Status) {
			return lifecycleJournalFailure(journal, record, "invalid_lifecycle_transition", "conflict", "Proxmox VM is not in a state that permits this lifecycle action", false, resourceID)
		}
		upid, actionErr := client.LifecycleVM(ctx, LifecycleRequest{Node: state.Node, VMID: state.VMID, Action: action})
		if actionErr != nil {
			observed, readErr := client.VMStatus(ctx, state.Node, state.VMID)
			if readErr != nil || observed.Status != desired {
				return lifecycleJournalFailure(journal, record, "unknown_lifecycle_outcome", "unknown_outcome", "Proxmox lifecycle outcome is unknown", true, resourceID)
			}
		} else if upid.validateObject(state.Node, state.VMID, allowedType) != nil {
			return lifecycleJournalFailure(journal, record, "invalid_task_identity", "provider_protocol_error", "Proxmox lifecycle returned an invalid task identity", false, resourceID)
		} else {
			record.TaskUPID = upid.Raw
			record.Phase = "lifecycle-submitted"
			if journal.Write(record) != nil || service.waitTask(ctx, client, state.Node, upid) != nil {
				return lifecycleJournalFailure(journal, record, "provider_task_failed", "provider_failure", "Proxmox lifecycle task did not complete successfully", false, resourceID)
			}
		}
		status, err = client.VMStatus(ctx, state.Node, state.VMID)
		if err != nil || status.Status != desired {
			return lifecycleJournalFailure(journal, record, "lifecycle_not_confirmed", "unknown_outcome", "Proxmox lifecycle state could not be confirmed", true, resourceID)
		}
	}
	state.Phase = desired
	state.TaskUPID = record.TaskUPID
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, PowerState: desired, ConfigDigest: current.Digest, Networks: append([]NetworkBinding(nil), state.Networks...)}
	result := successResult(&state, observed, request.GetPriorState().GetOwnershipIds())
	record.Phase = "ready"
	record.Result = &JournalResult{State: &state, Observed: observed, CompensationComplete: true}
	if journal.Write(record) != nil {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox lifecycle result could not be persisted", true, resourceID)
	}
	return result
}

func validLifecycleSource(action, status string) bool {
	switch action {
	case "start":
		return status == "stopped"
	case "stop", "pause":
		return status == "running"
	case "resume":
		return status == "paused"
	default:
		return false
	}
}

func emptyActionParameters(contents []byte) bool {
	if len(contents) == 0 {
		return true
	}
	var parameters *struct{}
	return decodeEnvelope(contents, 1024, &parameters) == nil && parameters != nil
}

func lifecycleNames(action pluginv1.LifecycleAction) (string, string, string) {
	switch action {
	case pluginv1.LifecycleAction_LIFECYCLE_ACTION_START:
		return "start", "running", "qmstart"
	case pluginv1.LifecycleAction_LIFECYCLE_ACTION_STOP:
		return "stop", "stopped", "qmshutdown"
	case pluginv1.LifecycleAction_LIFECYCLE_ACTION_PAUSE:
		return "pause", "paused", "qmsuspend"
	case pluginv1.LifecycleAction_LIFECYCLE_ACTION_RESUME:
		return "resume", "running", "qmresume"
	default:
		return "", "", ""
	}
}

func lifecycleJournalFailure(journal *InvocationJournal, record JournalRecord, code, class, message string, retryable bool, resource string) *pluginv1.ResourceResult {
	failure := &JournalError{Code: code, Class: class, SafeMessage: message, Retryable: retryable}
	record.Phase = "failed"
	record.Result = &JournalResult{Observed: &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: record.Node, VMID: record.VMID}, Error: failure}
	if journal.Write(record) != nil {
		return journalPersistenceFailure(record.Result.Observed, false, false, resource)
	}
	return executionFailure(code, class, message, retryable, resource)
}

func (service *Service) GetInvocation(_ context.Context, request *pluginv1.GetInvocationRequest) (*pluginv1.GetInvocationResponse, error) {
	if request == nil || request.GetTarget() == nil || request.GetInvocationId() == "" {
		return &pluginv1.GetInvocationResponse{Result: executionFailure("invalid_invocation_request", "invalid_declaration", "Proxmox invocation request is invalid", false, "")}, nil
	}
	config, err := mutationConfig(request.GetTarget())
	if err != nil {
		return &pluginv1.GetInvocationResponse{Result: executionFailure("invalid_target_config", "invalid_declaration", "Proxmox mutation target configuration is invalid", false, "")}, nil
	}
	journal, err := OpenInvocationJournal(config.JournalDirectory)
	if err != nil {
		return &pluginv1.GetInvocationResponse{Result: executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal is unavailable", true, "")}, nil
	}
	record, err := journal.Read(request.GetInvocationId())
	if errors.Is(err, os.ErrNotExist) {
		return &pluginv1.GetInvocationResponse{Result: executionFailure("invocation_not_found", "not_found", "Proxmox invocation is not known", false, "")}, nil
	}
	if err != nil || record.TargetID != request.GetTarget().GetTargetId() {
		return &pluginv1.GetInvocationResponse{Result: executionFailure("journal_corrupt", "state_version_incompatible", "Proxmox invocation journal record is invalid", false, "")}, nil
	}
	ownership := []string{}
	if record.Result != nil && record.Result.State != nil && record.Result.State.Ownership != nil {
		ownership = []string{record.Result.State.Ownership.Token}
	}
	return &pluginv1.GetInvocationResponse{Result: journalResultToProto(record.Result, ownership)}, nil
}

type preparedExecution struct {
	service       *Service
	input         *pluginv1.ResourceMutationInput
	config        TargetConfig
	plan          QEMUPlan
	client        MutationAPI
	journal       *InvocationJournal
	record        JournalRecord
	existing      *JournalRecord
	ownershipText string
}

func (service *Service) prepareExecution(input *pluginv1.ResourceMutationInput, action string) (*preparedExecution, *pluginv1.ResourceResult) {
	resourceID := ""
	if input != nil && input.GetContext() != nil {
		resourceID = input.GetContext().GetLogicalResourceId()
	}
	if !service.allowT0Mutation {
		return nil, executionFailure("mutation_gate_closed", "policy_denied", "Proxmox mutation execution is disabled pending exact-target qualification", false, resourceID)
	}
	if input == nil || input.GetTarget() == nil || input.GetContext() == nil {
		return nil, executionFailure("invalid_mutation_input", "invalid_declaration", "Proxmox mutation input is incomplete", false, resourceID)
	}
	config, err := mutationConfig(input.GetTarget())
	if err != nil || !config.MutationEnabled {
		return nil, executionFailure("mutation_disabled", "policy_denied", "Proxmox mutation target configuration is invalid or disabled", false, resourceID)
	}
	var plan QEMUPlan
	if err := decodeEnvelope(input.GetProviderPlan(), maximumConfigBytes, &plan); err != nil || plan.validate() != nil || validatePlanBindings(config, plan, input.GetCanonicalResourceJson()) != nil {
		return nil, executionFailure("invalid_provider_plan", "invalid_declaration", "Proxmox provider plan is invalid", false, resourceID)
	}
	call := input.GetContext()
	if plan.TargetID != input.GetTarget().GetTargetId() || plan.ConfigFingerprint != input.GetTarget().GetConfigFingerprint() || plan.CapabilityDigest != config.Qualification.CapabilityDigest || plan.InventoryDigest != config.Qualification.InventoryDigest || plan.PolicyDigest != config.Qualification.PolicyDigest || plan.InputDigest != digestDocument(input.GetCanonicalResourceJson()) || plan.InstanceID != call.GetLabInstanceId() || plan.LogicalResourceID != call.GetLogicalResourceId() || plan.Generation != call.GetDesiredGeneration() || call.GetProviderTargetId() != plan.TargetID || call.GetInvocationId() == "" || call.GetOperationId() == "" || call.GetIdempotencyKey() == "" || call.GetFencingToken() == 0 {
		return nil, executionFailure("stale_plan", "conflict", "Proxmox provider plan binding is stale", false, resourceID)
	}
	if len(input.GetOwnershipTokens()) != 1 || plan.Ownership != nil && plan.Ownership.Token != input.GetOwnershipTokens()[0] {
		return nil, executionFailure("ownership_mismatch", "ownership_mismatch", "Proxmox ownership token does not match the plan", false, resourceID)
	}
	if err := validateCommittedAllocations(input, plan); err != nil {
		return nil, executionFailure("allocation_mismatch", "conflict", "Committed allocation claims do not match the Proxmox plan", false, resourceID)
	}
	journal, err := OpenInvocationJournal(config.JournalDirectory)
	if err != nil {
		return nil, executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal is unavailable", true, resourceID)
	}
	created, err := service.factory.New(input.GetTarget().GetConfigEnvelope(), input.GetTarget().GetCredentialEnvelope())
	if err != nil {
		return nil, executionFailure("invalid_target_credentials", "provider_unavailable", "Proxmox target credentials are invalid", false, resourceID)
	}
	client, ok := created.(MutationAPI)
	if !ok {
		created.CloseIdleConnections()
		return nil, executionFailure("mutation_client_unavailable", "provider_unavailable", "Proxmox mutation client is unavailable", false, resourceID)
	}
	ownershipText := input.GetOwnershipTokens()[0]
	if plan.Ownership != nil {
		encoded, encodeErr := encodeOwnershipMarker(*plan.Ownership)
		if encodeErr != nil {
			client.CloseIdleConnections()
			return nil, executionFailure("invalid_ownership_marker", "invalid_declaration", "Proxmox ownership marker is invalid", false, resourceID)
		}
		ownershipText = encoded
	}
	requestDigest := digestDocument(append(append([]byte(action+"\x00"), input.GetProviderPlan()...), input.GetCanonicalResourceJson()...))
	ownershipDigest := digestDocument([]byte(ownershipText))
	record := JournalRecord{
		SchemaVersion: journalV10, InvocationID: call.GetInvocationId(), TargetID: plan.TargetID, Action: action,
		RequestDigest: requestDigest, Node: config.DestinationNode, OwnershipDigest: ownershipDigest, Phase: "prepared",
	}
	if plan.ResourceKind == "VM" {
		record.VMID = planVMID(plan)
	}
	var existing *JournalRecord
	stored, readErr := journal.Read(call.GetInvocationId())
	if readErr == nil {
		if stored.TargetID != record.TargetID || stored.Action != action || stored.RequestDigest != record.RequestDigest || stored.Node != record.Node || stored.VMID != record.VMID || stored.OwnershipDigest != record.OwnershipDigest {
			client.CloseIdleConnections()
			return nil, executionFailure("invocation_binding_mismatch", "conflict", "Invocation ID is already bound to a different request", false, resourceID)
		}
		existing = &stored
	} else if !errors.Is(readErr, os.ErrNotExist) {
		client.CloseIdleConnections()
		return nil, executionFailure("journal_corrupt", "state_version_incompatible", "Proxmox invocation journal record is invalid", false, resourceID)
	} else if err := journal.Write(record); err != nil {
		client.CloseIdleConnections()
		return nil, executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal could not be persisted", true, resourceID)
	}
	return &preparedExecution{service: service, input: input, config: config, plan: plan, client: client, journal: journal, record: record, existing: existing, ownershipText: ownershipText}, nil
}

func (prepared *preparedExecution) cachedResult() *pluginv1.ResourceResult {
	if prepared.existing == nil || prepared.existing.Result == nil {
		return nil
	}
	return journalResultToProto(prepared.existing.Result, prepared.input.GetOwnershipTokens())
}

func (prepared *preparedExecution) applyNetwork(ctx context.Context) *pluginv1.ResourceResult {
	if err := verifyNetworkPreflight(ctx, prepared.client, prepared.config, prepared.plan); err != nil {
		code := "stale_plan"
		if errors.Is(err, errPermissionGate) {
			code = "permission_gate_failed"
		}
		return prepared.fail("failed", code, "conflict", "Proxmox target evidence no longer satisfies the network binding gate", false, false, false, nil)
	}
	binding := prepared.plan.Networks[0]
	state := &QEMUProviderState{
		SchemaVersion: qemuStateV10, ResourceKind: "Network", TargetID: prepared.plan.TargetID,
		Node: prepared.config.DestinationNode, LogicalResourceID: prepared.plan.LogicalResourceID,
		Generation: prepared.plan.Generation, Networks: []NetworkBinding{binding}, Phase: "ready",
	}
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "Network", Node: state.Node, Networks: state.Networks}
	result := successResult(state, observed, prepared.input.GetOwnershipTokens())
	if prepared.finish("ready", state, observed, nil, false, true) != nil {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox network result could not be persisted", true, prepared.plan.LogicalResourceID)
	}
	return result
}

func verifyNetworkPreflight(ctx context.Context, client MutationAPI, config TargetConfig, plan QEMUPlan) error {
	inventory, err := client.Discover(ctx)
	if err != nil || inventory.normalize() != nil {
		return fmt.Errorf("target inventory is unavailable")
	}
	digest, err := inventory.MutationDigest()
	if err != nil || digest != plan.InventoryDigest || !strings.HasPrefix(inventory.Version.Release, "9.") {
		return fmt.Errorf("target inventory is stale")
	}
	nodeOK, bridgeOK := false, false
	for _, node := range inventory.Nodes {
		if node.Name == config.DestinationNode && node.Status == "online" && node.QEMUAPI {
			nodeOK = true
		}
	}
	for _, bridge := range inventory.Bridges {
		if bridge.Node == config.DestinationNode && bridge.Name == plan.Networks[0].Bridge && bridge.Active && bridge.VLANAware {
			bridgeOK = true
		}
	}
	permissions, err := client.EffectivePermissions(ctx)
	if err != nil || !permissionsSatisfy(config.PermissionScopes, permissions) {
		return errPermissionGate
	}
	if !nodeOK || !bridgeOK {
		return fmt.Errorf("target network evidence is stale")
	}
	return nil
}

func (service *Service) applyVM(ctx context.Context, prepared *preparedExecution) *pluginv1.ResourceResult {
	plan := prepared.plan
	vmid := planVMID(plan)
	if err := verifyMutationPreflight(ctx, prepared.client, prepared.config, plan); err != nil {
		code := "stale_plan"
		if errors.Is(err, errPermissionGate) {
			code = "permission_gate_failed"
		}
		return prepared.fail("failed", code, "conflict", "Proxmox target evidence no longer satisfies the mutation gate", false, false, false, nil)
	}
	templateConfig, err := prepared.client.VMConfig(ctx, plan.Template.SourceNode, plan.Template.VMID)
	if err != nil || verifyTemplateConfig(plan, templateConfig) != nil {
		return prepared.fail("failed", "template_drift", "conflict", "Approved Proxmox template configuration has drifted", false, false, false, nil)
	}
	if prepared.existing != nil && prepared.existing.Phase == "clone-submitted" {
		upid, parseErr := ParseUPID(prepared.existing.TaskUPID)
		if parseErr != nil || upid.validateObject(plan.Template.SourceNode, vmid, "qmclone") != nil {
			return prepared.fail("failed", "invalid_task_identity", "provider_protocol_error", "Persisted Proxmox clone task identity is invalid", false, false, false, nil)
		}
		prepared.record.TaskUPID = upid.Raw
		if service.waitTask(ctx, prepared.client, plan.Template.SourceNode, upid) != nil {
			_, destinationExists, readErr := readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
			if readErr == nil && !destinationExists {
				return prepared.fail("failed", "provider_task_failed", "provider_failure", "Persisted Proxmox clone task did not complete successfully", false, false, true, nil)
			}
			complete, leftovers := service.compensateVM(ctx, prepared)
			phase := "failed"
			if !complete {
				phase = "partial"
			}
			return prepared.fail(phase, "provider_task_failed", "provider_failure", "Persisted Proxmox clone task did not complete successfully", false, true, complete, leftovers)
		}
		if !prepared.checkpoint("clone-complete", "persisted clone task completed") {
			return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation journal could not checkpoint clone completion", true, plan.LogicalResourceID)
		}
	}
	destinationConfig, exists, err := readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
	if err != nil {
		return prepared.fail("failed", "provider_read_failed", "provider_unavailable", "Proxmox destination preflight failed", true, false, false, nil)
	}
	if exists {
		if err := verifyOwnership(destinationConfig.Description, *plan.Ownership); err != nil {
			return prepared.fail("failed", "ownership_mismatch", "ownership_mismatch", "Claimed Proxmox VMID is occupied by a foreign VM", false, false, false, nil)
		}
	} else {
		available, checkErr := prepared.client.AdvisoryVMIDAvailable(ctx, vmid)
		if checkErr != nil || !available {
			return prepared.fail("failed", "vmid_collision", "conflict", "Claimed Proxmox VMID is unavailable", checkErr != nil, false, false, nil)
		}
		upid, cloneErr := prepared.client.CloneVM(ctx, CloneRequest{
			SourceNode: plan.Template.SourceNode, SourceVMID: plan.Template.VMID, NewVMID: vmid,
			Name: safeVMName(plan.InstanceID, plan.LogicalResourceID), TargetNode: plan.Destination.Node,
			StorageID: plan.Destination.StorageID, Format: plan.Destination.StorageFormat, Pool: plan.Destination.Pool,
			Description: prepared.ownershipText,
		})
		if cloneErr != nil {
			destinationConfig, exists, err = readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
			if err != nil {
				return prepared.fail("failed", "unknown_clone_outcome", "unknown_outcome", "Proxmox clone outcome is unknown", true, false, false, nil)
			}
			if !exists {
				return prepared.fail("prepared", "clone_submission_failed", "provider_unavailable", "Proxmox clone was not accepted", true, false, false, nil)
			}
			if verifyOwnership(destinationConfig.Description, *plan.Ownership) != nil {
				return prepared.fail("failed", "ownership_mismatch", "ownership_mismatch", "Clone response was lost and the destination VM is foreign", false, false, false, nil)
			}
			if destinationConfig, err = service.waitCloneUnlocked(ctx, prepared.client, plan.Destination.Node, vmid); err != nil {
				return prepared.fail("partial", "unknown_clone_outcome", "unknown_outcome", "Proxmox clone completion could not be confirmed", true, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
			}
		} else {
			if upid.validateObject(plan.Template.SourceNode, vmid, "qmclone") != nil {
				return prepared.fail("failed", "invalid_task_identity", "provider_protocol_error", "Proxmox clone returned an invalid task identity", false, false, false, nil)
			}
			prepared.record.TaskUPID = upid.Raw
			if !prepared.checkpoint("clone-submitted", "clone task accepted") {
				return executionFailure("journal_unavailable", "provider_unavailable", "Proxmox invocation journal could not checkpoint clone", true, plan.LogicalResourceID)
			}
			if err := service.waitTask(ctx, prepared.client, plan.Template.SourceNode, upid); err != nil {
				_, destinationExists, readErr := readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
				if readErr == nil && !destinationExists {
					return prepared.fail("failed", "provider_task_failed", "provider_failure", "Proxmox clone task did not complete successfully", false, false, true, nil)
				}
				complete, leftovers := service.compensateVM(ctx, prepared)
				phase := "failed"
				if !complete {
					phase = "partial"
				}
				return prepared.fail(phase, "provider_task_failed", "provider_failure", "Proxmox clone task did not complete successfully", false, true, complete, leftovers)
			}
		}
		if !prepared.checkpoint("clone-complete", "clone ownership observed") {
			return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation journal could not checkpoint clone completion", true, plan.LogicalResourceID)
		}
		destinationConfig, exists, err = readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
		if err != nil || !exists || verifyOwnership(destinationConfig.Description, *plan.Ownership) != nil {
			return prepared.fail("partial", "ownership_mismatch", "ownership_mismatch", "Cloned Proxmox VM ownership could not be verified", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
		}
	}
	if !managedConfigurationMatches(plan, destinationConfig) {
		configureErr := prepared.client.ConfigureVM(ctx, ConfigureRequest{
			Node: plan.Destination.Node, VMID: vmid, Digest: destinationConfig.Digest,
			Sockets: plan.Hardware.Sockets, Cores: plan.Hardware.Cores, CPULimit: plan.Hardware.CPULimit,
			MemoryMiB: plan.Hardware.MemoryMiB, Description: prepared.ownershipText, Tags: ownershipTag(*plan.Ownership), Networks: plan.Networks,
		})
		readback, readErr := prepared.client.VMConfig(ctx, plan.Destination.Node, vmid)
		if configureErr != nil || readErr != nil || !managedConfigurationMatches(plan, readback) {
			if configureErr != nil && readErr == nil && managedConfigurationMatches(plan, readback) {
				destinationConfig = readback
			} else {
				complete, leftovers := service.compensateVM(ctx, prepared)
				phase := "failed"
				if !complete {
					phase = "partial"
				}
				return prepared.fail(phase, "provider_config_failed", "provider_failure", "Proxmox VM configuration failed", false, true, complete, leftovers)
			}
		} else {
			destinationConfig = readback
		}
	}
	if prepared.existing != nil && prepared.existing.Phase == "lifecycle-submitted" {
		upid, parseErr := ParseUPID(prepared.existing.TaskUPID)
		if parseErr != nil || upid.validateObject(plan.Destination.Node, vmid, "qmstart") != nil {
			return prepared.fail("partial", "invalid_task_identity", "provider_protocol_error", "Persisted Proxmox start task identity is invalid", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
		}
		prepared.record.TaskUPID = upid.Raw
		if service.waitTask(ctx, prepared.client, plan.Destination.Node, upid) != nil {
			return prepared.fail("partial", "provider_task_failed", "provider_failure", "Persisted Proxmox start task did not complete successfully", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
		}
	}
	if !prepared.checkpoint("configured", "managed configuration verified") {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation journal could not checkpoint configuration", true, plan.LogicalResourceID)
	}
	status, err := prepared.client.VMStatus(ctx, plan.Destination.Node, vmid)
	if err != nil {
		return prepared.fail("partial", "provider_read_failed", "provider_unavailable", "Proxmox VM status could not be read", true, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
	}
	if status.Status != "running" {
		upid, startErr := prepared.client.LifecycleVM(ctx, LifecycleRequest{Node: plan.Destination.Node, VMID: vmid, Action: "start"})
		if startErr != nil {
			observed, readErr := prepared.client.VMStatus(ctx, plan.Destination.Node, vmid)
			if readErr != nil || observed.Status != "running" {
				return prepared.fail("partial", "unknown_start_outcome", "unknown_outcome", "Proxmox VM start outcome is unknown", true, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
			}
		} else if upid.validateObject(plan.Destination.Node, vmid, "qmstart") != nil {
			return prepared.fail("partial", "invalid_task_identity", "provider_protocol_error", "Proxmox start returned an invalid task identity", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
		} else {
			prepared.record.TaskUPID = upid.Raw
			if !prepared.checkpoint("lifecycle-submitted", "start task accepted") {
				return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation journal could not checkpoint start", true, plan.LogicalResourceID)
			}
			if err := service.waitTask(ctx, prepared.client, plan.Destination.Node, upid); err != nil {
				return prepared.fail("partial", "provider_task_failed", "provider_failure", "Proxmox start task did not complete successfully", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
			}
		}
	}
	ready := false
	for attempt := 0; attempt < maximumReadinessPolls; attempt++ {
		status, err = prepared.client.VMStatus(ctx, plan.Destination.Node, vmid)
		if err == nil && status.Status == "running" && prepared.client.GuestAgentPing(ctx, plan.Destination.Node, vmid) == nil {
			ready = true
			break
		}
		service.sleep(boundedBackoff(attempt))
	}
	if !ready {
		return prepared.fail("partial", "guest_not_ready", "timeout", "Proxmox guest did not become ready", true, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
	}
	finalConfig, err := prepared.client.VMConfig(ctx, plan.Destination.Node, vmid)
	if err != nil || verifyOwnership(finalConfig.Description, *plan.Ownership) != nil || !managedConfigurationMatches(plan, finalConfig) {
		return prepared.fail("partial", "provider_drift", "conflict", "Proxmox VM drifted before readiness", false, false, false, []string{fmt.Sprintf("qemu/%d", vmid)})
	}
	state := qemuVMState(plan, finalConfig, "ready", prepared.record.TaskUPID)
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: plan.Destination.Node, VMID: vmid, PowerState: "running", GuestReady: true, ConfigDigest: finalConfig.Digest, Networks: append([]NetworkBinding(nil), plan.Networks...)}
	result := successResult(state, observed, prepared.input.GetOwnershipTokens())
	if prepared.finish("ready", state, observed, nil, false, true) != nil {
		return executionFailure("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, plan.LogicalResourceID)
	}
	return result
}

func (service *Service) waitCloneUnlocked(ctx context.Context, client MutationAPI, node string, vmid uint32) (VMConfig, error) {
	for attempt := 0; attempt < maximumTaskPolls; attempt++ {
		config, err := client.VMConfig(ctx, node, vmid)
		if err != nil {
			return VMConfig{}, err
		}
		if config.Fields["lock"] == "" {
			return config, nil
		}
		if config.Fields["lock"] != "clone" {
			return VMConfig{}, fmt.Errorf("destination has an unexpected lock")
		}
		service.sleep(boundedBackoff(attempt))
	}
	return VMConfig{}, fmt.Errorf("clone remained locked")
}

var errPermissionGate = errors.New("effective permission gate failed")

func verifyMutationPreflight(ctx context.Context, client MutationAPI, config TargetConfig, plan QEMUPlan) error {
	inventory, err := client.Discover(ctx)
	if err != nil || inventory.normalize() != nil {
		return fmt.Errorf("target inventory is unavailable")
	}
	return verifyInventoryAndPermissions(ctx, client, config, plan, inventory)
}

func verifyInventoryAndPermissions(ctx context.Context, client MutationAPI, config TargetConfig, plan QEMUPlan, inventory Inventory) error {
	digest, err := inventory.MutationDigest()
	if err != nil || digest != plan.InventoryDigest || !qualifiedReleaseMatchesInventory(config.Qualification.PVERelease, inventory.Version) {
		return fmt.Errorf("target inventory is stale")
	}
	nodeOK, templateOK, storageOK, networkOK := false, false, false, len(plan.Networks) == 0
	for _, node := range inventory.Nodes {
		if node.Name == config.DestinationNode && node.Status == "online" && node.QEMUAPI {
			nodeOK = true
		}
	}
	for _, template := range inventory.Templates {
		if template.Node == plan.Template.SourceNode && template.VMID == int64(plan.Template.VMID) && template.Status == "stopped" {
			templateOK = true
		}
	}
	for _, storage := range inventory.Storages {
		if storage.Node == config.DestinationNode && storage.ID == plan.Destination.StorageID && storage.Active && storage.Enabled && contains(storage.Content, "images") && (contains(storage.Formats, plan.Destination.StorageFormat) || storage.DefaultFormat == plan.Destination.StorageFormat) {
			storageOK = true
		}
	}
	for _, bridge := range inventory.Bridges {
		if len(plan.Networks) > 0 && bridge.Node == config.DestinationNode && bridge.Name == plan.Networks[0].Bridge && bridge.Active && bridge.VLANAware {
			networkOK = true
		}
	}
	if !nodeOK || !templateOK || !storageOK || len(plan.Networks) > 0 && !networkOK {
		return fmt.Errorf("target component evidence is stale")
	}
	permissions, err := client.EffectivePermissions(ctx)
	if err != nil || !permissionsSatisfy(config.PermissionScopes, permissions) {
		return errPermissionGate
	}
	return nil
}

func qualifiedReleaseMatchesInventory(qualified string, observed Version) bool {
	return strings.HasPrefix(observed.Release, "9.") && (observed.Version == qualified || strings.HasPrefix(observed.Version, "pve-manager/"+qualified+"/"))
}

func permissionsSatisfy(required []PermissionScope, actual EffectivePermissions) bool {
	for _, privileges := range actual.Paths {
		for _, privilege := range privileges {
			if _, forbidden := forbiddenMutationPrivileges[privilege]; forbidden {
				return false
			}
		}
	}
	for _, scope := range required {
		granted := make(map[string]bool, len(actual.Paths[scope.Path]))
		for _, privilege := range actual.Paths[scope.Path] {
			granted[privilege] = true
		}
		for _, privilege := range scope.Privileges {
			if !granted[privilege] {
				return false
			}
		}
	}
	return true
}

func (service *Service) waitTask(ctx context.Context, client MutationAPI, node string, upid UPID) error {
	for attempt := 0; attempt < maximumTaskPolls; attempt++ {
		status, err := client.TaskStatus(ctx, node, upid)
		if err != nil {
			return err
		}
		if status.Status == "stopped" {
			if status.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("provider task failed")
		}
		service.sleep(boundedBackoff(attempt))
	}
	return fmt.Errorf("provider task timed out")
}

func boundedBackoff(attempt int) time.Duration {
	delay := 50 * time.Millisecond << min(attempt, 5)
	if delay > 2*time.Second {
		return 2 * time.Second
	}
	return delay
}

func (service *Service) compensateVM(ctx context.Context, prepared *preparedExecution) (bool, []string) {
	plan := prepared.plan
	vmid := planVMID(plan)
	config, exists, err := readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
	if err != nil || !exists || verifyOwnership(config.Description, *plan.Ownership) != nil {
		if !exists && err == nil {
			return true, nil
		}
		return false, []string{fmt.Sprintf("qemu/%d", vmid)}
	}
	status, err := prepared.client.VMStatus(ctx, plan.Destination.Node, vmid)
	if err == nil && status.Status == "running" {
		upid, stopErr := prepared.client.LifecycleVM(ctx, LifecycleRequest{Node: plan.Destination.Node, VMID: vmid, Action: "stop"})
		if stopErr != nil || upid.validateObject(plan.Destination.Node, vmid, "qmshutdown") != nil || service.waitTask(ctx, prepared.client, plan.Destination.Node, upid) != nil {
			return false, []string{fmt.Sprintf("qemu/%d", vmid)}
		}
	}
	upid, err := prepared.client.DeleteVM(ctx, plan.Destination.Node, vmid)
	if err != nil || upid.validateObject(plan.Destination.Node, vmid, "qmdestroy") != nil || service.waitTask(ctx, prepared.client, plan.Destination.Node, upid) != nil {
		return false, []string{fmt.Sprintf("qemu/%d", vmid)}
	}
	_, exists, err = readOptionalVMConfig(ctx, prepared.client, plan.Destination.Node, vmid)
	if err != nil || exists {
		return false, []string{fmt.Sprintf("qemu/%d", vmid)}
	}
	return true, nil
}

func (prepared *preparedExecution) checkpoint(phase, observation string) bool {
	prepared.record.Phase = phase
	prepared.record.LastSafeObservation = observation
	prepared.record.Result = nil
	return prepared.journal.Write(prepared.record) == nil
}

func (prepared *preparedExecution) finish(phase string, state *QEMUProviderState, observed *QEMUObservedState, failure *JournalError, attempted, complete bool) error {
	prepared.record.Phase = phase
	prepared.record.Result = &JournalResult{State: state, Observed: observed, Error: failure, CompensationAttempted: attempted, CompensationComplete: complete}
	return prepared.journal.Write(prepared.record)
}

func (prepared *preparedExecution) fail(phase, code, class, message string, retryable, attempted, complete bool, leftovers []string) *pluginv1.ResourceResult {
	failure := &JournalError{Code: code, Class: class, SafeMessage: message, Retryable: retryable}
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: prepared.plan.ResourceKind, Node: prepared.config.DestinationNode, VMID: prepared.record.VMID, Leftovers: append([]string(nil), leftovers...)}
	if phase == "prepared" && retryable {
		prepared.record.Phase = phase
		prepared.record.LastSafeObservation = "no provider object observed"
		prepared.record.Result = nil
		if prepared.journal.Write(prepared.record) != nil {
			return journalPersistenceFailure(observed, attempted, complete, prepared.plan.LogicalResourceID)
		}
		return &pluginv1.ResourceResult{ObservedStateJson: mustJSON(observed), Error: mutationErrorWithRetry(code, class, message, retryable, prepared.plan.LogicalResourceID), CompensationAttempted: attempted, CompensationComplete: complete}
	}
	if prepared.finish(phase, nil, observed, failure, attempted, complete) != nil {
		return journalPersistenceFailure(observed, attempted, complete, prepared.plan.LogicalResourceID)
	}
	return journalResultToProto(prepared.record.Result, prepared.input.GetOwnershipTokens())
}

func journalPersistenceFailure(observed *QEMUObservedState, attempted, complete bool, resource string) *pluginv1.ResourceResult {
	return &pluginv1.ResourceResult{
		ObservedStateJson:     mustJSON(observed),
		Error:                 mutationErrorWithRetry("journal_unavailable", "unknown_outcome", "Proxmox invocation result could not be persisted", true, resource),
		CompensationAttempted: attempted,
		CompensationComplete:  complete,
	}
}

func readOptionalVMConfig(ctx context.Context, client MutationAPI, node string, vmid uint32) (VMConfig, bool, error) {
	config, err := client.VMConfig(ctx, node, vmid)
	if err == nil {
		return config, true, nil
	}
	var status *APIStatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
		return VMConfig{}, false, nil
	}
	return VMConfig{}, false, err
}

func verifyTemplateConfig(plan QEMUPlan, config VMConfig) error {
	if config.Digest != plan.Template.ConfigDigest || !agentEnabled(config.Fields["agent"]) {
		return fmt.Errorf("template digest or guest agent mismatch")
	}
	cloudInit := false
	declaredDisks := make(map[string]struct{}, len(plan.Disks))
	for _, disk := range plan.Disks {
		declaredDisks[disk.Device] = struct{}{}
		if config.Fields[disk.Device] == "" {
			return fmt.Errorf("template disk is missing")
		}
	}
	for key, value := range config.Fields {
		if forbiddenQEMUField(key) {
			return fmt.Errorf("template has a forbidden device")
		}
		if diskFieldPattern.MatchString(key) {
			if strings.Contains(strings.ToLower(value), "cloudinit") {
				cloudInit = true
				continue
			}
			if _, approved := declaredDisks[key]; !approved {
				return fmt.Errorf("template has an unknown disk")
			}
			volume := strings.SplitN(value, ",", 2)[0]
			separator := strings.IndexByte(volume, ':')
			if separator <= 0 || !storageIDPattern.MatchString(volume[:separator]) || strings.Contains(strings.ToLower(value), "media=cdrom") {
				return fmt.Errorf("template has a raw or arbitrary disk")
			}
		}
	}
	return boolError(!cloudInit, "template cloud-init drive is missing")
}

func managedConfigurationMatches(plan QEMUPlan, config VMConfig) bool {
	if plan.Ownership == nil || verifyOwnership(config.Description, *plan.Ownership) != nil {
		return false
	}
	if config.Fields["lock"] != "" || hasForbiddenOrUnknownDevice(plan, config.Fields) {
		return false
	}
	for _, disk := range plan.Disks {
		if !strings.HasPrefix(config.Fields[disk.Device], disk.StorageID+":") {
			return false
		}
	}
	expected := map[string]string{
		"sockets": strconv.FormatUint(uint64(plan.Hardware.Sockets), 10), "cores": strconv.FormatUint(uint64(plan.Hardware.Cores), 10),
		"cpulimit": plan.Hardware.CPULimit, "memory": strconv.FormatUint(plan.Hardware.MemoryMiB, 10),
		"tags": ownershipTag(*plan.Ownership), "ciupgrade": "0", "onboot": "0",
	}
	for index, binding := range plan.Networks {
		prefix, err := netip.ParsePrefix(binding.IPv4CIDR)
		if err != nil {
			return false
		}
		expected[fmt.Sprintf("net%d", index)] = fmt.Sprintf("virtio=%s,bridge=%s,tag=%d", strings.ToUpper(binding.MAC), binding.Bridge, binding.VLAN)
		expected[fmt.Sprintf("ipconfig%d", index)] = fmt.Sprintf("ip=%s/%d", binding.IPv4Address, prefix.Bits())
		if binding.Gateway != "" {
			expected[fmt.Sprintf("ipconfig%d", index)] += ",gw=" + binding.Gateway
		}
	}
	for key, value := range expected {
		if config.Fields[key] != value {
			return false
		}
	}
	for key := range config.Fields {
		if networkFieldPattern.MatchString(key) {
			indexText := strings.TrimPrefix(strings.TrimPrefix(key, "ipconfig"), "net")
			index, err := strconv.Atoi(indexText)
			if err != nil || index >= len(plan.Networks) {
				return false
			}
		}
	}
	return true
}

func hasForbiddenOrUnknownDevice(plan QEMUPlan, fields map[string]string) bool {
	approvedDisks := make(map[string]bool, len(plan.Disks))
	for _, disk := range plan.Disks {
		approvedDisks[disk.Device] = true
	}
	for key, value := range fields {
		if forbiddenQEMUField(key) {
			return true
		}
		if diskFieldPattern.MatchString(key) && !approvedDisks[key] && !strings.Contains(strings.ToLower(value), "cloudinit") {
			return true
		}
	}
	return false
}

func forbiddenQEMUField(key string) bool {
	for _, prefix := range []string{"audio", "efidisk", "hostpci", "ivshmem", "mp", "parallel", "rng", "serial", "tpmstate", "unused", "usb", "virtiofs"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return key == "args" || key == "hookscript"
}

func qemuVMState(plan QEMUPlan, config VMConfig, phase, upid string) *QEMUProviderState {
	volumes := make([]string, 0, len(plan.Disks))
	for _, disk := range plan.Disks {
		value := config.Fields[disk.Device]
		if separator := strings.IndexByte(value, ','); separator >= 0 {
			value = value[:separator]
		}
		volumes = append(volumes, value)
	}
	sort.Strings(volumes)
	markerBytes, _ := json.Marshal(plan.Ownership)
	return &QEMUProviderState{
		SchemaVersion: qemuStateV10, ResourceKind: "VM", TargetID: plan.TargetID, Node: plan.Destination.Node,
		VMID: planVMID(plan), Ownership: plan.Ownership, OwnershipDigest: digestDocument(markerBytes), LogicalResourceID: plan.LogicalResourceID,
		Generation: plan.Generation, Volumes: volumes, Networks: append([]NetworkBinding(nil), plan.Networks...), ConfigDigest: config.Digest, TaskUPID: upid, Phase: phase,
	}
}

func validateCommittedAllocations(input *pluginv1.ResourceMutationInput, plan QEMUPlan) error {
	direct, err := claimSet(input.GetCommittedAllocations())
	if err != nil {
		return err
	}
	dependencies := make(map[string]*pluginv1.ResourceDependency)
	for _, dependency := range input.GetDependencies() {
		if dependency == nil || dependencies[dependency.GetLogicalResourceId()] != nil {
			return fmt.Errorf("invalid dependency")
		}
		dependencies[dependency.GetLogicalResourceId()] = dependency
	}
	expectedDirect := 1
	if len(direct) != expectedDirect {
		return fmt.Errorf("unexpected direct allocation count")
	}
	for _, allocation := range plan.Allocations {
		key := allocation.Namespace + "\x00" + allocation.Value
		if allocation.Namespace == vmidClaimNamespace || plan.ResourceKind == "Network" {
			if !direct[key] {
				return fmt.Errorf("direct allocation is not committed")
			}
			continue
		}
		matched := false
		for _, binding := range plan.Networks {
			if strconv.FormatUint(uint64(binding.VLAN), 10) != allocation.Value {
				continue
			}
			dependency := dependencies[binding.LogicalResourceID]
			if dependency == nil {
				continue
			}
			claims, claimErr := claimSet(dependency.GetCommittedAllocations())
			if claimErr == nil && claims[key] {
				state, stateErr := decodeQEMUState(dependency.GetProviderState())
				if stateErr == nil && state.ResourceKind == "Network" && state.TargetID == plan.TargetID && len(state.Networks) == 1 && reflect.DeepEqual(state.Networks[0], bindingWithoutVMFields(binding)) {
					matched = true
				}
			}
		}
		if !matched {
			return fmt.Errorf("dependency allocation or state is not committed")
		}
	}
	return nil
}

func claimSet(claims []*pluginv1.AllocationClaim) (map[string]bool, error) {
	result := make(map[string]bool, len(claims))
	for _, claim := range claims {
		if claim == nil || (claim.GetNamespace() != vmidClaimNamespace && claim.GetNamespace() != vlanClaimNamespace) || claim.GetValue() == "" {
			return nil, fmt.Errorf("invalid allocation")
		}
		value, err := strconv.ParseUint(claim.GetValue(), 10, 32)
		if err != nil || (len(claim.GetValue()) > 1 && strings.HasPrefix(claim.GetValue(), "0")) || claim.GetNamespace() == vmidClaimNamespace && value < 100 || claim.GetNamespace() == vlanClaimNamespace && (value < 1 || value > 4094) {
			return nil, fmt.Errorf("invalid allocation value")
		}
		key := claim.GetNamespace() + "\x00" + claim.GetValue()
		if result[key] {
			return nil, fmt.Errorf("duplicate allocation")
		}
		result[key] = true
	}
	return result, nil
}

func bindingWithoutVMFields(binding NetworkBinding) NetworkBinding {
	binding.MAC = ""
	binding.IPv4Address = ""
	binding.Gateway = ""
	return binding
}

func stateMatchesTargetConfig(config TargetConfig, state QEMUProviderState) bool {
	if state.Node != config.DestinationNode {
		return false
	}
	if state.Ownership != nil && state.Ownership.InstallationID != config.InstallationID {
		return false
	}
	if state.ResourceKind == "VM" && !valueInRanges(state.VMID, config.VMIDRanges) {
		return false
	}
	for _, binding := range state.Networks {
		profile, ok := findNetworkProfile(config, binding.ProfileAlias)
		if !ok || binding.Bridge != profile.Bridge || !valueInRanges(binding.VLAN, profile.VLANRanges) {
			return false
		}
	}
	return true
}

func stateMatchesPlan(config TargetConfig, state QEMUProviderState, plan QEMUPlan, outer *pluginv1.ProviderState) bool {
	if !stateMatchesTargetConfig(config, state) || state.ResourceKind != plan.ResourceKind || state.TargetID != plan.TargetID || state.LogicalResourceID != plan.LogicalResourceID || state.Generation != plan.Generation {
		return false
	}
	if state.ResourceKind == "Network" {
		return reflect.DeepEqual(state.Networks, plan.Networks)
	}
	return state.VMID == planVMID(plan) && state.Ownership != nil && plan.Ownership != nil && *state.Ownership == *plan.Ownership && len(outer.GetOwnershipIds()) == 1 && outer.GetOwnershipIds()[0] == state.Ownership.Token && reflect.DeepEqual(state.Networks, plan.Networks)
}

func encodeOwnershipMarker(marker OwnershipMarker) (string, error) {
	if marker.InstallationID == "" || marker.TargetID == "" || marker.InstanceID == "" || marker.LogicalResource == "" || marker.Generation == 0 || marker.Token == "" || len(marker.Token) > 512 {
		return "", fmt.Errorf("ownership marker is invalid")
	}
	contents, err := json.Marshal(marker)
	if err != nil {
		return "", err
	}
	encoded := "LCOWN1:" + base64.RawURLEncoding.EncodeToString(contents)
	if len(encoded) > 2048 {
		return "", fmt.Errorf("ownership marker exceeds limit")
	}
	return encoded, nil
}

func verifyOwnership(encoded string, expected OwnershipMarker) error {
	if !strings.HasPrefix(encoded, "LCOWN1:") {
		return fmt.Errorf("ownership marker is missing")
	}
	contents, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, "LCOWN1:"))
	if err != nil || len(contents) > 1536 {
		return fmt.Errorf("ownership marker is invalid")
	}
	var actual OwnershipMarker
	if err := decodeClosedJSON(contents, &actual); err != nil || actual != expected {
		return fmt.Errorf("ownership marker does not match")
	}
	return nil
}

func ownershipTag(marker OwnershipMarker) string {
	contents, _ := json.Marshal(marker)
	digest := sha256.Sum256(contents)
	return "labcentral_" + hex.EncodeToString(digest[:8])
}

func safeVMName(instanceID, resourceID string) string {
	value := strings.ToLower(instanceID + "-" + resourceID)
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	name := strings.Trim(builder.String(), "-")
	if len(name) > 63 {
		name = name[:46] + "-" + strings.TrimPrefix(digestDocument([]byte(value)), "sha256:")[:16]
	}
	if name == "" {
		return "labcentral-vm"
	}
	return name
}

func planVMID(plan QEMUPlan) uint32 {
	for _, allocation := range plan.Allocations {
		if allocation.Namespace == vmidClaimNamespace {
			value, _ := strconv.ParseUint(allocation.Value, 10, 32)
			return uint32(value)
		}
	}
	return 0
}

func agentEnabled(value string) bool {
	return value == "1" || strings.Contains(value, "enabled=1")
}

func boolError(condition bool, message string) error {
	if condition {
		return errors.New(message)
	}
	return nil
}

func successResult(state *QEMUProviderState, observed *QEMUObservedState, ownership []string) *pluginv1.ResourceResult {
	observedBytes, _ := json.Marshal(observed)
	result := &pluginv1.ResourceResult{ObservedStateJson: observedBytes, CompensationComplete: true}
	if state != nil {
		stateBytes, _ := json.Marshal(state)
		digest := sha256.Sum256(stateBytes)
		result.ProviderState = &pluginv1.ProviderState{Schema: &pluginv1.SchemaVersion{Name: "proxmox.qemu-state", Major: 1, Minor: 0}, PluginId: PluginID, PluginVersion: PluginVersion, Opaque: stateBytes, OwnershipIds: append([]string(nil), ownership...), Digest: digest[:]}
	}
	return result
}

func mustJSON(value any) []byte {
	contents, _ := json.Marshal(value)
	return contents
}

func executionFailure(code, class, message string, retryable bool, resource string) *pluginv1.ResourceResult {
	return &pluginv1.ResourceResult{Error: mutationErrorWithRetry(code, class, message, retryable, resource)}
}

func mutationErrorWithRetry(code, class, message string, retryable bool, resource string) *pluginv1.StructuredError {
	return &pluginv1.StructuredError{Code: code, ErrorClass: class, SafeMessage: message, Retryable: retryable, ResourceRef: resource}
}

func journalResultToProto(result *JournalResult, ownership []string) *pluginv1.ResourceResult {
	if result == nil {
		return executionFailure("invocation_incomplete", "unknown_outcome", "Proxmox invocation has not completed", true, "")
	}
	if result.Error != nil {
		observed, _ := json.Marshal(result.Observed)
		return &pluginv1.ResourceResult{ObservedStateJson: observed, Error: mutationErrorWithRetry(result.Error.Code, result.Error.Class, result.Error.SafeMessage, result.Error.Retryable, ""), CompensationAttempted: result.CompensationAttempted, CompensationComplete: result.CompensationComplete}
	}
	return successResult(result.State, result.Observed, ownership)
}

func progressInvocationEvent(now time.Time, phase, message, task string) *pluginv1.InvocationEvent {
	return &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Progress{Progress: &pluginv1.Progress{Phase: phase, SafeMessage: message, ProviderTaskRef: task, OccurredAt: timestamppb.New(now.UTC())}}}
}

func resultInvocationEvent(result *pluginv1.ResourceResult) *pluginv1.InvocationEvent {
	return &pluginv1.InvocationEvent{Event: &pluginv1.InvocationEvent_Result{Result: result}}
}

func decodeQEMUState(state *pluginv1.ProviderState) (QEMUProviderState, error) {
	var decoded QEMUProviderState
	if state == nil || state.GetPluginId() != PluginID || state.GetSchema().GetName() != "proxmox.qemu-state" || state.GetSchema().GetMajor() != 1 || state.GetSchema().GetMinor() != 0 {
		return decoded, fmt.Errorf("provider state metadata is invalid")
	}
	digest := sha256.Sum256(state.GetOpaque())
	if !reflect.DeepEqual(digest[:], state.GetDigest()) {
		return decoded, fmt.Errorf("provider state digest is invalid")
	}
	if err := decodeEnvelope(state.GetOpaque(), maximumConfigBytes, &decoded); err != nil || decoded.validate() != nil {
		return decoded, fmt.Errorf("provider state payload is invalid")
	}
	return decoded, nil
}
