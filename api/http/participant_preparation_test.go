package httpapi

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"net/http"
	"os"
	"strings"
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
	response = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/revisions?projectId=project.alpha", map[string]any{"source": strings.Replace(string(source), "spec:\n", "spec:\n  parameters:\n    - {name: count, type: integer}\n    - {name: choice, type: enum, enum: [0, 1]}\n", 1)}, "", "participant-revision")
	requireGovernanceStatus(t, response, 200)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &revision)
	zero := int64(0)
	preparation := app.PreparationWork{Version: 2, RevisionID: domain.RevisionID(revision.ID), TargetID: controlplane.LocalDockerTarget, Inputs: domain.TypedInputs{{Name: "count", Kind: domain.InputInteger, Integer: 2}, {Name: "choice", Kind: domain.InputEnum, Enum: &domain.EnumValue{Integer: &zero}}}, Acknowledged: true, Lifetime: app.InstanceLifetimeRequest{TTLSeconds: 3600}}
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
	response = participant.request(http.MethodGet, "/api/v1/deployment-preparations?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, 200)
	var own struct {
		Items []app.ExtensionWork `json:"items"`
	}
	decodeGovernanceResponse(t, response, &own)
	if len(own.Items) != 1 || own.Items[0].ID != work.ID {
		t.Fatal("own preparation missing")
	}
	response = author.request(http.MethodGet, "/api/v1/deployment-preparations?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, 200)
	decodeGovernanceResponse(t, response, &own)
	if len(own.Items) != 0 {
		t.Fatal("another user's preparations disclosed")
	}
	response = participant.request(http.MethodGet, "/api/v1/deployment-preparations?projectId=project.foreign", nil, "", "")
	if response.Code == http.StatusOK {
		t.Fatal("foreign project accepted")
	}
	response = participant.request(http.MethodGet, "/api/v1/extension-operations?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, response, http.StatusForbidden)
	preparation.Owner = &app.PreparationOwner{Kind: app.OwnerUser, ID: "user.author"}
	body["preparation"] = preparation
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", body, "", "participant-foreign-owner")
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign owner: %d %s", response.Code, response.Body.String())
	}
	// Classroom's typed bridge uses the existing author-only batch preparation.
	batch := map[string]any{"projectId": "project.alpha", "preparation": map[string]any{
		"version": 1, "revisionId": revision.ID, "targetId": string(controlplane.LocalDockerTarget),
		"inputs": []any{}, "acknowledged": true, "lifetime": map[string]any{"ttlSeconds": 3600},
		"owners": []any{map[string]any{"Kind": "user", "ID": "user.author"}, map[string]any{"Kind": "user", "ID": "user.auditor"}},
	}}
	response = author.request(http.MethodPost, "/api/v1/deployment-preparations", batch, "", "classroom-batch-prepare")
	requireGovernanceStatus(t, response, 200)
	var classroom app.ExtensionWork
	decodeGovernanceResponse(t, response, &classroom)
	if classroom.Request.Preparation.Version != 1 || len(classroom.Request.Preparation.Owners) != 2 || classroom.Request.Preparation.Owners[1].ID != "user.auditor" {
		t.Fatal("batch owner contract was not preserved")
	}
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", batch, "", "participant-batch-prepare")
	requireGovernanceStatus(t, response, http.StatusForbidden)
	response = participant.request(http.MethodPost, "/api/v1/builds", map[string]any{"projectId": "project.alpha", "revisionId": revision.ID, "recipeId": "base"}, "", "participant-build")
	requireGovernanceStatus(t, response, http.StatusForbidden)
	response = participant.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "forbidden"}, "", "participant-author")
	requireGovernanceStatus(t, response, http.StatusForbidden)
}
