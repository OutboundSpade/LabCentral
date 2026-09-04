package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

const NoAuthWarning = "Authentication is disabled. LabCentral is bound to loopback and grants every request local administrator privileges. Ordinary Docker containers are not safe for arbitrary hostile workloads, and published-port laptop labs cannot prove denied egress."

type Server struct {
	Handler http.Handler
	API     huma.API
}

type versionedOutput[T any] struct {
	ETag string `header:"ETag"`
	Body T
}

type Options struct {
	NoAuthWarning           string
	DefaultProviderTargetID string
	ProviderExamples        map[string]ProviderExample
	Authentication          AuthenticationOptions
}

type ProviderExample struct {
	Source           string          `json:"source"`
	ProviderOverride json.RawMessage `json:"providerOverride,omitempty"`
}

func New(service *controlplane.Service, exampleSource []byte, static fs.FS, options Options) (*Server, error) {
	if service == nil || len(exampleSource) == 0 || static == nil {
		return nil, fmt.Errorf("control-plane service, Example A, and static assets are required")
	}
	mux := http.NewServeMux()
	security, err := newRequestSecurity(options.Authentication)
	if err != nil {
		return nil, err
	}
	config := huma.DefaultConfig("LabCentral API", "1.0.0")
	config.Info.Description = "Canonical local LabCentral control-plane API. Mutations are durable asynchronous operations."
	api := humago.NewWithPrefix(mux, "/api/v1", config)
	if options.NoAuthWarning == "" {
		options.NoAuthWarning = NoAuthWarning
	}
	register(api, service, exampleSource, options, security)
	mux.Handle("/", staticHandler(static))
	return &Server{Handler: securityHeaders(security.wrap(mux)), API: api}, nil
}

type ServerInfo struct {
	ServerVersion           string   `json:"serverVersion"`
	APIVersion              string   `json:"apiVersion"`
	APIBase                 string   `json:"apiBase"`
	AuthMode                string   `json:"authMode"`
	AuthMethods             []string `json:"authMethods"`
	EnabledProviderKinds    []string `json:"enabledProviderKinds"`
	DefaultProviderTargetID string   `json:"defaultProviderTargetId"`
	NoAuthWarning           string   `json:"noAuthWarning"`
	UIFeatures              []string `json:"uiFeatures"`
}

type Session struct {
	SubjectID   string   `json:"subjectId"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
}

type LabRequest struct {
	Source                         string          `json:"source,omitempty" maxLength:"8388608"`
	RevisionID                     string          `json:"revisionId,omitempty" maxLength:"128"`
	AcknowledgeDockerIsolationRisk bool            `json:"acknowledgeDockerIsolationRisk"`
	ProjectID                      string          `json:"projectId,omitempty" maxLength:"128"`
	OwnerKind                      string          `json:"ownerKind,omitempty" enum:"user,team,project"`
	OwnerID                        string          `json:"ownerId,omitempty" maxLength:"128"`
	TTLSeconds                     uint64          `json:"ttlSeconds,omitempty" maximum:"31622400"`
	Persistent                     bool            `json:"persistent,omitempty"`
	ProviderTargetID               string          `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage `json:"providerOverride,omitempty"`
}

type InstanceBatchOwnerRequest struct {
	OwnerKind string `json:"ownerKind" enum:"user,team"`
	OwnerID   string `json:"ownerId" minLength:"1" maxLength:"128"`
}

