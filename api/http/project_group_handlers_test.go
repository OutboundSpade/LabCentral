package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"net/http"
	"testing"
	"time"
)

func TestProjectGroupAPIEnrollmentIsSelfScoped(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	admin := f.client(t, "admin", "a-production-length-password")
	member := f.client(t, "auditor", "an-auditor-production-password")
	root := "/api/v1/projects/project.alpha/groups"
	body := struct {
		Names []string `json:"names"`
		Open  bool     `json:"enrollmentOpen"`
	}{[]string{"Team one", "Team two"}, true}
	response := admin.request(http.MethodPost, root, body, "", "group-create-api")
	requireGovernanceStatus(t, response, 200)
	var page app.ProjectGroupPage
	decodeGovernanceResponse(t, response, &page)
	if len(page.Items) != 2 {
		t.Fatal("bulk result")
	}
	first := root + "/" + string(page.Items[0].ID)
	second := root + "/" + string(page.Items[1].ID)
	response = member.request(http.MethodPost, root, body, "", "group-create-denied")
	requireGovernanceStatus(t, response, 403)
	response = member.request(http.MethodGet, root, nil, "", "")
	requireGovernanceStatus(t, response, 200)
	response = member.request(http.MethodGet, "/api/v1/projects/project.beta/groups", nil, "", "")
	requireGovernanceStatus(t, response, 403)
	requireGovernanceStatus(t, member.request(http.MethodGet, first+"/members", nil, "", ""), 404)
	requireGovernanceStatus(t, admin.request(http.MethodGet, first+"/members", nil, "", ""), 200)
	response = member.request(http.MethodPost, first+"/join", nil, "", "group-self-join")
	requireGovernanceStatus(t, response, 200)
	var joined app.ProjectGroup
	decodeGovernanceResponse(t, response, &joined)
	if !joined.Joined || joined.MemberCount != 1 {
		t.Fatal("joined projection")
	}
	var roster app.ProjectGroupMemberPage
	response = member.request(http.MethodGet, first+"/members", nil, "", "")
	requireGovernanceStatus(t, response, 200)
	decodeGovernanceResponse(t, response, &roster)
	if len(roster.Items) != 1 || roster.Items[0].ID != "user.auditor" {
		t.Fatal("unexpected roster", roster)
	}
	requireGovernanceStatus(t, member.request(http.MethodGet, second+"/members", nil, "", ""), 404)
	requireGovernanceStatus(t, member.request(http.MethodGet, "/api/v1/projects/project.beta/groups/"+string(page.Items[0].ID)+"/members", nil, "", ""), 403)
	response = member.request(http.MethodPost, second+"/join", nil, "", "group-conflict-join")
	requireGovernanceStatus(t, response, 409)
	response = member.request(http.MethodPut, first+"/enrollment", map[string]bool{"open": false}, `"1"`, "group-close-denied")
	requireGovernanceStatus(t, response, 403)
	response = admin.request(http.MethodPut, first+"/enrollment", map[string]bool{"open": false}, `"1"`, "group-close-api")
	requireGovernanceStatus(t, response, 200)
	response = admin.request(http.MethodPut, first+"/enrollment", map[string]bool{"open": true}, `"1"`, "group-stale-version")
	requireGovernanceStatus(t, response, 412)
	response = member.request(http.MethodPost, first+"/leave", nil, "", "group-self-leave")
	requireGovernanceStatus(t, response, 200)
	response = member.request(http.MethodPost, first+"/join", nil, "", "group-closed-join")
	requireGovernanceStatus(t, response, 409)
	requireGovernanceStatus(t, member.request(http.MethodGet, first+"/members", nil, "", ""), 404)
	// The enrolled viewer has not gained deployment or administrator roles.
	snapshot, err := f.database.AuthorizationSnapshot(context.Background(), "user.auditor", time.Now())
	if err != nil || len(snapshot.Assignments) != 1 || snapshot.Assignments[0].Role != app.RoleViewer {
		t.Fatal("enrollment changed roles", err)
	}
}
