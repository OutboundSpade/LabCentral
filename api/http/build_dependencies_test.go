package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
)

type dependencyBuilder struct {
	app.Builder
	mu    sync.Mutex
	calls map[string]int
	data  map[domain.SHA256Digest][]byte
}

func (b *dependencyBuilder) WriteArtifact(_ context.Context, _ app.ProviderCall, a app.ExtensionArtifact, r io.Reader) (app.ExtensionArtifact, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return a, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.Digest.Hex() {
		return a, fmt.Errorf("transfer changed")
	}
	b.mu.Lock()
	b.data[a.Digest] = data
	b.mu.Unlock()
	return a, nil
}
func (b *dependencyBuilder) ReadArtifact(_ context.Context, _ app.ProviderCall, a app.ExtensionArtifact) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return io.NopCloser(bytes.NewReader(b.data[a.Digest])), nil
}
func (b *dependencyBuilder) ValidateBuild(context.Context, app.BuildRequest) error { return nil }
func (b *dependencyBuilder) Build(_ context.Context, r app.BuildRequest) (string, app.BuildResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	expected := map[string][]string{"base": {}, "left": {"base"}, "right": {"base"}, "final": {"left", "right"}}
	if len(r.Dependencies) != len(expected[r.Recipe.ID]) {
		return "", app.BuildResult{}, fmt.Errorf("dependency closure missing")
	}
	for i, name := range expected[r.Recipe.ID] {
		if r.Dependencies[i].RecipeID != name || string(b.data[r.Dependencies[i].Artifact.Digest]) != "image-"+name {
			return "", app.BuildResult{}, fmt.Errorf("wrong dependency output")
		}
	}
	b.calls[r.Recipe.ID]++
	data := []byte("image-" + r.Recipe.ID)
	sum := sha256.Sum256(data)
	digest := domain.SHA256Digest(hex.EncodeToString(sum[:]))
	b.data[digest] = data
	return "succeeded", app.BuildResult{Image: app.ExtensionArtifact{Digest: digest, Size: uint64(len(data)), MediaType: "application/vnd.oci.image.layout.v1.tar"}, ProvenanceDigest: digest}, nil
}
func TestBuildDependencyDiamondSharesBaseAndRetainsOutputs(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	author := f.client(t, "author", "an-author-production-password")
	builder := &dependencyBuilder{calls: map[string]int{}, data: map[domain.SHA256Digest][]byte{}}
	if err := f.service.SetBuilders([]controlplane.BuilderBinding{{InstanceID: "test", ID: "linux", Requirement: "linux", Kind: "dockerfile", Dependencies: true, Projects: []domain.ProjectID{"project.alpha"}, PluginDigest: "fixture", Builder: builder, Limits: app.BuildLimits{TimeoutSeconds: 60, CPUCount: 1, MemoryBytes: 1 << 30, InputBytes: 1 << 20, OutputBytes: 1 << 20, Egress: []string{}}}}); err != nil {
		t.Fatal(err)
	}
	recipes := []labbundle.BuildRecipe{}
	for _, item := range []struct {
		id           string
		dependencies []string
	}{{"base", nil}, {"left", []string{"base"}}, {"right", []string{"base"}}, {"final", []string{"left", "right"}}} {
		recipes = append(recipes, labbundle.BuildRecipe{ID: item.id, DependsOn: item.dependencies, Builder: "linux", Schema: "fixture-v1", Architecture: "amd64", ConfigurationJSON: fmt.Sprintf(`{"label":%q}`, item.id), Context: []string{"Dockerfile"}})
	}
	alias := recipes[0]
	alias.ID = "base-alias"
	alias.ConfigurationJSON = "{ \"label\": \"base\" }"
	recipes = append(recipes, alias)
	archive, err := labbundle.Pack(labbundle.Manifest{APIVersion: labbundle.PortableVersion, Kind: "LabBundle", Entrypoint: "lab.yaml", Package: &labbundle.SourcePackage{APIVersion: labbundle.SourcePackageVersion, Builders: []labbundle.BuilderRequirement{{ID: "linux", Kind: "dockerfile", Schema: "fixture-v1"}}}, Builds: &labbundle.BuildMetadata{APIVersion: "labcentral.build/v2", Recipes: recipes}}, map[string][]byte{"lab.yaml": []byte("apiVersion: labs.labcentral.io/v1alpha1\nkind: Lab\nmetadata: {name: diamond}\nspec: {resources: {}}\n"), "Dockerfile": []byte("FROM scratch\n")})
	if err != nil {
		t.Fatal(err)
	}
	response := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "diamond"}, "", "diamond-template")
	requireGovernanceStatus(t, response, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, response, &template)
	response = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/bundle-revisions?projectId=project.alpha", map[string]any{"archive": archive}, "", "diamond-revision")
	requireGovernanceStatus(t, response, 200)
	var revision struct {
		ID string `json:"id"`
	}
	decodeGovernanceResponse(t, response, &revision)
	request := func(key string) app.ExtensionWork {
		response := author.request(http.MethodPost, "/api/v1/builds", map[string]any{"projectId": "project.alpha", "revisionId": revision.ID, "recipeId": "final"}, "", key)
		requireGovernanceStatus(t, response, 200)
		var work app.ExtensionWork
		decodeGovernanceResponse(t, response, &work)
		return work
	}
	first := request("diamond-first")
	second := request("diamond-second")
	if first.ID != second.ID {
		t.Fatal("effective inputs did not deduplicate")
	}
	base, err := f.service.RequestBuild(context.Background(), "user.author", "project.alpha", domain.RevisionID(revision.ID), "base", "base-original")
	if err != nil {
		t.Fatal(err)
	}
	aliasWork, err := f.service.RequestBuild(context.Background(), "user.author", "project.alpha", domain.RevisionID(revision.ID), "base-alias", "base-alias")
	if err != nil || aliasWork.ID != base.ID {
		t.Fatal("portable recipe alias duplicated effective build", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.service.RunBuildWork(ctx, "diamond-worker", func(err error) { t.Errorf("dependency worker: %v", err) })
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		first, err = f.database.GetExtensionWork(context.Background(), first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if first.State == domain.OperationSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.State != domain.OperationSucceeded {
		t.Fatalf("diamond did not complete: %+v", first)
	}
	builder.mu.Lock()
	defer builder.mu.Unlock()
	if len(builder.calls) != 4 {
		t.Fatalf("unexpected executions: %+v", builder.calls)
	}
	for name, count := range builder.calls {
		if count != 1 {
			t.Fatalf("shared %s executed %d times", name, count)
		}
	}
	var result app.BuildResult
	if err = json.Unmarshal(first.Result, &result); err != nil || result.ArtifactRef == "" {
		t.Fatal("dependency output was not durably retained", err)
	}
}
