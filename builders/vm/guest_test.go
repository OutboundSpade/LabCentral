package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func TestGuestStartIsIdempotentAndDoesNotAcceptPostStartUploads(t *testing.T) {
	agent, err := NewGuestAgent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"files":[{"path":"Dockerfile","data":"RlJPTSBzY3JhdGNoCg=="}]}`)
	sum := sha256.Sum256(data)
	artifact := &wire.ArtifactHandle{Digest: hex.EncodeToString(sum[:]), Size: uint64(len(data)), MediaType: "application/vnd.labcentral.build-context+json"}
	recipe := &wire.BuildRecipe{Digest: strings.Repeat("1", 64), Architecture: "amd64", Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: DockerfileSchema, Major: 1}, CanonicalJson: []byte(`{"dockerfile":"Dockerfile"}`)}, Inputs: []*wire.ArtifactHandle{artifact}}
	limits := &wire.BuildLimits{TimeoutSeconds: 60, CpuMillis: 1000, MemoryBytes: 512 << 20, MaximumInputBytes: 1 << 20, MaximumOutputBytes: 1 << 20}
	var executions atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{})
	agent.execute = func(context.Context, string, *wire.BuildRecipe, *wire.BuildLimits) (string, string, error) {
		defer close(done)
		executions.Add(1)
		<-release
		target := filepath.Join(agent.root, "result")
		return target, OCIMedia, os.WriteFile(target, []byte("image"), 0600)
	}
	call := func(request GuestRequest) GuestResponse {
		request.Version = 1
		request.ID = "fixture"
		return agent.handle(request)
	}
	if result := call(GuestRequest{Method: "configure", Recipe: recipe, Limits: limits}); result.Error != "" {
		t.Fatal(result)
	}
	if result := call(GuestRequest{Method: "start"}); result.Error == "" {
		t.Fatal("missing source accepted")
	}
	if result := call(GuestRequest{Method: "upload", Digest: artifact.Digest, Chunk: data}); result.Error != "" {
		t.Fatal(result)
	}
	for i := 0; i < 2; i++ {
		if result := call(GuestRequest{Method: "start"}); result.Error != "" {
			t.Fatal(result)
		}
	}
	if result := call(GuestRequest{Method: "upload", Digest: artifact.Digest, Chunk: data}); result.Error == "" {
		t.Fatal("running source changed")
	}
	close(release)
	<-done
	for i := 0; i < 100; i++ {
		if call(GuestRequest{Method: "status"}).State == "succeeded" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if result := call(GuestRequest{Method: "start"}); result.State != "succeeded" || result.Size != 5 {
		t.Fatal("completed invocation was not retained", result)
	}
	if executions.Load() != 1 {
		t.Fatal("guest repeated original build")
	}
}
