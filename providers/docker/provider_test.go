package docker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type memoryEngine struct {
	networks   map[string]memoryObject
	volumes    map[string]memoryObject
	containers map[string]memoryObject
	removed    []string
}

type memoryObject struct {
	id     string
	labels map[string]string
}

func newMemoryEngine() *memoryEngine {
	return &memoryEngine{
		networks: make(map[string]memoryObject), volumes: make(map[string]memoryObject), containers: make(map[string]memoryObject),
	}
}

func (*memoryEngine) Discover(context.Context) (TargetInfo, error) {
	return TargetInfo{APIVersion: "1.55", EngineVersion: "29.7.2", OSType: "linux", Architecture: "amd64", OperatingSystem: "test"}, nil
}

func (*memoryEngine) EnsureImage(context.Context, string) error { return nil }

func (e *memoryEngine) EnsureNetwork(_ context.Context, desired NetworkCreate) (string, error) {
	if desired.Internal {
		return "", io.ErrUnexpectedEOF
	}
	id := "network-id"
	e.networks[id] = memoryObject{id: id, labels: cloneLabels(desired.Labels)}
	return id, nil
}

func (e *memoryEngine) RemoveNetwork(_ context.Context, id string, labels map[string]string) error {
	object := e.networks[id]
	if err := verifyLabels(object.labels, labels); err != nil {
		return err
	}
	delete(e.networks, id)
	e.removed = append(e.removed, id)
	return nil
}

func (e *memoryEngine) EnsureVolume(_ context.Context, desired VolumeCreate) (string, error) {
	e.volumes[desired.Name] = memoryObject{id: desired.Name, labels: cloneLabels(desired.Labels)}
	return desired.Name, nil
}

func (e *memoryEngine) RemoveVolume(_ context.Context, id string, labels map[string]string) error {
	object := e.volumes[id]
	if err := verifyLabels(object.labels, labels); err != nil {
		return err
	}
	delete(e.volumes, id)
	e.removed = append(e.removed, id)
	return nil
}

func (e *memoryEngine) EnsureContainer(_ context.Context, desired ContainerCreate) (ContainerObservation, error) {
	id := "container-id"
	e.containers[id] = memoryObject{id: id, labels: cloneLabels(desired.Labels)}
	return ContainerObservation{ID: id, Running: true, Labels: cloneLabels(desired.Labels), Ports: map[string]uint16{"8080/tcp": 49152}}, nil
}

func (e *memoryEngine) InspectContainer(_ context.Context, id string) (ContainerObservation, error) {
	object, ok := e.containers[id]
	if !ok {
		return ContainerObservation{}, errNotFound
	}
	return ContainerObservation{ID: id, Running: true, Labels: cloneLabels(object.labels), Ports: map[string]uint16{"8080/tcp": 49152}}, nil
}

func (e *memoryEngine) RemoveContainer(_ context.Context, id string, labels map[string]string) error {
	object := e.containers[id]
	if err := verifyLabels(object.labels, labels); err != nil {
		return err
	}
	delete(e.containers, id)
	e.removed = append(e.removed, id)
	return nil
}

