package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type stubDiscoveryClient struct {
	inventory Inventory
	err       error
	closed    bool
}

func (client *stubDiscoveryClient) Discover(context.Context) (Inventory, error) {
	return client.inventory, client.err
}

func (client *stubDiscoveryClient) CloseIdleConnections() { client.closed = true }

func TestServiceProducesDeterministicExpiringEvidenceWithoutAdvertisingMutations(t *testing.T) {
	fixedTime := time.Date(2026, 9, 2, 17, 0, 0, 0, time.UTC)
	client := &stubDiscoveryClient{inventory: Inventory{
		Version: Version{Release: "9.0", Version: "pve-manager/9.0.3/abcd1234", RepoID: "abcd1234"},
		Nodes: []Node{
			{Name: "pve-b", Status: "offline", MaxCPU: 8},
			{Name: "pve-a", Status: "online", MaxCPU: 32, MaxMemoryBytes: 128 << 30, UsedMemoryBytes: 8 << 30, QEMUAPI: true},
		},
		Storages:  []Storage{{Node: "pve-a", ID: "local-zfs", Type: "zfspool", Content: []string{"images"}, Active: true, Enabled: true, TotalBytes: 1000, AvailableBytes: 700}},
		Bridges:   []Bridge{{Node: "pve-a", Name: "vmbr0", Active: true, VLANAware: true}},
		Templates: []Template{{Node: "pve-a", VMID: 9000, Name: "debian-template", Status: "stopped"}},
	}}
	service := NewServiceWithFactory(ClientFactoryFunc(func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
		if string(configEnvelope) != "config" || string(credentialEnvelope) != "secret" {
			t.Errorf("factory envelopes were not passed through")
		}
		return client, nil
	}))
	service.now = func() time.Time { return fixedTime }
	request := &pluginv1.DiscoverTargetRequest{Target: &pluginv1.TargetContext{
		TargetId: "pt_proxmox", ConfigEnvelope: []byte("config"), CredentialEnvelope: []byte("secret"), ConfigFingerprint: "sha256:" + strings.Repeat("a", 64),
	}}
	first, err := service.DiscoverTarget(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.DiscoverTarget(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	marshal := proto.MarshalOptions{Deterministic: true}
	firstBytes, _ := marshal.Marshal(first)
	secondBytes, _ := marshal.Marshal(second)
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("discovery response is not deterministic")
	}
	if !client.closed {
		t.Fatal("client idle connections were not closed")
	}
	if !first.GetDiscoveredAt().AsTime().Equal(fixedTime) || !first.GetValidUntil().AsTime().Equal(fixedTime.Add(capabilityTTL)) {
		t.Fatalf("unexpected validity window: %s - %s", first.GetDiscoveredAt(), first.GetValidUntil())
	}
	lastFeature := ""
	for _, capability := range first.GetCapabilities() {
		if capability.GetFeatureId() <= lastFeature {
			t.Fatalf("capabilities not sorted: %q after %q", capability.GetFeatureId(), lastFeature)
		}
		lastFeature = capability.GetFeatureId()
		if strings.Contains(capability.GetEvidence(), "secret") {
			t.Fatal("credential leaked into evidence")
		}
	}
	if findCapability(t, first, "workload.vm").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED {
		t.Fatal("read-only slice advertised VM mutation support")
	}
	if findCapability(t, first, "lifecycle.snapshot").GetSupport() != pluginv1.Capability_SUPPORT_UNSUPPORTED {
		t.Fatal("read-only slice inferred snapshot support")
	}
	if findCapability(t, first, "proxmox.discovery.qemu").GetSupport() != pluginv1.Capability_SUPPORT_NATIVE {
		t.Fatal("QEMU discovery evidence missing")
	}
	if findCapability(t, first, "scheduling.capacity").GetSupport() != pluginv1.Capability_SUPPORT_CONSTRAINED {
		t.Fatal("capacity should be observational/constrained")
	}
	var capacity capacityDocument
	if err := json.Unmarshal(first.GetCapacityJson(), &capacity); err != nil {
		t.Fatal(err)
	}
	if capacity.InventoryDigest == "" || len(capacity.Nodes) != 2 || len(capacity.Storages) != 1 || len(capacity.Bridges) != 1 || len(capacity.Templates) != 1 {
		t.Fatalf("capacity = %#v", capacity)
	}
	if strings.Join(capacity.Storages[0].Content, ",") != "images" || capacity.Templates[0].VMID != 9000 {
		t.Fatalf("typed discovery inventory missing: %#v", capacity)
	}
	if len(first.GetOverrideJsonSchema()) == 0 || len(first.GetOverrideSchemaDigest()) != 64 {
		t.Fatal("empty override schema contract")
	}
	if !strings.Contains(first.GetCapabilities()[0].GetEvidence(), "sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("canonical configuration fingerprint missing from evidence")
	}
}

