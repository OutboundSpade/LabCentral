package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
)

func TestAuthenticatedTemplateAPIScopesCRUDAndPublication(t *testing.T) {
	fixture := newGovernanceHTTPFixture(t)
	author := fixture.client(t, "author", "an-author-production-password")

	response := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{
		"projectId": "project.alpha", "name": "intro-lab", "description": "Initial classroom lab",
	}, "", "template-create-alpha")
	requireGovernanceStatus(t, response, http.StatusOK)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, response, &template)
	if template.ID == "" || template.ProjectID != "project.alpha" || template.Name != "intro-lab" || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("created Template = %#v ETag=%q", template, response.Header().Get("ETag"))
	}

	response = author.request(http.MethodGet, "/api/v1/lab-templates?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var templates struct {
		Items []LabTemplateDTO `json:"items"`
	}
	decodeGovernanceResponse(t, response, &templates)
	if len(templates.Items) != 1 || templates.Items[0].ID != template.ID {
		t.Fatalf("scoped Template list = %#v", templates.Items)
	}

	response = author.request(http.MethodGet, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var loaded LabTemplateDTO
	decodeGovernanceResponse(t, response, &loaded)
	if loaded.ID != template.ID || loaded.ProjectID != "project.alpha" {
		t.Fatalf("loaded Template = %#v", loaded)
	}

	response = author.request(http.MethodPut, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.alpha", map[string]any{
		"name": "intro-lab-v2", "description": "Updated classroom lab",
	}, `"1"`, "template-update-alpha")
	requireGovernanceStatus(t, response, http.StatusOK)
	decodeGovernanceResponse(t, response, &template)
	if template.Name != "intro-lab-v2" || template.ResourceVersion != 2 || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("updated Template = %#v ETag=%q", template, response.Header().Get("ETag"))
	}
	response = author.request(http.MethodPut, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.alpha", map[string]any{
		"name": "intro-lab-v2", "description": "Updated classroom lab",
	}, `"1"`, "template-update-alpha")
	requireGovernanceStatus(t, response, http.StatusOK)
	var replayedTemplate LabTemplateDTO
	decodeGovernanceResponse(t, response, &replayedTemplate)
	if replayedTemplate != template || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replayed Template update = %#v ETag=%q", replayedTemplate, response.Header().Get("ETag"))
	}
	response = author.request(http.MethodPut, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.alpha", map[string]any{
		"name": "changed-under-same-key", "description": "conflicting retry",
	}, `"1"`, "template-update-alpha")
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting Template update replay returned %d: %s", response.Code, response.Body.String())
	}

	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	response = author.request(http.MethodPost,
		"/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions?projectId=project.alpha",
		map[string]any{"source": string(source)}, "", "template-publish-alpha")
	requireGovernanceStatus(t, response, http.StatusOK)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &revision)
	if revision.ID == "" || revision.TemplateID != template.ID || revision.RevisionNumber != 1 || revision.BundleDigest == "" {
		t.Fatalf("published Template Revision = %#v", revision)
	}

	response = author.request(http.MethodGet,
		"/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var revisions struct {
		Items []TemplateRevisionDTO `json:"items"`
	}
	decodeGovernanceResponse(t, response, &revisions)
	if len(revisions.Items) != 1 || revisions.Items[0].ID != revision.ID {
		t.Fatalf("scoped Template Revision list = %#v", revisions.Items)
	}

	response = author.request(http.MethodGet,
		"/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions/"+url.PathEscape(revision.ID)+"?projectId=project.alpha",
		nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var loadedRevision TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &loadedRevision)
	if loadedRevision.ID != revision.ID || loadedRevision.TemplateID != template.ID {
		t.Fatalf("loaded Template Revision = %#v", loadedRevision)
	}

	storeBefore := fixture.store.snapshot()
	artifactsBefore := fixture.artifacts.snapshot()
	if storeBefore.createTemplate != 1 || storeBefore.updateTemplate != 3 || storeBefore.listTemplates == 0 ||
		storeBefore.getTemplate == 0 || storeBefore.publishRevision != 1 || storeBefore.listRevisions == 0 || storeBefore.getRevision == 0 {
		t.Fatalf("successful Template API requests bypassed access counters: %+v", storeBefore)
	}
	if artifactsBefore.stage != 1 || artifactsBefore.write != 1 || artifactsBefore.commit != 1 || artifactsBefore.open != 0 {
		t.Fatalf("successful Template publication artifact calls = %+v", artifactsBefore)
	}
	denied := []*httptest.ResponseRecorder{
		author.request(http.MethodGet, "/api/v1/lab-templates?projectId=project.gamma", nil, "", ""),
		author.request(http.MethodGet, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.gamma", nil, "", ""),
		author.request(http.MethodGet, "/api/v1/lab-templates?projectId=project.beta", nil, "", ""),
		author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{
			"projectId": "project.beta", "name": "stolen-lab", "description": "must not persist",
		}, "", "template-create-beta"),
		author.request(http.MethodGet, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.beta", nil, "", ""),
		author.request(http.MethodPut, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"?projectId=project.beta", map[string]any{
			"name": "stolen-lab", "description": "must not persist",
		}, `"2"`, "template-update-beta"),
		author.request(http.MethodGet, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions?projectId=project.beta", nil, "", ""),
		author.request(http.MethodPost, "/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions?projectId=project.beta",
			map[string]any{"source": string(source)}, "", "template-publish-beta"),
		author.request(http.MethodGet,
			"/api/v1/lab-templates/"+url.PathEscape(template.ID)+"/revisions/"+url.PathEscape(revision.ID)+"?projectId=project.beta",
			nil, "", ""),
	}
	for index, deniedResponse := range denied {
		if deniedResponse.Code != http.StatusForbidden {
			t.Fatalf("cross-organization Template request %d returned %d: %s", index, deniedResponse.Code, deniedResponse.Body.String())
		}
	}
	if got := fixture.store.snapshot(); got != storeBefore {
		t.Fatalf("denied Template requests reached persistent Template data: before=%+v after=%+v", storeBefore, got)
	}
	if got := fixture.artifacts.snapshot(); got != artifactsBefore {
		t.Fatalf("denied Template requests reached artifact storage: before=%+v after=%+v", artifactsBefore, got)
	}

	response = author.request(http.MethodPost, "/api/v1/lab-instances", map[string]any{
		"revisionId": revision.ID, "projectId": "project.gamma", "ownerKind": "user", "ownerId": "user.author",
		"acknowledgeDockerIsolationRisk": false,
	}, "", "instance-from-foreign-revision")
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-organization revision instance request returned %d: %s", response.Code, response.Body.String())
	}
	if got := fixture.artifacts.snapshot(); got != artifactsBefore {
		t.Fatalf("denied revision instance request opened artifact storage: before=%+v after=%+v", artifactsBefore, got)
	}
}