func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func TestValidateLabRequiresExplicitIsolationAcknowledgement(t *testing.T) {
	client, closeClient := providerClient(t, newMemoryEngine())
	defer closeClient()
	lab := []byte(`{"apiVersion":"canonical.labcentral.io/v1alpha1","requirements":{"isolationClass":"process","capabilities":["network.segment.isolated","network.portPublication.host","workload.container.native"]},"resources":[{"id":"network.app","kind":"Network","spec":{"kind":"isolated","ipam":{"mode":"providerManaged"},"egress":{"mode":"deny"}}}]}`)

	withoutAck, err := client.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{CanonicalLabJson: lab, ProviderOverrideJson: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutAck.Errors) != 1 || withoutAck.Errors[0].Code != "docker_isolation_acknowledgement_required" {
		t.Fatalf("errors = %#v", withoutAck.Errors)
	}
	withAck, err := client.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{CanonicalLabJson: lab, ProviderOverrideJson: []byte(`{"trustedLaptopIsolationAccepted":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(withAck.Errors) != 0 || len(withAck.Warnings) != 1 || withAck.Warnings[0].Code != warningCode {
		t.Fatalf("validation = errors %#v warnings %#v", withAck.Errors, withAck.Warnings)
	}
}

func TestApplyReplayAndOwnershipFencedDestroy(t *testing.T) {
	engine := newMemoryEngine()
	client, closeClient := providerClient(t, engine)
	defer closeClient()
	input := networkMutationInput()

	first := receiveApplyResult(t, client, input)
	if first.GetError() != nil || first.GetProviderState() == nil {
		t.Fatalf("apply result = %#v", first)
	}
	second := receiveApplyResult(t, client, input)
	if string(first.GetProviderState().GetDigest()) != string(second.GetProviderState().GetDigest()) || len(engine.networks) != 1 {
		t.Fatalf("idempotent replay changed provider state")
	}

	wrong := cloneMutationInput(t, input)
	wrong.Context.InvocationId = "inv_destroy_wrong"
	wrong.Context.IdempotencyKey = "destroy-wrong-key"
	wrong.PriorState = first.ProviderState
	wrong.OwnershipTokens = []string{"wrong-token"}
	failed := receiveDestroyResult(t, client, wrong)
	if failed.GetError().GetCode() != "provider_state_ownership_mismatch" || len(engine.networks) != 1 {
		t.Fatalf("ownership mismatch did not fence delete: %#v", failed)
	}

	destroy := cloneMutationInput(t, input)
	destroy.Context.InvocationId = "inv_destroy_network"
	destroy.Context.IdempotencyKey = "destroy-network-key"
	destroy.PriorState = first.ProviderState
	result := receiveDestroyResult(t, client, destroy)
	if result.GetError() != nil || len(engine.networks) != 0 || len(engine.removed) != 1 {
		t.Fatalf("destroy result = %#v removed=%v", result, engine.removed)
	}
}

func networkMutationInput() *pluginv1.ResourceMutationInput {
	return &pluginv1.ResourceMutationInput{
		Context: &pluginv1.CallContext{
			RequestId: "req_network", OperationId: "op_network", InvocationId: "inv_network",
			LabInstanceId: "inst_example", LogicalResourceId: "network.app", ProviderTargetId: "target_docker",
			IdempotencyKey: "apply-network-key", DesiredGeneration: 1, FencingToken: 1,
		},
		CanonicalResourceJson: []byte(`{"id":"network.app","kind":"Network","spec":{"egress":{"mode":"deny"},"ipam":{"mode":"providerManaged"},"kind":"isolated"}}`),
		OwnershipTokens:       []string{"ownership-network"},
	}
}

func cloneMutationInput(t *testing.T, input *pluginv1.ResourceMutationInput) *pluginv1.ResourceMutationInput {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginv1.ResourceMutationInput
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func receiveApplyResult(t *testing.T, client pluginv1.ProviderServiceClient, input *pluginv1.ResourceMutationInput) *pluginv1.ResourceResult {
	t.Helper()
	stream, err := client.ApplyResource(context.Background(), &pluginv1.ApplyResourceRequest{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var result *pluginv1.ResourceResult
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if message.GetEvent().GetResult() != nil {
			result = message.GetEvent().GetResult()
		}
	}
	if result == nil {
		t.Fatal("apply stream did not return a result")
	}
	return result
}

func receiveDestroyResult(t *testing.T, client pluginv1.ProviderServiceClient, input *pluginv1.ResourceMutationInput) *pluginv1.ResourceResult {
	t.Helper()
	stream, err := client.DestroyResource(context.Background(), &pluginv1.DestroyResourceRequest{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var result *pluginv1.ResourceResult
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if message.GetEvent().GetResult() != nil {
			result = message.GetEvent().GetResult()
		}
	}
	if result == nil {
		t.Fatal("destroy stream did not return a result")
	}
	return result
}

func providerClient(t *testing.T, engine Engine) (pluginv1.ProviderServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	service, err := NewService(engine)
	if err != nil {
		t.Fatal(err)
	}
	pluginv1.RegisterProviderServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient(
		"passthrough:///docker-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pluginv1.NewProviderServiceClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}
