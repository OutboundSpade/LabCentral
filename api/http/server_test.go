package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/render"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	_ "modernc.org/sqlite"
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
		"/server-info":                               {"get"},
		"/session":                                   {"get"},
		"/examples/docker-laptop":                    {"get"},
		"/provider-targets":                          {"get"},
		"/provider-targets/{id}/actions/refresh":     {"post"},
		"/lab-templates":                             {"get", "post"},
		"/lab-templates/{id}":                        {"get", "put"},
		"/lab-templates/{id}/revisions":              {"get", "post"},
		"/lab-templates/{id}/revisions/{revisionId}": {"get"},
		"/audit-records":                             {"get"},
		"/audit-records/export":                      {"get"},
		"/events":                                    {"get"},
		"/lab-validations":                           {"post"},
		"/lab-plans":                                 {"post"},
		"/lab-instance-batches":                      {"post"},
		"/lab-instance-batches/{id}":                 {"get"},
		"/lab-instances":                             {"get", "post"},
		"/lab-instances/{id}":                        {"get"},
		"/lab-instances/{id}/actions/destroy":        {"post"},
		"/operations/{id}":                           {"get"},
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

func TestInstanceBatchRequestIsBoundedBeforeApplicationWork(t *testing.T) {
	server := testServer(t)
	owners := make([]InstanceBatchOwnerRequest, controlplane.MaximumInstanceBatchSize+1)
	for index := range owners {
		owners[index] = InstanceBatchOwnerRequest{OwnerKind: "user", OwnerID: fmt.Sprintf("user.%03d", index)}
	}
	body, err := json.Marshal(InstanceBatchRequest{
		RevisionID: "revision.alpha", ProjectID: "project.alpha", Owners: owners,
		AcknowledgeDockerIsolationRisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/lab-instance-batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "batch-size-boundary")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized batch returned %d: %s", response.Code, response.Body.String())
	}
}

func TestRecordCursorRoundTripAndRejectsMalformedValues(t *testing.T) {
	encoded := encodeRecordCursor(&application.RecordCursor{StreamPosition: 42})
	decoded, err := decodeRecordCursor(encoded)
	if err != nil || decoded == nil || decoded.StreamPosition != 42 {
		t.Fatalf("record cursor round trip = %#v, %v", decoded, err)
	}
	if encoded == "42" {
		t.Fatal("record cursor exposed its implementation value")
	}
	for _, invalid := range []string{"AQ", "AAAAAAAAAAAA", "AgAAAAAAAAAq", "AQAAAAAAAAAA"} {
		if _, err := decodeRecordCursor(invalid); err == nil {
			t.Fatalf("malformed cursor %q was accepted", invalid)
		}
	}
	if decoded, err := decodeRecordCursor(""); err != nil || decoded != nil {
		t.Fatalf("empty cursor = %#v, %v", decoded, err)
	}
}

func TestTemplateCollectionCursorsRoundTripAndRejectCrossKindValues(t *testing.T) {
	templateCursor := &controlplane.LabTemplateCursor{
		CreatedAt: time.UnixMicro(1_725_000_000_123_456).UTC(),
		ID:        "template.cursor",
	}
	encodedTemplate := encodeLabTemplateCursor(templateCursor)
	decodedTemplate, err := decodeLabTemplateCursor(encodedTemplate)
	if err != nil || decodedTemplate == nil || !decodedTemplate.CreatedAt.Equal(templateCursor.CreatedAt) || decodedTemplate.ID != templateCursor.ID {
		t.Fatalf("Lab Template cursor round trip = %#v, %v", decodedTemplate, err)
	}
	if _, err := decodeTemplateRevisionCursor(encodedTemplate); err == nil {
		t.Fatal("Lab Template cursor was accepted as a Template Revision cursor")
	}

	revisionCursor := &controlplane.TemplateRevisionCursor{RevisionNumber: 42, ID: "revision.cursor"}
	encodedRevision := encodeTemplateRevisionCursor(revisionCursor)
	decodedRevision, err := decodeTemplateRevisionCursor(encodedRevision)
	if err != nil || decodedRevision == nil || *decodedRevision != *revisionCursor {
		t.Fatalf("Template Revision cursor round trip = %#v, %v", decodedRevision, err)
	}
	if _, err := decodeLabTemplateCursor(encodedRevision); err == nil {
		t.Fatal("Template Revision cursor was accepted as a Lab Template cursor")
	}

	for _, invalid := range []string{"AQ", "AQEAAAAAAAAAAA", encodeCollectionCursor(templateCursorKind, 1, "not a valid id")} {
		if _, err := decodeLabTemplateCursor(invalid); err == nil {
			t.Fatalf("malformed Lab Template cursor %q was accepted", invalid)
		}
	}
	if decoded, err := decodeLabTemplateCursor(""); err != nil || decoded != nil {
		t.Fatalf("empty Lab Template cursor = %#v, %v", decoded, err)
	}
}

