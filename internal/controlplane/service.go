package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
)

const (
	LocalAdminID         = domain.UserID("local-admin")
	LocalDockerTarget    = domain.ProviderTargetID("target.local-docker")
	jobPayloadVersion    = 1
	IsolationWarningCode = "docker_trusted_laptop_only"
)

type IDSource interface {
	New(string) (string, error)
}

type CryptoIDs struct{}

func (CryptoIDs) New(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}

type Issue struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	FieldPath  string `json:"fieldPath,omitempty"`
	ResourceID string `json:"resourceRef,omitempty"`
}

type ValidationResult struct {
	Canonical domain.CanonicalLab
	Warnings  []Issue
}

type ResourcePlan struct {
	ResourceID string  `json:"resourceId"`
	Kind       string  `json:"kind"`
	Action     string  `json:"action"`
	Warnings   []Issue `json:"warnings,omitempty"`
}

type PlanResult struct {
	CanonicalDigest string         `json:"canonicalDigest"`
	Resources       []ResourcePlan `json:"resources"`
	Warnings        []Issue        `json:"warnings"`
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "Lab validation failed"
	}
	return e.Issues[0].Message
}

type Service struct {
	renderer *render.Renderer
	store    Store
	provider pluginv1.ProviderServiceClient
	ids      IDSource
}

func NewService(renderer *render.Renderer, store Store, provider pluginv1.ProviderServiceClient, ids IDSource) (*Service, error) {
	if renderer == nil || store == nil || provider == nil || ids == nil {
		return nil, fmt.Errorf("renderer, store, provider, and ID source are required")
	}
	return &Service{renderer: renderer, store: store, provider: provider, ids: ids}, nil
}

func (s *Service) Validate(ctx context.Context, source []byte, acknowledged bool) (ValidationResult, error) {
	canonical, report, err := s.renderer.Render(ctx, domain.RevisionContent{VerifiedBundle: source}, nil, domain.RenderPolicy{})
	if err != nil {
		return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "render_failed", Message: err.Error()}}}
	}
	override, _ := json.Marshal(map[string]bool{"trustedLaptopIsolationAccepted": acknowledged})
	callContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := s.provider.ValidateLab(callContext, &pluginv1.ValidateLabRequest{
		Context:              &pluginv1.CallContext{RequestId: "validate", ProviderTargetId: string(LocalDockerTarget)},
		Target:               &pluginv1.TargetContext{TargetId: string(LocalDockerTarget)},
		CanonicalLabJson:     canonical.CanonicalBytes,
		ProviderOverrideJson: override,
	})
	if err != nil {
		return ValidationResult{}, fmt.Errorf("Docker provider validation is unavailable: %w", err)
	}
	issues := providerErrors(response.GetErrors())
	if len(issues) > 0 {
		return ValidationResult{}, &ValidationError{Issues: issues}
	}
	warnings := make([]Issue, 0, len(report.Warnings)+len(response.GetWarnings()))
	for _, warning := range report.Warnings {
		warnings = append(warnings, Issue{Code: warning.Code, Message: warning.Message, FieldPath: warning.FieldPath, ResourceID: string(warning.ResourceID)})
	}
	warnings = append(warnings, providerWarnings(response.GetWarnings())...)
	return ValidationResult{Canonical: canonical, Warnings: deduplicateIssues(warnings)}, nil
}

func (s *Service) Plan(ctx context.Context, source []byte, acknowledged bool) (PlanResult, error) {
	validation, err := s.Validate(ctx, source, acknowledged)
	if err != nil {
		return PlanResult{}, err
	}
	previewID := domain.InstanceID("preview_" + validation.Canonical.Digest.Hex()[:24])
	result, _, err := s.planCanonical(ctx, validation, previewID)
	return result, err
}

