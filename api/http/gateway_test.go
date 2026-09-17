package httpapi

import (
	"context"
	"encoding/json"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGatewayDeviceFlowBindsBrowserSubjectClientAndCurrentAuthority(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	admin := f.client(t, "admin", "a-production-length-password")
	author := f.client(t, "author", "an-author-production-password")
	created := admin.request("POST", "/api/v1/admin/gateway-clients", map[string]any{"name": "External fixture"}, "", "")
	requireGovernanceStatus(t, created, 200)
	var registered struct {
		Client app.GatewayClient `json:"client"`
		Secret string            `json:"clientSecret"`
	}
	if e := json.Unmarshal(created.Body.Bytes(), &registered); e != nil || registered.Secret == "" {
		t.Fatal("client secret missing", e)
	}
	response := author.request("POST", "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "gateway-template"}, "", "gateway-template")
	requireGovernanceStatus(t, response, 200)
	var template LabTemplateDTO
	if e := json.Unmarshal(response.Body.Bytes(), &template); e != nil {
		t.Fatal(e)
	}
	scope, _ := json.Marshal(app.GatewayScope{ProjectID: "project.alpha", Actions: []string{"templates.read"}, TemplateIDs: []domain.TemplateID{domain.TemplateID(template.ID)}, InstanceIDs: []domain.InstanceID{}, TargetIDs: []domain.ProviderTargetID{}})
	formCall := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := secureRequest("POST", path, []byte(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(registered.Client.ID, registered.Secret)
		out := httptest.NewRecorder()
		f.server.Handler.ServeHTTP(out, req)
		return out
	}
	device := formCall("/api/v1/device/authorization", url.Values{"client_id": {registered.Client.ID}, "scope": {string(scope)}})
	requireGovernanceStatus(t, device, 200)
	var d deviceResponseBody
	if e := json.Unmarshal(device.Body.Bytes(), &d); e != nil || d.Interval != 5 || d.ExpiresIn != 300 || d.DeviceCode == "" || d.UserCode == "" {
		t.Fatalf("device %+v %v", d, e)
	}
	tampered := author.request("POST", "/api/v1/device/approve", map[string]any{"userCode": d.UserCode, "approve": true, "subjectId": "user.admin"}, "", "")
	requireGovernanceStatus(t, tampered, 422)
	reviewed := author.request("POST", "/api/v1/device/inspect", map[string]any{"userCode": d.UserCode}, "", "")
	requireGovernanceStatus(t, reviewed, 200)
	approval := author.request("POST", "/api/v1/device/approve", map[string]any{"userCode": d.UserCode, "approve": true}, "", "")
	requireGovernanceStatus(t, approval, 204)
	again := author.request("POST", "/api/v1/device/approve", map[string]any{"userCode": d.UserCode, "approve": true}, "", "")
	requireGovernanceStatus(t, again, 400)
	// RFC interval uses database time. Wait once for the published initial interval.
	time.Sleep(5100 * time.Millisecond)
	pollValues := url.Values{"client_id": {registered.Client.ID}, "device_code": {d.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
	issued := formCall("/api/v1/device/token", pollValues)
	requireGovernanceStatus(t, issued, 200)
	var token deviceTokenBody
	if e := json.Unmarshal(issued.Body.Bytes(), &token); e != nil || token.AccessToken == "" || token.ExpiresIn != 900 {
		t.Fatalf("token %+v %v", token, e)
	}
	requireGovernanceStatus(t, formCall("/api/v1/device/token", pollValues), 400)
	gateway := func(path, secret string) *httptest.ResponseRecorder {
		req := secureRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("X-Gateway-Client", registered.Client.ID)
		req.Header.Set("X-Gateway-Secret", secret)
		out := httptest.NewRecorder()
		f.server.Handler.ServeHTTP(out, req)
		return out
	}
	visible := gateway("/api/v1/gateway/templates", registered.Secret)
	requireGovernanceStatus(t, visible, 200)
	if !strings.Contains(visible.Body.String(), template.ID) {
		t.Fatal("approved template missing")
	}
	requireGovernanceStatus(t, gateway("/api/v1/admin/gateway-clients", registered.Secret), 403)
	requireGovernanceStatus(t, gateway("/api/v1/gateway/templates", "wrong"), 401)
	grants := author.request("GET", "/api/v1/session/gateway-grants", nil, "", "")
	requireGovernanceStatus(t, grants, 200)
	var list struct {
		Items []app.GatewayGrant `json:"items"`
	}
	if e := json.Unmarshal(grants.Body.Bytes(), &list); e != nil || len(list.Items) != 1 || list.Items[0].SubjectID != "user.author" {
		t.Fatalf("grant subject: %+v %v", list, e)
	}
	// Current authority is evaluated on every API call, after token authentication.
	originalUser, err := f.database.UserByID(context.Background(), "user.author")
	if err != nil {
		t.Fatal(err)
	}
	disabledUser := originalUser
	disabledUser.Disabled = true
	if e := f.database.PutUser(context.Background(), disabledUser); e != nil {
		t.Fatal(e)
	}
	requireGovernanceStatus(t, gateway("/api/v1/gateway/templates", registered.Secret), 401)
	if e := f.database.PutUser(context.Background(), originalUser); e != nil {
		t.Fatal(e)
	}
	author = f.client(t, "author", "an-author-production-password")
	revoked := author.request("POST", "/api/v1/session/gateway-grants/"+list.Items[0].ID+"/revoke", nil, "", "")
	requireGovernanceStatus(t, revoked, 204)
	requireGovernanceStatus(t, gateway("/api/v1/gateway/templates", registered.Secret), 401)
	audit, e := f.database.ListAuditRecords(context.Background(), app.AuditRecordFilter{ActorID: "user.author", Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, r := range audit.Records {
		if r.Actor.GatewayClientID == registered.Client.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("audit omitted gateway identity")
	}
}
