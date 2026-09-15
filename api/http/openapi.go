package httpapi

import (
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"net/http"
)

// OpenAPIDocument registers the complete product contract on a private,
// unserved mux. Runtime no-auth installations still omit administration.
func OpenAPIDocument() *huma.OpenAPI {
	config := huma.DefaultConfig("LabCentral API", "1.0.0")
	config.Info.Description = "Canonical LabCentral control-plane API. Authentication-dependent endpoints require the configured authentication mode."
	api := humago.NewWithPrefix(http.NewServeMux(), "/api/v1", config)
	register(api, &controlplane.Service{}, []byte("example"), Options{}, &requestSecurity{options: AuthenticationOptions{Mode: AuthModeLocal}})
	return api.OpenAPI()
}
