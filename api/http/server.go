package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

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

type Options struct {
	NoAuthWarning string
}

func New(service *controlplane.Service, exampleSource []byte, static fs.FS, options Options) (*Server, error) {
	if service == nil || len(exampleSource) == 0 || static == nil {
		return nil, fmt.Errorf("control-plane service, Example A, and static assets are required")
	}
	mux := http.NewServeMux()
	config := huma.DefaultConfig("LabCentral API", "1.0.0")
	config.Info.Description = "Canonical local LabCentral control-plane API. Mutations are durable asynchronous operations."
	api := humago.NewWithPrefix(mux, "/api/v1", config)
	if options.NoAuthWarning == "" {
		options.NoAuthWarning = NoAuthWarning
	}
	register(api, service, exampleSource, options)
	mux.Handle("/", staticHandler(static))
	return &Server{Handler: securityHeaders(mux), API: api}, nil
}

type ServerInfo struct {
	ServerVersion        string   `json:"serverVersion"`
	APIVersion           string   `json:"apiVersion"`
	APIBase              string   `json:"apiBase"`
	AuthMode             string   `json:"authMode"`
	AuthMethods          []string `json:"authMethods"`
	EnabledProviderKinds []string `json:"enabledProviderKinds"`
	NoAuthWarning        string   `json:"noAuthWarning"`
	UIFeatures           []string `json:"uiFeatures"`
}

type Session struct {
	SubjectID   string   `json:"subjectId"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
}

type LabRequest struct {
	Source                         string          `json:"source" minLength:"1" maxLength:"8388608"`
	AcknowledgeDockerIsolationRisk bool            `json:"acknowledgeDockerIsolationRisk"`
	ProviderTargetID               string          `json:"providerTargetId,omitempty" maxLength:"128"`
	ProviderOverride               json.RawMessage `json:"providerOverride,omitempty"`
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
	ProviderTargetID string         `json:"providerTargetId"`
	Phase            string         `json:"phase"`
	DesiredLifecycle string         `json:"desiredLifecycle"`
	CanonicalDigest  string         `json:"canonicalDigest"`
	ResourceVersion  uint64         `json:"resourceVersion"`
	Outputs          map[string]any `json:"outputs"`
	Resources        []ResourceDTO  `json:"resources,omitempty"`
	ErrorCode        string         `json:"errorCode,omitempty"`
	ErrorMessage     string         `json:"errorMessage,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

func register(api huma.API, service *controlplane.Service, exampleSource []byte, options Options) {
	huma.Get(api, "/server-info", func(context.Context, *struct{}) (*struct{ Body ServerInfo }, error) {
		return &struct{ Body ServerInfo }{Body: ServerInfo{
			ServerVersion: "0.1.0", APIVersion: "v1", APIBase: "/api/v1", AuthMode: "none", AuthMethods: []string{"none"},
			EnabledProviderKinds: service.ProviderKinds(), NoAuthWarning: options.NoAuthWarning,
			UIFeatures: []string{"docker-example-a", "validate", "plan", "apply", "destroy"},
		}}, nil
	})

	huma.Get(api, "/session", func(context.Context, *struct{}) (*struct{ Body Session }, error) {
		return &struct{ Body Session }{Body: Session{SubjectID: string(controlplane.LocalAdminID), DisplayName: "Local administrator", Roles: []string{"installation-admin"}}}, nil
	})

	huma.Get(api, "/examples/docker-laptop", func(context.Context, *struct{}) (*struct {
		Body struct {
			Source string `json:"source"`
		}
	}, error) {
		output := &struct {
			Body struct {
				Source string `json:"source"`
			}
		}{}
		output.Body.Source = string(exampleSource)
		return output, nil
	})

	huma.Post(api, "/lab-validations", func(ctx context.Context, input *struct{ Body LabRequest }) (*struct{ Body ValidationBody }, error) {
		targetID := requestTargetID(input.Body)
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
		result, err := service.PlanTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, requestTargetID(input.Body), input.Body.ProviderOverride)
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
			result, err := service.CreateInstanceTarget(ctx, []byte(input.Body.Source), input.Body.AcknowledgeDockerIsolationRisk, input.IdempotencyKey, requestTargetID(input.Body), input.Body.ProviderOverride)
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

	huma.Get(api, "/lab-instances", func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Items      []InstanceDTO `json:"items"`
			NextCursor string        `json:"nextCursor,omitempty"`
		}
	}, error) {
		instances, err := service.ListInstances(ctx)
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
			result, err := service.DestroyInstance(ctx, domain.InstanceID(input.ID), input.IdempotencyKey)
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

	type OperationInput struct {
		ID string `path:"id"`
	}
	huma.Get(api, "/operations/{id}", func(ctx context.Context, input *OperationInput) (*struct{ Body OperationDTO }, error) {
		operation, err := service.GetOperation(ctx, domain.OperationID(input.ID))
		if err != nil {
			return nil, apiError(err)
		}
		return &struct{ Body OperationDTO }{Body: operationDTO(operation)}, nil
	})
}

func operationDTO(source controlplane.OperationRecord) OperationDTO {
	return OperationDTO{ID: string(source.ID), InstanceID: string(source.InstanceID), Kind: string(source.Kind), State: string(source.State), Message: source.Message, ErrorCode: source.ErrorCode, Attempt: source.Attempt, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt}
}

func instanceDTO(source controlplane.InstanceRecord, resources []controlplane.ResourceRecord) InstanceDTO {
	outputs := make(map[string]any)
	_ = json.Unmarshal(source.OutputsJSON, &outputs)
	result := InstanceDTO{
		ID: string(source.ID), ProviderTargetID: string(source.ProviderTargetID), Phase: string(source.Phase), DesiredLifecycle: string(source.DesiredLifecycle),
		CanonicalDigest: source.CanonicalDigest.String(), ResourceVersion: uint64(source.ResourceVersion), Outputs: outputs,
		ErrorCode: source.ErrorCode, ErrorMessage: source.ErrorMessage, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
	for _, resource := range resources {
		result.Resources = append(result.Resources, ResourceDTO{ID: string(resource.ID), Kind: string(resource.Kind), Phase: string(resource.Phase), ErrorCode: resource.ErrorCode})
	}
	return result
}

func requestTargetID(request LabRequest) domain.ProviderTargetID {
	if request.ProviderTargetID == "" {
		return controlplane.LocalDockerTarget
	}
	return domain.ProviderTargetID(request.ProviderTargetID)
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
	var domainError *domain.DomainError
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
