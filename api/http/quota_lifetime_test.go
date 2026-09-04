package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
)

func TestQuotaLifetimeOpenAPIContract(t *testing.T) {
	fixture := newNoAuthQuotaHTTPFixture(t)
	encoded, err := json.Marshal(fixture.server.API.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	type openAPIParameter struct {
		Name     string `json:"name"`
		In       string `json:"in"`
		Required bool   `json:"required"`
	}
	type openAPIOperation struct {
		Parameters  []openAPIParameter `json:"parameters"`
		RequestBody json.RawMessage    `json:"requestBody"`
	}
	var document struct {
		Paths map[string]map[string]*openAPIOperation `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	type operationContract struct {
		path       string
		method     string
		parameters map[string]string
		body       bool
	}
	contracts := []operationContract{
		{path: "/quotas/{scopeKind}/{scopeId}/{dimension}", method: "get", parameters: map[string]string{
			"scopeKind": "path", "scopeId": "path", "dimension": "path", "organizationId": "query",
		}},
		{path: "/quotas/{scopeKind}/{scopeId}/{dimension}", method: "put", body: true, parameters: map[string]string{
			"scopeKind": "path", "scopeId": "path", "dimension": "path", "organizationId": "query",
			"If-Match": "header", "If-None-Match": "header", "Idempotency-Key": "header",
		}},
		{path: "/quota-usage/{scopeKind}/{scopeId}", method: "get", parameters: map[string]string{
			"scopeKind": "path", "scopeId": "path", "organizationId": "query",
		}},
		{path: "/lifetime-policies/{scopeKind}/{scopeId}", method: "get", parameters: map[string]string{
			"scopeKind": "path", "scopeId": "path", "organizationId": "query",
		}},
		{path: "/lifetime-policies/{scopeKind}/{scopeId}", method: "put", body: true, parameters: map[string]string{
			"scopeKind": "path", "scopeId": "path", "organizationId": "query",
			"If-Match": "header", "If-None-Match": "header", "Idempotency-Key": "header",
		}},
		{path: "/lab-instances/{id}/actions/extend-ttl", method: "post", body: true, parameters: map[string]string{
			"id": "path", "If-Match": "header", "Idempotency-Key": "header",
		}},
	}
	for _, contract := range contracts {
		operation := document.Paths[contract.path][contract.method]
		if operation == nil {
			t.Errorf("OpenAPI operation %s %s is missing", contract.method, contract.path)
			continue
		}
		actual := make(map[string]openAPIParameter, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			actual[parameter.Name] = parameter
		}
		for name, location := range contract.parameters {
			parameter, ok := actual[name]
			if !ok || parameter.In != location {
				t.Errorf("OpenAPI operation %s %s parameter %s = %#v, want location %s", contract.method, contract.path, name, parameter, location)
				continue
			}
			if location == "path" && !parameter.Required {
				t.Errorf("OpenAPI operation %s %s path parameter %s is not required", contract.method, contract.path, name)
			}
		}
		if contract.body != (len(operation.RequestBody) != 0) {
			t.Errorf("OpenAPI operation %s %s request body presence = %t, want %t", contract.method, contract.path, len(operation.RequestBody) != 0, contract.body)
		}
	}
}

func TestNoAuthQuotaAndLifetimeHTTPResourceLifecycle(t *testing.T) {
	fixture := newNoAuthQuotaHTTPFixture(t)
	quotaPath := "/api/v1/quotas/project/project.local/instances?organizationId=org.local"
	response := quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath,
		QuotaLimitMutationBody{Limit: 7}, map[string]string{"If-None-Match": "*", "Idempotency-Key": "quota-create-project"})
	requireQuotaStatus(t, response, http.StatusOK)
	var quota application.QuotaLimitRecord
	decodeQuotaResponse(t, response, &quota)
	if quota.Scope.Kind != application.QuotaScopeProject || quota.Scope.ID != "project.local" ||
		quota.Scope.OrganizationID != "org.local" || quota.Dimension != application.QuotaInstances ||
		quota.Limit != 7 || quota.ResourceVersion != 1 || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("created quota = %#v ETag=%q", quota, response.Header().Get("ETag"))
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodGet, quotaPath, nil, nil)
	requireQuotaStatus(t, response, http.StatusOK)
	var loadedQuota application.QuotaLimitRecord
	decodeQuotaResponse(t, response, &loadedQuota)
	if loadedQuota != quota || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("loaded quota = %#v ETag=%q", loadedQuota, response.Header().Get("ETag"))
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodGet,
		"/api/v1/quota-usage/project/project.local?organizationId=org.local", nil, nil)
	requireQuotaStatus(t, response, http.StatusOK)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("quota usage cache control = %q", response.Header().Get("Cache-Control"))
	}
	var usage struct {
		Scope application.QuotaScope `json:"scope"`
		Items []QuotaUsageDTO        `json:"items"`
	}
	decodeQuotaResponse(t, response, &usage)
	if usage.Scope != quota.Scope || len(usage.Items) != 1 || usage.Items[0].Dimension != application.QuotaInstances ||
		usage.Items[0].Total != 0 || usage.Items[0].UpdatedAt.IsZero() {
		t.Fatalf("initial quota usage = %#v", usage)
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath,
		QuotaLimitMutationBody{Limit: 9}, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "quota-update-project"})
	requireQuotaStatus(t, response, http.StatusOK)
	decodeQuotaResponse(t, response, &quota)
	if quota.Limit != 9 || quota.ResourceVersion != 2 || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("updated quota = %#v ETag=%q", quota, response.Header().Get("ETag"))
	}

	policyPath := "/api/v1/lifetime-policies/project/project.local?organizationId=org.local"
	policyBody := LifetimePolicyMutationBody{
		DefaultTTLSeconds: 300, MaximumTTLSeconds: 3600, RenewalWindowSeconds: 300,
		WarningBeforeSeconds: 60, DestructionGraceSeconds: 30, RenewalAllowed: true,
	}
	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, policyPath, policyBody,
		map[string]string{"If-None-Match": "*", "Idempotency-Key": "lifetime-create-project"})
	requireQuotaStatus(t, response, http.StatusOK)
	var policy application.LifetimePolicyRecord
	decodeQuotaResponse(t, response, &policy)
	if policy.Scope != quota.Scope || policy.DefaultTTLSeconds != 300 || policy.MaximumTTLSeconds != 3600 ||
		!policy.RenewalAllowed || policy.ResourceVersion != 1 || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("created lifetime policy = %#v ETag=%q", policy, response.Header().Get("ETag"))
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodGet, policyPath, nil, nil)
	requireQuotaStatus(t, response, http.StatusOK)
	var loadedPolicy application.LifetimePolicyRecord
	decodeQuotaResponse(t, response, &loadedPolicy)
	if loadedPolicy != policy || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("loaded lifetime policy = %#v ETag=%q", loadedPolicy, response.Header().Get("ETag"))
	}

	policyBody.DefaultTTLSeconds = 600
	policyBody.PersistentAllowed = true
	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, policyPath, policyBody,
		map[string]string{"If-Match": `"1"`, "Idempotency-Key": "lifetime-update-project"})
	requireQuotaStatus(t, response, http.StatusOK)
	decodeQuotaResponse(t, response, &policy)
	if policy.DefaultTTLSeconds != 600 || !policy.PersistentAllowed || policy.ResourceVersion != 2 || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("updated lifetime policy = %#v ETag=%q", policy, response.Header().Get("ETag"))
	}
}

func TestQuotaLifetimeHTTPPreconditionsAndMalformedScopes(t *testing.T) {
	fixture := newNoAuthQuotaHTTPFixture(t)
	quotaPath := "/api/v1/quotas/project/project.local/instances?organizationId=org.local"
	body := QuotaLimitMutationBody{Limit: 7}

	response := quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, body,
		map[string]string{"Idempotency-Key": "quota-missing-condition"})
	requireQuotaStatus(t, response, http.StatusPreconditionRequired)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, body, map[string]string{
		"If-Match": `"1"`, "If-None-Match": "*", "Idempotency-Key": "quota-both-conditions",
	})
	requireQuotaStatus(t, response, http.StatusBadRequest)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, body,
		map[string]string{"If-None-Match": "not-a-star", "Idempotency-Key": "quota-invalid-none-match"})
	requireQuotaStatus(t, response, http.StatusBadRequest)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, body,
		map[string]string{"If-None-Match": "*", "Idempotency-Key": "quota-create-precondition"})
	requireQuotaStatus(t, response, http.StatusOK)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, QuotaLimitMutationBody{Limit: 8},
		map[string]string{"If-Match": `"99"`, "Idempotency-Key": "quota-stale-version"})
	requireQuotaStatus(t, response, http.StatusPreconditionFailed)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPut, quotaPath, QuotaLimitMutationBody{Limit: 8},
		map[string]string{"If-None-Match": "*", "Idempotency-Key": "quota-create-existing"})
	requireQuotaStatus(t, response, http.StatusPreconditionFailed)

	for path, want := range map[string]int{
		"/api/v1/quotas/project/project.local/instances":                            http.StatusBadRequest,
		"/api/v1/quota-usage/platform/platform?organizationId=org.local":            http.StatusBadRequest,
		"/api/v1/lifetime-policies/organization/org.local?organizationId=org.other": http.StatusBadRequest,
	} {
		response = quotaHTTPRequest(t, fixture.server, http.MethodGet, path, nil, nil)
		if response.Code != want {
			t.Errorf("malformed quota scope %s returned %d, want %d: %s", path, response.Code, want, response.Body.String())
		}
	}
}

func TestNoAuthTTLRenewalHTTPResourceVersion(t *testing.T) {
	fixture := newNoAuthQuotaHTTPFixture(t)
	instance := seedQuotaHTTPInstance(t, fixture.store, "instance.ttl-noauth", "project.local", controlplane.LocalAdminID, application.OwnerUser, string(controlplane.LocalAdminID), 60)
	path := "/api/v1/lab-instances/" + string(instance.ID) + "/actions/extend-ttl"

	response := quotaHTTPRequest(t, fixture.server, http.MethodPost, path, map[string]any{"ttlSeconds": 120},
		map[string]string{"If-Match": `"1"`, "Idempotency-Key": "ttl-extend-noauth"})
	requireQuotaStatus(t, response, http.StatusOK)
	var renewed InstanceLifetimeDTO
	decodeQuotaResponse(t, response, &renewed)
	if renewed.InstanceID != string(instance.ID) || renewed.ResourceVersion != 2 ||
		!renewed.ExpiresAt.After(instance.ExpiresAt) || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("renewed instance = %#v ETag=%q, original expiry=%s", renewed, response.Header().Get("ETag"), instance.ExpiresAt)
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodPost, path, map[string]any{"ttlSeconds": 120},
		map[string]string{"If-Match": `"1"`, "Idempotency-Key": "ttl-extend-noauth"})
	requireQuotaStatus(t, response, http.StatusOK)
	var replayed InstanceLifetimeDTO
	decodeQuotaResponse(t, response, &replayed)
	if replayed.ResourceVersion != renewed.ResourceVersion || !replayed.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("replayed TTL renewal = %#v, want expiry %v and version %d", replayed, renewed.ExpiresAt, renewed.ResourceVersion)
	}

	response = quotaHTTPRequest(t, fixture.server, http.MethodPost, path, map[string]any{"ttlSeconds": 180},
		map[string]string{"If-Match": `"1"`, "Idempotency-Key": "ttl-stale-version"})
	requireQuotaStatus(t, response, http.StatusPreconditionFailed)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPost, path, map[string]any{"ttlSeconds": 180},
		map[string]string{"Idempotency-Key": "ttl-missing-condition"})
	requireQuotaStatus(t, response, http.StatusPreconditionRequired)

	response = quotaHTTPRequest(t, fixture.server, http.MethodPost, "/api/v1/lab-instances/not%20valid/actions/extend-ttl",
		map[string]any{"ttlSeconds": 180}, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "ttl-invalid-instance"})
	requireQuotaStatus(t, response, http.StatusNotFound)
}

func TestAuthenticatedQuotaAndTTLDenialsPrecedeGovernanceStore(t *testing.T) {
	base := newGovernanceHTTPFixture(t)
	server, counted := newAuthenticatedQuotaCountingServer(t, base.database)
	author := governanceClient{t: t, server: server}
	author.session, author.csrf = loginCookies(t, server, "author", "an-author-production-password")
	auditor := governanceClient{t: t, server: server}
	auditor.session, auditor.csrf = loginCookies(t, server, "auditor", "an-auditor-production-password")

	instance := seedQuotaHTTPInstance(t, base.database, "instance.ttl-authz", "project.alpha", "user.author", application.OwnerUser, "user.author", 60)
	before := counted.snapshot()
	requests := []*httptest.ResponseRecorder{
		author.request(http.MethodGet, "/api/v1/quotas/project/project.alpha/instances?organizationId=org.alpha", nil, "", ""),
		author.request(http.MethodPut, "/api/v1/quotas/project/project.alpha/instances?organizationId=org.alpha",
			QuotaLimitMutationBody{Limit: 7}, `"1"`, "quota-authz-denied"),
		author.request(http.MethodGet, "/api/v1/quota-usage/project/project.alpha?organizationId=org.alpha", nil, "", ""),
		author.request(http.MethodGet, "/api/v1/lifetime-policies/project/project.alpha?organizationId=org.alpha", nil, "", ""),
		author.request(http.MethodPut, "/api/v1/lifetime-policies/project/project.alpha?organizationId=org.alpha",
			LifetimePolicyMutationBody{DefaultTTLSeconds: 60, MaximumTTLSeconds: 3600, RenewalAllowed: true}, `"1"`, "lifetime-authz-denied"),
	}
	for index, response := range requests {
		if response.Code != http.StatusForbidden {
			t.Fatalf("unauthorized quota/lifetime request %d returned %d: %s", index, response.Code, response.Body.String())
		}
	}
	response := auditor.request(http.MethodPost, "/api/v1/lab-instances/"+string(instance.ID)+"/actions/extend-ttl",
		map[string]any{"ttlSeconds": 120}, `"1"`, "ttl-authz-denied")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unauthorized TTL renewal returned %d: %s", response.Code, response.Body.String())
	}
	if after := counted.snapshot(); after != before {
		t.Fatalf("unauthorized quota/lifetime requests reached governance Store: before=%+v after=%+v", before, after)
	}
}

type noAuthQuotaHTTPFixture struct {
	server  *Server
	store   *localdb.Store
	service *controlplane.Service
}

func newNoAuthQuotaHTTPFixture(t *testing.T) noAuthQuotaHTTPFixture {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:quota-http-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	service := newQuotaHTTPService(t, store)
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>LabCentral</h1>")}}
	server, err := New(service, []byte("example"), static, Options{
		DefaultProviderTargetID: string(controlplane.LocalDockerTarget),
		Authentication:          AuthenticationOptions{Mode: AuthModeNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	return noAuthQuotaHTTPFixture{server: server, store: store, service: service}
}

func newQuotaHTTPService(t *testing.T, store controlplane.Store) *controlplane.Service {
	t.Helper()
	schema, err := os.ReadFile("../../schemas/lab-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewRenderer(schema, render.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	service, err := controlplane.NewService(renderer, store, providergrpc.New(pluginv1.NewProviderServiceClient(nil)), controlplane.CryptoIDs{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func quotaHTTPRequest(t *testing.T, server *Server, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	return response
}

func requireQuotaStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("HTTP response = %d, want %d: %s", response.Code, want, response.Body.String())
	}
}

func decodeQuotaResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode HTTP %d response: %v body=%s", response.Code, err, response.Body.String())
	}
}

func seedQuotaHTTPInstance(t *testing.T, store *localdb.Store, instanceID domain.InstanceID, projectID domain.ProjectID,
	actor domain.UserID, ownerKind application.OwnerKind, ownerID string, ttlSeconds uint64,
) controlplane.InstanceRecord {
	t.Helper()
	plan := []byte(`{"planned":true}`)
	canonical := []byte(`{"apiVersion":"labcentral.io/v1alpha1"}`)
	planDigest, canonicalDigest, requestDigest := sha256.Sum256(plan), sha256.Sum256(canonical), sha256.Sum256([]byte(instanceID))
	_, err := store.CreateInstance(context.Background(), controlplane.CreateInstanceRequest{
		InstanceID: instanceID, ProjectID: projectID, OwnerKind: ownerKind, OwnerID: ownerID, CreatedBy: actor,
		ProviderTargetID: controlplane.LocalDockerTarget, OperationID: domain.OperationID("operation." + string(instanceID)),
		CanonicalDigest: domain.SHA256Digest(fmt.Sprintf("%x", canonicalDigest)), CanonicalJSON: canonical,
		SourceYAML: []byte("apiVersion: labcentral.io/v1alpha1\n"), Acknowledged: true,
		Resources: []controlplane.ResourceSeed{{
			ID: "container.ttl", Kind: domain.ResourceContainer, CanonicalJSON: []byte(`{"kind":"Container"}`),
			ProviderPlan: plan, ProviderPlanDigest: domain.SHA256Digest(fmt.Sprintf("%x", planDigest)),
			OwnershipToken: "ownership." + string(instanceID),
		}},
		SubjectID: actor, IdempotencyKey: "idempotency." + string(instanceID),
		RequestDigest: domain.SHA256Digest(fmt.Sprintf("%x", requestDigest)), Payload: []byte(`{"schemaVersion":1}`),
		AuditID: "audit." + string(instanceID), EventID: "event." + string(instanceID),
		Quota: application.InstanceQuotaRequest{
			ReservationID: domain.QuotaReservationID("quota." + string(instanceID)),
			Amounts:       []application.QuotaAmount{{Dimension: application.QuotaInstances, Value: 1}},
			Lifetime:      application.InstanceLifetimeRequest{TTLSeconds: ttlSeconds},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := store.GetInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

type quotaHTTPStoreSnapshot struct {
	putQuota      int64
	getQuota      int64
	listUsage     int64
	putPolicy     int64
	getPolicy     int64
	renewLifetime int64
}

type quotaHTTPCountingStore struct {
	controlplane.Store
	putQuota      atomic.Int64
	getQuota      atomic.Int64
	listUsage     atomic.Int64
	putPolicy     atomic.Int64
	getPolicy     atomic.Int64
	renewLifetime atomic.Int64
}

func (store *quotaHTTPCountingStore) PutQuotaLimit(ctx context.Context, request controlplane.PutQuotaLimitRequest) (application.QuotaLimitRecord, error) {
	store.putQuota.Add(1)
	return store.Store.PutQuotaLimit(ctx, request)
}

func (store *quotaHTTPCountingStore) GetQuotaLimit(ctx context.Context, scope application.QuotaScope, dimension application.QuotaDimension) (application.QuotaLimitRecord, error) {
	store.getQuota.Add(1)
	return store.Store.GetQuotaLimit(ctx, scope, dimension)
}

func (store *quotaHTTPCountingStore) ListQuotaUsage(ctx context.Context, scope application.QuotaScope) ([]application.QuotaUsageRecord, error) {
	store.listUsage.Add(1)
	return store.Store.ListQuotaUsage(ctx, scope)
}

func (store *quotaHTTPCountingStore) PutLifetimePolicy(ctx context.Context, request controlplane.PutLifetimePolicyRequest) (application.LifetimePolicyRecord, error) {
	store.putPolicy.Add(1)
	return store.Store.PutLifetimePolicy(ctx, request)
}

func (store *quotaHTTPCountingStore) GetLifetimePolicy(ctx context.Context, scope application.QuotaScope) (application.LifetimePolicyRecord, error) {
	store.getPolicy.Add(1)
	return store.Store.GetLifetimePolicy(ctx, scope)
}

func (store *quotaHTTPCountingStore) RenewInstanceLifetime(ctx context.Context, request controlplane.RenewInstanceLifetimeRequest) (controlplane.InstanceRecord, error) {
	store.renewLifetime.Add(1)
	return store.Store.RenewInstanceLifetime(ctx, request)
}

func (store *quotaHTTPCountingStore) snapshot() quotaHTTPStoreSnapshot {
	return quotaHTTPStoreSnapshot{
		putQuota: store.putQuota.Load(), getQuota: store.getQuota.Load(), listUsage: store.listUsage.Load(),
		putPolicy: store.putPolicy.Load(), getPolicy: store.getPolicy.Load(), renewLifetime: store.renewLifetime.Load(),
	}
}

func newAuthenticatedQuotaCountingServer(t *testing.T, database *localdb.Store) (*Server, *quotaHTTPCountingStore) {
	t.Helper()
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
	evaluator, err := authorization.New(database)
	if err != nil {
		t.Fatal(err)
	}
	counted := &quotaHTTPCountingStore{Store: database}
	service := newQuotaHTTPService(t, counted)
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
	return server, counted
}
