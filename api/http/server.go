package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type Server struct {
	Handler http.Handler
	API     huma.API
}

type Options struct {
	ServerVersion           string
	NoAuthWarning           string
	DefaultProviderTargetID string
	ProviderExamples        map[string]ProviderExample
	Authentication          AuthenticationOptions
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
	if options.ServerVersion == "" {
		options.ServerVersion = "dev"
	}
	if len(options.ServerVersion) > 128 || strings.TrimSpace(options.ServerVersion) != options.ServerVersion ||
		strings.IndexFunc(options.ServerVersion, func(value rune) bool { return value < ' ' || value == 0x7f }) >= 0 {
		return nil, fmt.Errorf("server version must be a trimmed printable value of at most 128 bytes")
	}
	config := huma.DefaultConfig("LabCentral API", "1.0.0")
	config.Info.Description = "Canonical local LabCentral control-plane API. Mutations are durable asynchronous operations."
	api := humago.NewWithPrefix(mux, "/api/v1", config)
	if options.NoAuthWarning == "" {
		options.NoAuthWarning = NoAuthWarning
	}
	service.SetOperationsAuthorizer(options.Authentication.Authorizer)
	register(api, service, exampleSource, options, security)
	mux.Handle("/", staticHandler(static))
	return &Server{Handler: securityHeaders(security.wrap(mux)), API: api}, nil
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
			// SvelteKit exports these static route shells as <route>.html.
			// Keep unknown URLs and API paths out of the SPA fallback.
			switch clean {
			case "launch", "templates", "classroom", "people", "settings", "lab", "operations", "workspaces", "plugins":
				if _, err := fs.Stat(content, clean+".html"); err == nil {
					clone := request.Clone(request.Context())
					clone.URL.Path = "/" + clean + ".html"
					files.ServeHTTP(response, clone)
					return
				}
			}
			if strings.HasPrefix(clean, "access/") {
				if _, err := domain.NewAccessSessionID(strings.TrimPrefix(clean, "access/")); err == nil {
					clone := request.Clone(request.Context())
					clone.URL.Path = "/200.html"
					files.ServeHTTP(response, clone)
					return
				}
			}
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
