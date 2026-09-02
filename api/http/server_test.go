package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/OutboundSpade/LabCentral/internal/controlplane"
)

func TestOpenAPIContainsLocalVerticalSlice(t *testing.T) {
	server := testServer(t)
	encoded, err := json.Marshal(server.API.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q", document.OpenAPI)
	}
	expected := map[string][]string{
		"/server-info":                        {"get"},
		"/session":                            {"get"},
		"/examples/docker-laptop":             {"get"},
		"/lab-validations":                    {"post"},
		"/lab-plans":                          {"post"},
		"/lab-instances":                      {"get", "post"},
		"/lab-instances/{id}":                 {"get"},
		"/lab-instances/{id}/actions/destroy": {"post"},
		"/operations/{id}":                    {"get"},
	}
	for path, methods := range expected {
		operations := document.Paths[path]
		if operations == nil {
			t.Errorf("OpenAPI path %s is missing", path)
			continue
		}
		for _, method := range methods {
			if operations[method] == nil {
				t.Errorf("OpenAPI operation %s %s is missing", method, path)
			}
		}
	}
}

func TestBootstrapAndStaticAssetsCarrySecurityWarnings(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/server-info", nil)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("server-info response = %d headers=%v", response.Code, response.Header())
	}
	var info ServerInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.AuthMode != "none" || info.NoAuthWarning == "" {
		t.Fatalf("server info did not expose no-auth warning: %#v", info)
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "<h1>LabCentral</h1>" {
		t.Fatalf("static response = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/not-a-client-route", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("undocumented static fallback returned %d", response.Code)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	server, err := New(new(controlplane.Service), []byte("example"), static, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
