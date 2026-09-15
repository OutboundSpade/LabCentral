package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

const NoAuthWarning = "Authentication is disabled. LabCentral is bound to loopback and grants every request local administrator privileges. Ordinary Docker containers are not safe for arbitrary hostile workloads, and published-port laptop labs cannot prove denied egress."

func register(api huma.API, service *controlplane.Service, exampleSource []byte, options Options, security *requestSecurity) {
	huma.Get(api, "/server-info", func(context.Context, *struct{}) (*struct{ Body ServerInfo }, error) {
		warning := ""
		if security.options.Mode == AuthModeNone {
			warning = options.NoAuthWarning
		}
		return &struct{ Body ServerInfo }{Body: ServerInfo{
			ServerVersion: options.ServerVersion, APIVersion: "v1", APIBase: "/api/v1", AuthMode: security.options.Mode, AuthMethods: authMethods(security.options.Mode),
			EnabledProviderKinds: service.ProviderKinds(), DefaultProviderTargetID: options.DefaultProviderTargetID, NoAuthWarning: warning,
			UIFeatures: uiFeatures(service),
		}}, nil
	})
	registerAuthentication(api, security)
	registerAdministration(api, security)

	type providerTargetsInput struct {
		ProjectID string `query:"projectId" maxLength:"128"`
	}
	huma.Get(api, "/provider-targets", func(ctx context.Context, input *providerTargetsInput) (*struct {
		Body struct {
			Items []ProviderTargetDTO `json:"items"`
		}
	}, error) {
		authorizationRequest := application.AuthorizationRequest{
			Action: application.ActionProviderTargetHealthRead, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}
		var allowedTargetIDs []domain.ProviderTargetID
		if security.options.Mode != AuthModeNone {
			projectID, err := domain.NewProjectID(input.ProjectID)
			if err != nil {
				return nil, huma.Error400BadRequest("Project ID is required and must be valid")
			}
			authorizationRequest.ScopeKind, authorizationRequest.ScopeID = application.ScopeProject, string(projectID)
		}
		if err := authorize(ctx, security, authorizationRequest); err != nil {
			return nil, err
		}
		if security.options.Mode != AuthModeNone {
			actor, _ := requestPrincipal(ctx)
			for _, targetID := range service.ProviderTargetIDs() {
				request := authorizationRequest
				request.SubjectID, request.TargetID = actor.user.ID, targetID
				if err := security.options.Authorizer.Authorize(ctx, request); err == nil {
					allowedTargetIDs = append(allowedTargetIDs, targetID)
				} else if !errors.Is(err, authorization.ErrDenied) {
					return nil, authorizationError(err, false)
				}
			}
		}
		var targets []controlplane.ProviderTargetStatus
		var err error
		if security.options.Mode == AuthModeNone {
			targets, err = service.ListProviderTargets(ctx)
		} else {
			targets, err = service.ListProviderTargetsByID(ctx, allowedTargetIDs)
		}
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items []ProviderTargetDTO `json:"items"`
			}
		}{}
		output.Body.Items = make([]ProviderTargetDTO, 0, len(targets))
		for _, target := range targets {
			item := ProviderTargetDTO{
				ID: string(target.ID), ProviderKind: target.ProviderKind, ConfigFingerprint: target.ConfigFingerprint,
				ProtocolMajor: target.ProtocolMajor, ProtocolMinor: target.ProtocolMinor,
				Features: append([]string(nil), target.Features...), Status: target.Status,
			}
			item.Example = providerExample(options, string(target.ID))
			if len(target.DiscoveryJSON) > 0 {
				item.Discovery = append(json.RawMessage(nil), target.DiscoveryJSON...)
				discoveredAt, validUntil := target.DiscoveredAt, target.ValidUntil
				item.DiscoveredAt, item.ValidUntil = &discoveredAt, &validUntil
			}
			output.Body.Items = append(output.Body.Items, item)
		}
		return output, nil
	})

	type RefreshTargetInput struct {
		ID string `path:"id"`
	}
	huma.Post(api, "/provider-targets/{id}/actions/refresh", func(ctx context.Context, input *RefreshTargetInput) (*struct{ Body ProviderTargetDTO }, error) {
		id, err := domain.NewProviderTargetID(input.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("Provider target ID is invalid")
		}
		actor, err := authorizeActor(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProviderTargetHealthRead, ScopeKind: application.ScopePlatform, ScopeID: "platform", TargetID: id,
		})
		if err != nil {
			return nil, err
		}
		if err := service.RefreshTargetAs(ctx, actor.user.ID, id); err != nil {
			return nil, huma.Error503ServiceUnavailable("Provider target discovery failed")
		}
		targets, err := service.ListProviderTargetsByID(ctx, []domain.ProviderTargetID{id})
		if err != nil {
			return nil, apiError(err)
		}
		for _, target := range targets {
			if target.ID == id {
				discoveredAt, validUntil := target.DiscoveredAt, target.ValidUntil
				return &struct{ Body ProviderTargetDTO }{Body: ProviderTargetDTO{
					ID: string(target.ID), ProviderKind: target.ProviderKind, ConfigFingerprint: target.ConfigFingerprint,
					ProtocolMajor: target.ProtocolMajor, ProtocolMinor: target.ProtocolMinor,
					Features: target.Features, Status: target.Status, Discovery: append(json.RawMessage(nil), target.DiscoveryJSON...),
					Example: providerExample(options, string(target.ID)), DiscoveredAt: &discoveredAt, ValidUntil: &validUntil,
				}}, nil
			}
		}
		return nil, huma.Error404NotFound("Provider target was not found")
	})

	huma.Get(api, "/session", func(ctx context.Context, _ *struct{}) (*struct{ Body Session }, error) {
		session, err := sessionForPrincipal(ctx, security)
		if err != nil {
			return nil, err
		}
		return &struct{ Body Session }{Body: session}, nil
	})

	type exampleInput struct {
		ProjectID string `query:"projectId" maxLength:"128"`
	}
	huma.Get(api, "/examples/docker-laptop", func(ctx context.Context, input *exampleInput) (*struct {
		Body struct {
			Source string `json:"source"`
		}
	}, error) {
		request := LabRequest{ProjectID: input.ProjectID}
		if _, _, _, _, err := authorizeLabRequest(ctx, security, request, application.ActionInstanceValidate, controlplane.LocalDockerTarget); err != nil {
			return nil, err
		}
		output := &struct {
			Body struct {
				Source string `json:"source"`
			}
		}{}
		output.Body.Source = string(exampleSource)
		return output, nil
	})

	registerTemplates(api, service, security)
	registerAuditAndEvents(api, service, security)
	registerQuotas(api, service, security)
	registerAccess(api, service, security)
	registerOperations(api, service, security)

	huma.Post(api, "/lab-validations", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body ValidationBody }, error) {
		if input.Body.Source == "" || input.Body.RevisionID != "" {
			return nil, huma.Error400BadRequest("Validation requires inline source and does not accept revisionId")
		}
		renderOptions, err := decodeRenderOptions(input.Body.Inputs)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstanceValidate, targetID); err != nil {
			return nil, err
		}
		result, err := service.ValidateTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride, renderOptions)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body ValidationBody }{Body: ValidationBody{
			CanonicalDigest: result.Canonical.Digest.String(), ResourceCount: len(result.Canonical.Resources),
			ProviderTargetID: string(result.TargetID), ProviderKind: result.ProviderKind, Warnings: result.Warnings,
		}}, nil
	})

	huma.Post(api, "/lab-plans", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body controlplane.PlanResult }, error) {
		if input.Body.Source == "" || input.Body.RevisionID != "" {
			return nil, huma.Error400BadRequest("Planning requires inline source and does not accept revisionId")
		}
		renderOptions, err := decodeRenderOptions(input.Body.Inputs)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstancePlan, targetID); err != nil {
			return nil, err
		}
		result, err := service.PlanTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride, renderOptions)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body controlplane.PlanResult }{Body: result}, nil
	})

	type CreateInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           LabRequest
	}
	type AcceptedOutput struct {
		Status   int
		Location string `header:"Location"`
		Body     struct {
			Operation OperationDTO `json:"operation"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "create-lab-instance", Method: http.MethodPost, Path: "/lab-instances", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *CreateInput) (*AcceptedOutput, error) {
			if (input.Body.Source == "") == (input.Body.RevisionID == "") {
				return nil, huma.Error400BadRequest("Exactly one of source or revisionId is required")
			}
			renderOptions, err := decodeRenderOptions(input.Body.Inputs)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
			actor, projectID, ownerKind, ownerID, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstanceCreate, targetID)
			if err != nil {
				return nil, err
			}
			var result controlplane.EnqueueResult
			lifetime := application.InstanceLifetimeRequest{TTLSeconds: input.Body.TTLSeconds, Persistent: input.Body.Persistent}
			if input.Body.RevisionID != "" {
				if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateRead, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
					return nil, err
				}
				revisionID, parseErr := domain.NewRevisionID(input.Body.RevisionID)
				if parseErr != nil {
					return nil, huma.Error404NotFound("Template revision was not found")
				}
				_, source, openErr := service.OpenTemplateRevisionForProject(ctx, actor.user.ID, projectID, revisionID)
				if openErr != nil {
					return nil, apiError(openErr)
				}
				result, err = service.CreateInstanceRevisionTargetForLifetime(ctx, actor.user.ID, projectID, ownerKind, ownerID, revisionID, source, lifetime, input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride, renderOptions)
			} else {
				result, err = service.CreateInstanceTargetForLifetime(ctx, actor.user.ID, projectID, ownerKind, ownerID, []byte(input.Body.Source), lifetime, input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride, renderOptions)
			}
			if err != nil {
				return nil, apiError(err)
			}
			operation, err := service.GetOperation(ctx, result.OperationID)
			if err != nil {
				return nil, apiError(err)
			}
			output := &AcceptedOutput{Status: http.StatusAccepted, Location: "/api/v1/operations/" + string(result.OperationID)}
			output.Body.Operation = operationDTO(operation)
			return output, nil
		})

	type CreateBatchInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           InstanceBatchRequest
	}
	type AcceptedBatchOutput struct {
		Status   int
		Location string `header:"Location"`
		Body     InstanceBatchDTO
	}
	huma.Register(api, huma.Operation{OperationID: "create-lab-instance-batch", Method: http.MethodPost, Path: "/lab-instance-batches", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *CreateBatchInput) (*AcceptedBatchOutput, error) {
			renderOptions, err := decodeRenderOptions(input.Body.Inputs)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			targetID := requestBatchTargetID(input.Body, options.DefaultProviderTargetID)
			actor, projectID, owners, err := authorizeLabBatchRequest(ctx, security, input.Body, targetID)
			if err != nil {
				return nil, err
			}
			if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateRead, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
				return nil, err
			}
			revisionID, err := domain.NewRevisionID(input.Body.RevisionID)
			if err != nil {
				return nil, huma.Error404NotFound("Template revision was not found")
			}
			_, source, err := service.OpenTemplateRevisionForProject(ctx, actor.user.ID, projectID, revisionID)
			if err != nil {
				return nil, apiError(err)
			}
			record, err := service.CreateInstanceBatchRevisionTargetForLifetime(ctx, actor.user.ID, projectID, owners,
				revisionID, source, application.InstanceLifetimeRequest{TTLSeconds: input.Body.TTLSeconds, Persistent: input.Body.Persistent},
				input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride, renderOptions)
			if err != nil {
				return nil, apiError(err)
			}
			body, err := instanceBatchDTO(ctx, service, record)
			if err != nil {
				return nil, apiError(err)
			}
			return &AcceptedBatchOutput{
				Status: http.StatusAccepted, Location: "/api/v1/lab-instance-batches/" + string(record.ID), Body: body,
			}, nil
		})

	type InstanceBatchInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/lab-instance-batches/{id}", func(ctx context.Context, input *InstanceBatchInput) (*struct{ Body InstanceBatchDTO }, error) {
		batchID, err := domain.NewInstanceBatchID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Resource was not found")
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		if security.options.Mode != AuthModeNone {
			if _, err := security.options.Authorizer.AuthorizeInstanceBatch(ctx, actor.user.ID, application.ActionInstanceBatchCreate, batchID); err != nil {
				return nil, authorizationError(err, true)
			}
		}
		record, err := service.GetInstanceBatch(ctx, batchID)
		if err != nil {
			return nil, apiError(err)
		}
		body, err := instanceBatchDTO(ctx, service, record)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body InstanceBatchDTO }{Body: body}, nil
	})

	huma.Get(api, "/lab-instances", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items      []InstanceDTO `json:"items"`
			NextCursor string        `json:"nextCursor,omitempty"`
		}
	}, error) {
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		var instances []controlplane.InstanceRecord
		var err error
		if security.options.Mode == AuthModeNone {
			instances, err = service.ListInstances(ctx)
		} else {
			ids, authorizationErr := security.options.Authorizer.AuthorizedInstanceIDs(ctx, actor.user.ID, application.ActionInstanceRead)
			if authorizationErr != nil {
				return nil, authorizationError(authorizationErr, false)
			}
			instances, err = service.ListInstancesByID(ctx, ids)
		}
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []InstanceDTO `json:"items"`
				NextCursor string        `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]InstanceDTO, 0, len(instances))
		for _, instance := range instances {
			_, resources, err := service.GetInstance(ctx, instance.ID)
			if err != nil {
				return nil, apiError(err)
			}
			output.Body.Items = append(output.Body.Items, instanceDTO(instance, resources))
		}
		return output, nil
	})

	type InstanceInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/lab-instances/{id}", func(ctx context.Context, input *InstanceInput) (*struct {
		ETag string `header:"ETag"`
		Body InstanceDTO
	}, error) {
		if err := authorizeInstance(ctx, security, application.ActionInstanceRead, domain.InstanceID(input.ID)); err != nil {
			return nil, err
		}
		instance, resources, err := service.GetInstance(ctx, domain.InstanceID(input.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &struct {
			ETag string `header:"ETag"`
			Body InstanceDTO
		}{ETag: fmt.Sprintf(`"%d"`, instance.ResourceVersion), Body: instanceDTO(instance, resources)}, nil
	})

	type DestroyInput struct {
		ID             string `path:"id"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct{}
	}
	huma.Register(api, huma.Operation{OperationID: "destroy-lab-instance", Method: http.MethodPost, Path: "/lab-instances/{id}/actions/destroy", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *DestroyInput) (*AcceptedOutput, error) {
			actor, err := authorizeInstanceActor(ctx, security, application.ActionInstanceDestroy, domain.InstanceID(input.ID))
			if err != nil {
				return nil, err
			}
			result, err := service.DestroyInstanceAs(ctx, actor.user.ID, domain.InstanceID(input.ID), input.IdempotencyKey)
			if err != nil {
				return nil, apiError(err)
			}
			operation, err := service.GetOperation(ctx, result.OperationID)
			if err != nil {
				return nil, apiError(err)
			}
			output := &AcceptedOutput{Status: http.StatusAccepted, Location: "/api/v1/operations/" + string(result.OperationID)}
			output.Body.Operation = operationDTO(operation)
			return output, nil
		})

	type ExtendTTLInput struct {
		ID             string `path:"id"`
		IfMatch        string `header:"If-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			TTLSeconds uint64 `json:"ttlSeconds" minimum:"1" maximum:"31622400"`
		}
	}
	huma.Post(api, "/lab-instances/{id}/actions/extend-ttl", func(ctx context.Context, input *ExtendTTLInput) (*versionedOutput[InstanceLifetimeDTO], error) {
		instanceID, err := domain.NewInstanceID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("Resource was not found")
		}
		actor, err := authorizeInstanceActor(ctx, security, application.ActionInstanceTTLExtend, instanceID)
		if err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		instance, err := service.RenewInstanceLifetime(ctx, actor.user.ID, instanceID, input.Body.TTLSeconds, expected, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[InstanceLifetimeDTO]{ETag: resourceETag(instance.ResourceVersion), Body: instanceLifetimeDTO(instance)}, nil
	})

	type OperationInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/operations/{id}", func(ctx context.Context, input *OperationInput) (*struct{ Body OperationDTO }, error) {
		if err := authorizeOperation(ctx, security, application.ActionInstanceRead, domain.OperationID(input.ID)); err != nil {
			return nil, err
		}
		operation, err := service.GetOperation(ctx, domain.OperationID(input.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body OperationDTO }{Body: operationDTO(operation)}, nil
	})
}

