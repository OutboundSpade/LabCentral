package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// This opt-in qualification harness uses only explicitly confirmed, initially
// absent VMIDs. The package-private test constructor cannot authorize production.
func TestCredentialedProxmoxT3ImageImport(t *testing.T) {
	prefix := "LABCENTRAL_PROXMOX_T3_IMAGE_"
	if os.Getenv(prefix+"EXECUTE") == "" {
		t.Skip("image import qualification not explicitly enabled")
	}
	if os.Getenv(prefix+"EXECUTE") != t3ExecuteSentinel {
		t.Fatal("invalid qualification confirmation")
	}
	targetID := os.Getenv(prefix + "CONFIRM_TARGET")
	if !logicalIDPattern.MatchString(targetID) {
		t.Fatal("exact target required")
	}
	configRaw, err := readT3File(os.Getenv(prefix+"TARGET_CONFIG"), maximumConfigBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := readT3File(os.Getenv(prefix+"CREDENTIAL_FILE"), maximumCredentialBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytesForT3(credential)
	var config TargetConfig
	if err := decodeEnvelope(configRaw, maximumConfigBytes, &config); err != nil || config.ImageImport == nil {
		t.Fatal("image import profile required", err)
	}
	vmid, err := strconv.ParseUint(os.Getenv(prefix+"VMID"), 10, 32)
	if err != nil || vmid == 0 {
		t.Fatal("exact disposable VMID required")
	}
	runID := os.Getenv(prefix + "RUN_ID")
	if !invocationIDPattern.MatchString(runID) {
		t.Fatal("unique run ID required")
	}
	artifactPath := os.Getenv(prefix + "ARTIFACT")
	f, err := os.Open(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
		t.Fatal("bounded qualification qcow2 required")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectStandaloneQCOW(bytes.NewReader(data), config.ImageImport.MaximumDiskBytes); err != nil {
		t.Fatal(err)
	}
	target := &wire.TargetContext{TargetId: targetID, ConfigEnvelope: configRaw, CredentialEnvelope: credential, ConfigFingerprint: "sha256:" + bareDigest(configRaw)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client, err := NewClientFromEnvelopes(configRaw, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if available, err := client.AdvisoryVMIDAvailable(ctx, uint32(vmid)); err != nil || !available {
		t.Fatal("qualification VMID is not absent", err)
	}
	lost := false
	provider := newT0MutationService(ClientFactoryFunc(func(c, k []byte) (DiscoveryClient, error) {
		underlying, err := NewClientFromEnvelopes(c, k)
		if err != nil {
			return nil, err
		}
		return &t3LostImageCreate{ImageImportAPI: underlying, lost: &lost}, nil
	}))
	service, err := NewImageService(provider, config.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	request := &wire.ImageImportServiceImportRequest{Context: &wire.CallContext{InvocationId: runID, OperationId: runID, ProviderTargetId: targetID, FencingToken: 1}, Target: target, Artifact: &wire.ArtifactHandle{Digest: bareDigest(data), Size: uint64(len(data)), MediaType: qcowArtifactMedia}, Architecture: "amd64", ProvenanceDigest: bareDigest([]byte(runID))}
	var exclusions []*wire.AllocationClaim
	for _, r := range config.ImageImport.VMIDRanges {
		for id := r.Start; id <= r.End; id++ {
			if uint64(id) != vmid {
				exclusions = append(exclusions, &wire.AllocationClaim{Namespace: vmidClaimNamespace, Value: strconv.FormatUint(uint64(id), 10)})
			}
			if len(exclusions) > 4096 {
				t.Fatal("qualification exclusion bound")
			}
		}
	}
	planned, err := service.Plan(ctx, &wire.ImageImportServicePlanRequest{Input: request, ExcludedAllocations: exclusions})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Plan.Claims) != 1 || planned.Plan.Claims[0].Value != strconv.FormatUint(vmid, 10) {
		t.Fatal("unexpected proposed allocation")
	}
	request.ReservedPlan = planned.Plan
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	wire.RegisterArtifactTransferServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	defer listener.Close()
	conn, err := grpc.NewClient("passthrough:///qualification", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := wire.NewArtifactTransferServiceClient(conn).Write(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(data); offset += 64 << 10 {
		end := min(offset+64<<10, len(data))
		if err = stream.Send(&wire.ArtifactTransferServiceWriteRequest{Context: request.Context, Artifact: request.Artifact, Chunk: data[offset:end]}); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	request.Artifact = receipt.Artifact
	if _, err = service.Import(ctx, request); err == nil || !lost {
		t.Fatal("accepted create response loss was not exercised", err)
	}
	service, err = NewImageService(provider, config.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	request.Context.FencingToken++
	recovered, err := service.GetInvocation(ctx, &wire.ImageImportServiceGetInvocationRequest{Context: request.Context, Target: target})
	if err != nil || recovered.GetResult().GetState() != "succeeded" {
		t.Fatal("restart recovery failed", err)
	}
	binding := recovered.Result.Binding
	if _, err = service.ValidateBinding(ctx, &wire.ImageImportServiceValidateBindingRequest{Context: request.Context, Target: target, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	again, err := service.Import(ctx, request)
	if err != nil || !proto.Equal(again.Binding, binding) {
		t.Fatal("import replay changed binding", err)
	}
	retireCall := proto.Clone(request.Context).(*wire.CallContext)
	retireCall.InvocationId = runID + ".retire"
	retired, err := service.Retire(ctx, &wire.ImageImportServiceRetireRequest{Context: retireCall, Target: target, Binding: binding})
	if err != nil || retired.GetState() != "retired" {
		t.Fatal("owned image retirement failed", err)
	}
	if available, err := client.AdvisoryVMIDAvailable(ctx, uint32(vmid)); err != nil || !available {
		t.Fatal("owned image remains after retirement", err)
	}
	encoded, _ := json.Marshal(map[string]any{"targetID": targetID, "vmid": vmid, "artifactDigest": request.Artifact.Digest, "acceptedCreateResponseLoss": lost, "restartRecovered": true, "retired": true})
	t.Logf("imageImportEvidence=%s", encoded)
}

type t3LostImageCreate struct {
	ImageImportAPI
	lost *bool
}

func (c *t3LostImageCreate) CreateImageVM(ctx context.Context, r ImageVMCreate) (UPID, error) {
	result, err := c.ImageImportAPI.CreateImageVM(ctx, r)
	if err == nil && !*c.lost {
		*c.lost = true
		return UPID{}, fmt.Errorf("qualification: accepted create response withheld")
	}
	return result, err
}
