package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type libraryHTTPSource struct {
	app.ExtensionSource
	archive []byte
}

func (f libraryHTTPSource) ResolveCollection(context.Context, app.ExtensionSourceRequest) (app.ExtensionSourceResult, error) {
	digest := sha256.Sum256(f.archive)
	return app.ExtensionSourceResult{ResolvedIdentity: strings.Repeat("a", 40), ProvenanceDigest: domain.SHA256Digest(fmt.Sprintf("%x", digest)), Collection: &app.SourceCollection{Entries: []app.SourceCollectionEntry{{Key: "shared", Name: "shared", Bundle: app.ExtensionArtifact{Digest: domain.SHA256Digest(fmt.Sprintf("%x", digest)), Size: uint64(len(f.archive))}}}}}, nil
}
func (f libraryHTTPSource) ReadArtifact(context.Context, app.ProviderCall, app.ExtensionArtifact) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.archive)), nil
}
func (f libraryHTTPSource) Cancel(context.Context, app.ProviderCall) error { return nil }
func TestLibraryAPIAtomicPublicationAndRevokedScheduleAuthority(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	admin := f.client(t, "admin", "a-production-length-password")
	archive, err := labbundle.ExportSource([]byte("apiVersion: labs.labcentral.io/v1alpha1\nkind: Lab\nmetadata: {name: shared}\nspec: {resources: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.SetSources([]controlplane.SourceBinding{{Collections: true, InstanceID: "github", ID: "library", Projects: []domain.ProjectID{"project.alpha"}, PluginDigest: "fixture", Source: libraryHTTPSource{archive: archive}}}); err != nil {
		t.Fatal(err)
	}
	r := author.request(http.MethodPost, "/api/v1/template-libraries", map[string]any{"projectId": "project.alpha", "binding": "github/library", "reference": "main"}, "", "library-create-test")
	requireGovernanceStatus(t, r, 200)
	var library app.TemplateLibrary
	decodeGovernanceResponse(t, r, &library)
	if library.IntervalSeconds != 900 {
		t.Fatal("incorrect default interval")
	}
	r = author.request(http.MethodGet, "/api/v1/template-libraries?projectId=project.beta", nil, "", "")
	requireGovernanceStatus(t, r, 403)
	r = author.request(http.MethodPost, "/api/v1/template-libraries/"+library.ID+"/sync?projectId=project.alpha", nil, "", "library-sync-test")
	requireGovernanceStatus(t, r, 200)
	var work app.ExtensionWork
	decodeGovernanceResponse(t, r, &work)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.service.RunSourceWork(ctx, "test-library", func(err error) { t.Errorf("source execution: %v", err) })
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		work, err = f.database.GetExtensionWork(context.Background(), work.ID)
		if err != nil {
			t.Fatal(err)
		}
		if work.State == domain.OperationSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if work.State != domain.OperationSucceeded {
		t.Fatalf("sync did not complete: %+v", work)
	}
	entries, err := f.database.LibraryEntries(context.Background(), library.ID)
	if err != nil || len(entries) != 1 {
		t.Fatal("missing collection publication")
	}
	r = author.request(http.MethodGet, "/api/v1/template-libraries/"+library.ID+"/entries?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, r, 200)
	r = author.request(http.MethodGet, "/api/v1/template-libraries/"+library.ID+"/history?projectId=project.alpha", nil, "", "")
	requireGovernanceStatus(t, r, 200)
	if _, err = f.database.RevokeMembership(context.Background(), "membership.author", "org.alpha", 1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// An administrator cannot use their authority to publish as the revoked author.
	r = admin.request(http.MethodPost, "/api/v1/template-libraries/"+library.ID+"/sync?projectId=project.alpha", nil, "", "library-revoked-sync")
	requireGovernanceStatus(t, r, 403)
	library, err = f.database.GetLibrary(context.Background(), library.ID)
	if err != nil || !library.Paused || library.PauseReason != "authority_revoked" {
		t.Fatal("revoked author did not pause schedule")
	}
}