type InstanceBatchRequest struct {
	RevisionID                     string                      `json:"revisionId" minLength:"1" maxLength:"128"`
	ProjectID                      string                      `json:"projectId" minLength:"1" maxLength:"128"`
	Owners                         []InstanceBatchOwnerRequest `json:"owners" minItems:"1" maxItems:"100"`
	TTLSeconds                     uint64                      `json:"ttlSeconds,omitempty" maximum:"31622400"`
	Persistent                     bool                        `json:"persistent,omitempty"`
	AcknowledgeDockerIsolationRisk bool                        `json:"acknowledgeDockerIsolationRisk"`
	ProviderTargetID               string                      `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage             `json:"providerOverride,omitempty"`
}

type InstanceBatchItemDTO struct {
	Ordinal    uint32       `json:"ordinal"`
	OwnerKind  string       `json:"ownerKind"`
	OwnerID    string       `json:"ownerId"`
	InstanceID string       `json:"instanceId"`
	Operation  OperationDTO `json:"operation"`
}

type InstanceBatchDTO struct {
	ID               string                 `json:"id"`
	RevisionID       string                 `json:"revisionId"`
	ProjectID        string                 `json:"projectId"`
	CreatedBy        string                 `json:"createdBy"`
	ProviderTargetID string                 `json:"providerTargetId"`
	Items            []InstanceBatchItemDTO `json:"items"`
	CreatedAt        time.Time              `json:"createdAt"`
	Replay           bool                   `json:"replay,omitempty"`
}

type ProviderTargetDTO struct {
	ID                string           `json:"id"`
	ProviderKind      string           `json:"providerKind"`
	ConfigFingerprint string           `json:"configFingerprint"`
	ProtocolMajor     uint32           `json:"protocolMajor"`
	ProtocolMinor     uint32           `json:"protocolMinor"`
	Features          []string         `json:"features"`
	Status            string           `json:"status"`
	Discovery         json.RawMessage  `json:"discovery,omitempty"`
	Example           *ProviderExample `json:"example,omitempty"`
	DiscoveredAt      *time.Time       `json:"discoveredAt,omitempty"`
	ValidUntil        *time.Time       `json:"validUntil,omitempty"`
}

type ValidationBody struct {
	CanonicalDigest  string               `json:"canonicalDigest"`
	ResourceCount    int                  `json:"resourceCount"`
	ProviderTargetID string               `json:"providerTargetId"`
	ProviderKind     string               `json:"providerKind"`
	Warnings         []controlplane.Issue `json:"warnings"`
}

type OperationDTO struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instanceId"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Message    string    `json:"message"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	Attempt    uint32    `json:"attempt"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type ResourceDTO struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type InstanceDTO struct {
	ID               string         `json:"id"`
	RevisionID       string         `json:"revisionId,omitempty"`
	ProjectID        string         `json:"projectId"`
	OwnerKind        string         `json:"ownerKind"`
	OwnerID          string         `json:"ownerId"`
	CreatedBy        string         `json:"createdBy"`
	ProviderTargetID string         `json:"providerTargetId"`
	Phase            string         `json:"phase"`
	DesiredLifecycle string         `json:"desiredLifecycle"`
	CanonicalDigest  string         `json:"canonicalDigest"`
	ResourceVersion  uint64         `json:"resourceVersion"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []ResourceDTO  `json:"resources,omitempty"`
	ErrorCode        string         `json:"errorCode,omitempty"`
	ErrorMessage     string         `json:"errorMessage,omitempty"`
	ExpiresAt        *time.Time     `json:"expiresAt,omitempty"`
	MaximumExpiresAt *time.Time     `json:"maximumExpiresAt,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type LabTemplateDTO struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	ResourceVersion uint64    `json:"resourceVersion"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type TemplateRevisionDTO struct {
	ID               string    `json:"id"`
	TemplateID       string    `json:"templateId"`
	RevisionNumber   uint64    `json:"revisionNumber"`
	BundleArtifact   string    `json:"bundleArtifact"`
	BundleDigest     string    `json:"bundleDigest"`
	BundleSizeBytes  uint64    `json:"bundleSizeBytes"`
	AuthorAPIVersion string    `json:"authorApiVersion"`
	RendererVersion  string    `json:"rendererVersion"`
	CanonicalVersion string    `json:"canonicalVersion"`
	CanonicalDigest  string    `json:"canonicalDigest"`
	PublishedBy      string    `json:"publishedBy"`
	CreatedAt        time.Time `json:"createdAt"`
}

type AuditRecordDTO struct {
	ID             string                    `json:"id"`
	StreamPosition uint64                    `json:"streamPosition"`
	SchemaVersion  string                    `json:"schemaVersion"`
	Category       application.AuditCategory `json:"category"`
	Action         string                    `json:"action"`
	Outcome        application.AuditOutcome  `json:"outcome"`
	Scope          application.RecordScope   `json:"scope"`
	Actor          application.RecordActor   `json:"actor"`
	AggregateType  string                    `json:"aggregateType"`
	AggregateID    string                    `json:"aggregateId"`
	Links          application.RecordLinks   `json:"links"`
	Payload        json.RawMessage           `json:"payload"`
	OccurredAt     time.Time                 `json:"occurredAt"`
}

type EventRecordDTO struct {
	ID                string                  `json:"id"`
	StreamPosition    uint64                  `json:"streamPosition"`
	AggregateSequence domain.EventSequence    `json:"aggregateSequence"`
	SchemaVersion     string                  `json:"schemaVersion"`
	Type              string                  `json:"type"`
	Scope             application.RecordScope `json:"scope"`
	Actor             application.RecordActor `json:"actor"`
	AggregateType     string                  `json:"aggregateType"`
	AggregateID       string                  `json:"aggregateId"`
	Links             application.RecordLinks `json:"links"`
	Payload           json.RawMessage         `json:"payload"`
	OccurredAt        time.Time               `json:"occurredAt"`
}

type QuotaLimitMutationBody struct {
	Limit uint64 `json:"limit" maximum:"9223372036854775807"`
}

type LifetimePolicyMutationBody struct {
	DefaultTTLSeconds       uint64 `json:"defaultTtlSeconds" minimum:"1" maximum:"31622400"`
	MaximumTTLSeconds       uint64 `json:"maximumTtlSeconds" minimum:"1" maximum:"31622400"`
	RenewalWindowSeconds    uint64 `json:"renewalWindowSeconds" maximum:"31622400"`
	WarningBeforeSeconds    uint64 `json:"warningBeforeSeconds" maximum:"31622400"`
	DestructionGraceSeconds uint64 `json:"destructionGraceSeconds" maximum:"86400"`
	RenewalAllowed          bool   `json:"renewalAllowed"`
	PersistentAllowed       bool   `json:"persistentAllowed"`
}

type QuotaUsageDTO struct {
	Dimension application.QuotaDimension `json:"dimension"`
	Reserved  uint64                     `json:"reserved"`
	Committed uint64                     `json:"committed"`
	Retained  uint64                     `json:"retained"`
	Total     uint64                     `json:"total"`
	UpdatedAt time.Time                  `json:"updatedAt"`
}

type InstanceLifetimeDTO struct {
	InstanceID                   string    `json:"instanceId"`
	ExpiresAt                    time.Time `json:"expiresAt"`
	MaximumExpiresAt             time.Time `json:"maximumExpiresAt"`
	ExpirationWarningAt          time.Time `json:"expirationWarningAt"`
	ExpirationGraceSeconds       uint64    `json:"expirationGraceSeconds"`
	LifetimeRenewalAllowed       bool      `json:"lifetimeRenewalAllowed"`
	LifetimeRenewalWindowSeconds uint64    `json:"lifetimeRenewalWindowSeconds"`
	LifetimePolicyDigest         string    `json:"lifetimePolicyDigest"`
	ResourceVersion              uint64    `json:"resourceVersion"`
	UpdatedAt                    time.Time `json:"updatedAt"`
}

func register(api huma.API, service *controlplane.Service, exampleSource []byte, options Options, security *requestSecurity) {
	huma.Get(api, "/server-info", func(context.Context, *struct{}) (*struct{ Body ServerInfo }, error) {
		warning := ""
		if security.options.Mode == AuthModeNone {
			warning = options.NoAuthWarning
		}
		return &struct{ Body ServerInfo }{Body: ServerInfo{
			ServerVersion: "0.1.0", APIVersion: "v1", APIBase: "/api/v1", AuthMode: security.options.Mode, AuthMethods: authMethods(security.options.Mode),
			EnabledProviderKinds: service.ProviderKinds(), DefaultProviderTargetID: options.DefaultProviderTargetID, NoAuthWarning: warning,
			UIFeatures: []string{"docker-example-a", "provider-targets", "validate", "plan", "apply", "destroy"},
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

	huma.Post(api, "/lab-validations", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body ValidationBody }, error) {
		if input.Body.Source == "" || input.Body.RevisionID != "" {
			return nil, huma.Error400BadRequest("Validation requires inline source and does not accept revisionId")
		}
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstanceValidate, targetID); err != nil {
			return nil, err
		}
		result, err := service.ValidateTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride)
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
		targetID := requestTargetID(input.Body, options.DefaultProviderTargetID)
		if _, _, _, _, err := authorizeLabRequest(ctx, security, input.Body, application.ActionInstancePlan, targetID); err != nil {
			return nil, err
		}
		result, err := service.PlanTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, targetID, input.Body.ProviderOverride)
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
				result, err = service.CreateInstanceRevisionTargetForLifetime(ctx, actor.user.ID, projectID, ownerKind, ownerID, revisionID, source, lifetime, input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride)
			} else {
				result, err = service.CreateInstanceTargetForLifetime(ctx, actor.user.ID, projectID, ownerKind, ownerID, []byte(input.Body.Source), lifetime, input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride)
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
				input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, targetID, input.Body.ProviderOverride)
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

type authenticationOutput struct {
	Cookies []http.Cookie `header:"Set-Cookie"`
	Body    Session
}

func registerAuthentication(api huma.API, security *requestSecurity) {
	consumeIPLimit := func(ctx context.Context, dimension string, window time.Duration, limit uint32) error {
		address, ok := requestClientAddress(ctx)
		if !ok {
			return huma.Error500InternalServerError("Authentication request context is unavailable")
		}
		allowed, err := security.options.Manager.ConsumeAuthenticationAttempt(ctx, dimension, address, window, limit)
		if err != nil {
			return huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return authenticationRateLimitError(window)
		}
		return nil
	}
	type localLoginInput struct {
		Body struct {
			Username string `json:"username" minLength:"1" maxLength:"63"`
			Password string `json:"password" minLength:"14" maxLength:"1024"`
		}
	}
	huma.Post(api, "/auth/local/login", func(ctx context.Context, input *localLoginInput) (*authenticationOutput, error) {
		if security.options.Mode != AuthModeLocal {
			return nil, huma.Error404NotFound("Authentication method is not enabled")
		}
		allowed, err := security.options.Manager.ConsumeAuthenticationAttempt(ctx, "login-global", "installation", time.Minute, 300)
		if err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return nil, authenticationRateLimitError(time.Minute)
		}
		if err := consumeIPLimit(ctx, "login-ip", time.Minute, 300); err != nil {
			return nil, err
		}
		allowed, err = security.options.Manager.ConsumeAuthenticationAttempt(ctx, "login-user", input.Body.Username, 15*time.Minute, 5)
		if err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		if !allowed {
			return nil, authenticationRateLimitError(15 * time.Minute)
		}
		password := []byte(input.Body.Password)
		defer clear(password)
		credentials, err := security.options.Manager.AuthenticateLocal(ctx, input.Body.Username, password)
		if err != nil {
			return nil, huma.Error401Unauthorized("Credentials were rejected")
		}
		if err := security.options.Manager.ClearAuthenticationAttempts(ctx, "login-user", input.Body.Username); err != nil {
			return nil, huma.Error500InternalServerError("Authentication rate limit is unavailable")
		}
		user, err := security.options.Repository.UserByID(ctx, credentials.Session.UserID)
		if err != nil {
			return nil, huma.Error401Unauthorized("Credentials were rejected")
		}
		body, err := sessionForUser(ctx, security, user)
		if err != nil {
			return nil, err
		}
		return &authenticationOutput{Cookies: sessionCookies(credentials), Body: body}, nil
	})

	type localRecoveryInput struct {
		Body struct {
			Token       string `json:"token" minLength:"32" maxLength:"256"`
			NewPassword string `json:"newPassword" minLength:"14" maxLength:"1024"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "recover-local-password", Method: http.MethodPost, Path: "/auth/local/recover", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, input *localRecoveryInput) (*struct{ Status int }, error) {
			if security.options.Mode != AuthModeLocal {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "recovery-ip", 15*time.Minute, 10); err != nil {
				return nil, err
			}
			password := []byte(input.Body.NewPassword)
			defer clear(password)
			if err := security.options.Manager.RecoverLocalPassword(ctx, input.Body.Token, password); err != nil {
				return nil, huma.Error401Unauthorized("Recovery token was rejected")
			}
			return &struct{ Status int }{Status: http.StatusNoContent}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, _ *struct{}) (*struct {
			Status  int
			Cookies []http.Cookie `header:"Set-Cookie"`
		}, error) {
			if security.options.Mode == AuthModeNone {
				return nil, huma.Error404NotFound("Authentication is not enabled")
			}
			actor, ok := requestPrincipal(ctx)
			if !ok || actor.session.ID == "" {
				return nil, huma.Error401Unauthorized("Authentication is required")
			}
			if err := security.options.Manager.Revoke(ctx, actor.session.ID); err != nil {
				return nil, huma.Error500InternalServerError("Session revocation failed")
			}
			return &struct {
				Status  int
				Cookies []http.Cookie `header:"Set-Cookie"`
			}{Status: http.StatusNoContent, Cookies: expiredCookies()}, nil
		})

	type bearerTokenOutput struct {
		Body struct {
			Token     string    `json:"token"`
			TokenType string    `json:"tokenType"`
			ExpiresAt time.Time `json:"expiresAt"`
		}
	}
	huma.Post(api, "/auth/tokens", func(ctx context.Context, _ *struct{}) (*bearerTokenOutput, error) {
		if security.options.Mode == AuthModeNone {
			return nil, huma.Error404NotFound("Authentication is not enabled")
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		credentials, err := security.options.Manager.NewBearerForUser(ctx, actor.user.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("Bearer credential could not be issued")
		}
		output := &bearerTokenOutput{}
		output.Body.Token = credentials.CookieToken
		output.Body.TokenType = "Bearer"
		output.Body.ExpiresAt = credentials.Session.ExpiresAt
		return output, nil
	})

	type oidcBeginInput struct {
		Redirect string `query:"redirect" default:"/" maxLength:"2048"`
	}
	type redirectOutput struct {
		Status   int
		Location string        `header:"Location"`
		Cookies  []http.Cookie `header:"Set-Cookie,omitempty"`
	}
	huma.Register(api, huma.Operation{OperationID: "begin-oidc-login", Method: http.MethodGet, Path: "/auth/oidc/begin", DefaultStatus: http.StatusSeeOther},
		func(ctx context.Context, input *oidcBeginInput) (*redirectOutput, error) {
			if security.options.Mode != AuthModeOIDC {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "oidc-begin-ip", 10*time.Minute, 300); err != nil {
				return nil, err
			}
			result, err := security.options.OIDC.Begin(ctx, input.Redirect)
			if err != nil {
				return nil, huma.Error400BadRequest("OIDC login request was rejected")
			}
			maxAge := int(time.Until(result.ExpiresAt).Seconds())
			if maxAge < 1 {
				maxAge = 1
			}
			return &redirectOutput{Status: http.StatusSeeOther, Location: result.AuthorizationURL, Cookies: []http.Cookie{{
				Name: oidcBindingCookieName, Value: result.BrowserBinding, Path: "/", Secure: true, HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: result.ExpiresAt,
			}}}, nil
		})

	type oidcCallbackInput struct {
		State          string      `query:"state" required:"true" maxLength:"256"`
		Code           string      `query:"code" required:"true" maxLength:"4096"`
		BrowserBinding http.Cookie `cookie:"__Host-labcentral-oidc"`
	}
	huma.Register(api, huma.Operation{OperationID: "complete-oidc-login", Method: http.MethodGet, Path: "/auth/oidc/callback", DefaultStatus: http.StatusSeeOther},
		func(ctx context.Context, input *oidcCallbackInput) (*redirectOutput, error) {
			if security.options.Mode != AuthModeOIDC {
				return nil, huma.Error404NotFound("Authentication method is not enabled")
			}
			if err := consumeIPLimit(ctx, "oidc-callback-ip", 10*time.Minute, 300); err != nil {
				return nil, err
			}
			result, err := security.options.OIDC.Callback(ctx, input.State, input.Code, input.BrowserBinding.Value)
			if err != nil {
				return nil, huma.Error401Unauthorized("OIDC login response was rejected")
			}
			cookies := sessionCookies(result.Credentials)
			cookies = append(cookies, http.Cookie{
				Name: oidcBindingCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
			})
			return &redirectOutput{Status: http.StatusSeeOther, Location: result.RedirectPath, Cookies: cookies}, nil
		})
}

func authenticationRateLimitError(window time.Duration) error {
	retrySeconds := int64((window + time.Second - 1) / time.Second)
	return huma.ErrorWithHeaders(huma.Error429TooManyRequests("Too many authentication attempts"), http.Header{
		"Retry-After": []string{strconv.FormatInt(retrySeconds, 10)},
	})
}

func registerTemplates(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type projectInput struct {
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
		Cursor    string `query:"cursor" maxLength:"256"`
		Limit     uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}
	projectScope := func(value string) (domain.ProjectID, error) {
		projectID, err := domain.NewProjectID(value)
		if err != nil {
			return "", huma.Error400BadRequest("Project ID is invalid")
		}
		return projectID, nil
	}
	huma.Get(api, "/lab-templates", func(ctx context.Context, input *projectInput) (*struct {
		Body struct {
			Items      []LabTemplateDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		projectID, err := projectScope(input.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateRead, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
			return nil, err
		}
		after, err := decodeLabTemplateCursor(input.Cursor)
		if err != nil {
			return nil, huma.Error400BadRequest("Lab Template cursor is invalid")
		}
		page, err := service.ListLabTemplates(ctx, controlplane.LabTemplateListRequest{ProjectID: projectID, After: after, Limit: input.Limit})
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []LabTemplateDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]LabTemplateDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, labTemplateDTO(record))
		}
		output.Body.NextCursor = encodeLabTemplateCursor(page.NextCursor)
		return output, nil
	})

	type createTemplateInput struct {
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			ProjectID   string `json:"projectId" minLength:"1" maxLength:"128"`
			Name        string `json:"name" minLength:"1" maxLength:"63"`
			Description string `json:"description,omitempty" maxLength:"4096"`
		}
	}
	huma.Post(api, "/lab-templates", func(ctx context.Context, input *createTemplateInput) (*versionedOutput[LabTemplateDTO], error) {
		projectID, err := projectScope(input.Body.ProjectID)
		if err != nil {
			return nil, err
		}
		actor, err := authorizeActor(ctx, security, application.AuthorizationRequest{Action: application.ActionTemplateCreate, ScopeKind: application.ScopeProject, ScopeID: string(projectID)})
		if err != nil {
			return nil, err
		}
		record, err := service.CreateLabTemplate(ctx, actor.user.ID, projectID, input.Body.Name, input.Body.Description, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type templateInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
	}
	loadTemplate := func(ctx context.Context, id, project string, action application.PermissionAction) (controlplane.LabTemplateRecord, error) {
		projectID, err := projectScope(project)
		if err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: action, ScopeKind: application.ScopeProject, ScopeID: string(projectID)}); err != nil {
			return controlplane.LabTemplateRecord{}, err
		}
		templateID, err := domain.NewTemplateID(id)
		if err != nil {
			return controlplane.LabTemplateRecord{}, huma.Error404NotFound("Lab template was not found")
		}
		record, err := service.GetLabTemplate(ctx, templateID)
		if err != nil || record.ProjectID != projectID {
			return controlplane.LabTemplateRecord{}, huma.Error404NotFound("Lab template was not found")
		}
		return record, nil
	}
	huma.Get(api, "/lab-templates/{id}", func(ctx context.Context, input *templateInput) (*versionedOutput[LabTemplateDTO], error) {
		record, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type updateTemplateInput struct {
		ID             string `path:"id" maxLength:"128"`
		ProjectID      string `query:"projectId" required:"true" maxLength:"128"`
		IfMatch        string `header:"If-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Name        string `json:"name" minLength:"1" maxLength:"63"`
			Description string `json:"description,omitempty" maxLength:"4096"`
		}
	}
	huma.Put(api, "/lab-templates/{id}", func(ctx context.Context, input *updateTemplateInput) (*versionedOutput[LabTemplateDTO], error) {
		record, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateUpdate)
		if err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		record, err = service.UpdateLabTemplate(ctx, actor.user.ID, record.ID, input.Body.Name, input.Body.Description, expected, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[LabTemplateDTO]{ETag: resourceETag(record.ResourceVersion), Body: labTemplateDTO(record)}, nil
	})

	type revisionListInput struct {
		ID        string `path:"id" maxLength:"128"`
		ProjectID string `query:"projectId" required:"true" maxLength:"128"`
		Cursor    string `query:"cursor" maxLength:"256"`
		Limit     uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}
	huma.Get(api, "/lab-templates/{id}/revisions", func(ctx context.Context, input *revisionListInput) (*struct {
		Body struct {
			Items      []TemplateRevisionDTO `json:"items"`
			NextCursor string                `json:"nextCursor,omitempty"`
		}
	}, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		after, err := decodeTemplateRevisionCursor(input.Cursor)
		if err != nil {
			return nil, huma.Error400BadRequest("Template Revision cursor is invalid")
		}
		page, err := service.ListTemplateRevisions(ctx, controlplane.TemplateRevisionListRequest{TemplateID: template.ID, After: after, Limit: input.Limit})
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []TemplateRevisionDTO `json:"items"`
				NextCursor string                `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]TemplateRevisionDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, templateRevisionDTO(record))
		}
		output.Body.NextCursor = encodeTemplateRevisionCursor(page.NextCursor)
		return output, nil
	})

	type publishRevisionInput struct {
		ID             string `path:"id" maxLength:"128"`
		ProjectID      string `query:"projectId" required:"true" maxLength:"128"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           struct {
			Source string `json:"source" minLength:"1" maxLength:"8388608"`
		}
	}
	huma.Post(api, "/lab-templates/{id}/revisions", func(ctx context.Context, input *publishRevisionInput) (*struct{ Body TemplateRevisionDTO }, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRevisionPublish)
		if err != nil {
			return nil, err
		}
		actor, ok := requestPrincipal(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication is required")
		}
		revision, err := service.PublishTemplateRevision(ctx, actor.user.ID, template.ID, []byte(input.Body.Source), input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body TemplateRevisionDTO }{Body: templateRevisionDTO(revision)}, nil
	})

	type revisionInput struct {
		ID         string `path:"id" maxLength:"128"`
		RevisionID string `path:"revisionId" maxLength:"128"`
		ProjectID  string `query:"projectId" required:"true" maxLength:"128"`
	}
	huma.Get(api, "/lab-templates/{id}/revisions/{revisionId}", func(ctx context.Context, input *revisionInput) (*struct{ Body TemplateRevisionDTO }, error) {
		template, err := loadTemplate(ctx, input.ID, input.ProjectID, application.ActionTemplateRead)
		if err != nil {
			return nil, err
		}
		revisionID, err := domain.NewRevisionID(input.RevisionID)
		if err != nil {
			return nil, huma.Error404NotFound("Template revision was not found")
		}
		revision, err := service.GetTemplateRevision(ctx, revisionID)
		if err != nil || revision.TemplateID != template.ID {
			return nil, huma.Error404NotFound("Template revision was not found")
		}
		return &struct{ Body TemplateRevisionDTO }{Body: templateRevisionDTO(revision)}, nil
	})
}

type RecordQueryInput struct {
	OrganizationID string `query:"organizationId" maxLength:"128"`
	ProjectID      string `query:"projectId" maxLength:"128"`
	ActorID        string `query:"actorId" maxLength:"128"`
	AggregateType  string `query:"aggregateType" maxLength:"128"`
	AggregateID    string `query:"aggregateId" maxLength:"128"`
	OccurredFrom   string `query:"occurredFrom" maxLength:"64"`
	OccurredBefore string `query:"occurredBefore" maxLength:"64"`
	Cursor         string `query:"cursor" maxLength:"64"`
	Limit          uint32 `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

func registerAuditAndEvents(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type auditInput struct {
		RecordQueryInput
		Category application.AuditCategory `query:"category" enum:"authentication,authorization,target,template,operation,access,administrator"`
	}
	huma.Get(api, "/audit-records", func(ctx context.Context, input *auditInput) (*struct {
		Body struct {
			Items      []AuditRecordDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		filter := application.AuditRecordFilter{
			Scope: scope, ActorID: input.ActorID, Category: input.Category, AggregateType: input.AggregateType,
			AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: input.Limit,
		}
		if err := application.ValidateAuditRecordFilter(filter); err != nil {
			return nil, huma.Error400BadRequest("Audit record filter is invalid")
		}
		page, err := service.ListAuditRecords(ctx, filter)
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []AuditRecordDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]AuditRecordDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, auditRecordDTO(record))
		}
		output.Body.NextCursor = encodeRecordCursor(page.NextCursor)
		return output, nil
	})

	type eventInput struct {
		RecordQueryInput
		Type string `query:"type" maxLength:"128"`
	}
	huma.Get(api, "/events", func(ctx context.Context, input *eventInput) (*struct {
		Body struct {
			Items      []EventRecordDTO `json:"items"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
	}, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		filter := application.EventRecordFilter{
			Scope: scope, ActorID: input.ActorID, Type: input.Type, AggregateType: input.AggregateType,
			AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: input.Limit,
		}
		if err := application.ValidateEventRecordFilter(filter); err != nil {
			return nil, huma.Error400BadRequest("Event record filter is invalid")
		}
		page, err := service.ListEventRecords(ctx, filter)
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			Body struct {
				Items      []EventRecordDTO `json:"items"`
				NextCursor string           `json:"nextCursor,omitempty"`
			}
		}{}
		output.Body.Items = make([]EventRecordDTO, 0, len(page.Records))
		for _, record := range page.Records {
			output.Body.Items = append(output.Body.Items, eventRecordDTO(record))
		}
		output.Body.NextCursor = encodeRecordCursor(page.NextCursor)
		return output, nil
	})

	type exportInput struct {
		RecordQueryInput
		Category   application.AuditCategory `query:"category" enum:"authentication,authorization,target,template,operation,access,administrator"`
		MaxRecords uint32                    `query:"maxRecords" default:"1000" minimum:"1" maximum:"10000"`
		MaxBytes   uint64                    `query:"maxBytes" default:"8388608" minimum:"1" maximum:"67108864"`
	}
	type exportOutput struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		Truncated          string `header:"X-LabCentral-Export-Truncated"`
		NextCursor         string `header:"X-LabCentral-Next-Cursor"`
		Body               []byte
	}
	huma.Get(api, "/audit-records/export", func(ctx context.Context, input *exportInput) (*exportOutput, error) {
		scope, from, before, after, err := authorizeRecordQuery(ctx, security, input.RecordQueryInput)
		if err != nil {
			return nil, err
		}
		request := application.AuditExportRequest{
			Filter: application.AuditRecordFilter{
				Scope: scope, ActorID: input.ActorID, Category: input.Category, AggregateType: input.AggregateType,
				AggregateID: input.AggregateID, OccurredFrom: from, OccurredBefore: before, After: after, Limit: 1,
			},
			MaxRecords: input.MaxRecords, MaxBytes: input.MaxBytes,
		}
		if err := application.ValidateAuditExportRequest(request); err != nil {
			return nil, huma.Error400BadRequest("Audit export request is invalid")
		}
		result, body, err := service.ExportAuditRecords(ctx, request)
		if err != nil {
			return nil, apiError(err)
		}
		return &exportOutput{
			ContentType: "application/x-ndjson", ContentDisposition: `attachment; filename="labcentral-audit.ndjson"`,
			Truncated: strconv.FormatBool(result.Truncated), NextCursor: encodeRecordCursor(result.Last), Body: body,
		}, nil
	})
}

