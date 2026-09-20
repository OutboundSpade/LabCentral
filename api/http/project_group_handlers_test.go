package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"net/http"
	"os"
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

func TestProjectGroupDeletionAPI(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	admin := f.client(t, "admin", "a-production-length-password")
	member := f.client(t, "auditor", "an-auditor-production-password")
	root := "/api/v1/projects/project.alpha/groups"
	response := admin.request(http.MethodPost, root, map[string]any{"names": []string{"Delete me"}, "enrollmentOpen": true}, "", "group-delete-setup")
	requireGovernanceStatus(t, response, 200)
	var groups app.ProjectGroupPage
	decodeGovernanceResponse(t, response, &groups)
	path := root + "/" + string(groups.Items[0].ID)
	requireGovernanceStatus(t, member.request(http.MethodPost, path+"/join", nil, "", "group-delete-join"), 200)
	requireGovernanceStatus(t, member.request(http.MethodDelete, path, nil, `"1"`, "group-delete-denied"), 403)
	requireGovernanceStatus(t, admin.request(http.MethodDelete, path, nil, `"2"`, "group-delete-stale"), 412)
	requireGovernanceStatus(t, admin.request(http.MethodDelete, path, nil, `"1"`, "group-delete-allowed"), 204)
	requireGovernanceStatus(t, admin.request(http.MethodDelete, path, nil, `"1"`, "group-delete-allowed"), 204)
	requireGovernanceStatus(t, member.request(http.MethodPost, path+"/join", nil, "", "group-delete-rejoin"), 404)
	response = member.request(http.MethodGet, root, nil, "", "")
	requireGovernanceStatus(t, response, 200)
	decodeGovernanceResponse(t, response, &groups)
	if len(groups.Items) != 0 {
		t.Fatal("deleted group remains visible")
	}
	requireGovernanceStatus(t, admin.request(http.MethodPost, root, map[string]any{"names": []string{"Delete me"}, "enrollmentOpen": true}, "", "group-delete-reuse-name"), 200)
}

func TestFeatureAccessScopedToProject(t *testing.T) {
	f := newGovernanceHTTPFixtureOptions(t, []BrowserExtensionDTO{{ID: "teams", Slot: "page", Icon: "users", RequiredActions: []string{"template.read"}}, {ID: "library", Slot: "page", Icon: "library", RequiredActions: []string{"template.create", "instance.batchCreate"}}, {ID: "classroom", Slot: "page", Icon: "graduation-cap", RequiredActions: []string{"instance.batchCreate"}}})
	for _, test := range []struct {
		user, password, project string
		count                   int
	}{{"auditor", "an-auditor-production-password", "project.alpha", 1}, {"author", "an-author-production-password", "project.alpha", 3}, {"author", "an-author-production-password", "project.beta", 0}, {"admin", "a-production-length-password", "project.alpha", 3}, {"admin", "a-production-length-password", "", 0}} {
		client := f.client(t, test.user, test.password)
		response := client.request(http.MethodGet, "/api/v1/extensions/browser?projectId="+test.project, nil, "", "")
		requireGovernanceStatus(t, response, 200)
		var result struct {
			Items []BrowserExtensionDTO `json:"items"`
		}
		decodeGovernanceResponse(t, response, &result)
		if len(result.Items) != test.count {
			t.Fatalf("%s/%s: %v", test.user, test.project, result.Items)
		}
	}
}

func TestRecentBatchHistoryAndLabPresentation(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	viewer := f.client(t, "auditor", "an-auditor-production-password")
	response := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "Team exercise", "description": "Compare hostname with your teammate."}, "", "history-template-key")
	requireGovernanceStatus(t, response, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, response, &template)
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	response = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/revisions?projectId=project.alpha", map[string]any{"source": string(source)}, "", "history-revision-key")
	requireGovernanceStatus(t, response, 200)
	var rev TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &rev)

	digest := func(v string) domain.SHA256Digest {
		return domain.SHA256Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(v))))
	}
	// Seed durable admission through the store; provider execution is outside this read API test.
	instance := app.CreateInstanceRequest{InstanceID: "instance.history", RevisionID: domain.RevisionID(rev.ID), ProjectID: "project.alpha", OwnerKind: app.OwnerUser, OwnerID: "user.author", CreatedBy: "user.author", SubjectID: "user.author", ProviderTargetID: controlplane.LocalDockerTarget, OperationID: "operation.history", CanonicalDigest: digest(`{"resources":[]}`), CanonicalJSON: []byte(`{"resources":[]}`), SourceYAML: source, Resources: []app.ResourceSeed{{ID: "container.test", Kind: domain.ResourceContainer, CanonicalJSON: []byte(`{"id":"container.test","kind":"Container","spec":{"compute":{"cpuMillis":250,"memoryBytes":134217728}}}`), ProviderPlan: []byte(`{}`), ProviderPlanDigest: digest(`{}`), OwnershipToken: "ownership.history"}}, Acknowledged: true, IdempotencyKey: "history-instance-key", RequestDigest: digest("instance"), Payload: []byte(`{"schemaVersion":1}`), AuditID: "audit.history", EventID: "event.history", Quota: app.InstanceQuotaRequest{ReservationID: "quota.history", Amounts: []app.QuotaAmount{{Dimension: app.QuotaInstances, Value: 1}}, Lifetime: app.InstanceLifetimeRequest{TTLSeconds: 3600}}}
	record, err := f.database.CreateInstanceBatch(context.Background(), app.CreateInstanceBatchRequest{GovernanceRecordIDs: app.GovernanceRecordIDs{AuditID: "audit.batchhistory", EventID: "event.batchhistory"}, ID: "batch.history", RevisionID: domain.RevisionID(rev.ID), ProjectID: "project.alpha", CreatedBy: "user.author", TargetID: controlplane.LocalDockerTarget, IdempotencyKey: "history-batch-key", RequestDigest: digest("batch"), Instances: []app.CreateInstanceRequest{instance}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := instanceBatchDTO(context.Background(), f.service, record)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response = author.request(http.MethodGet, "/api/v1/lab-instance-batches?projectId=project.alpha", nil, "", "")
		requireGovernanceStatus(t, response, 200)
		var list struct {
			Items []InstanceBatchDTO `json:"items"`
		}
		decodeGovernanceResponse(t, response, &list)
		if len(list.Items) != 1 || list.Items[0].ID != batch.ID {
			t.Fatal("history missing", list)
		}
	}
	response = viewer.request(http.MethodGet, "/api/v1/lab-instance-batches?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, 200)
	var hidden struct {
		Items []InstanceBatchDTO `json:"items"`
	}
	decodeGovernanceResponse(t, response, &hidden)
	if len(hidden.Items) != 0 {
		t.Fatal("viewer received instructor batches")
	}
	requireGovernanceStatus(t, author.request(http.MethodGet, "/api/v1/lab-instance-batches?projectId=project.beta", nil, "", ""), 403)
	response = author.request(http.MethodGet, "/api/v1/lab-instances/"+batch.Items[0].InstanceID, nil, "", "")
	requireGovernanceStatus(t, response, 200)
	var lab InstanceDTO
	decodeGovernanceResponse(t, response, &lab)
	if lab.Name != template.Name || lab.Instructions != template.Description || lab.OwnerName != "Author" {
		t.Fatal("lab presentation", lab)
	}
}
