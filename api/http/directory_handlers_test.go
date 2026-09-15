package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectoryVisibilityPaginationAndAdministration(t *testing.T) {
	server, _ := localAuthenticationFixture(t, nil)
	admin, csrf := loginCookies(t, server, "admin", "a-production-length-password")
	post := func(path, body string) {
		t.Helper()
		r := secureRequest(http.MethodPost, "/api/v1"+path, []byte(body))
		r.Header.Set("Origin", "https://lab.example")
		r.Header.Set("X-CSRF-Token", csrf.Value)
		r.AddCookie(admin)
		r.AddCookie(csrf)
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/organizations", `{"id":"org.class","name":"class"}`)
	post("/projects", `{"id":"project.hidden-first","organizationId":"org.class","name":"hidden"}`)
	post("/projects", `{"id":"project.visible","organizationId":"org.class","name":"visible"}`)
	post("/projects", `{"id":"project.z-hidden-last","organizationId":"org.class","name":"hidden-last"}`)
	post("/users", `{"id":"user.student","username":"student","displayName":"Student","password":"a-student-production-password"}`)
	post("/memberships", `{"id":"membership.student","userId":"user.student","organizationId":"org.class"}`)
	post("/role-assignments", `{"id":"role.student","subjectKind":"user","subjectId":"user.student","scopeKind":"project","scopeId":"project.visible","role":"participant"}`)
	student, _ := loginCookies(t, server, "student", "a-student-production-password")
	get := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := secureRequest(http.MethodGet, "/api/v1"+path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, r)
		return w
	}
	w := get("/projects?limit=1", student)
	if w.Code != 200 {
		t.Fatalf("project list: %d %s", w.Code, w.Body.String())
	}
	var page ProjectDirectoryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "project.visible" || strings.Contains(w.Body.String(), "hidden") {
		t.Fatalf("visibility: %s", w.Body.String())
	}
	if len(page.Items[0].AllowedActions) != 1 || page.Items[0].AllowedActions[0] != "template.read" {
		t.Fatalf("student capabilities: %s", w.Body.String())
	}
	w = get("/projects?limit=1&after="+page.NextCursor, student)
	page = ProjectDirectoryDTO{}
	if w.Code != 200 || strings.Contains(w.Body.String(), "hidden") {
		t.Fatalf("last page: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("pagination did not end: %s", w.Body.String())
	}
	for _, path := range []string{"/organizations", "/provider-target-grants", "/admin/provider-targets", "/admin/operations", "/admin/lab-instances/instance.hidden/usage", "/users", "/memberships?organizationId=org.class", "/projects/project.visible/members?targetId=target.docker"} {
		w = get(path, student)
		if w.Code != 403 {
			t.Fatalf("student directory %s: %d %s", path, w.Code, w.Body.String())
		}
	}

	for _, path := range []string{"/organizations?limit=201", "/provider-target-grants?limit=201", "/admin/operations?limit=201", "/admin/operations?cursor=bad", "/admin/operations?filter=unknown"} {
		w = get(path, admin)
		if w.Code != 400 && w.Code != 422 {
			t.Fatalf("invalid query accepted: %s %d", path, w.Code)
		}
	}

	post("/organizations", `{"id":"org.empty","name":"No workspaces yet"}`)
	post("/provider-target-grants", `{"id":"grant.class","targetId":"target.docker","scopeKind":"project","scopeId":"project.visible"}`)
	post("/provider-target-grants", `{"id":"grant.empty","targetId":"target.docker","scopeKind":"organization","scopeId":"org.empty"}`)
	for _, path := range []string{"/organizations", "/provider-target-grants"} {
		w = get(path+"?limit=1", admin)
		var first struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.NextCursor != first.Items[0].ID {
			t.Fatalf("first admin page %s: %d %s", path, w.Code, w.Body.String())
		}
		w = get(path+"?after="+first.NextCursor, admin)
		if w.Code != 200 || strings.Contains(w.Body.String(), `"id":"`+first.Items[0].ID+`"`) || !strings.Contains(w.Body.String(), "empty") {
			t.Fatalf("next admin page %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = get("/admin/provider-targets", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || strings.Contains(w.Body.String(), "credential") {
		t.Fatalf("global target inventory: %d %s", w.Code, w.Body.String())
	}
	w = get("/users?limit=1", admin)
	if w.Code != 200 || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "externalSubject") {
		t.Fatalf("admin directory: %d %s", w.Code, w.Body.String())
	}
	w = get("/memberships?organizationId=org.class", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "membership.student") {
		t.Fatalf("memberships: %d %s", w.Code, w.Body.String())
	}
	w = get("/projects?limit=201", admin)
	if w.Code != 422 {
		t.Fatalf("unbounded query: %d", w.Code)
	}
}

func TestOpenAPIDocumentIncludesAuthenticatedDirectories(t *testing.T) {
	document := OpenAPIDocument()
	for _, path := range []string{"/organizations", "/provider-target-grants", "/admin/provider-targets", "/users", "/projects", "/memberships", "/role-assignments", "/projects/{id}/members", "/lab-templates/{id}/revisions/{revisionId}/content"} {
		if document.Paths[path] == nil {
			t.Errorf("missing complete API contract %s", path)
		}
	}
}