func (s *Service) planCanonical(ctx context.Context, validation ValidationResult, instanceID domain.InstanceID) (PlanResult, map[domain.LogicalResourceID][]byte, error) {
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return PlanResult{}, nil, err
	}
	result := PlanResult{CanonicalDigest: validation.Canonical.Digest.String(), Warnings: validation.Warnings}
	providerPlans := make(map[domain.LogicalResourceID][]byte, len(validation.Canonical.Resources))
	providerPlanDigests := make(map[domain.LogicalResourceID]string, len(validation.Canonical.Resources))
	graph, err := render.BuildResourceGraph(validation.Canonical.Resources)
	if err != nil {
		return PlanResult{}, nil, err
	}
	for _, resource := range graph.Resources {
		plan := ResourcePlan{ResourceID: string(resource.ID()), Kind: string(resource.Kind()), Action: "ensure"}
		if !providerManagedKind(resource.Kind()) {
			plan.Action = "project"
			result.Resources = append(result.Resources, plan)
			providerPlans[resource.ID()] = []byte(`{"action":"project"}`)
			planDigest := sha256.Sum256(providerPlans[resource.ID()])
			providerPlanDigests[resource.ID()] = hex.EncodeToString(planDigest[:])
			continue
		}
		canonicalResource, err := render.CanonicalResource(resource)
		if err != nil {
			return PlanResult{}, nil, err
		}
		dependencies := make([]*pluginv1.ResourceDependency, 0, len(resource.Dependencies()))
		for _, dependencyID := range resource.Dependencies() {
			dependencyPlan, exists := providerPlans[dependencyID]
			if !exists {
				return PlanResult{}, nil, fmt.Errorf("dependency %s was not planned before %s", dependencyID, resource.ID())
			}
			dependencies = append(dependencies, &pluginv1.ResourceDependency{
				LogicalResourceId: string(dependencyID), ProviderPlan: append([]byte(nil), dependencyPlan...),
				ProviderPlanDigest: providerPlanDigests[dependencyID],
			})
		}
		planContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		response, callErr := s.provider.PlanResource(planContext, &pluginv1.PlanResourceRequest{Input: &pluginv1.ResourceMutationInput{
			Context: &pluginv1.CallContext{
				RequestId: "plan", LabInstanceId: string(instanceID), LogicalResourceId: string(resource.ID()),
				ProviderTargetId: string(LocalDockerTarget), DesiredGeneration: 1,
			},
			Target: &pluginv1.TargetContext{TargetId: string(LocalDockerTarget)}, CanonicalResourceJson: canonicalResource,
			OwnershipTokens: []string{ownershipToken(instanceID, resource.ID())}, Dependencies: dependencies,
		}})
		cancel()
		if callErr != nil {
			return PlanResult{}, nil, fmt.Errorf("plan Docker resource %s: %w", resource.ID(), callErr)
		}
		if response.GetError() != nil {
			return PlanResult{}, nil, &ValidationError{Issues: providerErrors([]*pluginv1.StructuredError{response.GetError()})}
		}
		if len(response.GetProposedAllocations()) > 0 {
			return PlanResult{}, nil, fmt.Errorf("provider allocation planning is unavailable in the local Docker control plane")
		}
		providerPlans[resource.ID()] = append([]byte(nil), response.GetProviderPlan()...)
		planDigest := sha256.Sum256(providerPlans[resource.ID()])
		providerPlanDigests[resource.ID()] = hex.EncodeToString(planDigest[:])
		if response.GetProviderPlanDigest() != providerPlanDigests[resource.ID()] {
			return PlanResult{}, nil, fmt.Errorf("provider plan digest mismatch for %s", resource.ID())
		}
		plan.Warnings = providerWarnings(response.GetWarnings())
		result.Resources = append(result.Resources, plan)
	}
	return result, providerPlans, nil
}

func (s *Service) CreateInstance(ctx context.Context, source []byte, acknowledged bool, idempotencyKey string) (EnqueueResult, error) {
	validation, err := s.Validate(ctx, source, acknowledged)
	if err != nil {
		return EnqueueResult{}, err
	}
	instanceValue, err := s.ids.New("inst")
	if err != nil {
		return EnqueueResult{}, err
	}
	instanceID := domain.InstanceID(instanceValue)
	_, providerPlans, err := s.planCanonical(ctx, validation, instanceID)
	if err != nil {
		return EnqueueResult{}, err
	}
	operationValue, err := s.ids.New("op")
	if err != nil {
		return EnqueueResult{}, err
	}
	operationID := domain.OperationID(operationValue)
	resources := make([]ResourceSeed, 0, len(validation.Canonical.Resources))
	for ordinal, resource := range validation.Canonical.Resources {
		canonicalResource, err := render.CanonicalResource(resource)
		if err != nil {
			return EnqueueResult{}, err
		}
		resources = append(resources, ResourceSeed{
			ID: resource.ID(), Kind: resource.Kind(), Ordinal: ordinal, CanonicalJSON: canonicalResource,
			ProviderPlan:   providerPlans[resource.ID()],
			OwnershipToken: ownershipToken(instanceID, resource.ID()),
		})
	}
	payload, _ := json.Marshal(jobPayload{Version: jobPayloadVersion, Kind: OperationApply, InstanceID: instanceID})
	requestDigest := digestBytes("labcentral.http.create-instance/v1", source, []byte(fmt.Sprintf("%t", acknowledged)))
	auditID, err := s.ids.New("audit")
	if err != nil {
		return EnqueueResult{}, err
	}
	eventID, err := s.ids.New("event")
	if err != nil {
		return EnqueueResult{}, err
	}
	return s.store.CreateInstance(ctx, CreateInstanceRequest{
		InstanceID: instanceID, OperationID: operationID, CanonicalDigest: validation.Canonical.Digest,
		CanonicalJSON: validation.Canonical.CanonicalBytes, SourceYAML: source, Resources: resources, Acknowledged: acknowledged,
		SubjectID: LocalAdminID, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, Payload: payload,
		AuditID: auditID, EventID: eventID,
	})
}