func authorizeRecordQuery(ctx context.Context, security *requestSecurity, input RecordQueryInput) (application.RecordScope, time.Time, time.Time, *application.RecordCursor, error) {
	var scope application.RecordScope
	authorizationScope := application.AuthorizationRequest{Action: application.ActionAuditRead, ScopeKind: application.ScopePlatform, ScopeID: "platform"}
	if input.OrganizationID != "" {
		organizationID, err := domain.NewOrganizationID(input.OrganizationID)
		if err != nil {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Organization ID is invalid")
		}
		scope.OrganizationID = organizationID
		authorizationScope.ScopeKind, authorizationScope.ScopeID = application.ScopeOrganization, string(organizationID)
	}
	if input.ProjectID != "" {
		if scope.OrganizationID == "" {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Project-scoped record queries require organizationId")
		}
		projectID, err := domain.NewProjectID(input.ProjectID)
		if err != nil {
			return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Project ID is invalid")
		}
		scope.ProjectID = projectID
		authorizationScope.ScopeKind, authorizationScope.ScopeID = application.ScopeProject, string(projectID)
	}
	if err := authorize(ctx, security, authorizationScope); err != nil {
		return scope, time.Time{}, time.Time{}, nil, err
	}
	from, err := parseRecordTime(input.OccurredFrom)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("occurredFrom must be an RFC3339 timestamp")
	}
	before, err := parseRecordTime(input.OccurredBefore)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("occurredBefore must be an RFC3339 timestamp")
	}
	after, err := decodeRecordCursor(input.Cursor)
	if err != nil {
		return scope, time.Time{}, time.Time{}, nil, huma.Error400BadRequest("Record cursor is invalid")
	}
	return scope, from, before, after, nil
}

func parseRecordTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func encodeRecordCursor(cursor *application.RecordCursor) string {
	if cursor == nil || cursor.StreamPosition == 0 {
		return ""
	}
	var bytes [9]byte
	bytes[0] = 1
	binary.BigEndian.PutUint64(bytes[1:], cursor.StreamPosition)
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

func decodeRecordCursor(value string) (*application.RecordCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 9 || decoded[0] != 1 {
		return nil, fmt.Errorf("invalid record cursor")
	}
	position := binary.BigEndian.Uint64(decoded[1:])
	if position == 0 {
		return nil, fmt.Errorf("invalid record cursor")
	}
	return &application.RecordCursor{StreamPosition: position}, nil
}

const (
	templateCursorKind byte = 1
	revisionCursorKind byte = 2
)

func encodeLabTemplateCursor(cursor *controlplane.LabTemplateCursor) string {
	if cursor == nil || cursor.CreatedAt.IsZero() || cursor.CreatedAt.UTC().UnixMicro() <= 0 || cursor.ID == "" {
		return ""
	}
	return encodeCollectionCursor(templateCursorKind, uint64(cursor.CreatedAt.UTC().UnixMicro()), string(cursor.ID))
}

func decodeLabTemplateCursor(value string) (*controlplane.LabTemplateCursor, error) {
	if value == "" {
		return nil, nil
	}
	position, id, err := decodeCollectionCursor(value, templateCursorKind)
	if err != nil || position > math.MaxInt64 {
		return nil, fmt.Errorf("invalid Lab Template cursor")
	}
	templateID, err := domain.NewTemplateID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid Lab Template cursor")
	}
	return &controlplane.LabTemplateCursor{CreatedAt: time.UnixMicro(int64(position)).UTC(), ID: templateID}, nil
}