func TestAuditListPaginationAndBoundedExport(t *testing.T) {
	server, store := auditTestServer(t)
	payload, err := application.NewRedactedAuditPayload([]byte(`{"change":"created"}`))
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		if _, err := store.AppendAuditRecord(context.Background(), application.AppendAuditRecord{
			ID: fmt.Sprintf("audit.api.%d", index), SchemaVersion: "audit.labcentral.io/v1",
			Category: application.AuditTemplate, Action: "template.create", Outcome: application.AuditSucceeded,
			Actor:         application.RecordActor{Kind: application.RecordActorSystem, ID: "api-test"},
			AggregateType: "lab-template", AggregateID: fmt.Sprintf("template.api.%d", index), RedactedPayload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/audit-records?limit=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("audit list returned %d: %s", response.Code, response.Body.String())
	}
	var page struct {
		Items      []AuditRecordDTO `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "audit.api.1" || page.NextCursor == "" {
		t.Fatalf("first audit page = %#v", page)
	}

	response = httptest.NewRecorder()
	secondURL := "/api/v1/audit-records?limit=1&cursor=" + url.QueryEscape(page.NextCursor)
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, secondURL, nil))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "audit.api.2" {
		t.Fatalf("second audit page = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/audit-records/export?maxRecords=1&maxBytes=65536", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/x-ndjson" ||
		response.Header().Get("X-LabCentral-Export-Truncated") != "true" || response.Header().Get("X-LabCentral-Next-Cursor") == "" {
		t.Fatalf("audit export = %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n"); len(lines) != 1 || !json.Valid([]byte(lines[0])) {
		t.Fatalf("audit export body = %q", response.Body.String())
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/events?projectId=project.local", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("project-only event query returned %d: %s", response.Code, response.Body.String())
	}
}

func auditTestServer(t *testing.T) (*Server, *localdb.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:audit-http-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-")))
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pluginv1.RegisterProviderServiceServer(grpcServer, &pluginv1.UnimplementedProviderServiceServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///audit-http-provider",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	schema, err := os.ReadFile("../../schemas/lab-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewRenderer(schema, render.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	service, err := controlplane.NewService(renderer, store, pluginv1.NewProviderServiceClient(connection), controlplane.CryptoIDs{})
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	server, err := New(service, []byte("example"), static, Options{
		DefaultProviderTargetID: "target.local-docker", Authentication: AuthenticationOptions{Mode: AuthModeNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
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
	if info.AuthMode != "none" || info.NoAuthWarning == "" || info.DefaultProviderTargetID != "target.local-docker" {
		t.Fatalf("server info did not expose no-auth warning: %#v", info)
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/provider-targets", nil))
	var targets struct {
		Items []ProviderTargetDTO `json:"items"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &targets) != nil || targets.Items == nil || len(targets.Items) != 0 {
		t.Fatalf("empty provider-target response = %d %q", response.Code, response.Body.String())
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
	server, err := New(new(controlplane.Service), []byte("example"), static, Options{
		DefaultProviderTargetID: "target.local-docker",
		Authentication:          AuthenticationOptions{Mode: AuthModeNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestServerConstructionRequiresExplicitAuthenticationMode(t *testing.T) {
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	if _, err := New(new(controlplane.Service), []byte("example"), static, Options{}); err == nil {
		t.Fatal("server construction accepted an omitted authentication mode")
	}
}
