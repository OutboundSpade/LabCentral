package httpapi

import (
	"context"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
)

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
