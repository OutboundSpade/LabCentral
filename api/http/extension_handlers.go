package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/danielgtaylor/huma/v2"
)

type BrowserExtensionDTO struct {
	ID              string   `json:"id"`
	Icon            string   `json:"icon,omitempty"`
	RequiredActions []string `json:"requiredActions,omitempty"`
	RouteAlias      string   `json:"routeAlias,omitempty"`
	PluginInstance  string   `json:"pluginInstance"`
	Slot            string   `json:"slot"`
	Title           string   `json:"title"`
	URL             string   `json:"url"`
	Permissions     []string `json:"permissions"`
}
type ExtensionBindingDTO struct {
	ID             string   `json:"id"`
	ProjectIDs     []string `json:"projectIds"`
	HasCredentials bool     `json:"hasCredentials"`
}
type ExtensionInventoryDTO struct {
	Bindings            []ExtensionBindingDTO `json:"bindings"`
	TargetIDs           []string              `json:"targetIds"`
	Methods             []string              `json:"methods"`
	Permissions         []string              `json:"permissions"`
	InstanceID          string                `json:"instanceId"`
	PluginID            string                `json:"pluginId"`
	Version             string                `json:"version"`
	Category            string                `json:"category"`
	Kind                string                `json:"kind,omitempty"`
	ProtocolMajor       uint32                `json:"protocolMajor"`
	ProtocolMinor       uint32                `json:"protocolMinor"`
	Available           bool                  `json:"available"`
	ConfigurationSchema json.RawMessage       `json:"configurationSchema,omitempty"`
}

type browserExtensionList struct {
	Body struct {
		Items []BrowserExtensionDTO `json:"items"`
	}
}
type extensionInventoryList struct {
	Body struct {
		Items []ExtensionInventoryDTO `json:"items"`
	}
}

func registerExtensions(api huma.API, security *requestSecurity, options Options) {
	huma.Get(api, "/extensions/browser", func(ctx context.Context, in *struct {
		ProjectID string `query:"projectId" maxLength:"128"`
	}) (*browserExtensionList, error) {
		if _, ok := requestPrincipal(ctx); !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		out := &browserExtensionList{}
		out.Body.Items = []BrowserExtensionDTO{}
		for _, extension := range options.BrowserExtensions {
			if len(extension.RequiredActions) > 0 && security.options.Mode != AuthModeNone {
				if in.ProjectID == "" {
					continue
				}
				p, _ := requestPrincipal(ctx)
				snapshot, err := security.options.Repository.AuthorizationSnapshot(ctx, p.user.ID, time.Now().UTC())
				if err != nil {
					return nil, huma.Error500InternalServerError("Feature access could not be loaded")
				}
				allowed := false
				for _, action := range extension.RequiredActions {
					req := application.AuthorizationRequest{Action: application.PermissionAction(action), ScopeKind: application.ScopeProject, ScopeID: in.ProjectID}
					if action == string(application.ActionInstanceBatchCreate) {
						for _, grant := range snapshot.Grants {
							req.TargetID = grant.TargetID
							if authorize(ctx, security, req) == nil {
								allowed = true
								break
							}
						}
					} else if authorize(ctx, security, req) == nil {
						allowed = true
					}
				}
				if !allowed {
					continue
				}
			}
			if extension.Slot == "plugin-settings" {
				if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
					continue
				}
			}
			out.Body.Items = append(out.Body.Items, extension)
		}
		return out, nil
	})
	huma.Get(api, "/admin/extensions", func(ctx context.Context, _ *struct{}) (*extensionInventoryList, error) {
		if err := authorize(ctx, security, application.AuthorizationRequest{Action: application.ActionProviderTargetGrant, ScopeKind: application.ScopePlatform, ScopeID: "platform"}); err != nil {
			return nil, err
		}
		out := &extensionInventoryList{}
		out.Body.Items = []ExtensionInventoryDTO{}
		if options.ExtensionInventory != nil {
			out.Body.Items = options.ExtensionInventory()
		}
		return out, nil
	})
}
