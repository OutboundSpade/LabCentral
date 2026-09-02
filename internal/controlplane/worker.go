package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/protobuf/proto"
)

type Worker struct {
	store         Store
	provider      pluginv1.ProviderServiceClient
	ids           IDSource
	owner         string
	leaseDuration time.Duration
	pollInterval  time.Duration
	onError       func(error)
}

type WorkerOptions struct {
	Owner         string
	LeaseDuration time.Duration
	PollInterval  time.Duration
	OnError       func(error)
}

func NewWorker(store Store, provider pluginv1.ProviderServiceClient, ids IDSource, options WorkerOptions) (*Worker, error) {
	if store == nil || provider == nil || ids == nil {
		return nil, fmt.Errorf("worker store, provider, and ID source are required")
	}
	if options.Owner == "" {
		options.Owner = "local-worker"
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if options.OnError == nil {
		options.OnError = func(error) {}
	}
	return &Worker{store: store, provider: provider, ids: ids, owner: options.Owner, leaseDuration: options.LeaseDuration, pollInterval: options.PollInterval, onError: options.OnError}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		err := w.RunOnce(ctx)
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, context.Canceled) {
			w.onError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	claim, err := w.store.Claim(ctx, w.owner, w.leaseDuration)
	if err != nil {
		return err
	}
	fence := Fence{
		OperationID: claim.Operation.ID,
		LeaseOwner:  claim.LeaseOwner,
		Token:       claim.Operation.FencingToken,
		Generation:  claim.Operation.Generation,
		Attempt:     claim.Operation.Attempt,
	}
	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	renewalErrors := make(chan error, 1)
	go w.renewLease(workContext, fence, renewalErrors)
	err = w.process(workContext, fence, claim.Payload)
	cancel()
	select {
	case renewalErr := <-renewalErrors:
		if renewalErr != nil {
			return renewalErr
		}
	default:
	}
	return err
}

func (w *Worker) renewLease(ctx context.Context, fence Fence, failures chan<- error) {
	interval := w.leaseDuration / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.Renew(ctx, fence, w.leaseDuration); err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, fence Fence, payloadBytes []byte) error {
	var payload jobPayload
	if err := strictDecode(payloadBytes, &payload); err != nil || payload.Version != jobPayloadVersion || payload.InstanceID == "" {
		return fmt.Errorf("unsupported local job payload")
	}
	instance, err := w.store.GetInstance(ctx, payload.InstanceID)
	if err != nil {
		return err
	}
	if payload.InstanceID != instance.ID {
		return fmt.Errorf("job instance identity mismatch")
	}
	switch payload.Kind {
	case OperationApply:
		return w.apply(ctx, fence, instance)
	case OperationDestroy:
		return w.destroy(ctx, fence, instance)
	default:
		return fmt.Errorf("unsupported local operation kind %q", payload.Kind)
	}
}

func (w *Worker) apply(ctx context.Context, fence Fence, instance InstanceRecord) error {
	for _, transition := range []struct {
		phase   domain.InstancePhase
		message string
	}{{domain.InstanceValidating, "Revalidating pinned Lab"}, {domain.InstancePlanned, "Using persisted Docker plans"}, {domain.InstanceApplying, "Applying Docker resources"}} {
		if err := w.store.MarkInstancePhase(ctx, fence, transition.phase, transition.message); err != nil {
			return err
		}
	}
	validationContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	validation, err := w.provider.ValidateLab(validationContext, &pluginv1.ValidateLabRequest{
		Context: &pluginv1.CallContext{RequestId: "worker-validate", OperationId: string(fence.OperationID), LabInstanceId: string(instance.ID), ProviderTargetId: string(LocalDockerTarget)},
		Target:  &pluginv1.TargetContext{TargetId: string(LocalDockerTarget)}, CanonicalLabJson: instance.CanonicalLabJSON,
		ProviderOverrideJson: []byte(`{"trustedLaptopIsolationAccepted":true}`),
	})
	cancel()
	if err != nil {
		return fmt.Errorf("provider revalidation unavailable: %w", err)
	}
	if len(validation.GetErrors()) > 0 {
		first := validation.GetErrors()[0]
		return w.finalize(ctx, fence, OperationApply, domain.OperationFailed, domain.InstanceFailed, "Provider validation failed", first.GetCode(), first.GetSafeMessage(), nil)
	}
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Phase == domain.ResourceReady {
			continue
		}
		if err := w.store.MarkInstancePhase(ctx, fence, domain.InstanceApplying, "Applying "+string(resource.ID)); err != nil {
			return err
		}
		if !providerManagedKind(resource.Kind) {
			observed, projectionErr := projectCoreResource(resource, resources)
			if projectionErr != nil {
				_ = w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceFailed, []byte(`{}`))
				return w.compensateAndFinalize(ctx, fence, instance.ID, resource.ID, projectionErr)
			}
			if err := w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceReady, observed); err != nil {
				return err
			}
			continue
		}
		result, callErr := w.invoke(ctx, fence, instance.ID, resource, "apply")
		if callErr != nil {
			return callErr
		}
		if result.GetError() != nil {
			return w.compensateAndFinalize(ctx, fence, instance.ID, resource.ID, fmt.Errorf("%s", result.GetError().GetSafeMessage()))
		}
		resources, err = w.store.ListResources(ctx, instance.ID)
		if err != nil {
			return err
		}
	}
	resources, err = w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	outputs, err := collectOutputs(resources)
	if err != nil {
		return w.compensateAndFinalize(ctx, fence, instance.ID, "", err)
	}
	return w.finalize(ctx, fence, OperationApply, domain.OperationSucceeded, domain.InstanceReady, "Lab is ready", "", "", outputs)
}

