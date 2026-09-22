package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sync/singleflight"
	"sync"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	plugincontract "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
)

const (
	LocalAdminID         = application.LocalAdminID
	LocalDockerTarget    = application.LocalDockerTarget
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
	Canonical         domain.CanonicalLab
	Warnings          []Issue
	TargetID          domain.ProviderTargetID
	ProviderKind      string
	ValidationPlan    []byte
	EffectiveOverride []byte
}

type ResourcePlan struct {
	ResourceID          string              `json:"resourceId"`
	Kind                string              `json:"kind"`
	Action              string              `json:"action"`
	ProposedAllocations []AllocationPreview `json:"proposedAllocations,omitempty"`
	Warnings            []Issue             `json:"warnings,omitempty"`
}

type AllocationPreview struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
}

type PlanResult struct {
	CanonicalDigest  string                  `json:"canonicalDigest"`
	ProviderTargetID domain.ProviderTargetID `json:"providerTargetId"`
	ProviderKind     string                  `json:"providerKind"`
	Resources        []ResourcePlan          `json:"resources"`
	Warnings         []Issue                 `json:"warnings"`
}

type plannedResource struct {
	plan        []byte
	planDigest  domain.SHA256Digest
	allocations []application.AllocationKey
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "Lab validation failed"
	}
	return e.Issues[0].Message
}

type RenderOptions struct {
	WorkFence       *application.Fence
	ImageBindingIDs []string
	BuildImages     []domain.ResolvedBuildImage
	Inputs          domain.TypedInputs
	RendererVersion string
}

type Service struct {
	workLeaseDuration    time.Duration
	sources              map[string]SourceBinding
	builders             map[string]BuilderBinding
	operationsAuthorizer application.Authorizer
	usageMu              sync.Mutex
	usageReads           singleflight.Group
	usageCache           map[string]cachedUsage
	usageSlots           chan struct{}
	renderer             *render.Renderer
	store                Store
	artifacts            application.ArtifactStore
	targets              *TargetRegistry
	access               *AccessRegistry
	authorizer           application.Authorizer
	tokenHasher          application.KeyedTokenHasher
	ids                  IDSource
}

func NewService(renderer *render.Renderer, store Store, provider application.Provider, ids IDSource) (*Service, error) {
	registry, err := NewDockerTargetRegistry(provider)
	if err != nil {
		return nil, err
	}
	return NewServiceWithTargets(renderer, store, registry, ids)
}

func NewServiceWithTargets(renderer *render.Renderer, store Store, targets *TargetRegistry, ids IDSource) (*Service, error) {
	if renderer == nil || store == nil || targets == nil || ids == nil {
		return nil, fmt.Errorf("renderer, store, target registry, and ID source are required")
	}
	return &Service{renderer: renderer, store: store, targets: targets, ids: ids}, nil
}

// SetArtifactStore attaches the content-addressed store used for immutable
// template revisions. It must be called during startup before serving requests.
func (s *Service) SetArtifactStore(artifacts application.ArtifactStore) error {
	if s == nil || artifacts == nil {
		return fmt.Errorf("control-plane service and artifact store are required")
	}
	s.artifacts = artifacts
	return nil
}

func (s *Service) SetAccessRuntime(registry *AccessRegistry, authorizer application.Authorizer, tokenHasher application.KeyedTokenHasher) error {
	if s == nil || registry == nil || authorizer == nil || tokenHasher == nil {
		return fmt.Errorf("access registry, authorizer, and keyed token hasher are required")
	}
	s.access, s.authorizer, s.tokenHasher = registry, authorizer, tokenHasher
	return nil
}

func (s *Service) AccessAvailable() bool {
	return s != nil && s.access != nil && s.authorizer != nil && s.tokenHasher != nil
}

func (s *Service) Validate(ctx context.Context, source []byte, acknowledged bool) (ValidationResult, error) {
	return s.ValidateTarget(ctx, source, acknowledged, LocalDockerTarget, nil)
}