func TestServiceDoesNotEchoMalformedFingerprint(t *testing.T) {
	client := &stubDiscoveryClient{inventory: Inventory{
		Version: Version{Release: "9.0", Version: "pve-manager/9.0", RepoID: "abcd1234"},
		Nodes:   []Node{{Name: "pve", Status: "online"}},
	}}
	service := NewServiceWithFactory(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return client, nil }))
	response, err := service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: &pluginv1.TargetContext{
		ConfigFingerprint: "attacker\ncontrolled", ConfigEnvelope: []byte("x"), CredentialEnvelope: []byte("x"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range response.GetCapabilities() {
		if strings.Contains(capability.GetEvidence(), "attacker") {
			t.Fatal("malformed configuration fingerprint was echoed")
		}
	}
}

func TestServiceRedactsFactoryAndDiscoveryErrors(t *testing.T) {
	secret := "super-secret-token"
	service := NewServiceWithFactory(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) {
		return nil, errors.New(secret)
	}))
	_, err := service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: &pluginv1.TargetContext{ConfigEnvelope: []byte("x"), CredentialEnvelope: []byte(secret)}})
	if status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), secret) {
		t.Fatalf("factory error leaked: %v", err)
	}
	client := &stubDiscoveryClient{err: errors.New(secret)}
	service = NewServiceWithFactory(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return client, nil }))
	_, err = service.DiscoverTarget(context.Background(), &pluginv1.DiscoverTargetRequest{Target: &pluginv1.TargetContext{}})
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), secret) || !client.closed {
		t.Fatalf("discovery error leaked or client not closed: %v", err)
	}
}

func TestServiceLeavesMutationExecutionAndUnrelatedRPCsUnimplemented(t *testing.T) {
	service := NewService()
	ctx := context.Background()
	checks := map[string]func() error{
		"CancelInvocation": func() error {
			_, err := service.CancelInvocation(ctx, &pluginv1.CancelInvocationRequest{})
			return err
		},
		"MigrateState": func() error {
			_, err := service.MigrateState(ctx, &pluginv1.MigrateStateRequest{})
			return err
		},
		"DiscoverAccessEndpoint": func() error {
			_, err := service.DiscoverAccessEndpoint(ctx, &pluginv1.DiscoverAccessEndpointRequest{})
			return err
		},
		"ListImages": func() error {
			_, err := service.ListImages(ctx, &pluginv1.ListImagesRequest{})
			return err
		},
		"ImportImage": func() error { return service.ImportImage(&pluginv1.ImportImageRequest{}, nil) },
	}
	for name, check := range checks {
		if code := status.Code(check()); code != codes.Unimplemented {
			t.Errorf("%s status = %s, want Unimplemented", name, code)
		}
	}
}

func TestServiceValidationAndPlanningFailClosedWithoutMutationConfiguration(t *testing.T) {
	service := NewService()
	validation, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{})
	if err != nil || len(validation.GetErrors()) != 1 || validation.GetErrors()[0].GetCode() != "invalid_target" {
		t.Fatalf("validation = %#v, err = %v", validation, err)
	}
	plan, err := service.PlanResource(context.Background(), &pluginv1.PlanResourceRequest{})
	if err != nil || plan.GetError().GetCode() != "invalid_plan_request" {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}
}

func findCapability(t *testing.T, response *pluginv1.DiscoverTargetResponse, feature string) *pluginv1.Capability {
	t.Helper()
	for _, capability := range response.GetCapabilities() {
		if capability.GetFeatureId() == feature {
			return capability
		}
	}
	t.Fatalf("capability %q not found", feature)
	return nil
}
