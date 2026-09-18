package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func workerFixture(t *testing.T) (*Service, wire.BuilderServiceClient, wire.ArtifactTransferServiceClient) {
	t.Helper()
	s, err := NewService(filepath.Join(t.TempDir(), "worker"), Runner{Profile: Profile{MaximumParallel: 2, MaximumStorageBytes: 128 << 30, MaximumMemoryBytes: 12 << 30, MemoryOverheadBytes: 2 << 30}}, strings.Repeat("1", 64), strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	s.stop = func(context.Context, RunSpec) error { return nil }
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	wire.RegisterBuilderServiceServer(server, s)
	wire.RegisterArtifactTransferServiceServer(server, s)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); s.Close(); listener.Close() })
	return s, wire.NewBuilderServiceClient(conn), wire.NewArtifactTransferServiceClient(conn)
}
func workerRequest(t *testing.T, artifacts wire.ArtifactTransferServiceClient, id string) *wire.BuilderServiceBuildRequest {
	t.Helper()
	data, _ := json.Marshal(SourceContext{Version: 1, Files: []SourceFile{{Path: "Dockerfile", Data: []byte("FROM scratch\nCOPY message /message\n")}, {Path: "message", Data: []byte("worker-recovery")}}})
	digest := sha256.Sum256(data)
	call := &wire.CallContext{InvocationId: id, FencingToken: 1}
	artifact := &wire.ArtifactHandle{Digest: hex.EncodeToString(digest[:]), Size: uint64(len(data)), MediaType: "application/vnd.labcentral.build-context+json"}
	stream, err := artifacts.Write(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&wire.ArtifactTransferServiceWriteRequest{Context: call, Artifact: artifact, Chunk: data}); err != nil {
		t.Fatal(err)
	}
	receipt, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := json.Marshal(WorkerBinding{ProfileSHA256: strings.Repeat("1", 64), WorkerSHA256: strings.Repeat("2", 64)})
	return &wire.BuilderServiceBuildRequest{Context: call, BindingId: "dockerfile", BindingConfiguration: &wire.ExtensionConfiguration{CanonicalJson: binding}, Recipe: &wire.BuildRecipe{Digest: strings.Repeat("3", 64), Architecture: "amd64", Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: DockerfileSchema, Major: 1}, CanonicalJson: []byte(`{"dockerfile":"Dockerfile"}`)}, Inputs: []*wire.ArtifactHandle{receipt.Artifact}}, Limits: &wire.BuildLimits{TimeoutSeconds: 60, CpuMillis: 1000, MemoryBytes: 512 << 20, MaximumInputBytes: 1 << 20, MaximumOutputBytes: 1 << 20}}
}
func waitWorkerState(t *testing.T, client wire.BuilderServiceClient, call *wire.CallContext, state string) *wire.BuilderServiceBuildResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: call})
		if err != nil {
			t.Fatal(err)
		}
		if out.Result.State == state {
			return out.Result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("worker did not reach " + state)
	return nil
}
func TestWorkerResponseLossKeepsInvocationAndOutputCapabilitiesFenced(t *testing.T) {
	s, builder, artifacts := workerFixture(t)
	release := make(chan struct{})
	var calls atomic.Int32
	s.run = func(ctx context.Context, spec RunSpec, first bool) (RunOutput, error) {
		calls.Add(1)
		select {
		case <-ctx.Done():
			return RunOutput{}, ctx.Err()
		case <-release:
		}
		data := []byte("untrusted OCI output")
		sum := sha256.Sum256(data)
		if err := os.WriteFile(filepath.Join(spec.Directory, "output"), data, 0600); err != nil {
			return RunOutput{}, err
		}
		return RunOutput{Digest: hex.EncodeToString(sum[:]), Size: uint64(len(data)), MediaType: OCIMedia}, nil
	}
	request := workerRequest(t, artifacts, "inv.response-loss")
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := builder.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	// Losing the caller's response does not cancel the independently journaled VM.
	recovered := proto.Clone(request.Context).(*wire.CallContext)
	recovered.FencingToken = 2
	out, err := builder.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: recovered})
	if err != nil || out.Result.State != "running" {
		t.Fatal("lost invocation", out, err)
	}
	if _, err = builder.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: request.Context}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale fence accepted", err)
	}
	close(release)
	result := waitWorkerState(t, builder, recovered, "succeeded")
	if calls.Load() != 1 {
		t.Fatal("build executed more than once")
	}
	if len(result.ProvenanceDigest) != 64 {
		t.Fatal("missing effective-input provenance")
	}
	read, err := artifacts.Read(context.Background(), &wire.ArtifactTransferServiceReadRequest{Context: recovered, Artifact: result.Image})
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for {
		chunk, err := read.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, chunk.Chunk...)
	}
	if string(data) != "untrusted OCI output" {
		t.Fatal("output changed")
	}
	replay, err := artifacts.Read(context.Background(), &wire.ArtifactTransferServiceReadRequest{Context: recovered, Artifact: result.Image})
	if err == nil {
		_, err = replay.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal("output ticket replay accepted", err)
	}
	// The durable successful record remains available after a worker restart,
	// while old in-memory tickets do not survive.
	s.Close()
	restarted, err := NewService(s.root, s.runner, s.profileDigest, s.workerDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	saved, err := restarted.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: recovered})
	if err != nil || saved.Result.Image.Digest != result.Image.Digest {
		t.Fatal("output recovery failed", err)
	}
}
func TestCancellationWaitsForExternalGuestClosure(t *testing.T) {
	s, builder, artifacts := workerFixture(t)
	closure := make(chan struct{})
	defer close(closure)
	s.run = func(ctx context.Context, _ RunSpec, _ bool) (RunOutput, error) {
		<-ctx.Done()
		return RunOutput{}, ctx.Err()
	}
	s.stop = func(ctx context.Context, _ RunSpec) error {
		select {
		case <-closure:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	request := workerRequest(t, artifacts, "inv.cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := builder.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	canceled, err := builder.Cancel(context.Background(), &wire.BuilderServiceCancelRequest{Context: request.Context})
	if err != nil || canceled.Outcome != "pending" {
		t.Fatal("premature cancellation", canceled, err)
	}
	current, err := builder.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: request.Context})
	if err != nil || current.Result.State != "running" {
		t.Fatal("cancellation reported before closure", err)
	}
	closure <- struct{}{}
	result := waitWorkerState(t, builder, request.Context, "cancelled")
	if result.Image != nil {
		t.Fatal("canceled build published output")
	}
}
func TestWorkerRejectsWrongProfileAndCrossInvocationArtifacts(t *testing.T) {
	_, builder, artifacts := workerFixture(t)
	request := workerRequest(t, artifacts, "inv.first")
	other := workerRequest(t, artifacts, "inv.other")
	request.Recipe.Inputs = other.Recipe.Inputs
	stream, err := builder.Build(context.Background(), request)
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal("cross invocation input accepted", err)
	}
	request.BindingConfiguration.CanonicalJson = []byte(`{"profileSHA256":"wrong","workerSHA256":"wrong"}`)
	stream, err = builder.Build(context.Background(), request)
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal("wrong worker profile accepted", err)
	}
}
func TestBudgetCannotBeReplenishedByWorkerRestart(t *testing.T) {
	dir := t.TempDir()
	charge, err := durableEgressBudget(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err = charge(512); err != nil {
		t.Fatal(err)
	}
	restart, err := durableEgressBudget(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err = restart(1); err == nil {
		t.Fatal("restart replenished reserved budget")
	}
	if err = charge(512); err != nil {
		t.Fatal(err)
	}
	if err = charge(1); err == nil {
		t.Fatal("budget exceeded")
	}
}

func TestWorkerReservesMaximumDiskAndOutputBeforeLaunching(t *testing.T) {
	s, builder, artifacts := workerFixture(t)
	request := workerRequest(t, artifacts, "inv.capacity")
	s.mu.Lock()
	s.runner.Profile.GuestDiskBytes = 64 << 30
	s.runner.Profile.MaximumStorageBytes = 64 << 30
	s.mu.Unlock()
	stream, err := builder.Build(context.Background(), request)
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatal("output reservation overcommitted worker storage", err)
	}
	out, err := builder.GetInvocation(context.Background(), &wire.BuilderServiceGetInvocationRequest{Context: request.Context})
	if err != nil || out.Result.State != "unknown" {
		t.Fatal("capacity failure started a guest", out, err)
	}
}

func TestWorkerRecoveryRequiresOriginalPinnedIdentity(t *testing.T) {
	s, _, _ := workerFixture(t)
	id := "inv.interrupted"
	if err := os.MkdirAll(filepath.Join(s.root, id), 0700); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err := s.save(id, buildJournal{Fence: 1, State: "running", ProfileDigest: s.profileDigest, WorkerDigest: s.workerDigest})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if changed, err := NewService(s.root, s.runner, strings.Repeat("9", 64), s.workerDigest); err == nil {
		changed.Close()
		t.Fatal("changed toolchain resumed original invocation")
	}
	saved, err := s.load(id)
	if err != nil || saved.State != "running" {
		t.Fatal("failed startup changed pending work", err)
	}
}