func (s *Service) ValidateTarget(ctx context.Context, source []byte, acknowledged bool, targetID domain.ProviderTargetID, providerOverride []byte, options ...RenderOptions) (ValidationResult, error) {
	if len(providerOverride) > maximumTargetEnvelopeBytes {
		return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "provider_override_invalid", Message: "The provider override exceeds its size limit"}}}
	}
	if len(providerOverride) > 0 {
		trimmed := bytes.TrimSpace(providerOverride)
		if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
			return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "provider_override_invalid", Message: "The provider override must be one JSON object"}}}
		}
	}
	if len(options) > 1 {
		return ValidationResult{}, fmt.Errorf("only one render options object is allowed")
	}
	option := RenderOptions{}
	if len(options) == 1 {
		option = options[0]
	}
	canonical, report, err := s.renderer.RenderVersion(ctx, option.RendererVersion, domain.RevisionContent{VerifiedBundle: source}, option.Inputs, domain.RenderPolicy{BuildImages: option.BuildImages})
	if err != nil {
		return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "render_failed", Message: err.Error()}}}
	}
	session, err := s.targets.Acquire(ctx, targetID, application.InvocationScope{}, false)
	if err != nil {
		return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "provider_target_unavailable", Message: "The selected provider target is unavailable"}}}
	}
	defer session.Close()
	if !session.Supports(session.Policy.MinimumMinor, session.Policy.RequiredFeatures...) {
		return ValidationResult{}, &ValidationError{Issues: []Issue{{
			Code: "provider_protocol_features_unavailable", Message: "The selected target did not negotiate the required protocol features",
		}}}
	}
	override := append([]byte(nil), providerOverride...)
	if session.Policy.TrustedLaptop {
		if len(providerOverride) != 0 {
			return ValidationResult{}, &ValidationError{Issues: []Issue{{Code: "provider_override_unsupported", Message: "The Docker laptop target does not accept a caller-supplied provider override"}}}
		}
		override, _ = json.Marshal(map[string]bool{"trustedLaptopIsolationAccepted": acknowledged})
	}
	callContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := session.Provider.Validate(callContext, application.ProviderValidationRequest{
		Call:      application.ProviderCall{RequestID: "validate", TargetID: string(targetID)},
		Target:    session.Context,
		Canonical: canonical.CanonicalBytes,
		Override:  override,
	})
	if err != nil {
		return ValidationResult{}, fmt.Errorf("provider validation is unavailable: %w", err)
	}
	issues := providerErrors(response.Errors)
	if len(issues) > 0 {
		return ValidationResult{}, &ValidationError{Issues: issues}
	}
	if err := verifyProviderPlanDigest(response.Plan, response.PlanDigest); err != nil {
		return ValidationResult{}, fmt.Errorf("provider validation plan digest mismatch: %w", err)
	}
	warnings := make([]Issue, 0, len(report.Warnings)+len(response.Warnings))
	for _, warning := range report.Warnings {
		warnings = append(warnings, Issue{Code: warning.Code, Message: warning.Message, FieldPath: warning.FieldPath, ResourceID: string(warning.ResourceID)})
	}
	warnings = append(warnings, providerWarnings(response.Warnings)...)
	return ValidationResult{
		Canonical: canonical, Warnings: deduplicateIssues(warnings), TargetID: targetID, ProviderKind: session.ProviderKind,
		ValidationPlan: append([]byte(nil), response.Plan...), EffectiveOverride: append([]byte(nil), override...),
	}, nil
}

func (s *Service) Plan(ctx context.Context, source []byte, acknowledged bool) (PlanResult, error) {
	return s.PlanTarget(ctx, source, acknowledged, LocalDockerTarget, nil)
}

func (s *Service) PlanTarget(ctx context.Context, source []byte, acknowledged bool, targetID domain.ProviderTargetID, providerOverride []byte, options ...RenderOptions) (PlanResult, error) {
	validation, err := s.ValidateTarget(ctx, source, acknowledged, targetID, providerOverride, options...)
	if err != nil {
		return PlanResult{}, err
	}
	previewID := domain.InstanceID("preview_" + validation.Canonical.Digest.Hex()[:24])
	result, _, err := s.planCanonical(ctx, validation, previewID, nil)
	return result, err
}

