package vm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const PluginID = "io.labcentral.builder.vm"

var invocationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type WorkerBinding struct {
	ProfileSHA256 string `json:"profileSHA256"`
	WorkerSHA256  string `json:"workerSHA256"`
}
type buildJournal struct {
	ProfileDigest string                 `json:"profileDigest,omitempty"`
	WorkerDigest  string                 `json:"workerDigest,omitempty"`
	Version       uint32                 `json:"version"`
	Fence         uint64                 `json:"fence"`
	RequestDigest string                 `json:"requestDigest,omitempty"`
	State         string                 `json:"state"`
	Updated       time.Time              `json:"updated"`
	Inputs        []*wire.ArtifactHandle `json:"inputs"`
	Spec          RunSpec                `json:"spec"`
	Output        RunOutput              `json:"output"`
	Provenance    string                 `json:"provenance,omitempty"`
	Failure       string                 `json:"failure,omitempty"`
}
type artifactTicket struct {
	Invocation, Digest string
	Expires            time.Time
}
type Service struct {
	wire.UnimplementedBuilderServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
	mu                          sync.Mutex
	root                        string
	runner                      Runner
	profileDigest, workerDigest string
	tickets                     map[string]artifactTicket
	running                     map[string]context.CancelFunc
	writing                     map[string]bool
	run                         func(context.Context, RunSpec, bool) (RunOutput, error)
	stop                        func(context.Context, RunSpec) error
	wg                          sync.WaitGroup
	closed                      bool
	lock                        *os.File
}