func TestAuthenticatedAuditAndEventAPIScopeAndFilters(t *testing.T) {
	fixture := newGovernanceHTTPFixture(t)
	ctx := context.Background()
	auditPayload, err := application.NewRedactedAuditPayload([]byte(`{"change":"created"}`))
	if err != nil {
		t.Fatal(err)
	}
	eventPayload, err := application.NewSafeEventPayload([]byte(`{"change":"created"}`))
	if err != nil {
		t.Fatal(err)
	}
	alphaScope := application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"}
	betaScope := application.RecordScope{OrganizationID: "org.beta", ProjectID: "project.beta"}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: "user.author"}
	for _, record := range []application.AppendAuditRecord{
		{ID: "audit.alpha.match", SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditTemplate,
			Action: "template.create", Outcome: application.AuditSucceeded, Scope: alphaScope, Actor: actor,
			AggregateType: "lab-template", AggregateID: "template.alpha", RedactedPayload: auditPayload},
		{ID: "audit.alpha.other", SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditOperation,
			Action: "operation.started", Outcome: application.AuditAccepted, Scope: alphaScope, Actor: actor,
			AggregateType: "operation", AggregateID: "operation.alpha", RedactedPayload: auditPayload},
		{ID: "audit.beta.match", SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditTemplate,
			Action: "template.create", Outcome: application.AuditSucceeded, Scope: betaScope, Actor: actor,
			AggregateType: "lab-template", AggregateID: "template.beta", RedactedPayload: auditPayload},
	} {
		if _, err := fixture.database.AppendAuditRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []application.AppendEventRecord{
		{ID: "event.alpha.match", SchemaVersion: "event.labcentral.io/v1", Type: "template.created", Scope: alphaScope,
			Actor: actor, AggregateType: "lab-template", AggregateID: "template.alpha", SafePayload: eventPayload},
		{ID: "event.alpha.other", SchemaVersion: "event.labcentral.io/v1", Type: "operation.started", Scope: alphaScope,
			Actor: actor, AggregateType: "operation", AggregateID: "operation.alpha", SafePayload: eventPayload},
		{ID: "event.beta.match", SchemaVersion: "event.labcentral.io/v1", Type: "template.created", Scope: betaScope,
			Actor: actor, AggregateType: "lab-template", AggregateID: "template.beta", SafePayload: eventPayload},
	} {
		if _, err := fixture.database.AppendEventRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	auditor := fixture.client(t, "auditor", "an-auditor-production-password")
	auditQuery := url.Values{
		"organizationId": {"org.alpha"}, "projectId": {"project.alpha"}, "actorId": {"user.author"},
		"category": {"template"}, "aggregateType": {"lab-template"}, "aggregateId": {"template.alpha"},
	}
	response := auditor.request(http.MethodGet, "/api/v1/audit-records?"+auditQuery.Encode(), nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var auditPage struct {
		Items []AuditRecordDTO `json:"items"`
	}
	decodeGovernanceResponse(t, response, &auditPage)
	if len(auditPage.Items) != 1 || auditPage.Items[0].ID != "audit.alpha.match" || auditPage.Items[0].Scope.ProjectID != "project.alpha" {
		t.Fatalf("filtered Audit page = %#v", auditPage.Items)
	}

	eventQuery := url.Values{
		"organizationId": {"org.alpha"}, "projectId": {"project.alpha"}, "actorId": {"user.author"},
		"type": {"template.created"}, "aggregateType": {"lab-template"}, "aggregateId": {"template.alpha"},
	}
	response = auditor.request(http.MethodGet, "/api/v1/events?"+eventQuery.Encode(), nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	var eventPage struct {
		Items []EventRecordDTO `json:"items"`
	}
	decodeGovernanceResponse(t, response, &eventPage)
	if len(eventPage.Items) != 1 || eventPage.Items[0].ID != "event.alpha.match" || eventPage.Items[0].Scope.ProjectID != "project.alpha" {
		t.Fatalf("filtered Event page = %#v", eventPage.Items)
	}

	exportQuery := auditQuery
	exportQuery.Set("maxRecords", "20")
	exportQuery.Set("maxBytes", "65536")
	response = auditor.request(http.MethodGet, "/api/v1/audit-records/export?"+exportQuery.Encode(), nil, "", "")
	requireGovernanceStatus(t, response, http.StatusOK)
	if response.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("Audit export Content-Type = %q", response.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("filtered Audit export = %q", response.Body.String())
	}
	var exported application.AuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &exported); err != nil || exported.ID != "audit.alpha.match" || exported.Scope.ProjectID != "project.alpha" {
		t.Fatalf("filtered Audit export record = %#v err=%v", exported, err)
	}

	before := fixture.store.snapshot()
	if before.listAuditRecords != 1 || before.listEventRecords != 1 || before.exportAudit != 1 {
		t.Fatalf("successful Audit/Event API requests bypassed access counters: %+v", before)
	}
	for index, deniedPath := range []string{
		"/api/v1/audit-records?organizationId=org.alpha&projectId=project.gamma",
		"/api/v1/events?organizationId=org.alpha&projectId=project.gamma",
		"/api/v1/audit-records/export?organizationId=org.alpha&projectId=project.gamma&maxRecords=20&maxBytes=65536",
		"/api/v1/audit-records?organizationId=org.beta&projectId=project.beta",
		"/api/v1/events?organizationId=org.beta&projectId=project.beta",
		"/api/v1/audit-records/export?organizationId=org.beta&projectId=project.beta&maxRecords=20&maxBytes=65536",
		"/api/v1/audit-records",
	} {
		denied := auditor.request(http.MethodGet, deniedPath, nil, "", "")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("unauthorized record query %d returned %d: %s", index, denied.Code, denied.Body.String())
		}
	}
	if got := fixture.store.snapshot(); got != before {
		t.Fatalf("denied Audit/Event requests reached record storage: before=%+v after=%+v", before, got)
	}
}

type governanceHTTPFixture struct {
	server    *Server
	database  *localdb.Store
	store     *governanceCountingStore
	artifacts *governanceCountingArtifacts
}

func newGovernanceHTTPFixture(t *testing.T) *governanceHTTPFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:governance-http-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	database, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	material := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32))
	keyring, err := authentication.ParseKeyring([]byte(fmt.Sprintf(
		`{"schemaVersion":"labcentral.session-keys/v1","currentKeyId":"current","keys":[{"id":"current","material":%q}]}`,
		material)))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := authentication.NewManager(database, keyring, authentication.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BootstrapLocalAdministrator(ctx, "user.admin", "admin", "Administrator", []byte("a-production-length-password")); err != nil {
		t.Fatal(err)
	}
	for _, organization := range []application.OrganizationRecord{
		{ID: "org.alpha", Name: "alpha"}, {ID: "org.beta", Name: "beta"},
	} {
		if err := database.PutOrganization(ctx, organization); err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []application.ProjectRecord{
		{ID: "project.alpha", OrganizationID: "org.alpha", Name: "alpha-classroom"},
		{ID: "project.gamma", OrganizationID: "org.alpha", Name: "gamma-classroom"},
		{ID: "project.beta", OrganizationID: "org.beta", Name: "beta-classroom"},
	} {
		if err := database.PutProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []struct {
		record   application.UserRecord
		password string
	}{
		{application.UserRecord{ID: "user.author", Username: "author", DisplayName: "Author"}, "an-author-production-password"},
		{application.UserRecord{ID: "user.auditor", Username: "auditor", DisplayName: "Auditor"}, "an-auditor-production-password"},
	} {
		hash, err := authentication.HashPassword([]byte(user.password))
		if err != nil {
			t.Fatal(err)
		}
		user.record.PasswordHash = hash
		if err := database.PutUser(ctx, user.record); err != nil {
			t.Fatal(err)
		}
		if err := database.PutMembership(ctx, application.MembershipRecord{
			ID: "membership." + strings.TrimPrefix(string(user.record.ID), "user."), UserID: user.record.ID,
			OrganizationID: "org.alpha", ValidFrom: time.Now().UTC().Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, assignment := range []application.RoleAssignmentRecord{
		{ID: "role.author", SubjectKind: application.SubjectUser, SubjectID: "user.author", ScopeKind: application.ScopeProject,
			ScopeID: "project.alpha", Role: application.RoleLabAuthor, ValidFrom: time.Now().UTC().Add(-time.Hour)},
		{ID: "role.instructor", SubjectKind: application.SubjectUser, SubjectID: "user.author", ScopeKind: application.ScopeProject,
			ScopeID: "project.alpha", Role: application.RoleInstructor, ValidFrom: time.Now().UTC().Add(-time.Hour)},
		{ID: "role.auditor", SubjectKind: application.SubjectUser, SubjectID: "user.auditor", ScopeKind: application.ScopeProject,
			ScopeID: "project.alpha", Role: application.RoleViewer, ValidFrom: time.Now().UTC().Add(-time.Hour)},
	} {
		if err := database.PutRoleAssignment(ctx, assignment); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.PutTargetGrant(ctx, application.TargetGrantRecord{
		ID: "grant.author", TargetID: controlplane.LocalDockerTarget, ScopeKind: application.ScopeProject,
		ScopeID: "project.alpha", ValidFrom: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	evaluator, err := authorization.New(database)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../schemas/lab-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewRenderer(schema, render.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	countingStore := &governanceCountingStore{Store: database}
	provider := pluginv1.NewProviderServiceClient(nil)
	service, err := controlplane.NewService(renderer, countingStore, providergrpc.New(provider), controlplane.CryptoIDs{})
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifactfs.New(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifactStore.Close() })
	countingArtifacts := &governanceCountingArtifacts{ArtifactStore: artifactStore}
	if err := service.SetArtifactStore(countingArtifacts); err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	server, err := New(service, []byte("example"), static, Options{
		DefaultProviderTargetID: string(controlplane.LocalDockerTarget),
		Authentication: AuthenticationOptions{
			Mode: AuthModeLocal, Manager: manager, Repository: database, Authorizer: evaluator,
			PublicOrigin: "https://lab.example", SecureCookies: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &governanceHTTPFixture{server: server, database: database, store: countingStore, artifacts: countingArtifacts}
}

type governanceClient struct {
	t       *testing.T
	server  *Server
	session *http.Cookie
	csrf    *http.Cookie
}

func (fixture *governanceHTTPFixture) client(t *testing.T, username, password string) governanceClient {
	t.Helper()
	session, csrf := loginCookies(t, fixture.server, username, password)
	return governanceClient{t: t, server: fixture.server, session: session, csrf: csrf}
}

func (client governanceClient) request(method, path string, body any, ifMatch, idempotencyKey string) *httptest.ResponseRecorder {
	client.t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			client.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, "https://lab.example"+path, bytes.NewReader(encoded))
	request.Host = "lab.example"
	request.TLS = &tls.ConnectionState{}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Origin", "https://lab.example")
		request.Header.Set("X-CSRF-Token", client.csrf.Value)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	request.AddCookie(client.session)
	request.AddCookie(client.csrf)
	response := httptest.NewRecorder()
	client.server.Handler.ServeHTTP(response, request)
	return response
}

func requireGovernanceStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("HTTP response = %d, want %d: %s", response.Code, want, response.Body.String())
	}
}

func decodeGovernanceResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode HTTP %d response: %v body=%s", response.Code, err, response.Body.String())
	}
}

type governanceStoreSnapshot struct {
	createTemplate   int64
	updateTemplate   int64
	getTemplate      int64
	listTemplates    int64
	publishRevision  int64
	getRevision      int64
	listRevisions    int64
	listAuditRecords int64
	listEventRecords int64
	exportAudit      int64
}

type governanceCountingStore struct {
	controlplane.Store
	createTemplate   atomic.Int64
	updateTemplate   atomic.Int64
	getTemplate      atomic.Int64
	listTemplates    atomic.Int64
	publishRevision  atomic.Int64
	getRevision      atomic.Int64
	listRevisions    atomic.Int64
	listAuditRecords atomic.Int64
	listEventRecords atomic.Int64
	exportAudit      atomic.Int64
}

func (store *governanceCountingStore) CreateLabTemplate(ctx context.Context, request controlplane.CreateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	store.createTemplate.Add(1)
	return store.Store.CreateLabTemplate(ctx, request)
}

func (store *governanceCountingStore) UpdateLabTemplate(ctx context.Context, request controlplane.UpdateLabTemplateRequest) (controlplane.LabTemplateRecord, error) {
	store.updateTemplate.Add(1)
	return store.Store.UpdateLabTemplate(ctx, request)
}

func (store *governanceCountingStore) GetLabTemplate(ctx context.Context, id domain.TemplateID) (controlplane.LabTemplateRecord, error) {
	store.getTemplate.Add(1)
	return store.Store.GetLabTemplate(ctx, id)
}

func (store *governanceCountingStore) ListLabTemplates(ctx context.Context, request controlplane.LabTemplateListRequest) (controlplane.LabTemplatePage, error) {
	store.listTemplates.Add(1)
	return store.Store.ListLabTemplates(ctx, request)
}

func (store *governanceCountingStore) PublishTemplateRevision(ctx context.Context, request controlplane.PublishTemplateRevisionRequest) (controlplane.TemplateRevisionRecord, error) {
	store.publishRevision.Add(1)
	return store.Store.PublishTemplateRevision(ctx, request)
}

func (store *governanceCountingStore) GetTemplateRevision(ctx context.Context, id domain.RevisionID) (controlplane.TemplateRevisionRecord, error) {
	store.getRevision.Add(1)
	return store.Store.GetTemplateRevision(ctx, id)
}

func (store *governanceCountingStore) ListTemplateRevisions(ctx context.Context, request controlplane.TemplateRevisionListRequest) (controlplane.TemplateRevisionPage, error) {
	store.listRevisions.Add(1)
	return store.Store.ListTemplateRevisions(ctx, request)
}

func (store *governanceCountingStore) ListAuditRecords(ctx context.Context, filter application.AuditRecordFilter) (application.AuditRecordPage, error) {
	store.listAuditRecords.Add(1)
	return store.Store.ListAuditRecords(ctx, filter)
}

func (store *governanceCountingStore) ListEventRecords(ctx context.Context, filter application.EventRecordFilter) (application.EventRecordPage, error) {
	store.listEventRecords.Add(1)
	return store.Store.ListEventRecords(ctx, filter)
}

func (store *governanceCountingStore) ExportAuditRecords(ctx context.Context, request application.AuditExportRequest, writer io.Writer) (application.AuditExportResult, error) {
	store.exportAudit.Add(1)
	return store.Store.ExportAuditRecords(ctx, request, writer)
}

func (store *governanceCountingStore) snapshot() governanceStoreSnapshot {
	return governanceStoreSnapshot{
		createTemplate: store.createTemplate.Load(), updateTemplate: store.updateTemplate.Load(), getTemplate: store.getTemplate.Load(),
		listTemplates: store.listTemplates.Load(), publishRevision: store.publishRevision.Load(), getRevision: store.getRevision.Load(),
		listRevisions: store.listRevisions.Load(), listAuditRecords: store.listAuditRecords.Load(),
		listEventRecords: store.listEventRecords.Load(), exportAudit: store.exportAudit.Load(),
	}
}

type governanceArtifactSnapshot struct {
	stage  int64
	write  int64
	commit int64
	open   int64
}

type governanceCountingArtifacts struct {
	application.ArtifactStore
	stage  atomic.Int64
	write  atomic.Int64
	commit atomic.Int64
	open   atomic.Int64
}

func (store *governanceCountingArtifacts) Stage(ctx context.Context, manifest application.VerifiedManifest) (domain.UploadID, error) {
	store.stage.Add(1)
	return store.ArtifactStore.Stage(ctx, manifest)
}

func (store *governanceCountingArtifacts) Write(ctx context.Context, id domain.UploadID, reader io.Reader) (application.MeasuredArtifact, error) {
	store.write.Add(1)
	return store.ArtifactStore.Write(ctx, id, reader)
}

func (store *governanceCountingArtifacts) Commit(ctx context.Context, id domain.UploadID, manifest application.VerifiedManifest) (domain.ArtifactRef, error) {
	store.commit.Add(1)
	return store.ArtifactStore.Commit(ctx, id, manifest)
}

func (store *governanceCountingArtifacts) Open(ctx context.Context, ref application.AuthorizedArtifactRef) (io.ReadCloser, error) {
	store.open.Add(1)
	return store.ArtifactStore.Open(ctx, ref)
}

func (store *governanceCountingArtifacts) snapshot() governanceArtifactSnapshot {
	return governanceArtifactSnapshot{stage: store.stage.Load(), write: store.write.Load(), commit: store.commit.Load(), open: store.open.Load()}
}