func encodeTemplateRevisionCursor(cursor *controlplane.TemplateRevisionCursor) string {
	if cursor == nil || cursor.RevisionNumber == 0 || cursor.ID == "" {
		return ""
	}
	return encodeCollectionCursor(revisionCursorKind, cursor.RevisionNumber, string(cursor.ID))
}

func decodeTemplateRevisionCursor(value string) (*controlplane.TemplateRevisionCursor, error) {
	if value == "" {
		return nil, nil
	}
	position, id, err := decodeCollectionCursor(value, revisionCursorKind)
	if err != nil {
		return nil, fmt.Errorf("invalid Template Revision cursor")
	}
	revisionID, err := domain.NewRevisionID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid Template Revision cursor")
	}
	return &controlplane.TemplateRevisionCursor{RevisionNumber: position, ID: revisionID}, nil
}

func encodeCollectionCursor(kind byte, position uint64, id string) string {
	if position == 0 || len(id) == 0 || len(id) > 128 {
		return ""
	}
	encoded := make([]byte, 10+len(id))
	encoded[0], encoded[1] = 1, kind
	binary.BigEndian.PutUint64(encoded[2:10], position)
	copy(encoded[10:], id)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCollectionCursor(value string, expectedKind byte) (uint64, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < 11 || len(decoded) > 138 || decoded[0] != 1 || decoded[1] != expectedKind {
		return 0, "", fmt.Errorf("invalid collection cursor")
	}
	position := binary.BigEndian.Uint64(decoded[2:10])
	if position == 0 {
		return 0, "", fmt.Errorf("invalid collection cursor")
	}
	return position, string(decoded[10:]), nil
}

func registerQuotas(api huma.API, service *controlplane.Service, security *requestSecurity) {
	type QuotaScopeInput struct {
		ScopeKind      string `path:"scopeKind" enum:"platform,organization,project,team,user,target"`
		ScopeID        string `path:"scopeId" maxLength:"128"`
		OrganizationID string `query:"organizationId" maxLength:"128"`
	}
	type QuotaLimitInput struct {
		QuotaScopeInput
		Dimension string `path:"dimension" enum:"instances,vms,containers,cpu_millis,memory_bytes,storage_bytes,public_ips,published_ports,snapshots"`
	}
	parseScope := func(kind, id, organizationID string) (application.QuotaScope, error) {
		scope := application.QuotaScope{Kind: application.QuotaScopeKind(kind), ID: id, OrganizationID: domain.OrganizationID(organizationID)}
		if err := application.ValidateQuotaScope(scope); err != nil {
			return application.QuotaScope{}, huma.Error400BadRequest("Quota scope is invalid")
		}
		return scope, nil
	}
	authorizeScope := func(ctx context.Context, scope application.QuotaScope) (principal, error) {
		request := application.AuthorizationRequest{Action: application.ActionQuotaManage}
		switch scope.Kind {
		case application.QuotaScopePlatform, application.QuotaScopeTarget:
			request.ScopeKind, request.ScopeID = application.ScopePlatform, "platform"
		case application.QuotaScopeOrganization:
			request.ScopeKind, request.ScopeID = application.ScopeOrganization, scope.ID
		case application.QuotaScopeProject:
			request.ScopeKind, request.ScopeID = application.ScopeProject, scope.ID
		case application.QuotaScopeTeam:
			request.ScopeKind, request.ScopeID = application.ScopeTeam, scope.ID
		case application.QuotaScopeUser:
			request.ScopeKind, request.ScopeID = application.ScopeOrganization, string(scope.OrganizationID)
		default:
			return principal{}, huma.Error400BadRequest("Quota scope is invalid")
		}
		return authorizeActor(ctx, security, request)
	}

	huma.Get(api, "/quotas/{scopeKind}/{scopeId}/{dimension}", func(ctx context.Context, input *QuotaLimitInput) (*versionedOutput[application.QuotaLimitRecord], error) {
		scope, err := parseScope(input.ScopeKind, input.ScopeID, input.OrganizationID)
		if err != nil {
			return nil, err
		}
		if _, err := authorizeScope(ctx, scope); err != nil {
			return nil, err
		}
		dimension := application.QuotaDimension(input.Dimension)
		if err := application.ValidateQuotaDimension(dimension); err != nil {
			return nil, huma.Error400BadRequest("Quota dimension is invalid")
		}
		record, err := service.GetQuotaLimit(ctx, scope, dimension)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[application.QuotaLimitRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type putQuotaLimitInput struct {
		QuotaLimitInput
		IfMatch        string `header:"If-Match"`
		IfNoneMatch    string `header:"If-None-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           QuotaLimitMutationBody
	}
	huma.Put(api, "/quotas/{scopeKind}/{scopeId}/{dimension}", func(ctx context.Context, input *putQuotaLimitInput) (*versionedOutput[application.QuotaLimitRecord], error) {
		scope, err := parseScope(input.ScopeKind, input.ScopeID, input.OrganizationID)
		if err != nil {
			return nil, err
		}
		dimension := application.QuotaDimension(input.Dimension)
		if err := application.ValidateQuotaDimension(dimension); err != nil {
			return nil, huma.Error400BadRequest("Quota dimension is invalid")
		}
		actor, err := authorizeScope(ctx, scope)
		if err != nil {
			return nil, err
		}
		expected, err := expectedUpsertResourceVersion(input.IfMatch, input.IfNoneMatch)
		if err != nil {
			return nil, err
		}
		record, err := service.PutQuotaLimit(ctx, actor.user.ID, scope, dimension, input.Body.Limit, expected, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[application.QuotaLimitRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	huma.Get(api, "/quota-usage/{scopeKind}/{scopeId}", func(ctx context.Context, input *QuotaScopeInput) (*struct {
		CacheControl string `header:"Cache-Control"`
		Body         struct {
			Scope application.QuotaScope `json:"scope"`
			Items []QuotaUsageDTO        `json:"items"`
		}
	}, error) {
		scope, err := parseScope(input.ScopeKind, input.ScopeID, input.OrganizationID)
		if err != nil {
			return nil, err
		}
		if _, err := authorizeScope(ctx, scope); err != nil {
			return nil, err
		}
		records, err := service.ListQuotaUsage(ctx, scope)
		if err != nil {
			return nil, apiError(err)
		}
		output := &struct {
			CacheControl string `header:"Cache-Control"`
			Body         struct {
				Scope application.QuotaScope `json:"scope"`
				Items []QuotaUsageDTO        `json:"items"`
			}
		}{}
		output.CacheControl = "no-store"
		output.Body.Scope = scope
		output.Body.Items = make([]QuotaUsageDTO, 0, len(records))
		for _, record := range records {
			output.Body.Items = append(output.Body.Items, QuotaUsageDTO{
				Dimension: record.Dimension, Reserved: record.Reserved, Committed: record.Committed,
				Retained: record.Retained, Total: record.Total(), UpdatedAt: record.UpdatedAt,
			})
		}
		return output, nil
	})

	huma.Get(api, "/lifetime-policies/{scopeKind}/{scopeId}", func(ctx context.Context, input *QuotaScopeInput) (*versionedOutput[application.LifetimePolicyRecord], error) {
		scope, err := parseScope(input.ScopeKind, input.ScopeID, input.OrganizationID)
		if err != nil {
			return nil, err
		}
		if _, err := authorizeScope(ctx, scope); err != nil {
			return nil, err
		}
		record, err := service.GetLifetimePolicy(ctx, scope)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[application.LifetimePolicyRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type putLifetimePolicyInput struct {
		QuotaScopeInput
		IfMatch        string `header:"If-Match"`
		IfNoneMatch    string `header:"If-None-Match"`
		IdempotencyKey string `header:"Idempotency-Key" minLength:"8" maxLength:"128"`
		Body           LifetimePolicyMutationBody
	}
	huma.Put(api, "/lifetime-policies/{scopeKind}/{scopeId}", func(ctx context.Context, input *putLifetimePolicyInput) (*versionedOutput[application.LifetimePolicyRecord], error) {
		scope, err := parseScope(input.ScopeKind, input.ScopeID, input.OrganizationID)
		if err != nil {
			return nil, err
		}
		policy := application.LifetimePolicyRecord{
			Scope: scope, DefaultTTLSeconds: input.Body.DefaultTTLSeconds,
			MaximumTTLSeconds: input.Body.MaximumTTLSeconds, RenewalWindowSeconds: input.Body.RenewalWindowSeconds,
			WarningBeforeSeconds: input.Body.WarningBeforeSeconds, DestructionGraceSecs: input.Body.DestructionGraceSeconds,
			RenewalAllowed: input.Body.RenewalAllowed, PersistentAllowed: input.Body.PersistentAllowed,
		}
		if err := application.ValidateLifetimePolicy(policy); err != nil {
			return nil, huma.Error400BadRequest("Lifetime policy is invalid")
		}
		actor, err := authorizeScope(ctx, policy.Scope)
		if err != nil {
			return nil, err
		}
		expected, err := expectedUpsertResourceVersion(input.IfMatch, input.IfNoneMatch)
		if err != nil {
			return nil, err
		}
		record, err := service.PutLifetimePolicy(ctx, actor.user.ID, policy, expected, input.IdempotencyKey)
		if err != nil {
			return nil, apiError(err)
		}
		return &versionedOutput[application.LifetimePolicyRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
}

func registerAdministration(api huma.API, security *requestSecurity) {
	if security.options.Mode == AuthModeNone {
		return
	}
	type userInput struct {
		Body struct {
			ID              string `json:"id" minLength:"1" maxLength:"128"`
			Username        string `json:"username" minLength:"1" maxLength:"63"`
			DisplayName     string `json:"displayName" minLength:"1" maxLength:"128"`
			Password        string `json:"password,omitempty" maxLength:"1024"`
			ExternalSubject string `json:"externalSubject,omitempty" maxLength:"512"`
		}
	}
	huma.Post(api, "/users", func(ctx context.Context, input *userInput) (*versionedOutput[application.UserRecord], error) {
		if security.options.Mode == AuthModeNone {
			return nil, huma.Error404NotFound("User administration is not enabled")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		userID, err := domain.NewUserID(input.Body.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("User ID is invalid")
		}
		switch security.options.Mode {
		case AuthModeLocal:
			if input.Body.ExternalSubject != "" {
				return nil, huma.Error400BadRequest("External subject is not accepted in local mode")
			}
			password := []byte(input.Body.Password)
			defer clear(password)
			if err := security.options.Manager.CreateLocalUser(ctx, userID, input.Body.Username, input.Body.DisplayName, password); err != nil {
				return nil, huma.Error400BadRequest("User could not be created")
			}
		case AuthModeOIDC:
			if input.Body.Password != "" || input.Body.ExternalSubject == "" {
				return nil, huma.Error400BadRequest("An external subject is required in OIDC mode")
			}
			if err := security.options.Manager.CreateOIDCUser(ctx, userID, input.Body.Username, input.Body.DisplayName, security.options.OIDCIssuer, input.Body.ExternalSubject); err != nil {
				return nil, huma.Error400BadRequest("User could not be created")
			}
		}
		user, err := security.options.Repository.UserByID(ctx, userID)
		if err != nil {
			return nil, huma.Error500InternalServerError("User could not be loaded")
		}
		user.PasswordHash = ""
		return &versionedOutput[application.UserRecord]{ETag: resourceETag(user.ResourceVersion), Body: user}, nil
	})
	type userStateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
	}
	setUserDisabled := func(disabled bool) func(context.Context, *userStateInput) (*versionedOutput[application.UserRecord], error) {
		return func(ctx context.Context, input *userStateInput) (*versionedOutput[application.UserRecord], error) {
			if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
				return nil, err
			}
			userID, err := domain.NewUserID(input.ID)
			if err != nil {
				return nil, huma.Error400BadRequest("User ID is invalid")
			}
			expected, err := expectedResourceVersion(input.IfMatch)
			if err != nil {
				return nil, err
			}
			user, err := security.options.Repository.SetUserDisabled(ctx, userID, disabled, expected, time.Now().UTC())
			if err != nil {
				return nil, administrationWriteError(err, "User state could not be changed")
			}
			user.PasswordHash = ""
			return &versionedOutput[application.UserRecord]{ETag: resourceETag(user.ResourceVersion), Body: user}, nil
		}
	}
	huma.Post(api, "/users/{id}/actions/disable", setUserDisabled(true))
	huma.Post(api, "/users/{id}/actions/enable", setUserDisabled(false))
	type issueRecoveryInput struct {
		ID string `path:"id"`
	}
	huma.Post(api, "/users/{id}/actions/issue-password-recovery", func(ctx context.Context, input *issueRecoveryInput) (*struct {
		Body struct {
			Token string `json:"token"`
		}
	}, error) {
		if security.options.Mode != AuthModeLocal {
			return nil, huma.Error404NotFound("Local password recovery is not enabled")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		userID, err := domain.NewUserID(input.ID)
		if err != nil {
			return nil, huma.Error404NotFound("User was not found")
		}
		token, err := security.options.Manager.IssuePasswordRecovery(ctx, userID)
		if err != nil {
			return nil, huma.Error404NotFound("User was not found")
		}
		output := &struct {
			Body struct {
				Token string `json:"token"`
			}
		}{}
		output.Body.Token = token
		return output, nil
	})

	type organizationInput struct {
		Body application.OrganizationRecord
	}
	huma.Post(api, "/organizations", func(ctx context.Context, input *organizationInput) (*versionedOutput[application.OrganizationRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionOrganizationManage, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateOrganization(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Organization could not be created")
		}
		return &versionedOutput[application.OrganizationRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type organizationUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.OrganizationRecord
	}
	huma.Put(api, "/organizations/{id}", func(ctx context.Context, input *organizationUpdateInput) (*versionedOutput[application.OrganizationRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionOrganizationManage, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Organization path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateOrganization(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Organization could not be updated")
		}
		return &versionedOutput[application.OrganizationRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type projectInput struct {
		Body application.ProjectRecord
	}
	huma.Post(api, "/projects", func(ctx context.Context, input *projectInput) (*versionedOutput[application.ProjectRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProjectManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateProject(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Project could not be created")
		}
		return &versionedOutput[application.ProjectRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type projectUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.ProjectRecord
	}
	huma.Put(api, "/projects/{id}", func(ctx context.Context, input *projectUpdateInput) (*versionedOutput[application.ProjectRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProjectManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Project path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateProject(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Project could not be updated")
		}
		return &versionedOutput[application.ProjectRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type teamInput struct {
		Body application.TeamRecord
	}
	huma.Post(api, "/teams", func(ctx context.Context, input *teamInput) (*versionedOutput[application.TeamRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionTeamManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		record, err := security.options.Repository.CreateTeam(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Team could not be created")
		}
		return &versionedOutput[application.TeamRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type teamUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.TeamRecord
	}
	huma.Put(api, "/teams/{id}", func(ctx context.Context, input *teamUpdateInput) (*versionedOutput[application.TeamRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionTeamManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != string(input.Body.ID) {
			return nil, huma.Error400BadRequest("Team path and body IDs must match")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateTeam(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Team could not be updated")
		}
		return &versionedOutput[application.TeamRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type membershipInput struct {
		Body application.MembershipRecord
	}
	huma.Post(api, "/memberships", func(ctx context.Context, input *membershipInput) (*versionedOutput[application.MembershipRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID),
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateMembership(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be created")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type membershipUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.MembershipRecord
	}
	huma.Put(api, "/memberships/{id}", func(ctx context.Context, input *membershipUpdateInput) (*versionedOutput[application.MembershipRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(input.Body.OrganizationID)}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID {
			return nil, huma.Error400BadRequest("Membership path and body IDs must match")
		}
		if input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Membership renewal requires validFrom")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateMembership(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be updated")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type membershipRevokeInput struct {
		ID             string `path:"id"`
		OrganizationID string `query:"organizationId" required:"true"`
		IfMatch        string `header:"If-Match"`
	}
	huma.Post(api, "/memberships/{id}/actions/revoke", func(ctx context.Context, input *membershipRevokeInput) (*versionedOutput[application.MembershipRecord], error) {
		organizationID, err := domain.NewOrganizationID(input.OrganizationID)
		if err != nil {
			return nil, huma.Error400BadRequest("Organization ID is invalid")
		}
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionMembershipManage, ScopeKind: application.ScopeOrganization, ScopeID: string(organizationID)}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeMembership(ctx, input.ID, organizationID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Membership could not be revoked")
		}
		return &versionedOutput[application.MembershipRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type roleAssignmentInput struct {
		Body application.RoleAssignmentRecord
	}
	huma.Post(api, "/role-assignments", func(ctx context.Context, input *roleAssignmentInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionRoleAssignmentManage, ScopeKind: input.Body.ScopeKind, ScopeID: input.Body.ScopeID,
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateRoleAssignment(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be created")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type roleAssignmentUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.RoleAssignmentRecord
	}
	huma.Put(api, "/role-assignments/{id}", func(ctx context.Context, input *roleAssignmentUpdateInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionRoleAssignmentManage, ScopeKind: input.Body.ScopeKind, ScopeID: input.Body.ScopeID}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID || input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Role assignment path/body or validity is invalid")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateRoleAssignment(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be updated")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type roleAssignmentRevokeInput struct {
		ID        string                `path:"id"`
		ScopeKind application.ScopeKind `query:"scopeKind" required:"true"`
		ScopeID   string                `query:"scopeId" required:"true"`
		IfMatch   string                `header:"If-Match"`
	}
	huma.Post(api, "/role-assignments/{id}/actions/revoke", func(ctx context.Context, input *roleAssignmentRevokeInput) (*versionedOutput[application.RoleAssignmentRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionRoleAssignmentManage, ScopeKind: input.ScopeKind, ScopeID: input.ScopeID}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeRoleAssignment(ctx, input.ID, input.ScopeKind, input.ScopeID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Role assignment could not be revoked")
		}
		return &versionedOutput[application.RoleAssignmentRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})

	type targetGrantInput struct {
		Body application.TargetGrantRecord
	}
	huma.Post(api, "/provider-target-grants", func(ctx context.Context, input *targetGrantInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{
			Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform",
		}); err != nil {
			return nil, err
		}
		if input.Body.ValidFrom.IsZero() {
			input.Body.ValidFrom = time.Now().UTC()
		}
		record, err := security.options.Repository.CreateTargetGrant(ctx, input.Body)
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be created")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type targetGrantUpdateInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
		Body    application.TargetGrantRecord
	}
	huma.Put(api, "/provider-target-grants/{id}", func(ctx context.Context, input *targetGrantUpdateInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		if input.ID != input.Body.ID || input.Body.ValidFrom.IsZero() {
			return nil, huma.Error400BadRequest("Provider target grant path/body or validity is invalid")
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.UpdateTargetGrant(ctx, input.Body, expected)
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be updated")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
	type targetGrantRevokeInput struct {
		ID      string `path:"id"`
		IfMatch string `header:"If-Match"`
	}
	huma.Post(api, "/provider-target-grants/{id}/actions/revoke", func(ctx context.Context, input *targetGrantRevokeInput) (*versionedOutput[application.TargetGrantRecord], error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		expected, err := expectedResourceVersion(input.IfMatch)
		if err != nil {
			return nil, err
		}
		record, err := security.options.Repository.RevokeTargetGrant(ctx, input.ID, expected, time.Now().UTC())
		if err != nil {
			return nil, administrationWriteError(err, "Provider target grant could not be revoked")
		}
		return &versionedOutput[application.TargetGrantRecord]{ETag: resourceETag(record.ResourceVersion), Body: record}, nil
	})
}

func resourceETag(version domain.ResourceVersion) string {
	return fmt.Sprintf(`"%d"`, version)
}

func expectedResourceVersion(value string) (domain.ResourceVersion, error) {
	if value == "" {
		return 0, huma.NewError(http.StatusPreconditionRequired, "If-Match is required")
	}
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, huma.Error400BadRequest("If-Match must contain one quoted resource version")
	}
	parsed, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	if err != nil || parsed == 0 {
		return 0, huma.Error400BadRequest("If-Match resource version is invalid")
	}
	return domain.ResourceVersion(parsed), nil
}

func expectedUpsertResourceVersion(ifMatch, ifNoneMatch string) (domain.ResourceVersion, error) {
	if ifMatch != "" && ifNoneMatch != "" {
		return 0, huma.Error400BadRequest("If-Match and If-None-Match cannot be combined")
	}
	if ifNoneMatch != "" {
		if ifNoneMatch != "*" {
			return 0, huma.Error400BadRequest("If-None-Match must be *")
		}
		return 0, nil
	}
	if ifMatch == "" {
		return 0, huma.NewError(http.StatusPreconditionRequired, "If-Match or If-None-Match is required")
	}
	if len(ifMatch) < 3 || ifMatch[0] != '"' || ifMatch[len(ifMatch)-1] != '"' || strings.Contains(ifMatch[1:len(ifMatch)-1], `"`) {
		return 0, huma.Error400BadRequest("If-Match must contain one quoted resource version")
	}
	parsed, err := strconv.ParseUint(ifMatch[1:len(ifMatch)-1], 10, 64)
	if err != nil || parsed == 0 {
		return 0, huma.Error400BadRequest("If-Match resource version is invalid")
	}
	return domain.ResourceVersion(parsed), nil
}

func administrationWriteError(err error, message string) error {
	switch {
	case errors.Is(err, application.ErrAuthVersionConflict):
		return huma.NewError(http.StatusPreconditionFailed, "The resource version no longer matches")
	case errors.Is(err, application.ErrLastPlatformAdministrator):
		return huma.Error409Conflict("At least one active platform administrator must remain")
	case errors.Is(err, application.ErrAuthRecordNotFound):
		return huma.Error404NotFound("The requested administrative resource was not found")
	default:
		return huma.Error400BadRequest(message)
	}
}

func sessionForPrincipal(ctx context.Context, security *requestSecurity) (Session, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return Session{}, huma.Error401Unauthorized("Authentication is required")
	}
	if security.options.Mode == AuthModeNone {
		return Session{SubjectID: string(actor.user.ID), DisplayName: actor.user.DisplayName, Roles: []string{string(application.RolePlatformAdministrator)}}, nil
	}
	return sessionForUser(ctx, security, actor.user)
}

func sessionForUser(ctx context.Context, security *requestSecurity, user application.UserRecord) (Session, error) {
	snapshot, err := security.options.Repository.AuthorizationSnapshot(ctx, user.ID, time.Now().UTC())
	if err != nil {
		return Session{}, huma.Error401Unauthorized("Authentication is required")
	}
	seen := make(map[string]bool, len(snapshot.Assignments))
	roles := make([]string, 0, len(snapshot.Assignments))
	for _, assignment := range snapshot.Assignments {
		role := string(assignment.Role)
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	return Session{SubjectID: string(user.ID), DisplayName: user.DisplayName, Roles: roles}, nil
}

func authMethods(mode string) []string {
	switch mode {
	case AuthModeLocal:
		return []string{"password"}
	case AuthModeOIDC:
		return []string{"oidc"}
	default:
		return []string{"none"}
	}
}

func authorize(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) error {
	_, err := authorizeActor(ctx, security, request)
	return err
}

func authorizeActor(ctx context.Context, security *requestSecurity, request application.AuthorizationRequest) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	request.SubjectID = actor.user.ID
	if err := security.options.Authorizer.Authorize(ctx, request); err != nil {
		return principal{}, authorizationError(err, false)
	}
	return actor, nil
}

func authorizeLabRequest(ctx context.Context, security *requestSecurity, request LabRequest, action application.PermissionAction, targetID domain.ProviderTargetID) (principal, domain.ProjectID, application.OwnerKind, string, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, "", "", "", huma.Error401Unauthorized("Authentication is required")
	}
	projectID := domain.ProjectID(request.ProjectID)
	ownerKind := application.OwnerKind(request.OwnerKind)
	ownerID := request.OwnerID
	if security.options.Mode == AuthModeNone {
		if projectID == "" {
			projectID = "project.local"
		}
		if ownerKind == "" {
			ownerKind = application.OwnerUser
		}
		if ownerID == "" {
			ownerID = string(controlplane.LocalAdminID)
		}
		return actor, projectID, ownerKind, ownerID, nil
	}
	if _, err := domain.NewProjectID(string(projectID)); err != nil {
		return principal{}, "", "", "", huma.Error400BadRequest("Project ID is required and must be valid")
	}
	if ownerKind == "" {
		ownerKind = application.OwnerUser
	}
	if ownerID == "" && ownerKind == application.OwnerUser {
		ownerID = string(actor.user.ID)
	}
	if !validRequestOwner(ownerKind, ownerID) {
		return principal{}, "", "", "", huma.Error400BadRequest("Instance owner is invalid")
	}
	authorizationRequest := application.AuthorizationRequest{
		SubjectID: actor.user.ID, Action: action, ScopeKind: application.ScopeProject, ScopeID: string(projectID),
		TargetID: targetID, OwnerKind: ownerKind, OwnerID: ownerID,
	}
	if err := security.options.Authorizer.Authorize(ctx, authorizationRequest); err != nil {
		return principal{}, "", "", "", authorizationError(err, false)
	}
	return actor, projectID, ownerKind, ownerID, nil
}

func authorizeLabBatchRequest(ctx context.Context, security *requestSecurity, request InstanceBatchRequest, targetID domain.ProviderTargetID) (principal, domain.ProjectID, []controlplane.InstanceOwner, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, "", nil, huma.Error401Unauthorized("Authentication is required")
	}
	projectID, err := domain.NewProjectID(request.ProjectID)
	if err != nil {
		return principal{}, "", nil, huma.Error400BadRequest("Project ID is required and must be valid")
	}
	if len(request.Owners) < 1 || len(request.Owners) > controlplane.MaximumInstanceBatchSize {
		return principal{}, "", nil, huma.Error400BadRequest(fmt.Sprintf("Batch owners must contain 1-%d entries", controlplane.MaximumInstanceBatchSize))
	}
	owners := make([]controlplane.InstanceOwner, 0, len(request.Owners))
	seen := make(map[string]bool, len(request.Owners))
	for _, requested := range request.Owners {
		owner := controlplane.InstanceOwner{Kind: application.OwnerKind(requested.OwnerKind), ID: requested.OwnerID}
		if owner.Kind != application.OwnerUser && owner.Kind != application.OwnerTeam || !validRequestOwner(owner.Kind, owner.ID) {
			return principal{}, "", nil, huma.Error400BadRequest("Batch owner is invalid")
		}
		identity := string(owner.Kind) + "\x00" + owner.ID
		if seen[identity] {
			return principal{}, "", nil, huma.Error400BadRequest("Batch owners must be unique")
		}
		seen[identity] = true
		owners = append(owners, owner)
	}
	if security.options.Mode == AuthModeNone {
		return actor, projectID, owners, nil
	}
	for _, owner := range owners {
		if err := security.options.Authorizer.Authorize(ctx, application.AuthorizationRequest{
			SubjectID: actor.user.ID, Action: application.ActionInstanceBatchCreate,
			ScopeKind: application.ScopeProject, ScopeID: string(projectID), TargetID: targetID,
			OwnerKind: owner.Kind, OwnerID: owner.ID,
		}); err != nil {
			return principal{}, "", nil, authorizationError(err, false)
		}
	}
	return actor, projectID, owners, nil
}

func validRequestOwner(kind application.OwnerKind, id string) bool {
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

func authorizeInstance(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) error {
	_, err := authorizeInstanceActor(ctx, security, action, instanceID)
	return err
}

func authorizeInstanceActor(ctx context.Context, security *requestSecurity, action application.PermissionAction, instanceID domain.InstanceID) (principal, error) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return principal{}, huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewInstanceID(string(instanceID)); err != nil {
		return principal{}, huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return actor, nil
	}
	if _, err := security.options.Authorizer.AuthorizeInstance(ctx, actor.user.ID, action, instanceID); err != nil {
		return principal{}, authorizationError(err, true)
	}
	return actor, nil
}

func authorizeOperation(ctx context.Context, security *requestSecurity, action application.PermissionAction, operationID domain.OperationID) error {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return huma.Error401Unauthorized("Authentication is required")
	}
	if _, err := domain.NewOperationID(string(operationID)); err != nil {
		return huma.Error404NotFound("Resource was not found")
	}
	if security.options.Mode == AuthModeNone {
		return nil
	}
	if _, err := security.options.Authorizer.AuthorizeOperation(ctx, actor.user.ID, action, operationID); err != nil {
		return authorizationError(err, true)
	}
	return nil
}

