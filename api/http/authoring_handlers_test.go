package httpapi

import (
	"github.com/OutboundSpade/LabCentral/internal/application"
	"net/http"
	"testing"
)

func TestDeploymentSettingsAuthorizationAndVersioning(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	admin := f.client(t, "admin", "a-production-length-password")
	author := f.client(t, "author", "an-author-production-password")
	path := "/api/v1/admin/provider-targets/target.local-docker/deployment-settings"
	requireGovernanceStatus(t, author.request(http.MethodGet, path, nil, "", ""), http.StatusForbidden)
	requireGovernanceStatus(t, author.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": 4}, `"0"`, ""), http.StatusForbidden)
	response := admin.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var settings application.DeploymentSettings
	decodeGovernanceResponse(t, response, &settings)
	if settings.MaxConcurrentDeployments != 1 || settings.ResourceVersion != 0 {
		t.Fatalf("default: %+v", settings)
	}
	for _, limit := range []int{0, 17} {
		requireGovernanceStatus(t, admin.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": limit}, `"0"`, ""), http.StatusUnprocessableEntity)
	}
	requireGovernanceStatus(t, admin.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": 4}, "", ""), http.StatusPreconditionRequired)
	response = admin.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": 4}, `"0"`, "")
	requireGovernanceStatus(t, response, http.StatusOK)
	decodeGovernanceResponse(t, response, &settings)
	if settings.MaxConcurrentDeployments != 4 || settings.ResourceVersion != 1 {
		t.Fatalf("stored: %+v", settings)
	}
	requireGovernanceStatus(t, admin.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": 8}, `"0"`, ""), http.StatusPreconditionFailed)
	requireGovernanceStatus(t, admin.request(http.MethodPut, path, map[string]any{"maxConcurrentDeployments": 2}, `"1"`, ""), http.StatusOK)
	response = admin.request(http.MethodGet, path, nil, "", "")
	decodeGovernanceResponse(t, response, &settings)
	if settings.MaxConcurrentDeployments != 2 || settings.ResourceVersion != 2 || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("persisted: %+v", settings)
	}
	requireGovernanceStatus(t, admin.request(http.MethodGet, "/api/v1/admin/provider-targets/target.missing/deployment-settings", nil, "", ""), http.StatusNotFound)
}
func TestAuthoringPreservesDocumentAndRejectsAmbiguousYAML(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	response := author.request(http.MethodGet, "/api/v1/lab-authoring-schema", nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var schema map[string]any
	decodeGovernanceResponse(t, response, &schema)
	if schema["type"] != "object" || schema["$defs"] == nil {
		t.Fatal("lab schema missing")
	}
	response = author.request(http.MethodPost, "/api/v1/lab-authoring-documents", map[string]any{"source": "name: café\nvalues: [true, 2, hello]\n"}, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var got map[string]any
	decodeGovernanceResponse(t, response, &got)
	if got["name"] != "café" || len(got["values"].([]any)) != 3 {
		t.Fatalf("document changed: %v", got)
	}
	for _, source := range []string{"name: first\nname: second\n", "a: 1\n---\nb: 2"} {
		response = author.request(http.MethodPost, "/api/v1/lab-authoring-documents", map[string]any{"source": source}, "", "")
		requireGovernanceStatus(t, response, http.StatusUnprocessableEntity)
	}
}
