// An independently built source contribution. The journal belongs to this
// process; neither the core database nor its artifact directory is accessible.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type record struct {
	Reference string `json:"reference"`
	Resolved  string `json:"resolved"`
	Data      []byte `json:"data"`
	Cancelled bool   `json:"cancelled"`
}
type source struct {
	wire.UnimplementedSourceServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
	mu      sync.Mutex
	root    string
	handles map[string]string
}

func (*source) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (s *source) read(id string) (record, error) {
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
func (s *source) save(id string, r record) error {
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
func (s *source) result(id string, r record) *wire.SourceServiceResolveResponse {
	sum := sha256.Sum256(r.Data)
	token := make([]byte, 32)
	if _, e := rand.Read(token); e != nil {
		panic(e)
	}
	h := hex.EncodeToString(token)
	s.handles[h] = id
	return &wire.SourceServiceResolveResponse{ResolvedIdentity: r.Resolved, ProvenanceDigest: hex.EncodeToString(sum[:]), Bundle: &wire.ArtifactHandle{Digest: hex.EncodeToString(sum[:]), Size: uint64(len(r.Data)), MediaType: "application/gzip", TransferToken: h}}
}
func (s *source) Resolve(_ context.Context, in *wire.SourceServiceResolveRequest) (*wire.SourceServiceResolveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	if !idPattern.MatchString(id) || len(in.GetReference()) == 0 || len(in.GetReference()) > 1024 {
		return nil, status.Error(codes.InvalidArgument, "invalid input")
	}
	r, e := s.read(id)
	if e == nil {
		if r.Cancelled || r.Reference != in.GetReference() {
			return nil, status.Error(codes.FailedPrecondition, "invocation conflict")
		}
		return s.result(id, r), nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	// Changing a reference changes the immutable identity and bundle. This fixture
	// deliberately has no network integration or executable template processing.
	hash := sha256.Sum256([]byte(in.GetReference()))
	identity := "fixture:" + hex.EncodeToString(hash[:])
	data, e := bundle(identity)
	if e != nil {
		return nil, e
	}
	if in.GetReference() == "malformed" {
		data = []byte("malformed")
	}
	r = record{Reference: in.GetReference(), Resolved: identity, Data: data}
	if e = s.save(id, r); e != nil {
		return nil, e
	}
	if in.GetReference() == "delayed" {
		time.Sleep(3 * time.Second)
	}
	return s.result(id, r), nil
}
func (s *source) GetInvocation(_ context.Context, in *wire.SourceServiceGetInvocationRequest) (*wire.SourceServiceGetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	r, e := s.read(id)
	if os.IsNotExist(e) {
		return &wire.SourceServiceGetInvocationResponse{State: "unknown"}, nil
	}
	if e != nil {
		return nil, e
	}
	if r.Cancelled {
		return &wire.SourceServiceGetInvocationResponse{State: "cancelled"}, nil
	}
	return &wire.SourceServiceGetInvocationResponse{State: "succeeded", Result: s.result(id, r)}, nil
}
func (s *source) Cancel(_ context.Context, in *wire.SourceServiceCancelRequest) (*wire.SourceServiceCancelResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	r, e := s.read(id)
	if os.IsNotExist(e) {
		r = record{}
	} else if e != nil {
		return nil, e
	}
	r.Cancelled = true
	if e = s.save(id, r); e != nil {
		return nil, e
	}
	return &wire.SourceServiceCancelResponse{Outcome: "cancelled"}, nil
}
func (s *source) Read(in *wire.ArtifactTransferServiceReadRequest, out grpc.ServerStreamingServer[wire.ArtifactTransferServiceReadResponse]) error {
	s.mu.Lock()
	id := in.GetContext().GetInvocationId()
	token := in.GetArtifact().GetTransferToken()
	bound := s.handles[token]
	delete(s.handles, token)
	r, e := s.read(id)
	s.mu.Unlock()
	if e != nil || bound != id || r.Cancelled {
		return status.Error(codes.PermissionDenied, "artifact handle denied")
	}
	sum := sha256.Sum256(r.Data)
	if in.GetArtifact().GetDigest() != hex.EncodeToString(sum[:]) || in.GetArtifact().GetSize() != uint64(len(r.Data)) {
		return status.Error(codes.InvalidArgument, "artifact identity mismatch")
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

//go:embed lab.yaml
var fixtureLab []byte

func bundle(identity string) ([]byte, error) {
	source := fixtureLab
	sum := sha256.Sum256(source)
	manifest := fmt.Sprintf("apiVersion: labcentral.bundle/v1alpha1\nkind: LabBundle\nentrypoint: lab.yaml\nfiles:\n  - path: lab.yaml\n    size: %d\n    sha256: %s\n    mediaType: application/yaml\nprovenance:\n  source: fixture\n  resolvedIdentity: %s\n", len(source), hex.EncodeToString(sum[:]), identity)
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"bundle.yaml", []byte(manifest)}, {"lab.yaml", source}} {
		if e := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data))}); e != nil {
			return nil, e
		}
		if _, e := tw.Write(f.data); e != nil {
			return nil, e
		}
	}
	if e := tw.Close(); e != nil {
		return nil, e
	}
	if e := gz.Close(); e != nil {
		return nil, e
	}
	return buffer.Bytes(), nil
}
func main() {
	root := flag.String("state", "", "plugin-owned journal directory")
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
	s := &source{root: *root, handles: map[string]string{}}
	e = runtime.Serve(context.Background(), runtime.Services{Info: &wire.PluginInfo{PluginId: "org.example.source", PluginVersion: "0.1.0", BuildDigest: "sha256:" + hex.EncodeToString(hash[:]), Categories: []string{"source"}, SourceKinds: []string{"fixture"}, SupportedSourceProtocols: []*wire.ProtocolVersionRange{{Major: 1}}, Features: []string{"artifact-transfer-v1"}}, Runtime: s, Source: s, Artifacts: s})
	if e != nil {
		panic(e)
	}
}