func (w *Worker) destroy(ctx context.Context, fence Fence, instance InstanceRecord) error {
	if err := w.store.MarkInstancePhase(ctx, fence, domain.InstanceDestroying, "Destroying Docker resources"); err != nil {
		return err
	}
	resources, err := w.store.ListResources(ctx, instance.ID)
	if err != nil {
		return err
	}
	leftovers := make([]string, 0)
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if resource.Phase == domain.ResourceDestroyed {
			continue
		}
		if !providerManagedKind(resource.Kind) || len(resource.ProviderState) == 0 {
			if err := w.store.SaveCoreResource(ctx, fence, resource.ID, domain.ResourceDestroyed, []byte(`{"destroyed":true}`)); err != nil {
				return err
			}
			continue
		}
		result, callErr := w.invoke(ctx, fence, instance.ID, resource, "destroy")
		if callErr != nil {
			leftovers = append(leftovers, string(resource.ID))
			continue
		}
		if result.GetError() != nil {
			leftovers = append(leftovers, string(resource.ID))
		}
	}
	if len(leftovers) > 0 {
		return w.finalize(ctx, fence, OperationDestroy, domain.OperationPartial, domain.InstanceDegraded,
			"Destroy completed with owned leftovers", "partial_cleanup", "Cleanup failed for: "+strings.Join(leftovers, ", "), nil)
	}
	return w.finalize(ctx, fence, OperationDestroy, domain.OperationSucceeded, domain.InstanceDestroyed, "Lab was destroyed", "", "", []byte(`{}`))
}