func authorizationError(err error, hideExistence bool) error {
	if errors.Is(err, authorization.ErrDenied) {
		if hideExistence {
			return huma.Error404NotFound("Resource was not found")
		}
		return huma.Error403Forbidden("Permission was denied")
	}
	return huma.Error500InternalServerError("Authorization could not be evaluated")
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

func labTemplateDTO(source controlplane.LabTemplateRecord) LabTemplateDTO {
	return LabTemplateDTO{
		ID: string(source.ID), ProjectID: string(source.ProjectID), Name: source.Name, Description: source.Description,
		ResourceVersion: uint64(source.ResourceVersion), CreatedBy: string(source.CreatedBy), CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func templateRevisionDTO(source controlplane.TemplateRevisionRecord) TemplateRevisionDTO {
	return TemplateRevisionDTO{
		ID: string(source.ID), TemplateID: string(source.TemplateID), RevisionNumber: source.RevisionNumber,
		BundleArtifact: string(source.BundleArtifact), BundleDigest: source.BundleDigest.String(), BundleSizeBytes: source.BundleSizeBytes,
		AuthorAPIVersion: source.AuthorAPIVersion, RendererVersion: source.RendererVersion, CanonicalVersion: source.CanonicalVersion,
		CanonicalDigest: source.CanonicalDigest.String(), PublishedBy: string(source.PublishedBy), CreatedAt: source.CreatedAt,
	}
}

func auditRecordDTO(source application.AuditRecord) AuditRecordDTO {
	return AuditRecordDTO{
		ID: source.ID, StreamPosition: source.StreamPosition, SchemaVersion: source.SchemaVersion,
		Category: source.Category, Action: source.Action, Outcome: source.Outcome, Scope: source.Scope,
		Actor: source.Actor, AggregateType: source.AggregateType, AggregateID: source.AggregateID,
		Links: source.Links, Payload: json.RawMessage(source.RedactedPayload.Bytes()), OccurredAt: source.OccurredAt,
	}
}

func eventRecordDTO(source application.EventRecord) EventRecordDTO {
	return EventRecordDTO{
		ID: source.ID, StreamPosition: source.StreamPosition, AggregateSequence: source.AggregateSequence,
		SchemaVersion: source.SchemaVersion, Type: source.Type, Scope: source.Scope, Actor: source.Actor,
		AggregateType: source.AggregateType, AggregateID: source.AggregateID, Links: source.Links,
		Payload: json.RawMessage(source.SafePayload.Bytes()), OccurredAt: source.OccurredAt,
	}
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

func instanceLifetimeDTO(source controlplane.InstanceRecord) InstanceLifetimeDTO {
	return InstanceLifetimeDTO{
		InstanceID: string(source.ID), ExpiresAt: source.ExpiresAt, MaximumExpiresAt: source.MaximumExpiresAt,
		ExpirationWarningAt: source.ExpirationWarningAt, ExpirationGraceSeconds: source.ExpirationGraceSeconds,
		LifetimeRenewalAllowed:       source.LifetimeRenewalAllowed,
		LifetimeRenewalWindowSeconds: source.LifetimeRenewalWindowSeconds,
		LifetimePolicyDigest:         source.LifetimePolicyDigest.String(), ResourceVersion: uint64(source.ResourceVersion),
		UpdatedAt: source.UpdatedAt,
	}
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

func apiError(err error) error {
	var validation *controlplane.ValidationError
	if errors.As(err, &validation) {
		details := make([]error, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			location := issue.FieldPath
			if location == "" {
				location = issue.ResourceID
			}
			details = append(details, &huma.ErrorDetail{Message: issue.Code + ": " + issue.Message, Location: location})
		}
		return huma.Error422UnprocessableEntity("Lab validation failed", details...)
	}
	if errors.Is(err, controlplane.ErrNotFound) {
		return huma.Error404NotFound("Resource was not found")
	}
	if errors.Is(err, controlplane.ErrAlreadyExists) {
		return huma.Error409Conflict("A resource with the same identity or scoped name already exists")
	}
	if errors.Is(err, controlplane.ErrVersionConflict) {
		return huma.NewError(http.StatusPreconditionFailed, "The resource version no longer matches")
	}
	if errors.Is(err, controlplane.ErrAuthorityChanged) {
		return huma.Error404NotFound("Resource was not found")
	}
	var domainError *domain.DomainError
	var quotaError *application.QuotaExceededError
	if errors.As(err, &quotaError) {
		return huma.Error422UnprocessableEntity("The request would exceed an applicable quota")
	}
	if errors.As(err, &domainError) && domainError.Class == domain.ErrorQuota {
		return huma.Error422UnprocessableEntity(domainError.Message)
	}
	if errors.As(err, &domainError) && domainError.Code == "idempotency_conflict" {
		return huma.Error409Conflict("Idempotency key is already bound to a different request")
	}
	return huma.Error500InternalServerError("The control plane could not complete the request")
}

func staticHandler(content fs.FS) http.Handler {
	files := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			http.NotFound(response, request)
			return
		}
		clean := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if clean == "." || clean == "" {
			files.ServeHTTP(response, request)
			return
		}
		if _, err := fs.Stat(content, clean); err != nil {
			http.NotFound(response, request)
			return
		}
		files.ServeHTTP(response, request)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}