func (s *Service) planCanonical(ctx context.Context, validation ValidationResult, instanceID domain.InstanceID, exclusions map[domain.LogicalResourceID][]application.AllocationKey) (PlanResult, map[domain.LogicalResourceID]plannedResource, error) {
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return PlanResult{}, nil, err
	}
	result := PlanResult{
		CanonicalDigest: validation.Canonical.Digest.String(), ProviderTargetID: validation.TargetID,
		ProviderKind: validation.ProviderKind, Warnings: validation.Warnings,
	}
	providerPlans := make(map[domain.LogicalResourceID]plannedResource, len(validation.Canonical.Resources))
	graph, err := render.BuildResourceGraph(validation.Canonical.Resources)
	if err != nil {
		return PlanResult{}, nil, err
	}
	session, err := s.targets.Acquire(ctx, validation.TargetID, application.InvocationScope{}, false)
	if err != nil {
		return PlanResult{}, nil, err
	}
	defer session.Close()
	plannedAllocations, err := s.store.ListTargetAllocationKeys(ctx, validation.TargetID)
	if err != nil {
		return PlanResult{}, nil, err
	}
	for _, resource := range graph.Resources {
		plan := ResourcePlan{ResourceID: string(resource.ID()), Kind: string(resource.Kind()), Action: "ensure"}
		if !providerManagedKind(resource.Kind()) {
			plan.Action = "project"
			result.Resources = append(result.Resources, plan)
			contents := []byte(`{"action":"project"}`)
			providerPlans[resource.ID()] = plannedResource{plan: contents, planDigest: rawDigest(contents)}
			continue
		}
		canonicalResource, err := render.CanonicalResource(resource)
		if err != nil {
			return PlanResult{}, nil, err
		}
		dependencies := make([]application.ProviderDependency, 0, len(resource.Dependencies()))
		for _, dependencyID := range resource.Dependencies() {
			dependencyPlan, exists := providerPlans[dependencyID]
			if !exists {
				return PlanResult{}, nil, fmt.Errorf("dependency %s was not planned before %s", dependencyID, resource.ID())
			}
			dependencies = append(dependencies, application.ProviderDependency{
				ResourceID: string(dependencyID), Plan: append([]byte(nil), dependencyPlan.plan...),
				PlanDigest: dependencyPlan.planDigest.Hex(), Allocations: protocolClaims(dependencyPlan.allocations),
			})
		}
		excluded := append([]application.AllocationKey(nil), exclusions[resource.ID()]...)
		// Earlier resources have not reached the database yet. Exclude their
		// tentative claims so one lab cannot propose the same VMID/VLAN twice.
		for _, claim := range plannedAllocations {
			excluded = appendUniqueClaim(excluded, claim)
		}
		planContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		response, callErr := session.Provider.Plan(planContext, application.ProviderMutation{
			Call: application.ProviderCall{
				RequestID: "plan", InstanceID: string(instanceID), ResourceID: string(resource.ID()),
				TargetID: string(validation.TargetID), Generation: 1,
			},
			Target: session.Context, Canonical: canonicalResource, Plan: validation.ValidationPlan,
			Ownership: []string{ownershipToken(validation.TargetID, instanceID, resource.ID())}, Dependencies: dependencies,
			ExcludedAllocations: protocolClaims(excluded)})
		cancel()
		if callErr != nil {
			return PlanResult{}, nil, fmt.Errorf("plan provider resource %s: %w", resource.ID(), callErr)
		}
		if response.Error != nil {
			return PlanResult{}, nil, &ValidationError{Issues: providerErrors([]application.ProviderIssue{*response.Error})}
		}
		if err := verifyProviderPlanDigest(response.Plan, response.PlanDigest); err != nil {
			return PlanResult{}, nil, fmt.Errorf("provider plan digest mismatch for %s", resource.ID())
		}
		planDigest, _ := domain.ParseSHA256Digest(response.PlanDigest)
		allocations := make([]application.AllocationKey, 0, len(response.Allocations))
		for _, proposal := range response.Allocations {
			allocations = append(allocations, application.AllocationKey{TargetID: validation.TargetID, Namespace: proposal.Namespace, Value: proposal.Value})
		}
		if len(allocations) > 0 {
			if !session.Supports(1, plugincontract.FeatureOpaqueAllocationClaimsV1) {
				return PlanResult{}, nil, fmt.Errorf("provider proposed allocations without negotiated allocation support")
			}
			binding := application.AllocationBinding{InstanceID: instanceID, ResourceID: resource.ID(), PlanDigest: planDigest}
			allocations, err = application.ValidateAllocationClaims(binding, allocations)
			if err != nil {
				return PlanResult{}, nil, fmt.Errorf("provider proposed invalid allocations for %s: %w", resource.ID(), err)
			}
		}
		for _, allocation := range allocations {
			for _, unavailable := range excluded {
				if allocation == unavailable {
					return PlanResult{}, nil, &ValidationError{Issues: []Issue{{Code: "provider_allocation_conflict", ResourceID: string(resource.ID()), Message: "Provider proposed an excluded allocation"}}}
				}
			}
		}
		plannedAllocations = append(plannedAllocations, allocations...)
		providerPlans[resource.ID()] = plannedResource{plan: append([]byte(nil), response.Plan...), planDigest: planDigest, allocations: allocations}
		for _, allocation := range allocations {
			plan.ProposedAllocations = append(plan.ProposedAllocations, AllocationPreview{Namespace: allocation.Namespace, Value: allocation.Value})
		}
		plan.Warnings = providerWarnings(response.Warnings)
		result.Resources = append(result.Resources, plan)
	}
	return result, providerPlans, nil
}

