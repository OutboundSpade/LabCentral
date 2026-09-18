package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestParticipantPreparationDoesNotGrantAuthorOrImagePrivileges(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	ctx := context.Background()
	if err := f.database.PutRoleAssignment(ctx, app.RoleAssignmentRecord{ID: "role.participant", SubjectKind: app.SubjectUser, SubjectID: "user.auditor", ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Role: app.RoleParticipant, ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	participant := f.client(t, "auditor", "an-auditor-production-password")
	response := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "participant-preparation"}, "", "participant-template")
	requireGovernanceStatus(t, response, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, response, &template)
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	response = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/revisions?projectId=project.alpha", map[string]any{"source": string(source)}, "", "participant-revision")
	requireGovernanceStatus(t, response, 200)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &revision)
	preparation := app.PreparationWork{Version: 2, RevisionID: domain.RevisionID(revision.ID), TargetID: controlplane.LocalDockerTarget, Inputs: domain.TypedInputs{}, Acknowledged: true, Lifetime: app.InstanceLifetimeRequest{TTLSeconds: 3600}}
	body := map[string]any{"projectId": "project.alpha", "preparation": preparation}
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", body, "", "participant-prepare")
	requireGovernanceStatus(t, response, 200)
	var work app.ExtensionWork
	decodeGovernanceResponse(t, response, &work)
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", body, "", "participant-prepare")
	requireGovernanceStatus(t, response, 200)
	var replay app.ExtensionWork
	decodeGovernanceResponse(t, response, &replay)
	if replay.ID != work.ID {
		t.Fatal("duplicate preparation")
	}
	response = participant.request(http.MethodGet, "/api/v1/extension-operations/"+string(work.ID)+"?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, 200)
	preparation.Owner = &app.PreparationOwner{Kind: app.OwnerUser, ID: "user.author"}
	body["preparation"] = preparation
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", body, "", "participant-foreign-owner")
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign owner: %d %s", response.Code, response.Body.String())
	}
	response = participant.request(http.MethodPost, "/api/v1/builds", map[string]any{"projectId": "project.alpha", "revisionId": revision.ID, "recipeId": "base"}, "", "participant-build")
	requireGovernanceStatus(t, response, http.StatusForbidden)
	response = participant.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "forbidden"}, "", "participant-author")
	requireGovernanceStatus(t, response, http.StatusForbidden)
}