func providerExample(options Options, targetID string) *ProviderExample {
	example, exists := options.ProviderExamples[targetID]
	if !exists || example.Source == "" {
		return nil
	}
	result := &ProviderExample{Source: example.Source}
	if len(example.ProviderOverride) > 0 {
		result.ProviderOverride = append(json.RawMessage(nil), example.ProviderOverride...)
	}
	return result
}

func operationDTO(source controlplane.OperationRecord) OperationDTO {
	return OperationDTO{ID: string(source.ID), InstanceID: string(source.InstanceID), Kind: string(source.Kind), State: string(source.State), Message: source.Message, ErrorCode: source.ErrorCode, Attempt: source.Attempt, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt}
}

func instanceDTO(source controlplane.InstanceRecord, resources []controlplane.ResourceRecord) InstanceDTO {
	outputs := make(map[string]any)
	_ = json.Unmarshal(source.OutputsJSON, &outputs)
	result := InstanceDTO{
		ID: string(source.ID), RevisionID: string(source.RevisionID), ProjectID: string(source.ProjectID), OwnerKind: string(source.OwnerKind), OwnerID: source.OwnerID, CreatedBy: string(source.CreatedBy),
		ProviderTargetID: string(source.ProviderTargetID), Phase: string(source.Phase), DesiredLifecycle: string(source.DesiredLifecycle),
		CanonicalDigest: source.CanonicalDigest.String(), ResourceVersion: uint64(source.ResourceVersion), Outputs: outputs,
		ErrorCode: source.ErrorCode, ErrorMessage: source.ErrorMessage, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
	if !source.ExpiresAt.IsZero() {
		expiresAt := source.ExpiresAt
		result.ExpiresAt = &expiresAt
	}
	if !source.MaximumExpiresAt.IsZero() {
		maximumExpiresAt := source.MaximumExpiresAt
		result.MaximumExpiresAt = &maximumExpiresAt
	}
	for _, resource := range resources {
		result.Resources = append(result.Resources, ResourceDTO{ID: string(resource.ID), Kind: string(resource.Kind), Phase: string(resource.Phase), ErrorCode: resource.ErrorCode})
	}
	return result
}

func uiFeatures(service *controlplane.Service) []string {
	features := []string{"docker-example-a", "provider-targets", "validate", "plan", "apply", "destroy"}
	if service.AccessAvailable() {
		features = append(features, "browser-vnc")
	}
	return features
}

func requestTargetID(request LabRequest, configuredDefault string) domain.ProviderTargetID {
	if request.ProviderTargetID == "" {
		if configuredDefault != "" {
			return domain.ProviderTargetID(configuredDefault)
		}
		return controlplane.LocalDockerTarget
	}
	return domain.ProviderTargetID(request.ProviderTargetID)
}

func requestBatchTargetID(request InstanceBatchRequest, configuredDefault string) domain.ProviderTargetID {
	if request.ProviderTargetID == "" {
		if configuredDefault != "" {
			return domain.ProviderTargetID(configuredDefault)
		}
		return controlplane.LocalDockerTarget
	}
	return domain.ProviderTargetID(request.ProviderTargetID)
}

func instanceBatchDTO(ctx context.Context, service *controlplane.Service, source controlplane.InstanceBatchRecord) (InstanceBatchDTO, error) {
	result := InstanceBatchDTO{
		ID: string(source.ID), RevisionID: string(source.RevisionID), ProjectID: string(source.ProjectID),
		CreatedBy: string(source.CreatedBy), ProviderTargetID: string(source.ProviderTargetID),
		Items: make([]InstanceBatchItemDTO, 0, len(source.Items)), CreatedAt: source.CreatedAt, Replay: source.Replay,
	}
	for _, item := range source.Items {
		operation, err := service.GetOperation(ctx, item.OperationID)
		if err != nil {
			return InstanceBatchDTO{}, err
		}
		result.Items = append(result.Items, InstanceBatchItemDTO{
			Ordinal: item.Ordinal, OwnerKind: string(item.OwnerKind), OwnerID: item.OwnerID,
			InstanceID: string(item.InstanceID), Operation: operationDTO(operation),
		})
	}
	return result, nil
}
