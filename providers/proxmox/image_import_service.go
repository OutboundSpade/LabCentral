package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const imageImportPlanV11 = "proxmox.image-import-plan/v1.1"
const imageImportPlanV1 = "proxmox.image-import-plan/v1"
const qcowArtifactMedia = "application/x-qemu-disk"

type importPlanDocument struct {
	ContainerRuntime *containervm.PackagedImage `json:"containerRuntime,omitempty"`
	Version          string                     `json:"version"`
	InvocationID     string                     `json:"invocationId"`
	TargetID         string                     `json:"targetId"`
	Fingerprint      string                     `json:"fingerprint"`
	ArtifactDigest   string                     `json:"artifactDigest"`
	ArtifactBytes    uint64                     `json:"artifactBytes"`
	Provenance       string                     `json:"provenance"`
	Node             string                     `json:"node"`
	VMID             uint32                     `json:"vmid"`
}
type importedImageEvidence struct {
	Version             string             `json:"version"`
	Plan                importPlanDocument `json:"plan"`
	ConfigurationDigest string             `json:"configurationDigest"`
	Profile             TemplateProfile    `json:"profile"`
	Ownership           string             `json:"ownership"`
	Retired             bool               `json:"retired"`
}
type imageImportRecord struct {
	Version   string                 `json:"version"`
	Plan      importPlanDocument     `json:"plan"`
	Fence     uint64                 `json:"fence"`
	Phase     string                 `json:"phase"`
	Task      string                 `json:"task,omitempty"`
	DiskBytes uint64                 `json:"diskBytes"`
	Evidence  *importedImageEvidence `json:"evidence,omitempty"`
}
type importTransferGrant struct {
	TargetID, Fingerprint          string
	MaximumBytes, MaximumDiskBytes uint64
	Fence                          uint64
	Expires                        time.Time
}

type ImageService struct {
	wire.UnimplementedImageImportServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	provider *Service
	journal  *InvocationJournal
	root     string
	mu       sync.Mutex
	grants   map[string]importTransferGrant
}

