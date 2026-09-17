package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type record struct {
	Reference string
	Data      []byte
	Cancelled bool
}
type builder struct {
	wire.UnimplementedBuilderServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
	mu      sync.Mutex
	root    string
	handles map[string]string
}

func (*builder) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (s *builder) read(id string) (record, error) {
	if !idPattern.MatchString(id) {
		return record{}, fmt.Errorf("invalid invocation")
	}
	b, e := os.ReadFile(filepath.Join(s.root, id+".json"))
	if e != nil {
		return record{}, e
	}
	var r record
	e = json.Unmarshal(b, &r)
	return r, e
}
func (s *builder) save(id string, r record) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid invocation")
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	p := filepath.Join(s.root, id+".next")
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	_ = f.Close()
	if e != nil {
		return e
	}
	if e = os.Rename(p, filepath.Join(s.root, id+".json")); e != nil {
		return e
	}
	d, e := os.Open(s.root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s *builder) handle(id string, data []byte) *wire.ArtifactHandle {
	token := make([]byte, 32)
	if _, e := rand.Read(token); e != nil {
		panic(e)
	}
	h := hex.EncodeToString(token)
	s.handles[h] = id
	sum := sha256.Sum256(data)
	return &wire.ArtifactHandle{Digest: hex.EncodeToString(sum[:]), Size: uint64(len(data)), MediaType: "application/octet-stream", TransferToken: h}
}
func (s *builder) Write(stream grpc.ClientStreamingServer[wire.ArtifactTransferServiceWriteRequest, wire.ArtifactTransferServiceWriteResponse]) error {
	var data []byte
	var id, digest string
	var size uint64
	for {
		v, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if id == "" {
			id = v.GetContext().GetInvocationId()
			digest = v.GetArtifact().GetDigest()
			size = v.GetArtifact().GetSize()
			if !idPattern.MatchString(id) || size == 0 || size > 8<<20 {
				return status.Error(codes.InvalidArgument, "invalid input")
			}
		}
		if id != v.GetContext().GetInvocationId() || digest != v.GetArtifact().GetDigest() || size != v.GetArtifact().GetSize() || len(v.GetChunk()) > 64<<10 || len(v.GetChunk()) == 0 || uint64(len(data)+len(v.GetChunk())) > size {
			return status.Error(codes.InvalidArgument, "transfer bounds")
		}
		data = append(data, v.GetChunk()...)
	}
	sum := sha256.Sum256(data)
	if id == "" || uint64(len(data)) != size || digest != hex.EncodeToString(sum[:]) {
		return status.Error(codes.InvalidArgument, "transfer digest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := id + ".input"
	old, e := s.read(name)
	if e == nil && old.Reference != digest {
		return status.Error(codes.FailedPrecondition, "input changed")
	}
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = s.save(name, record{Reference: digest, Data: data}); e != nil {
		return e
	}
	return stream.SendAndClose(&wire.ArtifactTransferServiceWriteResponse{Artifact: s.handle(name, data)})
}
func (s *builder) Read(in *wire.ArtifactTransferServiceReadRequest, out grpc.ServerStreamingServer[wire.ArtifactTransferServiceReadResponse]) error {
	s.mu.Lock()
	bound := s.handles[in.GetArtifact().GetTransferToken()]
	delete(s.handles, in.GetArtifact().GetTransferToken())
	r, e := s.read(bound)
	s.mu.Unlock()
	if bound != in.GetContext().GetInvocationId()+".output" || e != nil || r.Cancelled {
		return status.Error(codes.PermissionDenied, "handle denied")
	}
	sum := sha256.Sum256(r.Data)
	if in.GetArtifact().GetDigest() != hex.EncodeToString(sum[:]) || in.GetArtifact().GetSize() != uint64(len(r.Data)) {
		return status.Error(codes.InvalidArgument, "artifact mismatch")
	}
	for offset := 0; offset < len(r.Data); {
		end := min(offset+65536, len(r.Data))
		if e = out.Send(&wire.ArtifactTransferServiceReadResponse{Chunk: r.Data[offset:end]}); e != nil {
			return e
		}
		offset = end
	}
	return nil
}
func (*builder) Validate(_ context.Context, in *wire.BuilderServiceValidateRequest) (*wire.BuilderServiceValidateResponse, error) {
	if in.GetRecipe().GetConfiguration().GetSchema().GetName() != "fixture.image.v1" || in.GetLimits().GetTimeoutSeconds() == 0 || in.GetLimits().GetMemoryBytes() < 1024 || in.GetLimits().GetMaximumOutputBytes() < 256 || len(in.GetLimits().GetAllowedEgress()) != 0 {
		return &wire.BuilderServiceValidateResponse{Errors: []*wire.StructuredError{{Code: "fixture_limits", SafeMessage: "fixture requires bounded execution and no egress"}}}, nil
	}
	return &wire.BuilderServiceValidateResponse{}, nil
}
func (s *builder) result(id string, r record) *wire.BuilderServiceBuildResponse {
	if r.Cancelled {
		return &wire.BuilderServiceBuildResponse{State: "cancelled"}
	}
	sum := sha256.Sum256(r.Data)
	return &wire.BuilderServiceBuildResponse{State: "succeeded", Image: s.handle(id+".output", r.Data), ProvenanceDigest: hex.EncodeToString(sum[:])}
}
func (s *builder) Build(in *wire.BuilderServiceBuildRequest, out grpc.ServerStreamingServer[wire.BuilderServiceBuildResponse]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	if !idPattern.MatchString(id) || len(in.GetRecipe().GetInputs()) != 1 {
		return status.Error(codes.InvalidArgument, "invalid build")
	}
	old, e := s.read(id + ".output")
	if e == nil {
		if old.Reference != in.GetRecipe().GetDigest() {
			return status.Error(codes.FailedPrecondition, "invocation changed")
		}
		return out.Send(s.result(id, old))
	}
	if !os.IsNotExist(e) {
		return e
	}
	input := in.GetRecipe().GetInputs()[0]
	bound := s.handles[input.GetTransferToken()]
	delete(s.handles, input.GetTransferToken())
	r, e := s.read(id + ".input")
	if e != nil || bound != id+".input" || input.GetDigest() != r.Reference || input.GetSize() != uint64(len(r.Data)) {
		return status.Error(codes.PermissionDenied, "input denied")
	}
	sum := sha256.Sum256(r.Data)
	data := []byte("LABCENTRAL_FIXTURE_IMAGE_V1\n" + hex.EncodeToString(sum[:]) + "\n" + in.GetRecipe().GetDigest())
	old = record{Reference: in.GetRecipe().GetDigest(), Data: data}
	if uint64(len(data)) > in.GetLimits().GetMaximumOutputBytes() {
		return status.Error(codes.ResourceExhausted, "output limit")
	}
	if e = s.save(id+".output", old); e != nil {
		return e
	}
	if string(in.GetRecipe().GetConfiguration().GetCanonicalJson()) == `{"delay":true}` {
		select {
		case <-out.Context().Done():
			return out.Context().Err()
		case <-time.After(3 * time.Second):
		}
	}
	return out.Send(s.result(id, old))
}
func (s *builder) GetInvocation(_ context.Context, in *wire.BuilderServiceGetInvocationRequest) (*wire.BuilderServiceGetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	r, e := s.read(id + ".output")
	if os.IsNotExist(e) {
		return &wire.BuilderServiceGetInvocationResponse{Result: &wire.BuilderServiceBuildResponse{State: "unknown"}}, nil
	}
	if e != nil {
		return nil, e
	}
	return &wire.BuilderServiceGetInvocationResponse{Result: s.result(id, r)}, nil
}
func (s *builder) Cancel(_ context.Context, in *wire.BuilderServiceCancelRequest) (*wire.BuilderServiceCancelResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	r, e := s.read(id + ".output")
	if os.IsNotExist(e) {
		r = record{Cancelled: true}
	} else if e != nil {
		return nil, e
	} else {
		return &wire.BuilderServiceCancelResponse{Outcome: "completed"}, nil
	}
	if e = s.save(id+".output", r); e != nil {
		return nil, e
	}
	return &wire.BuilderServiceCancelResponse{Outcome: "cancelled"}, nil
}
func main() {
	root := flag.String("state", "", "plugin-owned journal")
	flag.Parse()
	if *root == "" {
		panic("state required")
	}
	if e := os.MkdirAll(*root, 0700); e != nil {
		panic(e)
	}
	path, e := os.Executable()
	if e != nil {
		panic(e)
	}
	binary, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	hash := sha256.Sum256(binary)
	s := &builder{root: *root, handles: map[string]string{}}
	e = runtime.Serve(context.Background(), runtime.Services{Info: &wire.PluginInfo{PluginId: "org.example.builder", PluginVersion: "0.1.0", BuildDigest: "sha256:" + hex.EncodeToString(hash[:]), Categories: []string{"builder"}, BuilderKinds: []string{"fixture"}, SupportedBuilderProtocols: []*wire.ProtocolVersionRange{{Major: 1}}, Features: []string{"artifact-transfer-v1"}}, Runtime: s, Builder: s, Artifacts: s})
	if e != nil {
		panic(e)
	}
}