func (w *Worker) invoke(ctx context.Context, fence Fence, instanceID domain.InstanceID, resource ResourceRecord, action string) (*pluginv1.ResourceResult, error) {
	requestDigest := digestBytes("labcentral.provider-invocation/v1", []byte(action), resource.CanonicalJSON, resource.ProviderPlan, resource.ProviderState)
	invocationValue, err := w.ids.New("inv")
	if err != nil {
		return nil, err
	}
	prepared, err := w.store.PrepareInvocation(ctx, PrepareInvocationRequest{
		Fence: fence, InvocationID: domain.InvocationID(invocationValue), ResourceID: resource.ID, Action: action,
		IdempotencyKey: requestDigest.Hex(), RequestDigest: requestDigest,
	})
	if err != nil {
		return nil, err
	}
	var priorState *pluginv1.ProviderState
	if len(resource.ProviderState) > 0 {
		priorState = &pluginv1.ProviderState{}
		if err := proto.Unmarshal(resource.ProviderState, priorState); err != nil {
			return nil, fmt.Errorf("decode persisted provider state for %s: %w", resource.ID, err)
		}
	}
	input := &pluginv1.ResourceMutationInput{
		Context: &pluginv1.CallContext{
			RequestId: string(prepared.ID), OperationId: string(fence.OperationID), InvocationId: string(prepared.ID),
			LabInstanceId: string(instanceID), LogicalResourceId: string(resource.ID), ProviderTargetId: string(LocalDockerTarget),
			IdempotencyKey: prepared.IdempotencyKey, DesiredGeneration: uint64(fence.Generation), FencingToken: uint64(fence.Token),
		},
		Target: &pluginv1.TargetContext{TargetId: string(LocalDockerTarget)}, CanonicalResourceJson: resource.CanonicalJSON,
		ProviderPlan: resource.ProviderPlan, PriorState: priorState, OwnershipTokens: []string{resource.OwnershipToken},
	}
	callContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var result *pluginv1.ResourceResult
	if action == "apply" {
		stream, err := w.provider.ApplyResource(callContext, &pluginv1.ApplyResourceRequest{Input: input})
		if err != nil {
			return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, err)
		}
		for {
			message, receiveErr := stream.Recv()
			if errors.Is(receiveErr, io.EOF) {
				break
			}
			if receiveErr != nil {
				return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, receiveErr)
			}
			if message.GetEvent().GetResult() != nil {
				result = message.GetEvent().GetResult()
			}
		}
	} else {
		stream, err := w.provider.DestroyResource(callContext, &pluginv1.DestroyResourceRequest{Input: input})
		if err != nil {
			return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, err)
		}
		for {
			message, receiveErr := stream.Recv()
			if errors.Is(receiveErr, io.EOF) {
				break
			}
			if receiveErr != nil {
				return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, receiveErr)
			}
			if message.GetEvent().GetResult() != nil {
				result = message.GetEvent().GetResult()
			}
		}
	}
	if result == nil {
		return nil, w.saveUnknown(ctx, fence, prepared.ID, resource, action, fmt.Errorf("provider stream ended without a result"))
	}
	providerState := resource.ProviderState
	if result.GetProviderState() != nil {
		providerState, err = proto.Marshal(result.GetProviderState())
		if err != nil {
			return nil, err
		}
	}
	phase := domain.ResourceReady
	invocationState := "succeeded"
	errorCode, errorMessage := "", ""
	if action != "apply" {
		phase = domain.ResourceDestroyed
	}
	if result.GetError() != nil {
		phase = domain.ResourceFailed
		if action != "apply" {
			phase = domain.ResourceCleanupFailed
		}
		invocationState = "failed"
		errorCode = result.GetError().GetCode()
		errorMessage = result.GetError().GetSafeMessage()
	}
	if err := w.store.SaveResourceResult(ctx, ResourceResultUpdate{
		Fence: fence, InvocationID: prepared.ID, ResourceID: resource.ID, InvocationState: invocationState,
		Phase: phase, ProviderState: providerState, ObservedStateJSON: result.GetObservedStateJson(), ErrorCode: errorCode, ErrorMessage: errorMessage,
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (w *Worker) saveUnknown(ctx context.Context, fence Fence, invocationID domain.InvocationID, resource ResourceRecord, action string, cause error) error {
	phase := domain.ResourceUnknown
	if action != "apply" {
		phase = domain.ResourceCleanupFailed
	}
	if err := w.store.SaveResourceResult(ctx, ResourceResultUpdate{
		Fence: fence, InvocationID: invocationID, ResourceID: resource.ID, InvocationState: "unknown", Phase: phase,
		ProviderState: resource.ProviderState, ObservedStateJSON: resource.ObservedStateJSON,
		ErrorCode: "provider_outcome_unknown", ErrorMessage: "Provider response was interrupted; the operation will reconcile",
	}); err != nil {
		return err
	}
	return fmt.Errorf("provider outcome for %s is unknown: %w", resource.ID, cause)
}

func (w *Worker) compensateAndFinalize(ctx context.Context, fence Fence, instanceID domain.InstanceID, failedResource domain.LogicalResourceID, cause error) error {
	resources, err := w.store.ListResources(ctx, instanceID)
	if err != nil {
		return err
	}
	leftovers := make([]string, 0)
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if !providerManagedKind(resource.Kind) || len(resource.ProviderState) == 0 || resource.Phase != domain.ResourceReady {
			continue
		}
		result, invokeErr := w.invoke(ctx, fence, instanceID, resource, "compensate")
		if invokeErr != nil || result.GetError() != nil {
			leftovers = append(leftovers, string(resource.ID))
		}
	}
	state := domain.OperationFailed
	phase := domain.InstanceFailed
	errorCode := "apply_failed"
	message := "Apply failed and created resources were compensated"
	if len(leftovers) > 0 {
		state = domain.OperationPartial
		phase = domain.InstanceDegraded
		errorCode = "partial_cleanup"
		message = "Apply failed with owned leftovers: " + strings.Join(leftovers, ", ")
	}
	if failedResource != "" {
		message += " (failed at " + string(failedResource) + ")"
	}
	return w.finalize(ctx, fence, OperationApply, state, phase, message, errorCode, cause.Error(), nil)
}

func (w *Worker) finalize(ctx context.Context, fence Fence, kind OperationKind, state domain.OperationState, phase domain.InstancePhase, message, errorCode, errorMessage string, outputs []byte) error {
	auditID, err := w.ids.New("audit")
	if err != nil {
		return err
	}
	eventID, err := w.ids.New("event")
	if err != nil {
		return err
	}
	return w.store.Finalize(ctx, FinalizeRequest{
		Fence: fence, OperationKind: kind, State: state, InstancePhase: phase, Message: message,
		ErrorCode: errorCode, ErrorMessage: errorMessage, OutputsJSON: outputs, AuditID: auditID, EventID: eventID,
	})
}

func projectCoreResource(resource ResourceRecord, resources []ResourceRecord) ([]byte, error) {
	var canonical struct {
		Kind string `json:"kind"`
		Spec struct {
			TargetRef string   `json:"targetRef"`
			Methods   []string `json:"methods"`
			ValueRef  string   `json:"valueRef"`
			Type      string   `json:"type"`
			Sensitive bool     `json:"sensitive"`
		} `json:"spec"`
	}
	if err := decodeJSON(resource.CanonicalJSON, &canonical); err != nil {
		return nil, err
	}
	switch resource.Kind {
	case domain.ResourceAccessEndpoint:
		return json.Marshal(map[string]any{"available": true, "methods": canonical.Spec.Methods, "targetRef": canonical.Spec.TargetRef})
	case domain.ResourceOutput:
		value, err := resolveOutput(canonical.Spec.ValueRef, resources)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"type": canonical.Spec.Type, "sensitive": canonical.Spec.Sensitive, "value": value})
	default:
		return nil, fmt.Errorf("resource %s is not core-projected", resource.ID)
	}
}

