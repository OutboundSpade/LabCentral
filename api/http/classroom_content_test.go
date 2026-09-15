package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClassroomRosterFiltersAndRechecksMembership(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	path := "/api/v1/projects/project.alpha/members?targetId=target.local-docker&limit=1"
	r := author.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, r, 200)
	if !strings.Contains(r.Body.String(), "user.author") || strings.Contains(r.Body.String(), "user.auditor") {
		t.Fatalf("roster: %s", r.Body.String())
	}
	r = author.request(http.MethodGet, "/api/v1/projects/project.gamma/members?targetId=target.local-docker", nil, "", "")
	requireGovernanceStatus(t, r, 403)
	r = author.request(http.MethodGet, "/api/v1/projects/project.alpha/members?targetId=target.foreign", nil, "", "")
	requireGovernanceStatus(t, r, 403)
	_, err := f.database.RevokeMembership(context.Background(), "membership.author", "org.alpha", 1, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r = author.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, r, 403)
}

func TestTemplateContentReturnsDeclarationsAndChecksScope(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	r := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]string{"projectId": "project.alpha", "name": "parameter-lab"}, "", "content-template-create")
	requireGovernanceStatus(t, r, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, r, &template)
	source := "apiVersion: labs.labcentral.io/v1alpha1\nkind: Lab\nmetadata:\n  name: parameter-lab\nspec:\n  parameters:\n    - name: hostname\n      type: string\n      required: true\n      description: Workstation name\n  resources: {}\n"
	r = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/revisions?projectId=project.alpha", map[string]string{"source": source}, "", "content-revision-publish")
	requireGovernanceStatus(t, r, 200)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, r, &revision)
	path := "/api/v1/lab-templates/" + template.ID + "/revisions/" + revision.ID + "/content"
	r = author.request(http.MethodGet, path+"?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, r, 200)
	var content TemplateContentDTO
	decodeGovernanceResponse(t, r, &content)
	if content.Source != source || len(content.Parameters) != 1 || content.Parameters[0].Name != "hostname" || !content.Parameters[0].Required {
		t.Fatalf("content: %#v", content)
	}
	before := f.artifacts.snapshot().open
	for _, project := range []string{"project.beta", "project.gamma"} {
		r = author.request(http.MethodGet, path+"?projectId="+project, nil, "", "")
		requireGovernanceStatus(t, r, 403)
	}
	if f.artifacts.snapshot().open != before {
		t.Fatal("denied content opened artifacts")
	}
	r = author.request(http.MethodGet, strings.Replace(path, template.ID, "template.missing", 1)+"?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, r, 404)
}
