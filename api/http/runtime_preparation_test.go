package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"github.com/OutboundSpade/LabCentral/pkg/ocilayout"
)

type runtimePipelineBuilder struct {
	dependencyBuilder
	output  []byte
	media   string
	runtime bool
}

func (b *runtimePipelineBuilder) Build(_ context.Context, r app.BuildRequest) (string, app.BuildResult, error) {
	if b.runtime {
		var settings containervm.RuntimeBuildSettings
		if r.Recipe.Schema != containervm.RuntimeBuildSchema || json.Unmarshal([]byte(r.Recipe.ConfigurationJSON), &settings) != nil || settings.Validate() != nil || len(r.Dependencies) != 1 || r.Dependencies[0].RecipeID != "oci" {
			return "", app.BuildResult{}, fmt.Errorf("unbound runtime packaging")
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[r.Recipe.ID]++
	sum := sha256.Sum256(b.output)
	digest := domain.SHA256Digest(hex.EncodeToString(sum[:]))
	b.data[digest] = b.output
	return "succeeded", app.BuildResult{Image: app.ExtensionArtifact{Digest: digest, Size: uint64(len(b.output)), MediaType: b.media}, ProvenanceDigest: digest}, nil
}

type runtimePipelineProvider struct {
	app.Provider
	mu      sync.Mutex
	imports int
}

func (*runtimePipelineProvider) ExtensionIdentity() string { return "runtime-fixture" }
func (*runtimePipelineProvider) ReadArtifact(context.Context, app.ProviderCall, app.ExtensionArtifact) (io.ReadCloser, error) {
	return nil, fmt.Errorf("unexpected read")
}
func (*runtimePipelineProvider) WriteArtifact(_ context.Context, _ app.ProviderCall, a app.ExtensionArtifact, r io.Reader) (app.ExtensionArtifact, error) {
	data, err := io.ReadAll(r)
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != a.Digest.Hex() {
		return a, fmt.Errorf("invalid transfer")
	}
	return a, nil
}
func (p *runtimePipelineProvider) ImportImage(_ context.Context, r app.ImportRequest) (string, app.ImageBinding, error) {
	if r.ContainerRuntime == nil || r.ContainerRuntime.Validate() != nil || r.Artifact.MediaType != "application/x-qemu-disk" {
		return "", app.ImageBinding{}, fmt.Errorf("raw OCI import forbidden")
	}
	p.mu.Lock()
	p.imports++
	p.mu.Unlock()
	return "succeeded", app.ImageBinding{ImageRef: "labcentral.local/images/img-fixture@" + r.ContainerRuntime.ManifestDigest, ProviderImageID: "201", OpaqueBinding: []byte("runtime-fixture"), ArtifactDigest: r.Artifact.Digest, TargetConfigurationDigest: r.Target.ConfigFingerprint, Architecture: "amd64"}, nil
}
func (*runtimePipelineProvider) RecoverImport(context.Context, app.ImportRequest) (string, app.ImageBinding, error) {
	return "unknown", app.ImageBinding{}, nil
}
func (*runtimePipelineProvider) ValidateImageBinding(context.Context, app.ImportRequest, app.ImageBinding) error {
	return nil
}
func pipelineOCI(t *testing.T) []byte {
	t.Helper()
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)}
	add := func(data []byte) string {
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		files["blobs/sha256/"+digest] = data
		return "sha256:" + digest
	}
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	configDigest := add(config)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, configDigest, len(config)))
	digest := add(manifest)
	files["index.json"] = []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d}]}`, digest, len(manifest)))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for name, data := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		writer.Write(data)
	}
	writer.Close()
	return output.Bytes()
}
func TestParticipantRuntimePreparationWaitsForApprovalAndRechecksAuthority(t *testing.T) {
	provider := &runtimePipelineProvider{}
	f := newGovernanceHTTPFixture(t, controlplane.TargetBinding{ID: controlplane.LocalDockerTarget, ProviderKind: "proxmox", Provider: provider, ProtocolMajor: 1, Features: []string{"image-import-v1", "artifact-transfer-v1", "container-image-import-v1"}})
	ctx := context.Background()
	author := f.client(t, "author", "an-author-production-password")
	if err := f.database.PutRoleAssignment(ctx, app.RoleAssignmentRecord{ID: "role.runtime-participant", SubjectKind: app.SubjectUser, SubjectID: "user.auditor", ScopeKind: app.ScopeProject, ScopeID: "project.alpha", Role: app.RoleParticipant, ValidFrom: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	participant := f.client(t, "auditor", "an-auditor-production-password")
	docker := &runtimePipelineBuilder{dependencyBuilder: dependencyBuilder{calls: map[string]int{}, data: map[domain.SHA256Digest][]byte{}}, output: pipelineOCI(t), media: ocilayout.MediaType}
	runtime := &runtimePipelineBuilder{dependencyBuilder: dependencyBuilder{calls: map[string]int{}, data: map[domain.SHA256Digest][]byte{}}, output: []byte("fixed qcow2 fixture"), media: "application/x-qemu-disk", runtime: true}
	limits := app.BuildLimits{TimeoutSeconds: 60, CPUCount: 1, MemoryBytes: 4 << 30, InputBytes: 1 << 20, OutputBytes: 1 << 20, Egress: []string{}}
	if err := f.service.SetBuilders([]controlplane.BuilderBinding{
		{OCIIdentity: true, Dependencies: true, InstanceID: "docker", ID: "linux", Kind: "dockerfile", Requirement: "linux", Projects: []domain.ProjectID{"project.alpha"}, PluginDigest: "docker-fixture", Builder: docker, Limits: limits},
		{Dependencies: true, InstanceID: "runtime", ID: "linux", Kind: "container-runtime", Projects: []domain.ProjectID{"project.alpha"}, PluginDigest: "runtime-fixture", Builder: runtime, Limits: limits, ContainerRuntime: &containervm.RuntimeBuildPolicy{BaseSHA256: strings.Repeat("a", 64), AgentSHA256: strings.Repeat("b", 64), DiskGiB: 12}},
	}); err != nil {
		t.Fatal(err)
	}
	lab, err := os.ReadFile("../../examples/template-library/dockerfile-web/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := labbundle.Pack(labbundle.Manifest{APIVersion: labbundle.PortableVersion, Kind: "LabBundle", Entrypoint: "lab.yaml", Package: &labbundle.SourcePackage{APIVersion: labbundle.SourcePackageVersion, Builders: []labbundle.BuilderRequirement{{ID: "linux", Kind: "dockerfile", Schema: "labcentral.dockerfile.v1"}}}, Builds: &labbundle.BuildMetadata{APIVersion: "labcentral.build/v2", Recipes: []labbundle.BuildRecipe{{ID: "web", Builder: "linux", Schema: "labcentral.dockerfile.v1", Architecture: "amd64", ConfigurationJSON: `{"dockerfile":"Dockerfile"}`, Context: []string{"Dockerfile"}}}}}, map[string][]byte{"lab.yaml": lab, "Dockerfile": []byte("FROM scratch\n")})
	if err != nil {
		t.Fatal(err)
	}
	response := author.request(http.MethodPost, "/api/v1/lab-templates", map[string]any{"projectId": "project.alpha", "name": "runtime-pipeline"}, "", "runtime-template")
	requireGovernanceStatus(t, response, 200)
	var template LabTemplateDTO
	decodeGovernanceResponse(t, response, &template)
	response = author.request(http.MethodPost, "/api/v1/lab-templates/"+template.ID+"/bundle-revisions?projectId=project.alpha", map[string]any{"archive": archive}, "", "runtime-revision")
	requireGovernanceStatus(t, response, 200)
	var revision TemplateRevisionDTO
	decodeGovernanceResponse(t, response, &revision)
	response = participant.request(http.MethodPost, "/api/v1/deployment-preparations", map[string]any{"projectId": "project.alpha", "preparation": app.PreparationWork{Version: 2, RevisionID: domain.RevisionID(revision.ID), TargetID: controlplane.LocalDockerTarget, Inputs: domain.TypedInputs{}, Acknowledged: true, Lifetime: app.InstanceLifetimeRequest{TTLSeconds: 3600}}}, "", "runtime-preparation")
	requireGovernanceStatus(t, response, 200)
	var preparation app.ExtensionWork
	decodeGovernanceResponse(t, response, &preparation)
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.service.RunBuildWork(workerCtx, "runtime-worker", func(err error) { t.Errorf("runtime pipeline: %v", err) })
	}()
	defer func() { cancel(); <-done }()
	var progress app.PreparationResult
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		preparation, err = f.database.GetExtensionWork(ctx, preparation.ID)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(preparation.Result, &progress)
		if progress.Stage == "awaiting-approval" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if progress.Stage != "awaiting-approval" || len(progress.BuildIDs) != 2 || len(progress.ImportIDs) != 1 {
		t.Fatalf("missing approval wait: %+v %+v", preparation, progress)
	}
	binding, err := f.database.GetImageBinding(ctx, string(progress.ImportIDs[0]))
	if err != nil || binding.Approved {
		t.Fatal("participant import implicitly approved", err)
	}
	response = participant.request(http.MethodPost, "/api/v1/image-bindings/"+binding.ID+"/policy", map[string]any{"projectId": "project.alpha", "approved": true, "retired": false}, "", "runtime-self-approval")
	requireGovernanceStatus(t, response, 403)
	// Revocation while waiting must stop admission even if the image later gains approval.
	if err = f.database.PutUser(ctx, app.UserRecord{ID: "user.auditor", Username: "auditor", DisplayName: "Auditor", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		preparation, err = f.database.GetExtensionWork(ctx, preparation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if preparation.State == domain.OperationCanceled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if preparation.State != domain.OperationCanceled {
		t.Fatal("revoked preparation remained active", preparation)
	}
	docker.mu.Lock()
	defer docker.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if docker.calls["web"] != 1 || runtime.calls["runtime"] != 1 || provider.imports != 1 {
		t.Fatal("duplicate or skipped pipeline stage", docker.calls, runtime.calls, provider.imports)
	}
}