func (s *Service) DestroyInstance(ctx context.Context, instanceID domain.InstanceID, idempotencyKey string) (EnqueueResult, error) {
	if _, err := s.store.GetInstance(ctx, instanceID); err != nil {
		return EnqueueResult{}, err
	}
	operationID, err := s.ids.New("op")
	if err != nil {
		return EnqueueResult{}, err
	}
	payload, _ := json.Marshal(jobPayload{Version: jobPayloadVersion, Kind: OperationDestroy, InstanceID: instanceID})
	requestDigest := digestBytes("labcentral.http.destroy-instance/v1", []byte(instanceID))
	auditID, err := s.ids.New("audit")
	if err != nil {
		return EnqueueResult{}, err
	}
	eventID, err := s.ids.New("event")
	if err != nil {
		return EnqueueResult{}, err
	}
	return s.store.QueueDestroy(ctx, QueueDestroyRequest{
		InstanceID: instanceID, OperationID: domain.OperationID(operationID), SubjectID: LocalAdminID,
		IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, Payload: payload, AuditID: auditID, EventID: eventID,
	})
}

func (s *Service) GetInstance(ctx context.Context, id domain.InstanceID) (InstanceRecord, []ResourceRecord, error) {
	instance, err := s.store.GetInstance(ctx, id)
	if err != nil {
		return InstanceRecord{}, nil, err
	}
	resources, err := s.store.ListResources(ctx, id)
	return instance, resources, err
}

func (s *Service) ListInstances(ctx context.Context) ([]InstanceRecord, error) {
	return s.store.ListInstances(ctx)
}

func (s *Service) GetOperation(ctx context.Context, id domain.OperationID) (OperationRecord, error) {
	return s.store.GetOperation(ctx, id)
}

type jobPayload struct {
	Version    int               `json:"version"`
	Kind       OperationKind     `json:"kind"`
	InstanceID domain.InstanceID `json:"instanceId"`
}

func providerManagedKind(kind domain.ResourceKind) bool {
	return kind == domain.ResourceNetwork || kind == domain.ResourceVolume || kind == domain.ResourceContainer || kind == domain.ResourceVM
}

func providerErrors(source []*pluginv1.StructuredError) []Issue {
	result := make([]Issue, 0, len(source))
	for _, issue := range source {
		if issue == nil {
			continue
		}
		result = append(result, Issue{Code: issue.GetCode(), Message: issue.GetSafeMessage(), FieldPath: issue.GetFieldPath(), ResourceID: issue.GetResourceRef()})
	}
	return result
}

func providerWarnings(source []*pluginv1.Warning) []Issue {
	result := make([]Issue, 0, len(source))
	for _, issue := range source {
		if issue != nil {
			result = append(result, Issue{Code: issue.GetCode(), Message: issue.GetSafeMessage(), ResourceID: issue.GetResourceRef()})
		}
	}
	return result
}

func deduplicateIssues(source []Issue) []Issue {
	seen := make(map[string]bool)
	result := make([]Issue, 0, len(source))
	for _, issue := range source {
		key := issue.Code + "\x00" + issue.ResourceID + "\x00" + issue.FieldPath
		if !seen[key] {
			seen[key] = true
			result = append(result, issue)
		}
	}
	return result
}

func ownershipToken(instanceID domain.InstanceID, resourceID domain.LogicalResourceID) string {
	digest := digestBytes("labcentral.ownership/v1", []byte("local"), []byte(LocalDockerTarget), []byte(instanceID), []byte(resourceID))
	return digest.Hex()
}

func digestBytes(frame string, values ...[]byte) domain.SHA256Digest {
	hash := sha256.New()
	writeFrame(hash, []byte(frame))
	for _, value := range values {
		writeFrame(hash, value)
	}
	parsed, _ := domain.ParseSHA256Digest(hex.EncodeToString(hash.Sum(nil)))
	return parsed
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeFrame(writer hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}
