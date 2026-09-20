package proxmox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type fakeImageAPI struct {
	*fakeMutationAPI
	config                           TargetConfig
	loss                             string
	upload, create, convert, cleanup int
	tasks                            map[string]UPID
}

func (f *fakeImageAPI) EffectivePermissions(context.Context) (EffectivePermissions, error) {
	out := EffectivePermissions{Paths: map[string][]string{}, PropagatingPaths: map[string][]string{}}
	for _, scope := range f.config.PermissionScopes {
		out.Paths[scope.Path] = scope.Privileges
		out.PropagatingPaths[scope.Path] = scope.Privileges
	}
	return out, nil
}
func (f *fakeImageAPI) ImportStorage(context.Context, string, string) (bool, error) { return true, nil }
func (f *fakeImageAPI) RecoverImageTask(_ context.Context, _ string, kind string, _ uint32, _ string) (UPID, bool, error) {
	v, ok := f.tasks[kind]
	return v, ok, nil
}
func (f *fakeImageAPI) UploadImage(_ context.Context, r ImageUpload, src io.Reader) (UPID, error) {
	data, err := io.ReadAll(src)
	if err != nil || uint64(len(data)) != r.Size || bareDigest(data) != r.Digest {
		return UPID{}, fmt.Errorf("transfer mismatch")
	}
	f.upload++
	task, _ := ParseUPID("UPID:pve-a:00000001:00000002:00000003:imgcopy::root@pam:")
	f.tasks["imgcopy"] = task
	if f.loss == "upload" {
		return UPID{}, fmt.Errorf("response lost")
	}
	return task, nil
}
func (f *fakeImageAPI) CreateImageVM(_ context.Context, r ImageVMCreate) (UPID, error) {
	f.create++
	current := cloneVMConfig(f.configs[9000])
	current.Description = r.Description
	current.Digest = strings40("d")
	current.Fields["description"] = r.Description
	current.Fields["digest"] = current.Digest
	current.Fields["onboot"] = "0"
	current.Fields["ciupgrade"] = "0"
	if r.CPUModel != "" {
		current.Fields["cpu"] = r.CPUModel
	}
	current.Fields["scsi0"] = fmt.Sprintf("%s:vm-%d-disk-0,size=8G", r.Storage, r.VMID)
	f.configs[r.VMID] = current
	task := fakeUPID(r.Node, "qmcreate", r.VMID)
	f.tasks["qmcreate"] = task
	if f.loss == "create" {
		return UPID{}, fmt.Errorf("response lost")
	}
	return task, nil
}
func (f *fakeImageAPI) MakeImageTemplate(_ context.Context, node string, vmid uint32) (UPID, error) {
	f.convert++
	v := f.configs[vmid]
	v.Fields["template"] = "1"
	v.Digest = strings40("e")
	v.Fields["digest"] = v.Digest
	f.configs[vmid] = v
	task := fakeUPID(node, "qmtemplate", vmid)
	f.tasks["qmtemplate"] = task
	if f.loss == "template" {
		return UPID{}, fmt.Errorf("response lost")
	}
	return task, nil
}
func (f *fakeImageAPI) DeleteImageStaging(context.Context, string, string, string) error {
	f.cleanup++
	if f.loss == "cleanup" {
		f.loss = ""
		return fmt.Errorf("response lost")
	}
	return nil
}
func imageFixture(t *testing.T) (*ImageService, *fakeImageAPI, *wire.ImageImportServiceImportRequest, []byte) {
	t.Helper()
	target := mutationTestTarget(t)
	var config TargetConfig
	if err := json.Unmarshal(target.ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = targetConfigV14
	config.ImageImport = &ImageImportProfile{StagingStorage: "local", StorageAlias: "primary", VMIDRanges: []IntegerRange{{Start: 200, End: 210}}, MaximumArtifactBytes: 1 << 30, MaximumDiskBytes: 16 << 30}
	config.PermissionScopes = nil
	for path, privileges := range expectedPermissionScopes(config) {
		scope := PermissionScope{Path: path}
		for privilege := range privileges {
			scope.Privileges = append(scope.Privileges, privilege)
		}
		config.PermissionScopes = append(config.PermissionScopes, scope)
	}
	inventory := mutationTestInventory()
	config.Qualification.InventoryDigest, _ = inventory.MutationDigestForConfig(config)
	target.ConfigEnvelope, _ = json.Marshal(config)
	fake := &fakeImageAPI{fakeMutationAPI: newFakeMutationAPI(), config: config, tasks: map[string]UPID{}}
	provider := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return fake, nil }))
	service, err := NewImageService(provider, config.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 112)
	copy(data, "QFI\xfb")
	binary.BigEndian.PutUint32(data[4:], 3)
	binary.BigEndian.PutUint32(data[20:], 16)
	binary.BigEndian.PutUint64(data[24:], 8<<30)
	binary.BigEndian.PutUint32(data[96:], 4)
	binary.BigEndian.PutUint32(data[100:], 112)
	request := &wire.ImageImportServiceImportRequest{Context: &wire.CallContext{InvocationId: "image.test", OperationId: "operation.image", ProviderTargetId: target.TargetId, FencingToken: 1}, Target: target, Artifact: &wire.ArtifactHandle{Digest: bareDigest(data), Size: uint64(len(data)), MediaType: qcowArtifactMedia}, Architecture: "amd64", ProvenanceDigest: strings.Repeat("a", 64)}
	return service, fake, request, data
}
func uploadImageFixture(t *testing.T, s *ImageService, r *wire.ImageImportServiceImportRequest, data []byte) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	wire.RegisterArtifactTransferServiceServer(server, s)
	go server.Serve(listener)
	defer server.Stop()
	defer listener.Close()
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := wire.NewArtifactTransferServiceClient(conn).Write(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&wire.ArtifactTransferServiceWriteRequest{Context: r.Context, Artifact: r.Artifact, Chunk: data}); err != nil {
		t.Fatal(err)
	}
	receipt, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	r.Artifact = receipt.Artifact
}
func TestImageImportRecoveryAndBinding(t *testing.T) {
	for _, loss := range []string{"", "upload", "create", "template", "cleanup"} {
		t.Run("lost-"+loss, func(t *testing.T) {
			ctx := context.Background()
			s, fake, r, data := imageFixture(t)
			planned, err := s.Plan(ctx, &wire.ImageImportServicePlanRequest{Input: r, ExcludedAllocations: []*wire.AllocationClaim{{Namespace: vmidClaimNamespace, Value: "200"}}})
			if err != nil {
				t.Fatal(err)
			}
			if planned.Plan.Claims[0].Value != "201" {
				t.Fatal("ignored occupied core reservation")
			}
			r.ReservedPlan = planned.Plan
			uploadImageFixture(t, s, r, data)
			fake.loss = loss
			result, err := s.Import(ctx, r)
			if loss != "" {
				if err == nil {
					t.Fatal("missing injected response loss")
				}
				s, err = NewImageService(s.provider, fake.config.JournalDirectory)
				if err != nil {
					t.Fatal(err)
				}
				r.Context = proto.Clone(r.Context).(*wire.CallContext)
				r.Context.FencingToken++
				recovered, e := s.GetInvocation(ctx, &wire.ImageImportServiceGetInvocationRequest{Context: r.Context, Target: r.Target})
				if e != nil {
					t.Fatal(e)
				}
				result = recovered.Result
			} else if err != nil {
				t.Fatal(err)
			}
			if result.State != "succeeded" || fake.upload != 1 || fake.create != 1 || fake.convert != 1 {
				t.Fatalf("duplicate or incomplete mutation: %s %d/%d/%d", result.State, fake.upload, fake.create, fake.convert)
			}
			valid, err := s.ValidateBinding(ctx, &wire.ImageImportServiceValidateBindingRequest{Context: r.Context, Target: r.Target, Binding: result.Binding})
			if err != nil || !valid.Valid {
				t.Fatal("invalid binding", valid, err)
			}
			if _, err = os.Stat(filepath.Join(s.root, imageArtifactName(r.Context.InvocationId))); !os.IsNotExist(err) {
				t.Fatal("staging bytes retained", err)
			}
			if profile, ok := importedTemplateProfile(fake.config, strings.TrimPrefix(strings.Split(result.Binding.ImageRef, "@")[0], "labcentral.local/images/")); !ok || profile.VMID != 201 {
				t.Fatal("image profile unavailable")
			}
			// A stale worker cannot revive an old invocation after recovery.
			stale := proto.Clone(r.Context).(*wire.CallContext)
			stale.FencingToken = 0
			if _, err = s.GetInvocation(ctx, &wire.ImageImportServiceGetInvocationRequest{Context: stale, Target: r.Target}); err == nil {
				t.Fatal("accepted stale fence")
			}
			current := fake.configs[201]
			current.Description = "foreign"
			fake.configs[201] = current
			valid, err = s.ValidateBinding(ctx, &wire.ImageImportServiceValidateBindingRequest{Context: r.Context, Target: r.Target, Binding: result.Binding})
			if err != nil || valid.Valid {
				t.Fatal("accepted foreign image", err)
			}
		})
	}
}
func TestImageImportRejectsChangedArtifactAndForeignAllocation(t *testing.T) {
	ctx := context.Background()
	s, fake, r, data := imageFixture(t)
	planned, err := s.Plan(ctx, &wire.ImageImportServicePlanRequest{Input: r})
	if err != nil {
		t.Fatal(err)
	}
	r.ReservedPlan = planned.Plan
	uploadImageFixture(t, s, r, data)
	changed := proto.Clone(r).(*wire.ImageImportServiceImportRequest)
	changed.Artifact.Digest = strings.Repeat("b", 64)
	if _, err = s.Import(ctx, changed); err == nil {
		t.Fatal("accepted changed artifact")
	}
	fake.configs[200] = VMConfig{Description: "foreign"}
	if _, err = s.Import(ctx, r); err == nil || fake.upload != 0 || fake.create != 0 {
		t.Fatal("touched foreign VMID")
	}
}
func TestImageImportRejectsExternalQCOWDependencies(t *testing.T) {
	_, _, _, data := imageFixture(t)
	if size, err := inspectStandaloneQCOW(bytes.NewReader(data), 16<<30); err != nil || size != 8<<30 {
		t.Fatal(size, err)
	}
	for _, offset := range []int{8, 16, 32, 60, 64, 72, 104} {
		bad := append([]byte(nil), data...)
		bad[offset] = 1
		if _, err := inspectStandaloneQCOW(bytes.NewReader(bad), 16<<30); err == nil {
			t.Fatalf("accepted unsafe header at %d", offset)
		}
	}
	if _, err := inspectStandaloneQCOW(bytes.NewReader(data), 1<<30); err == nil {
		t.Fatal("accepted excessive virtual disk")
	}
}

