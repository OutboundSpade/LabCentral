package httpapi

import (
	"bytes"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"net/http"
	"testing"
)

func TestPortablePreviewExportAndFreshImportRespectScope(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	source := []byte("apiVersion: labs.labcentral.io/v1alpha1\nkind: Lab\nmetadata: {name: portable}\nspec: {resources: {}}\n")
	archive, err := labbundle.ExportSource(source)
	if err != nil {
		t.Fatal(err)
	}
	r := author.request(http.MethodPost, "/api/v1/template-import-previews", map[string]any{"projectId": "project.alpha", "archive": archive}, "", "")
	requireGovernanceStatus(t, r, 200)
	var preview controlplane.PortablePreview
	decodeGovernanceResponse(t, r, &preview)
	if preview.APIVersion != labbundle.PortableVersion || preview.FileCount != 2 {
		t.Fatalf("preview: %+v", preview)
	}
	before := f.artifacts.snapshot()
	r = author.request(http.MethodPost, "/api/v1/template-import-previews", map[string]any{"projectId": "project.beta", "archive": archive}, "", "")
	requireGovernanceStatus(t, r, 403)
	if f.artifacts.snapshot() != before {
		t.Fatal("denied preview touched artifacts")
	}
	r = author.request(http.MethodPost, "/api/v1/lab-templates", map[string]string{"projectId": "project.alpha", "name": "portable"}, "", "portable-create")
	requireGovernanceStatus(t, r, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, r, &template)
	r = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/bundle-revisions?projectId=project.alpha", map[string]any{"archive": archive}, "", "portable-publish")
	requireGovernanceStatus(t, r, 200)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, r, &revision)
	path := "/api/v1/lab-templates/" + template.ID + "/revisions/" + revision.ID + "/export"
	r = author.request(http.MethodGet, path+"?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, r, 200)
	var exported struct {
		Archive []byte `json:"archive"`
	}
	decodeGovernanceResponse(t, r, &exported)
	if !bytes.Equal(archive, exported.Archive) {
		t.Fatal("export changed portable source")
	}
	before = f.artifacts.snapshot()
	r = author.request(http.MethodGet, path+"?projectId=project.beta", nil, "", "")
	requireGovernanceStatus(t, r, 403)
	if f.artifacts.snapshot() != before {
		t.Fatal("denied export opened artifacts")
	}
	fresh := newGovernanceHTTPFixture(t)
	other := fresh.client(t, "author", "an-author-production-password")
	r = other.request(http.MethodPost, "/api/v1/template-import-previews", map[string]any{"projectId": "project.alpha", "archive": exported.Archive}, "", "")
	requireGovernanceStatus(t, r, 200)
	r = other.request(http.MethodPost, "/api/v1/lab-templates", map[string]string{"projectId": "project.alpha", "name": "imported"}, "", "portable-create-fresh")
	requireGovernanceStatus(t, r, 200)
	decodeGovernanceResponse(t, r, &template)
	r = other.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/bundle-revisions?projectId=project.alpha", map[string]any{"archive": exported.Archive}, "", "portable-import-fresh")
	requireGovernanceStatus(t, r, 200)
}