func resolveOutput(expression string, resources []ResourceRecord) (string, error) {
	parts := strings.Split(expression, ".")
	if len(parts) != 6 || parts[0] != "resources" || parts[1] != "containers" || parts[3] != "publishedPorts" || parts[5] != "url" {
		return "", fmt.Errorf("local slice cannot project output expression %q", expression)
	}
	targetID := domain.LogicalResourceID("container." + parts[2])
	portName := parts[4]
	for _, resource := range resources {
		if resource.ID != targetID {
			continue
		}
		var canonical struct {
			Spec struct {
				PublishedPorts []struct {
					Name     string `json:"name"`
					Port     uint16 `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"publishedPorts"`
			} `json:"spec"`
		}
		if err := decodeJSON(resource.CanonicalJSON, &canonical); err != nil {
			return "", err
		}
		var observed struct {
			Ports map[string]uint16 `json:"ports"`
		}
		if err := decodeJSON(resource.ObservedStateJSON, &observed); err != nil {
			return "", err
		}
		for _, port := range canonical.Spec.PublishedPorts {
			if port.Name != portName {
				continue
			}
			hostPort := observed.Ports[fmt.Sprintf("%d/%s", port.Port, port.Protocol)]
			if hostPort == 0 {
				return "", fmt.Errorf("provider did not observe host port %s", portName)
			}
			return fmt.Sprintf("http://127.0.0.1:%d/", hostPort), nil
		}
	}
	return "", fmt.Errorf("output target or published port is unavailable")
}

func collectOutputs(resources []ResourceRecord) ([]byte, error) {
	outputs := make(map[string]any)
	for _, resource := range resources {
		if resource.Kind != domain.ResourceOutput {
			continue
		}
		var observed struct {
			Value any `json:"value"`
		}
		if err := decodeJSON(resource.ObservedStateJSON, &observed); err != nil {
			return nil, err
		}
		outputs[strings.TrimPrefix(string(resource.ID), "output.")] = observed.Value
	}
	return json.Marshal(outputs)
}

func strictDecode(input []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func decodeJSON(input []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}