func NewService(root string, runner Runner, profileDigest, workerDigest string) (*Service, error) {
	if !filepath.IsAbs(root) || !digestValid(profileDigest) || !digestValid(workerDigest) {
		return nil, fmt.Errorf("private worker state and pinned identity required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("worker state must be a private real directory")
	}
	s := &Service{root: root, runner: runner, profileDigest: profileDigest, workerDigest: workerDigest, tickets: map[string]artifactTicket{}, running: map[string]context.CancelFunc{}, writing: map[string]bool{}, run: runner.Run, stop: StopGuest}
	if err = s.lockState(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.mu.Lock()
	type pendingWork struct {
		id string
		j  buildJournal
	}
	var pending []pendingWork
	for _, entry := range entries {
		if !entry.IsDir() || !invocationPattern.MatchString(entry.Name()) {
			continue
		}
		j, err := s.load(entry.Name())
		if err != nil {
			s.mu.Unlock()
			s.Close()
			return nil, fmt.Errorf("invalid worker recovery journal")
		}
		if j.State == "running" || j.State == "canceling" {
			if j.ProfileDigest != s.profileDigest || j.WorkerDigest != s.workerDigest {
				s.mu.Unlock()
				s.Close()
				return nil, fmt.Errorf("pending invocation requires its original worker identity")
			}
			pending = append(pending, pendingWork{entry.Name(), j})
		}
	}
	for _, work := range pending {
		s.start(work.id, work.j, false)
	}
	s.mu.Unlock()
	return s, nil
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.running {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
}
func (*Service) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (s *Service) load(id string) (buildJournal, error) {
	if !invocationPattern.MatchString(id) {
		return buildJournal{}, fmt.Errorf("invalid invocation")
	}
	f, err := os.Open(filepath.Join(s.root, id, "journal.json"))
	if err != nil {
		return buildJournal{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return buildJournal{}, err
	}
	var j buildJournal
	err = strictJSON(data, &j)
	if err == nil && j.Version != 1 {
		err = fmt.Errorf("unsupported builder journal version")
	}
	return j, err
}
func (s *Service) save(id string, j buildJournal) error {
	j.Version = 1
	j.Updated = time.Now().UTC()
	return durableJSON(filepath.Join(s.root, id), "journal.json", j)
}
func (s *Service) journal(call *wire.CallContext, create bool) (string, buildJournal, error) {
	id, fence := call.GetInvocationId(), call.GetFencingToken()
	if !invocationPattern.MatchString(id) || fence == 0 {
		return "", buildJournal{}, status.Error(codes.InvalidArgument, "invalid invocation fence")
	}
	j, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = s.capacity(); err != nil {
			return "", j, err
		}
		dir := filepath.Join(s.root, id)
		if err = os.MkdirAll(filepath.Join(dir, "inputs"), 0700); err != nil {
			return "", j, err
		}
		j = buildJournal{Fence: fence, State: "staging"}
		err = s.save(id, j)
	}
	if err != nil {
		return id, j, err
	}
	if fence < j.Fence {
		return id, j, status.Error(codes.FailedPrecondition, "stale invocation fence")
	}
	if fence > j.Fence {
		j.Fence = fence
		err = s.save(id, j)
	}
	return id, j, err
}

// Reservations include the maximum overlay and output, not just currently
// allocated sparse bytes. Upload and launch cannot overcommit the worker pool.
func (s *Service) reserveStorage(id string, replacement buildJournal) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	var total, memory uint64
	for _, entry := range entries {
		if !entry.IsDir() || !invocationPattern.MatchString(entry.Name()) {
			continue
		}
		j := replacement
		if entry.Name() != id {
			j, err = s.load(entry.Name())
			if err != nil {
				return err
			}
		}
		total += j.Output.Size
		for _, input := range j.Inputs {
			total += input.Size
		}
		if j.State == "running" || j.State == "canceling" {
			total += s.runner.Profile.GuestDiskBytes + j.Spec.Limits.GetMaximumOutputBytes()
			memory += s.runner.Profile.MemoryOverheadBytes + j.Spec.Limits.GetMemoryBytes()
		}
		if memory > s.runner.Profile.MaximumMemoryBytes {
			return status.Error(codes.ResourceExhausted, "worker memory reservation exhausted")
		}
		if total > s.runner.Profile.MaximumStorageBytes {
			return status.Error(codes.ResourceExhausted, "worker storage reservation exhausted")
		}
	}
	return nil
}
func (s *Service) capacity() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	active := 0
	records := 0
	var retained uint64
	for _, entry := range entries {
		if !entry.IsDir() || !invocationPattern.MatchString(entry.Name()) {
			continue
		}
		records++
		j, err := s.load(entry.Name())
		if err != nil {
			return err
		}
		if j.State == "running" || j.State == "canceling" || j.State == "staging" {
			active++
		}
		retained += j.Output.Size
		for _, input := range j.Inputs {
			retained += input.Size
		}
	}
	if records >= 16384 || active >= int(s.runner.Profile.MaximumParallel)+8 || retained > 128<<30 {
		return status.Error(codes.ResourceExhausted, "builder storage or queue capacity reached")
	}
	return nil
}
func (s *Service) ticket(id, digest string) (string, error) {
	now := time.Now()
	for token, t := range s.tickets {
		if now.After(t.Expires) {
			delete(s.tickets, token)
		}
	}
	if len(s.tickets) >= 4096 {
		return "", status.Error(codes.ResourceExhausted, "transfer capacity reached")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	s.tickets[token] = artifactTicket{id, digest, now.Add(10 * time.Minute)}
	return token, nil
}
func (s *Service) result(id string, j buildJournal) (*wire.BuilderServiceBuildResponse, error) {
	out := &wire.BuilderServiceBuildResponse{State: j.State}
	if j.State == "failed" {
		out.SafeMessage = "The isolated image build failed; an operator can inspect its private worker record."
	}
	if j.State == "succeeded" {
		token, err := s.ticket(id, j.Output.Digest)
		if err != nil {
			return nil, err
		}
		out.Image = &wire.ArtifactHandle{Digest: j.Output.Digest, Size: j.Output.Size, MediaType: j.Output.MediaType, TransferToken: token}
		out.ProvenanceDigest = j.Provenance
	}
	if j.State == "staging" {
		out.State = "unknown"
	}
	if j.State == "canceling" {
		out.State = "running"
	}
	return out, nil
}
func (s *Service) Validate(_ context.Context, in *wire.BuilderServiceValidateRequest) (*wire.BuilderServiceValidateResponse, error) {
	if err := s.validate(in.GetRecipe(), in.GetLimits()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &wire.BuilderServiceValidateResponse{}, nil
}
func (s *Service) validate(recipe *wire.BuildRecipe, limits *wire.BuildLimits) error {
	if err := ValidateRecipe(recipe, limits); err != nil {
		return err
	}
	for _, host := range limits.AllowedEgress {
		if !slices.Contains(s.runner.Profile.AllowedEgress, host) {
			return fmt.Errorf("build egress exceeds worker policy")
		}
	}
	return nil
}
func requestIdentity(in *wire.BuilderServiceBuildRequest) (string, *wire.BuilderServiceBuildRequest, error) {
	clone := proto.Clone(in).(*wire.BuilderServiceBuildRequest)
	clone.Context = nil
	clone.Output = nil
	for _, a := range clone.Recipe.Inputs {
		a.TransferToken = ""
	}
	for _, d := range clone.Recipe.Dependencies {
		d.Artifact.TransferToken = ""
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), clone, nil
}
func (s *Service) Build(in *wire.BuilderServiceBuildRequest, stream grpc.ServerStreamingServer[wire.BuilderServiceBuildResponse]) error {
	if err := s.validate(in.GetRecipe(), in.GetLimits()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	var binding WorkerBinding
	if strictJSON(in.GetBindingConfiguration().GetCanonicalJson(), &binding) != nil || binding.ProfileSHA256 != s.profileDigest || binding.WorkerSHA256 != s.workerDigest {
		return status.Error(codes.FailedPrecondition, "original pinned worker profile required")
	}
	digest, clean, err := requestIdentity(in)
	if err != nil {
		return err
	}
	s.mu.Lock()
	id, j, err := s.journal(in.Context, false)
	if err == nil && j.RequestDigest != "" && j.RequestDigest != digest {
		err = status.Error(codes.AlreadyExists, "invocation input changed")
	}
	if err == nil && j.State == "staging" {
		if len(s.running) >= int(s.runner.Profile.MaximumParallel) {
			err = status.Error(codes.ResourceExhausted, "all disposable build slots occupied")
		} else {
			artifacts := append([]*wire.ArtifactHandle{}, in.Recipe.Inputs...)
			for _, d := range in.Recipe.Dependencies {
				artifacts = append(artifacts, d.Artifact)
			}
			var total uint64
			for _, a := range artifacts {
				t, ok := s.tickets[a.TransferToken]
				total += a.Size
				if !ok || t.Invocation != id || t.Digest != a.Digest || time.Now().After(t.Expires) || total > in.Limits.MaximumInputBytes {
					err = status.Error(codes.PermissionDenied, "invalid invocation input capability")
					break
				}
				found := false
				for _, saved := range j.Inputs {
					if saved.Digest == a.Digest && saved.Size == a.Size && saved.MediaType == a.MediaType {
						found = true
					}
				}
				if !found || s.writing[id+"/"+a.Digest] {
					err = status.Error(codes.FailedPrecondition, "input transfer incomplete")
					break
				}
			}
			if err == nil {
				listener, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					err = e
				} else {
					port := uint16(listener.Addr().(*net.TCPAddr).Port)
					listener.Close()
					sum := sha256.Sum256([]byte(s.root + "/" + id))
					j.Spec = RunSpec{Directory: filepath.Join(s.root, id), Unit: "labcentral-build-" + hex.EncodeToString(sum[:16]), ProxyPort: port, Deadline: time.Now().UTC().Add(time.Duration(in.Limits.TimeoutSeconds) * time.Second), Recipe: clean.Recipe, Limits: clean.Limits}
					j.RequestDigest = digest
					j.ProfileDigest = s.profileDigest
					j.WorkerDigest = s.workerDigest
					j.State = "running"
					err = s.reserveStorage(id, j)
					if err == nil {
						err = s.save(id, j)
					}
					if err == nil {
						s.start(id, j, true)
					}
				}
			}
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		_, j, err = s.journal(in.Context, false)
		var result *wire.BuilderServiceBuildResponse
		if err == nil {
			result, err = s.result(id, j)
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if err = stream.Send(result); err != nil {
			return err
		}
		if result.State != "running" {
			return nil
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
		}
	}
}
func (s *Service) start(id string, j buildJournal, first bool) {
	if s.closed {
		return
	}
	s.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	s.running[id] = cancel
	go func() {
		defer s.wg.Done()
		var output RunOutput
		var err error
		if j.State == "canceling" {
			err = fmt.Errorf("canceled")
		} else {
			output, err = s.run(ctx, j.Spec, first)
		}
		// Never report a terminal job until external guest closure is confirmed.
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupErr := s.stop(cleanupCtx, j.Spec)
		stop()
		s.mu.Lock()
		defer s.mu.Unlock()
		defer cancel()
		delete(s.running, id)
		current, loadErr := s.load(id)
		if loadErr != nil {
			return
		}
		if cleanupErr != nil {
			return
		} // recovery retries closure under the original unit
		if current.State == "canceling" {
			current.State = "cancelled"
		} else if err != nil {
			current.State = "failed"
			// Keep host diagnostics private; do not expose paths or tool output
			// through the product API or audit stream.
			current.Failure = err.Error()
			if len(current.Failure) > 512 {
				current.Failure = current.Failure[:512]
			}
		} else {
			current.State = "succeeded"
			current.Output = output
			data, _ := json.Marshal(struct {
				Version                  uint32
				Request, Profile, Worker string
				Output                   RunOutput
			}{1, current.RequestDigest, s.profileDigest, s.workerDigest, output})
			sum := sha256.Sum256(data)
			current.Provenance = hex.EncodeToString(sum[:])
		}
		if current.State != "succeeded" {
			for _, name := range []string{"disk.qcow2", "output.next"} {
				if e := os.Remove(filepath.Join(j.Spec.Directory, name)); e != nil && !os.IsNotExist(e) {
					return
				}
			}
			if e := os.RemoveAll(filepath.Join(j.Spec.Directory, "inputs")); e != nil {
				return
			}
		}
		current.Inputs = nil
		_ = s.save(id, current)
	}()
}
func (s *Service) GetInvocation(_ context.Context, in *wire.BuilderServiceGetInvocationRequest) (*wire.BuilderServiceGetInvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, j, err := s.journal(in.GetContext(), false)
	if errors.Is(err, os.ErrNotExist) {
		return &wire.BuilderServiceGetInvocationResponse{Result: &wire.BuilderServiceBuildResponse{State: "unknown"}}, nil
	}
	if err != nil {
		return nil, err
	}
	if (j.State == "running" || j.State == "canceling") && s.running[id] == nil {
		s.start(id, j, false)
	}
	result, err := s.result(id, j)
	return &wire.BuilderServiceGetInvocationResponse{Result: result}, err
}
func (s *Service) Cancel(_ context.Context, in *wire.BuilderServiceCancelRequest) (*wire.BuilderServiceCancelResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, j, err := s.journal(in.GetContext(), false)
	if errors.Is(err, os.ErrNotExist) {
		return &wire.BuilderServiceCancelResponse{Outcome: "absent"}, nil
	}
	if err != nil {
		return nil, err
	}
	if j.State == "succeeded" || j.State == "failed" {
		return &wire.BuilderServiceCancelResponse{Outcome: "completed"}, nil
	}
	if j.State == "cancelled" {
		return &wire.BuilderServiceCancelResponse{Outcome: "cancelled"}, nil
	}
	if j.State == "staging" {
		j.State = "cancelled"
		j.Inputs = nil
		err = s.save(id, j)
		if err == nil {
			err = os.RemoveAll(filepath.Join(s.root, id, "inputs"))
		}
		return &wire.BuilderServiceCancelResponse{Outcome: "cancelled"}, err
	}
	j.State = "canceling"
	if err = s.save(id, j); err != nil {
		return nil, err
	}
	if cancel := s.running[id]; cancel != nil {
		cancel()
	} else {
		s.start(id, j, false)
	}
	return &wire.BuilderServiceCancelResponse{Outcome: "pending"}, nil
}