func TestImageRetirementRecoveryCannotReviveOrDeleteForeignImage(t *testing.T) {
	ctx := context.Background()
	s, fake, r, data := imageFixture(t)
	plan, err := s.Plan(ctx, &wire.ImageImportServicePlanRequest{Input: r})
	if err != nil {
		t.Fatal(err)
	}
	r.ReservedPlan = plan.Plan
	uploadImageFixture(t, s, r, data)
	imported, err := s.Import(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	retireCall := proto.Clone(r.Context).(*wire.CallContext)
	retireCall.InvocationId = "retirement.test"
	retireCall.OperationId = "operation.retirement"
	retire := &wire.ImageImportServiceRetireRequest{Context: retireCall, Target: r.Target, Binding: imported.Binding}
	owned := fake.configs[200]
	foreign := cloneVMConfig(owned)
	foreign.Description = "foreign"
	fake.configs[200] = foreign
	if _, err = s.Retire(ctx, retire); err == nil || fake.deleteCalls != 0 {
		t.Fatal("deleted foreign image")
	}
	fake.configs[200] = owned
	fake.deleteErrorAfter = true
	if _, err = s.Retire(ctx, retire); err == nil || fake.deleteCalls != 1 {
		t.Fatal("expected deletion response loss", err)
	}
	s, err = NewImageService(s.provider, fake.config.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	retireCall.FencingToken++
	result, err := s.Retire(ctx, retire)
	if err != nil || result.State != "retired" || fake.deleteCalls != 1 {
		t.Fatal("retirement recovery", result, err)
	}
	if _, err = s.GetInvocation(ctx, &wire.ImageImportServiceGetInvocationRequest{Context: r.Context, Target: r.Target}); err == nil {
		t.Fatal("completed import revived retired image")
	}
	if _, err = s.Retire(ctx, retire); err != nil || fake.deleteCalls != 1 {
		t.Fatal("replayed retirement changed infrastructure", err)
	}
}

func TestImageImportBindsExplicitCPUModelAndRejectsDrift(t *testing.T) {
	s, fake, r, data := imageFixture(t)
	var config TargetConfig
	if err := json.Unmarshal(r.Target.ConfigEnvelope, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = targetConfigV15
	config.ImageImport.CPUModel = "host"
	r.Target.ConfigEnvelope, _ = json.Marshal(config)
	fake.config = config
	plan, err := s.Plan(context.Background(), &wire.ImageImportServicePlanRequest{Input: r})
	if err != nil {
		t.Fatal(err)
	}
	r.ReservedPlan = plan.Plan
	uploadImageFixture(t, s, r, data)
	imported, err := s.Import(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if fake.configs[200].Fields["cpu"] != "host" {
		t.Fatal("explicit CPU model not sent to creation")
	}
	check := &wire.ImageImportServiceValidateBindingRequest{Context: r.Context, Target: r.Target, Binding: imported.Binding}
	valid, err := s.ValidateBinding(context.Background(), check)
	if err != nil || !valid.Valid {
		t.Fatalf("qualified CPU binding rejected %v %+v", err, valid)
	}
	for _, cpu := range []string{"", "kvm64", "host,flags=+vmx"} {
		fake.configs[200].Fields["cpu"] = cpu
		valid, err = s.ValidateBinding(context.Background(), check)
		if err != nil || valid.Valid {
			t.Fatalf("CPU drift accepted %q %v %+v", cpu, err, valid)
		}
	}
}
