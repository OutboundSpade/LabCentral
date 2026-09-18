package github

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const PluginID = "io.labcentral.source.github"

type journalEntry struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Digest      string `json:"digest"`
	Size        uint64 `json:"size"`
}
type journal struct {
	RequestDigest string         `json:"requestDigest"`
	Commit        string         `json:"commit"`
	Fence         uint64         `json:"fence"`
	State         string         `json:"state"`
	Entries       []journalEntry `json:"entries"`
}
type transfer struct {
	Invocation, Digest string
	Expires            time.Time
}
type Service struct {
	wire.UnimplementedSourceServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
	mu      sync.Mutex
	root    string
	handles map[string]transfer
	resolve func(context.Context, Configuration, []byte, string, func(string) error) (Collection, error)
}

var archiveName = regexp.MustCompile(`^[0-9a-f]{64}\.bundle$`)
var invocationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func New(root string) (*Service, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute plugin journal directory required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("journal must be a private directory")
	}
	return &Service{root: root, handles: map[string]transfer{}, resolve: Resolve}, nil
}
func (*Service) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (s *Service) load(id string) (journal, error) {
	if !invocationID.MatchString(id) {
		return journal{}, fmt.Errorf("invalid invocation")
	}
	file, err := os.Open(filepath.Join(s.root, id+".json"))
	if err != nil {
		return journal{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return journal{}, err
	}
	var j journal
	err = json.Unmarshal(data, &j)
	return j, err
}
func (s *Service) write(name string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(s.root, name+".next"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(filepath.Join(s.root, name+".next"), filepath.Join(s.root, name)); err != nil {
		return err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Service) save(id string, j journal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.write(id+".json", data)
}
func (s *Service) reserve() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	var total int64
	records := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if archiveName.MatchString(entry.Name()) && time.Since(info.ModTime()) > 24*time.Hour {
			if err := os.Remove(filepath.Join(s.root, entry.Name())); err != nil {
				return err
			}
			continue
		}
		total += info.Size()
		if filepath.Ext(entry.Name()) == ".json" {
			records++
		}
	}
	if total > (1<<30)-(64<<20) || records >= 16384 {
		return fmt.Errorf("source journal capacity reached")
	}
	return nil
}
func (s *Service) result(id string, j journal) (*wire.SourceServiceResolveCollectionResponse, error) {
	now := time.Now()
	for token, handle := range s.handles {
		if now.After(handle.Expires) {
			delete(s.handles, token)
		}
	}
	if len(s.handles)+len(j.Entries) > 4096 {
		return nil, fmt.Errorf("transfer handle capacity reached")
	}
	metadata, _ := json.Marshal(j.Entries)
	provenance := sha256.Sum256(append([]byte(j.Commit+"\x00"), metadata...))
	result := &wire.SourceServiceResolveCollectionResponse{ResolvedIdentity: j.Commit, ProvenanceDigest: hex.EncodeToString(provenance[:])}
	for _, entry := range j.Entries {
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			return nil, err
		}
		encoded := hex.EncodeToString(token)
		s.handles[encoded] = transfer{Invocation: id, Digest: entry.Digest, Expires: now.Add(time.Minute)}
		result.Entries = append(result.Entries, &wire.SourceCollectionEntry{Key: entry.Key, Name: entry.Name, Description: entry.Description, Bundle: &wire.ArtifactHandle{Digest: entry.Digest, Size: entry.Size, MediaType: "application/gzip", TransferToken: encoded}})
	}
	return result, nil
}
func (s *Service) ResolveCollection(ctx context.Context, in *wire.SourceServiceResolveCollectionRequest) (*wire.SourceServiceResolveCollectionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	fence := in.GetContext().GetFencingToken()
	if !invocationID.MatchString(id) || fence == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid invocation")
	}
	configuration, err := ParseConfiguration(in.GetConfiguration().GetCanonicalJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid configured source")
	}
	if in.GetReference() != configuration.Reference || len(in.GetCredentialEnvelope()) > 8192 {
		return nil, status.Error(codes.PermissionDenied, "source reference is not configured")
	}
	raw, _ := json.Marshal(struct {
		Binding, Reference string
		Configuration      Configuration
	}{in.GetBindingId(), in.GetReference(), configuration})
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	j, err := s.load(id)
	if os.IsNotExist(err) {
		if err = s.reserve(); err != nil {
			return nil, err
		}
		j = journal{RequestDigest: digest, State: "running"}
	} else if err != nil {
		return nil, err
	}
	if j.RequestDigest != digest || fence < j.Fence || (j.State == "cancelled" || j.State == "failed") {
		return nil, status.Error(codes.FailedPrecondition, "invocation conflict")
	}
	j.Fence = fence
	if err = s.save(id, j); err != nil {
		return nil, err
	}
	if j.State == "succeeded" && s.hasArchives(j) {
		return s.result(id, j)
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	collection, err := s.resolve(ctx, configuration, in.GetCredentialEnvelope(), j.Commit, func(commit string) error { j.Commit = commit; return s.save(id, j) })
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrRemoteUnavailable) {
			return nil, status.Error(codes.Unavailable, "GitHub resolution interrupted")
		}
		j.State = "failed"
		if saveErr := s.save(id, j); saveErr != nil {
			return nil, saveErr
		}
		return nil, status.Error(codes.InvalidArgument, "library source validation failed")
	}
	j.Entries = nil
	for _, entry := range collection.Entries {
		digest := sha256.Sum256(entry.Archive)
		encoded := hex.EncodeToString(digest[:])
		if err = s.write(encoded+".bundle", entry.Archive); err != nil {
			return nil, err
		}
		j.Entries = append(j.Entries, journalEntry{Key: entry.Entry.Key, Name: entry.Entry.Name, Description: entry.Entry.Description, Digest: encoded, Size: uint64(len(entry.Archive))})
	}
	j.State = "succeeded"
	if err = s.save(id, j); err != nil {
		return nil, err
	}
	return s.result(id, j)
}
func (s *Service) GetInvocation(_ context.Context, in *wire.SourceServiceGetInvocationRequest) (*wire.SourceServiceGetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	j, err := s.load(id)
	if os.IsNotExist(err) {
		return &wire.SourceServiceGetInvocationResponse{State: "unknown"}, nil
	}
	if err != nil {
		return nil, err
	}
	if in.GetContext().GetFencingToken() < j.Fence {
		return nil, status.Error(codes.FailedPrecondition, "stale invocation fence")
	}
	j.Fence = in.GetContext().GetFencingToken()
	if err = s.save(id, j); err != nil {
		return nil, err
	}
	if j.State == "running" || (j.State == "succeeded" && !s.hasArchives(j)) {
		return &wire.SourceServiceGetInvocationResponse{State: "resumable"}, nil
	} // redispatch resumes the persisted commit
	out := &wire.SourceServiceGetInvocationResponse{State: j.State}
	if j.State == "succeeded" {
		out.Collection, err = s.result(id, j)
	}
	return out, err
}
func (s *Service) Cancel(_ context.Context, in *wire.SourceServiceCancelRequest) (*wire.SourceServiceCancelResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.GetContext().GetInvocationId()
	if !invocationID.MatchString(id) {
		return nil, status.Error(codes.InvalidArgument, "invalid invocation")
	}
	j, err := s.load(id)
	if !os.IsNotExist(err) && err != nil {
		return nil, err
	}
	if in.GetContext().GetFencingToken() < j.Fence {
		return nil, status.Error(codes.FailedPrecondition, "stale invocation fence")
	}
	j.Fence = in.GetContext().GetFencingToken()
	j.State = "cancelled"
	if err = s.save(id, j); err != nil {
		return nil, err
	}
	return &wire.SourceServiceCancelResponse{Outcome: "cancelled"}, nil
}
func (s *Service) Read(in *wire.ArtifactTransferServiceReadRequest, out grpc.ServerStreamingServer[wire.ArtifactTransferServiceReadResponse]) error {
	s.mu.Lock()
	id := in.GetContext().GetInvocationId()
	artifact := in.GetArtifact()
	handle, ok := s.handles[artifact.GetTransferToken()]
	delete(s.handles, artifact.GetTransferToken())
	j, err := s.load(id)
	allowed := ok && err == nil && j.State == "succeeded" && in.GetContext().GetFencingToken() == j.Fence && handle.Invocation == id && handle.Digest == artifact.GetDigest() && time.Now().Before(handle.Expires)
	match := false
	for _, entry := range j.Entries {
		if entry.Digest == artifact.GetDigest() && entry.Size == artifact.GetSize() {
			match = true
		}
	}
	s.mu.Unlock()
	if !allowed || !match {
		return status.Error(codes.PermissionDenied, "artifact transfer denied")
	}
	file, err := os.Open(filepath.Join(s.root, handle.Digest+".bundle"))
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, 64<<10)
	var total uint64
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			total += uint64(n)
			if total > artifact.GetSize() {
				return fmt.Errorf("artifact length mismatch")
			}
			if sendErr := out.Send(&wire.ArtifactTransferServiceReadResponse{Chunk: buffer[:n]}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != artifact.GetSize() {
		return fmt.Errorf("artifact length mismatch")
	}
	return nil
}

func (s *Service) hasArchives(j journal) bool {
	for _, entry := range j.Entries {
		info, err := os.Stat(filepath.Join(s.root, entry.Digest+".bundle"))
		if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != entry.Size {
			return false
		}
	}
	return true
}