func NewImageService(provider *Service, journalDirectory string) (*ImageService, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider required")
	}
	journal, err := OpenInvocationJournal(journalDirectory)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(journalDirectory, "images")
	if err = os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private real image directory required")
	}
	return &ImageService{provider: provider, journal: journal, root: root, grants: map[string]importTransferGrant{}}, nil
}
func (s *ImageService) qualified(ctx context.Context, call *wire.CallContext, target *wire.TargetContext) (TargetConfig, ImageImportAPI, error) {
	var zero TargetConfig
	if call == nil || target == nil || !invocationIDPattern.MatchString(call.InvocationId) || call.FencingToken == 0 || call.ProviderTargetId != target.TargetId || !logicalIDPattern.MatchString(target.TargetId) || !fingerprintPattern.MatchString(target.ConfigFingerprint) {
		return zero, nil, status.Error(codes.InvalidArgument, "invalid import invocation")
	}
	config, err := mutationConfig(target)
	if err != nil || config.ImageImport == nil || config.JournalDirectory != s.journal.directory {
		return zero, nil, status.Error(codes.FailedPrecondition, "qualified image import policy required")
	}
	discovered, err := s.provider.factory.New(target.ConfigEnvelope, target.CredentialEnvelope)
	if err != nil {
		return zero, nil, status.Error(codes.Unavailable, "import target unavailable")
	}
	client, ok := discovered.(ImageImportAPI)
	if !ok {
		discovered.CloseIdleConnections()
		return zero, nil, status.Error(codes.Unimplemented, "image import API unavailable")
	}
	fail := func() (TargetConfig, ImageImportAPI, error) {
		client.CloseIdleConnections()
		return zero, nil, status.Error(codes.FailedPrecondition, "image import qualification unavailable")
	}
	inv, err := client.Discover(ctx)
	if err != nil {
		return fail()
	}
	digest, err := inv.MutationDigestForConfig(config)
	if err != nil {
		return fail()
	}
	if enabled, _ := s.provider.mutationCapabilityGate(ctx, target, client, inv, digest); !enabled {
		return fail()
	}
	supported, err := client.ImportStorage(ctx, config.DestinationNode, config.ImageImport.StagingStorage)
	if err != nil || !supported {
		return fail()
	}
	s.mu.Lock()
	now := time.Now()
	for id, g := range s.grants {
		if now.After(g.Expires) {
			delete(s.grants, id)
		}
	}
	if len(s.grants) >= 256 {
		if _, found := s.grants[call.InvocationId]; !found {
			s.mu.Unlock()
			client.CloseIdleConnections()
			return zero, nil, status.Error(codes.ResourceExhausted, "import transfer limit")
		}
	}
	if prior, found := s.grants[call.InvocationId]; found && (prior.TargetID != target.TargetId || prior.Fingerprint != target.ConfigFingerprint || prior.Fence > call.FencingToken) {
		s.mu.Unlock()
		client.CloseIdleConnections()
		return zero, nil, status.Error(codes.FailedPrecondition, "import invocation grant changed")
	}
	s.grants[call.InvocationId] = importTransferGrant{Fence: call.FencingToken, TargetID: target.TargetId, Fingerprint: target.ConfigFingerprint, MaximumBytes: config.ImageImport.MaximumArtifactBytes, MaximumDiskBytes: config.ImageImport.MaximumDiskBytes, Expires: now.Add(15 * time.Minute)}
	s.mu.Unlock()
	return config, client, nil
}
func imageRecordName(id string) string { return "inv-" + bareDigest([]byte(id)) + ".json" }
func (s *ImageService) load(id string) (r imageImportRecord, err error) {
	raw, err := readImageFile(filepath.Join(s.root, imageRecordName(id)))
	if err != nil {
		return r, err
	}
	err = decodeEnvelope(raw, maximumJournalBytes, &r)
	if err == nil && (r.Version != imageImportPlanV1 || r.Plan.InvocationID != id || r.Fence == 0) {
		err = fmt.Errorf("invalid import journal")
	}
	return
}
func readImageFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maximumJournalBytes {
		return nil, fmt.Errorf("invalid image evidence file")
	}
	return os.ReadFile(path)
}
func writeImageFile(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > maximumJournalBytes {
		return fmt.Errorf("image evidence too large")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".image-next-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *ImageService) save(r imageImportRecord) error {
	return writeImageFile(filepath.Join(s.root, imageRecordName(r.Plan.InvocationID)), r)
}
func planWire(p importPlanDocument) (*wire.ImageImportPlan, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &wire.ImageImportPlan{Opaque: raw, Digest: bareDigest(raw), Claims: []*wire.AllocationClaim{{Namespace: vmidClaimNamespace, Value: strconv.FormatUint(uint64(p.VMID), 10)}}}, nil
}
func (s *ImageService) Plan(ctx context.Context, in *wire.ImageImportServicePlanRequest) (*wire.ImageImportServicePlanResponse, error) {
	r := in.GetInput()
	config, client, err := s.qualified(ctx, r.GetContext(), r.GetTarget())
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	runtime, err := importRuntimeConfiguration(r.GetConfigurationJson(), config)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime image evidence")
	}
	version := imageImportPlanV1
	if runtime != nil {
		version = imageImportPlanV11
	}
	a := r.GetArtifact()
	if r.Architecture != "amd64" || a.GetMediaType() != qcowArtifactMedia || !sha256Pattern.MatchString("sha256:"+a.GetDigest()) || a.GetSize() == 0 || a.Size > config.ImageImport.MaximumArtifactBytes || !sha256Pattern.MatchString("sha256:"+r.ProvenanceDigest) || len(in.ExcludedAllocations) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "invalid image artifact")
	}
	excluded := map[string]bool{}
	for _, v := range in.ExcludedAllocations {
		if v.Namespace == vmidClaimNamespace {
			excluded[v.Value] = true
		}
	}
	var vmid uint32
	attempts := 0
	for _, r := range config.ImageImport.VMIDRanges {
		for id := r.Start; id <= r.End && attempts < 4096; id++ {
			attempts++
			if excluded[strconv.FormatUint(uint64(id), 10)] {
				continue
			}
			available, err := client.AdvisoryVMIDAvailable(ctx, id)
			if err != nil {
				return nil, status.Error(codes.Unavailable, "VMID discovery failed")
			}
			if available {
				vmid = id
				break
			}
		}
		if vmid != 0 {
			break
		}
	}
	if vmid == 0 {
		return nil, status.Error(codes.ResourceExhausted, "no import VMID available")
	}
	p, err := planWire(importPlanDocument{ContainerRuntime: runtime, Version: version, InvocationID: r.Context.InvocationId, TargetID: r.Target.TargetId, Fingerprint: r.Target.ConfigFingerprint, ArtifactDigest: a.Digest, ArtifactBytes: a.Size, Provenance: r.ProvenanceDigest, Node: config.DestinationNode, VMID: vmid})
	return &wire.ImageImportServicePlanResponse{Plan: p}, err
}
func decodeImportPlan(r *wire.ImageImportServiceImportRequest, config TargetConfig) (p importPlanDocument, err error) {
	w := r.GetReservedPlan()
	if len(w.GetOpaque()) == 0 || w.Digest != bareDigest(w.Opaque) {
		return p, fmt.Errorf("reserved import plan required")
	}
	if err = decodeEnvelope(w.Opaque, maximumJournalBytes, &p); err != nil {
		return p, err
	}
	runtime, err := importRuntimeConfiguration(r.GetConfigurationJson(), config)
	if err != nil {
		return p, err
	}
	version := imageImportPlanV1
	if runtime != nil {
		version = imageImportPlanV11
	}
	if p.Version != version || !reflect.DeepEqual(p.ContainerRuntime, runtime) || p.InvocationID != r.GetContext().GetInvocationId() || p.TargetID != r.GetTarget().GetTargetId() || p.Fingerprint != r.GetTarget().GetConfigFingerprint() || p.Node != config.DestinationNode || p.ArtifactDigest != r.GetArtifact().GetDigest() || p.ArtifactBytes != r.GetArtifact().GetSize() || p.Provenance != r.ProvenanceDigest || r.Architecture != "amd64" || r.GetArtifact().GetMediaType() != qcowArtifactMedia || !sha256Pattern.MatchString("sha256:"+p.ArtifactDigest) || !sha256Pattern.MatchString("sha256:"+p.Provenance) || p.ArtifactBytes == 0 || p.ArtifactBytes > config.ImageImport.MaximumArtifactBytes {
		return p, fmt.Errorf("import plan binding mismatch")
	}
	valid := false
	for _, v := range config.ImageImport.VMIDRanges {
		if p.VMID >= v.Start && p.VMID <= v.End {
			valid = true
		}
	}
	if !valid || len(w.Claims) != 1 || w.Claims[0].Namespace != vmidClaimNamespace || w.Claims[0].Value != strconv.FormatUint(uint64(p.VMID), 10) {
		return p, fmt.Errorf("import reservation mismatch")
	}
	return p, nil
}
func imageOwnership(p importPlanDocument, config TargetConfig) string {
	raw, _ := json.Marshal(struct {
		Installation string
		Plan         importPlanDocument
	}{config.InstallationID, p})
	h := sha256.Sum256(raw)
	return fmt.Sprintf("LCIMAGE1:%x", h)
}
func imageFilename(p importPlanDocument) string {
	return "lc-image-" + bareDigest([]byte(p.TargetID+"\x00"+p.InvocationID+"\x00"+p.ArtifactDigest)) + ".qcow2"
}
func imageAlias(plan importPlanDocument) string {
	raw, _ := json.Marshal(plan)
	return "img-" + bareDigest(raw)[:56]
}

func importRuntimeConfiguration(data []byte, config TargetConfig) (*containervm.PackagedImage, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var runtime containervm.PackagedImage
	if decodeEnvelope(data, 4096, &runtime) != nil || runtime.Validate() != nil {
		return nil, fmt.Errorf("invalid runtime evidence")
	}
	if !validRuntimeEvidence(config, &runtime) {
		return nil, fmt.Errorf("runtime image does not match qualified pins")
	}
	return &runtime, nil
}
func validRuntimeEvidence(config TargetConfig, runtime *containervm.PackagedImage) bool {
	if runtime == nil {
		return true
	}
	return config.ImageImport != nil && config.ImageImport.ContainerRuntime != nil && runtime.Validate() == nil && runtime.BaseSHA256 == config.ImageImport.ContainerRuntime.BaseSHA256 && runtime.AgentSHA256 == config.ImageImport.ContainerRuntime.AgentSHA256
}