func (s *Service) CreateInstance(ctx context.Context, source []byte, acknowledged bool, idempotencyKey string) (EnqueueResult, error) {
	return s.CreateInstanceTarget(ctx, source, acknowledged, idempotencyKey, LocalDockerTarget, nil)
}

func (s *Service) CreateInstanceTarget(ctx context.Context, source []byte, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte) (EnqueueResult, error) {
	return s.CreateInstanceTargetFor(ctx, LocalAdminID, "project.local", application.OwnerUser, string(LocalAdminID), source, acknowledged, idempotencyKey, targetID, providerOverride)
}

func (s *Service) CreateInstanceTargetFor(ctx context.Context, subjectID domain.UserID, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, source []byte, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte) (EnqueueResult, error) {
	return s.createInstanceTargetFor(ctx, subjectID, projectID, ownerKind, ownerID, "", source, application.InstanceLifetimeRequest{}, acknowledged, idempotencyKey, targetID, providerOverride)
}

func (s *Service) CreateInstanceTargetForLifetime(ctx context.Context, subjectID domain.UserID, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, source []byte, lifetime application.InstanceLifetimeRequest, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte, options ...RenderOptions) (EnqueueResult, error) {
	return s.createInstanceTargetFor(ctx, subjectID, projectID, ownerKind, ownerID, "", source, lifetime, acknowledged, idempotencyKey, targetID, providerOverride, options...)
}

func (s *Service) CreateInstanceRevisionTargetFor(ctx context.Context, subjectID domain.UserID, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, revisionID domain.RevisionID, source []byte, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte) (EnqueueResult, error) {
	return s.CreateInstanceRevisionTargetForLifetime(ctx, subjectID, projectID, ownerKind, ownerID, revisionID, source, application.InstanceLifetimeRequest{}, acknowledged, idempotencyKey, targetID, providerOverride)
}

func (s *Service) CreateInstanceRevisionTargetForLifetime(ctx context.Context, subjectID domain.UserID, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, revisionID domain.RevisionID, source []byte, lifetime application.InstanceLifetimeRequest, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte, options ...RenderOptions) (EnqueueResult, error) {
	if _, err := domain.NewRevisionID(string(revisionID)); err != nil {
		return EnqueueResult{}, err
	}
	revision, err := s.store.GetTemplateRevision(ctx, revisionID)
	if err != nil {
		return EnqueueResult{}, err
	}
	if len(options) > 1 {
		return EnqueueResult{}, fmt.Errorf("only one render options object is allowed")
	}
	option := RenderOptions{}
	if len(options) == 1 {
		option = options[0]
	}
	option.RendererVersion = revision.RendererVersion
	options = []RenderOptions{option}
	return s.createInstanceTargetFor(ctx, subjectID, projectID, ownerKind, ownerID, revisionID, source, lifetime, acknowledged, idempotencyKey, targetID, providerOverride, options...)
}

func (s *Service) createInstanceTargetFor(ctx context.Context, subjectID domain.UserID, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, revisionID domain.RevisionID, source []byte, lifetime application.InstanceLifetimeRequest, acknowledged bool, idempotencyKey string, targetID domain.ProviderTargetID, providerOverride []byte, options ...RenderOptions) (EnqueueResult, error) {
	if _, err := domain.NewUserID(string(subjectID)); err != nil {
		return EnqueueResult{}, err
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return EnqueueResult{}, err
	}
	if !validOwner(ownerKind, ownerID) {
		return EnqueueResult{}, fmt.Errorf("instance owner is invalid")
	}
	if err := application.ValidateInstanceLifetimeRequest(lifetime); err != nil {
		return EnqueueResult{}, err
	}
	validation, err := s.ValidateTarget(ctx, source, acknowledged, targetID, providerOverride, options...)
	if err != nil {
		return EnqueueResult{}, err
	}
	if err := s.requireTargetAdmission(ctx, subjectID, targetID); err != nil {
		return EnqueueResult{}, err
	}
	instanceValue, err := s.ids.New("inst")
	if err != nil {
		return EnqueueResult{}, err
	}
	instanceID := domain.InstanceID(instanceValue)
	operationValue, err := s.ids.New("op")
	if err != nil {
		return EnqueueResult{}, err
	}
	operationID := domain.OperationID(operationValue)
	reservationValue, err := s.ids.New("qres")
	if err != nil {
		return EnqueueResult{}, err
	}
	reservationID := domain.QuotaReservationID(reservationValue)
	payload, _ := json.Marshal(jobPayload{Version: jobPayloadVersion, Kind: OperationApply, InstanceID: instanceID})
	lifetimeDigest := []byte(fmt.Sprintf("%d:%t", lifetime.TTLSeconds, lifetime.Persistent))
	requestDigest := digestBytes("labcentral.http.create-instance/v2", []byte(projectID), []byte(ownerKind), []byte(ownerID), source, lifetimeDigest, []byte(fmt.Sprintf("%t", acknowledged)), []byte(targetID), providerOverride)
	if revisionID != "" {
		requestDigest = digestBytes("labcentral.http.create-instance-revision/v2", []byte(projectID), []byte(ownerKind), []byte(ownerID), []byte(revisionID), lifetimeDigest, []byte(fmt.Sprintf("%t", acknowledged)), []byte(targetID), providerOverride)
	}
	if len(options) == 1 && len(options[0].Inputs) > 0 {
		requestDigest = digestBytes("labcentral.http.instance-inputs/v1", []byte(requestDigest), []byte(validation.Canonical.Digest))
	}

	quotaAmounts, err := quotaAmountsForCanonical(validation.Canonical)
	if err != nil {
		return EnqueueResult{}, err
	}
	auditID, err := s.ids.New("audit")
	if err != nil {
		return EnqueueResult{}, err
	}
	eventID, err := s.ids.New("event")
	if err != nil {
		return EnqueueResult{}, err
	}
	exclusions := make(map[domain.LogicalResourceID][]application.AllocationKey)
	for attempt := 0; attempt < application.MaximumAllocationPlanAttempts; attempt++ {
		_, providerPlans, err := s.planCanonical(ctx, validation, instanceID, exclusions)
		if err != nil {
			return EnqueueResult{}, err
		}
		resources := make([]ResourceSeed, 0, len(validation.Canonical.Resources))
		for ordinal, resource := range validation.Canonical.Resources {
			canonicalResource, err := render.CanonicalResource(resource)
			if err != nil {
				return EnqueueResult{}, err
			}
			planned := providerPlans[resource.ID()]
			resources = append(resources, ResourceSeed{
				ID: resource.ID(), Kind: resource.Kind(), Ordinal: ordinal, CanonicalJSON: canonicalResource,
				ProviderPlan: planned.plan, ProviderPlanDigest: planned.planDigest, Allocations: planned.allocations,
				OwnershipToken: ownershipToken(targetID, instanceID, resource.ID()),
			})
		}
		option := RenderOptions{}
		if len(options) == 1 {
			option = options[0]
		}
		result, createErr := s.store.CreateInstance(ctx, CreateInstanceRequest{WorkFence: option.WorkFence, ImageBindingIDs: option.ImageBindingIDs,
			InstanceID: instanceID, RevisionID: revisionID, ProjectID: projectID, OwnerKind: ownerKind, OwnerID: ownerID, CreatedBy: subjectID,
			ProviderTargetID: targetID, ProviderOverrideJSON: validation.EffectiveOverride,
			OperationID: operationID, CanonicalDigest: validation.Canonical.Digest,
			CanonicalJSON: validation.Canonical.CanonicalBytes, SourceYAML: source, Resources: resources, Acknowledged: acknowledged,
			SubjectID: subjectID, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, Payload: payload,
			AuditID: auditID, EventID: eventID,
			Quota: application.InstanceQuotaRequest{ReservationID: reservationID, Amounts: quotaAmounts, Lifetime: lifetime},
		})
		if createErr == nil {
			return result, nil
		}
		var conflict *application.AllocationConflictError
		if !errors.As(createErr, &conflict) || len(conflict.Conflicts) == 0 {
			return EnqueueResult{}, createErr
		}
		if !mergePlanConflicts(exclusions, providerPlans, conflict.Conflicts) {
			return EnqueueResult{}, createErr
		}
	}
	return EnqueueResult{}, fmt.Errorf("allocation planning exhausted %d conflict attempts", application.MaximumAllocationPlanAttempts)
}

func (s *Service) DestroyInstance(ctx context.Context, instanceID domain.InstanceID, idempotencyKey string) (EnqueueResult, error) {
	return s.DestroyInstanceAs(ctx, LocalAdminID, instanceID, idempotencyKey)
}

func (s *Service) DestroyInstanceAs(ctx context.Context, subjectID domain.UserID, instanceID domain.InstanceID, idempotencyKey string) (EnqueueResult, error) {
	if _, err := domain.NewUserID(string(subjectID)); err != nil {
		return EnqueueResult{}, err
	}
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
		InstanceID: instanceID, OperationID: domain.OperationID(operationID), SubjectID: subjectID,
		IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, Payload: payload, AuditID: auditID, EventID: eventID,
	})
}

func validOwner(kind application.OwnerKind, id string) bool {
	var err error
	switch kind {
	case application.OwnerUser:
		_, err = domain.NewUserID(id)
	case application.OwnerTeam:
		_, err = domain.NewTeamID(id)
	case application.OwnerProject:
		_, err = domain.NewProjectID(id)
	default:
		return false
	}
	return err == nil
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

func (s *Service) ListInstancesByID(ctx context.Context, ids []domain.InstanceID) ([]InstanceRecord, error) {
	return s.store.ListInstancesByID(ctx, ids)
}

func (s *Service) ProviderKinds() []string {
	if s == nil || s.targets == nil {
		return []string{"docker"}
	}
	return s.targets.ProviderKinds()
}

func (s *Service) ListProviderTargets(ctx context.Context) ([]ProviderTargetStatus, error) {
	return s.listProviderTargets(ctx, nil)
}

func (s *Service) ProviderTargetIDs() []domain.ProviderTargetID {
	if s == nil || s.targets == nil {
		return []domain.ProviderTargetID{}
	}
	return s.targets.IDs()
}

func (s *Service) ListProviderTargetsByID(ctx context.Context, ids []domain.ProviderTargetID) ([]ProviderTargetStatus, error) {
	allowed := make(map[domain.ProviderTargetID]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return s.listProviderTargets(ctx, allowed)
}

func (s *Service) listProviderTargets(ctx context.Context, allowed map[domain.ProviderTargetID]bool) ([]ProviderTargetStatus, error) {
	if s == nil || s.targets == nil || s.store == nil {
		return []ProviderTargetStatus{}, nil
	}
	registered := s.targets.RegisteredTargets()
	result := make([]ProviderTargetStatus, 0, len(registered))
	now := time.Now()
	for _, target := range registered {
		if allowed != nil && !allowed[target.ID] {
			continue
		}
		status := ProviderTargetStatus{
			ID: target.ID, ProviderKind: target.ProviderKind, ConfigFingerprint: target.ConfigFingerprint,
			ProtocolMajor: target.ProtocolMajor, ProtocolMinor: target.ProtocolMinor,
			Features: append([]string(nil), target.Features...), Status: "undiscovered",
		}
		snapshot, err := s.store.GetTargetSnapshot(ctx, target.ID)
		if err == nil {
			status.DiscoveryJSON = append([]byte(nil), snapshot.DiscoveryJSON...)
			status.DiscoveredAt = snapshot.DiscoveredAt
			status.ValidUntil = snapshot.ValidUntil
			status.Status = "fresh"
			if !snapshot.ValidUntil.After(now) || snapshot.ProviderKind != target.ProviderKind || snapshot.ConfigFingerprint != target.ConfigFingerprint || snapshot.ProtocolMajor != target.ProtocolMajor || snapshot.ProtocolMinor != target.ProtocolMinor || !equalFeatureSets(snapshot.Features, target.Features) {
				status.Status = "stale"
			}
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if session, err := s.targets.Acquire(ctx, target.ID, application.InvocationScope{}, false); err != nil {
			status.Status = "unavailable"
		} else {
			session.Close()
		}
		result = append(result, status)
	}
	return result, nil
}

func (s *Service) RefreshTarget(ctx context.Context, targetID domain.ProviderTargetID) error {
	return s.refreshTarget(ctx, LocalAdminID, application.RecordActor{Kind: application.RecordActorSystem, ID: "system.target-refresh"}, targetID)
}

func (s *Service) RefreshTargetAs(ctx context.Context, subjectID domain.UserID, targetID domain.ProviderTargetID) error {
	return s.refreshTarget(ctx, subjectID, application.RecordActor{Kind: application.RecordActorUser, ID: string(subjectID)}, targetID)
}

func (s *Service) refreshTarget(ctx context.Context, subjectID domain.UserID, actor application.RecordActor, targetID domain.ProviderTargetID) error {
	if _, err := domain.NewUserID(string(subjectID)); err != nil {
		return err
	}
	invocationID, err := s.ids.New("inv")
	if err != nil {
		return err
	}
	scope := application.InvocationScope{
		InvocationID: domain.InvocationID(invocationID), InstanceID: "inst_target-discovery",
		ResourceID: "vm.target-discovery", ExpiresAt: time.Now().Add(45 * time.Second),
	}
	session, err := s.targets.AcquireForSubject(ctx, targetID, subjectID, scope, true)
	if err != nil {
		return fmt.Errorf("acquire provider target for discovery: %w", err)
	}
	defer session.Close()
	discoveryContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	response, err := session.Provider.Discover(discoveryContext, session.Context)
	if err != nil {
		return fmt.Errorf("provider target discovery failed: %w", err)
	}
	discoveredAt, validUntil := response.DiscoveredAt, response.ValidUntil
	if !validUntil.After(discoveredAt) || validUntil.After(discoveredAt.Add(24*time.Hour)) || !validUntil.After(time.Now()) || discoveredAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("provider target discovery returned an invalid validity interval")
	}
	seen := make(map[string]bool, len(response.Capabilities))
	for _, capability := range response.Capabilities {
		if capability.ID == "" || len(capability.ID) > 128 || seen[capability.ID] || capability.Support == "SUPPORT_UNSPECIFIED" {
			return fmt.Errorf("provider target discovery returned an invalid capability set")
		}
		seen[capability.ID] = true
	}
	encoded := response.JSON
	features := make([]string, 0, len(session.features))
	for feature := range session.features {
		features = append(features, feature)
	}
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return err
	}
	return s.store.SaveTargetSnapshot(ctx, TargetSnapshot{
		GovernanceRecordIDs: recordIDs, Actor: actor,
		ID: targetID, ProviderKind: session.ProviderKind, ConfigFingerprint: session.Context.ConfigFingerprint,
		ProtocolMajor: session.ProtocolMajor, ProtocolMinor: session.ProtocolMinor, Features: features,
		DiscoveryJSON: encoded, DiscoveredAt: discoveredAt, ValidUntil: validUntil,
	})
}

func (s *Service) requireNativeCapability(ctx context.Context, targetID domain.ProviderTargetID, featureID string) error {
	snapshot, err := s.store.GetTargetSnapshot(ctx, targetID)
	if err != nil {
		return &ValidationError{Issues: []Issue{{Code: "provider_capability_unavailable", Message: "The selected provider target has no current qualification evidence"}}}
	}
	session, err := s.targets.Acquire(ctx, targetID, application.InvocationScope{}, false)
	if err != nil {
		return &ValidationError{Issues: []Issue{{Code: "provider_target_unavailable", Message: "The selected provider target is unavailable"}}}
	}
	defer session.Close()
	currentFeatures := make([]string, 0, len(session.features))
	for feature := range session.features {
		currentFeatures = append(currentFeatures, feature)
	}
	if snapshot.ProviderKind != session.ProviderKind || snapshot.ConfigFingerprint != session.Context.ConfigFingerprint || snapshot.ProtocolMajor != session.ProtocolMajor || snapshot.ProtocolMinor != session.ProtocolMinor || !equalFeatureSets(snapshot.Features, currentFeatures) || !snapshot.ValidUntil.After(time.Now()) {
		return &ValidationError{Issues: []Issue{{Code: "provider_capability_stale", Message: "The selected provider target qualification evidence is stale"}}}
	}
	discovery, err := session.Provider.DecodeDiscovery(snapshot.DiscoveryJSON)
	if err != nil {
		return fmt.Errorf("decode persisted provider target discovery: %w", err)
	}
	if discovery.DiscoveredAt.UnixMicro() != snapshot.DiscoveredAt.UnixMicro() || discovery.ValidUntil.UnixMicro() != snapshot.ValidUntil.UnixMicro() {
		return fmt.Errorf("persisted discovery validity does not match snapshot")
	}
	for _, capability := range discovery.Capabilities {
		if capability.ID == featureID && capability.Support == "SUPPORT_NATIVE" && capability.Evidence != "" {
			return nil
		}
	}
	return &ValidationError{Issues: []Issue{{Code: "provider_capability_unavailable", Message: "The selected provider target is not qualified for the requested mutation"}}}
}

func equalFeatureSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, feature := range left {
		if seen[feature] {
			return false
		}
		seen[feature] = true
	}
	for _, feature := range right {
		if !seen[feature] {
			return false
		}
	}
	return true
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

func providerErrors(source []application.ProviderIssue) []Issue {
	result := make([]Issue, 0, len(source))
	for _, issue := range source {
		result = append(result, Issue{Code: issue.Code, Message: issue.Message, FieldPath: issue.FieldPath, ResourceID: issue.ResourceID})
	}
	return result
}
func providerWarnings(source []application.ProviderIssue) []Issue { return providerErrors(source) }

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

func verifyProviderPlanDigest(plan []byte, claimed string) error {
	if len(plan) == 0 || claimed == "" {
		return fmt.Errorf("provider plan and digest are required")
	}
	digest := rawDigest(plan)
	if claimed != digest.Hex() {
		return fmt.Errorf("claimed %q, computed %q", claimed, digest.Hex())
	}
	return nil
}

func rawDigest(value []byte) domain.SHA256Digest {
	digest := sha256.Sum256(value)
	parsed, _ := domain.ParseSHA256Digest(hex.EncodeToString(digest[:]))
	return parsed
}

func protocolClaims(source []application.AllocationKey) []application.AllocationKey {
	return append([]application.AllocationKey(nil), source...)
}

func mergePlanConflicts(exclusions map[domain.LogicalResourceID][]application.AllocationKey, plans map[domain.LogicalResourceID]plannedResource, conflicts []application.AllocationKey) bool {
	matched := false
	for _, conflict := range conflicts {
		for resourceID, plan := range plans {
			for _, claim := range plan.allocations {
				if claim == conflict {
					exclusions[resourceID] = appendUniqueClaim(exclusions[resourceID], conflict)
					matched = true
				}
			}
		}
	}
	return matched
}

func appendUniqueClaim(existing []application.AllocationKey, addition application.AllocationKey) []application.AllocationKey {
	for _, claim := range existing {
		if claim == addition {
			return existing
		}
	}
	return append(existing, addition)
}

func ownershipToken(targetID domain.ProviderTargetID, instanceID domain.InstanceID, resourceID domain.LogicalResourceID) string {
	digest := digestBytes("labcentral.ownership/v1", []byte("local"), []byte(targetID), []byte(instanceID), []byte(resourceID))
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

func (s *Service) requireTargetAdmission(ctx context.Context, actor domain.UserID, id domain.ProviderTargetID) error {
	session, err := s.targets.Acquire(ctx, id, application.InvocationScope{}, false)
	if err != nil {
		return err
	}
	required := append([]string(nil), session.Policy.NativeCapabilities...)
	session.Close()
	if len(required) == 0 {
		return nil
	}
	_ = s.RefreshTargetAs(ctx, actor, id)
	for _, feature := range required {
		if err := s.requireNativeCapability(ctx, id, feature); err != nil {
			return err
		}
	}
	return nil
}

// SetWorkLeaseDuration configures durable extension leases before workers start.
// Heartbeats remain frequent; a longer lease only increases crash recovery delay.
func (s *Service) SetWorkLeaseDuration(d time.Duration) error {
	if d < 30*time.Second || d > 5*time.Minute {
		return fmt.Errorf("worker lease duration must be between 30 seconds and 5 minutes")
	}
	s.workLeaseDuration = d
	return nil
}
func (s *Service) workLease() time.Duration {
	if s.workLeaseDuration == 0 {
		return 30 * time.Second
	}
	return s.workLeaseDuration
}
