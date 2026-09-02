package controlplane_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/render"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	_ "modernc.org/sqlite"
)

func TestExampleAApplyAndDestroyThroughDurableWorker(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	provider, engine, closeProvider := testProvider(t)
	defer closeProvider()
	renderer := testRenderer(t)
	ids := &sequenceIDs{}
	service, err := controlplane.NewService(renderer, store, provider, ids)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := controlplane.NewWorker(store, provider, ids, controlplane.WorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateInstance(ctx, source, true, "create-example-a")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateInstance(ctx, source, true, "create-example-a")
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replay || replayed.InstanceID != created.InstanceID || replayed.OperationID != created.OperationID {
		t.Fatalf("idempotent replay = %#v, original = %#v", replayed, created)
	}
	changedSource := append(append([]byte(nil), source...), '\n')
	_, err = service.CreateInstance(ctx, changedSource, true, "create-example-a")
	var idempotencyConflict *domain.DomainError
	if !errors.As(err, &idempotencyConflict) || idempotencyConflict.Code != "idempotency_conflict" {
		t.Fatalf("changed idempotent request returned %v", err)
	}
	for table, expected := range map[string]int{
		"labcentral_local_instances": 1, "labcentral_local_resources": 6, "labcentral_jobs": 1,
		"labcentral_local_operations": 1, "labcentral_audit_intents": 1, "labcentral_job_events": 1,
		"labcentral_outbox_deliveries": 1, "labcentral_idempotency_records": 1,
	} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != expected {
			t.Fatalf("%s count = %d, want %d", table, count, expected)
		}
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	instance, resources, err := service.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Phase != domain.InstanceReady || string(instance.OutputsJSON) != `{"web-url":"http://127.0.0.1:49152/"}` {
		t.Fatalf("ready instance = phase %q outputs %s", instance.Phase, instance.OutputsJSON)
	}
	for _, resource := range resources {
		if resource.Phase != domain.ResourceReady {
			t.Fatalf("resource %s phase = %s", resource.ID, resource.Phase)
		}
	}
	operation, err := service.GetOperation(ctx, created.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("apply operation = %#v, %v", operation, err)
	}
	if engine.objectCount() != 4 {
		t.Fatalf("Docker object count after apply = %d, want 4", engine.objectCount())
	}

	destroyed, err := service.DestroyInstance(ctx, created.InstanceID, "destroy-example-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	instance, resources, err = service.GetInstance(ctx, created.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Phase != domain.InstanceDestroyed || engine.objectCount() != 0 {
		t.Fatalf("destroyed instance = phase %q, Docker objects %d", instance.Phase, engine.objectCount())
	}
	for _, resource := range resources {
		if resource.Phase != domain.ResourceDestroyed {
			t.Fatalf("resource %s phase after destroy = %s", resource.ID, resource.Phase)
		}
	}
	operation, err = service.GetOperation(ctx, destroyed.OperationID)
	if err != nil || operation.State != domain.OperationSucceeded {
		t.Fatalf("destroy operation = %#v, %v", operation, err)
	}
}

func TestExampleARejectsMissingDockerRiskAcknowledgement(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	provider, _, closeProvider := testProvider(t)
	defer closeProvider()
	service, err := controlplane.NewService(testRenderer(t), store, provider, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateInstance(ctx, source, false, "missing-ack")
	var validation *controlplane.ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) == 0 || validation.Issues[0].Code != "docker_isolation_acknowledgement_required" {
		t.Fatalf("missing acknowledgement error = %#v", err)
	}
}

func testRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	schema, err := os.ReadFile("../../schemas/lab-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewRenderer(schema, render.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

type sequenceIDs struct {
	mu   sync.Mutex
	next uint64
}

func (s *sequenceIDs) New(prefix string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("%s_%08d", prefix, s.next), nil
}

type testEngine struct {
	mu      sync.Mutex
	objects map[string]map[string]string
}

func newTestEngine() *testEngine { return &testEngine{objects: make(map[string]map[string]string)} }

func (*testEngine) Discover(context.Context) (dockerprovider.TargetInfo, error) {
	return dockerprovider.TargetInfo{APIVersion: "1.55", EngineVersion: "29.7.2", OSType: "linux", Architecture: "amd64", OperatingSystem: "test"}, nil
}

func (*testEngine) EnsureImage(context.Context, string) error { return nil }

func (e *testEngine) EnsureNetwork(_ context.Context, desired dockerprovider.NetworkCreate) (string, error) {
	if desired.Internal {
		return "", fmt.Errorf("published-port laptop network unexpectedly used Docker internal mode")
	}
	return e.ensure("network:"+desired.Name, desired.Labels), nil
}

func (e *testEngine) RemoveNetwork(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) EnsureVolume(_ context.Context, desired dockerprovider.VolumeCreate) (string, error) {
	return e.ensure("volume:"+desired.Name, desired.Labels), nil
}

func (e *testEngine) RemoveVolume(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) EnsureContainer(_ context.Context, desired dockerprovider.ContainerCreate) (dockerprovider.ContainerObservation, error) {
	if len(desired.Published) > 0 && desired.Published[0].HostIP != "127.0.0.1" {
		return dockerprovider.ContainerObservation{}, fmt.Errorf("published port was not loopback")
	}
	id := e.ensure("container:"+desired.Name, desired.Labels)
	ports := map[string]uint16{}
	for _, port := range desired.Published {
		ports[fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol)] = 49152
	}
	return dockerprovider.ContainerObservation{ID: id, Running: true, Ports: ports, Labels: desired.Labels}, nil
}

func (e *testEngine) InspectContainer(_ context.Context, id string) (dockerprovider.ContainerObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	labels, ok := e.objects[id]
	if !ok {
		return dockerprovider.ContainerObservation{}, fmt.Errorf("not found")
	}
	return dockerprovider.ContainerObservation{ID: id, Running: true, Ports: map[string]uint16{"8080/tcp": 49152}, Labels: labels}, nil
}

func (e *testEngine) RemoveContainer(_ context.Context, id string, labels map[string]string) error {
	return e.remove(id, labels)
}

func (e *testEngine) ensure(id string, labels map[string]string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	copyOf := make(map[string]string, len(labels))
	for key, value := range labels {
		copyOf[key] = value
	}
	e.objects[id] = copyOf
	return id
}

func (e *testEngine) remove(id string, expected map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	actual, ok := e.objects[id]
	if !ok {
		return nil
	}
	for key, value := range expected {
		if actual[key] != value {
			return fmt.Errorf("ownership mismatch")
		}
	}
	delete(e.objects, id)
	return nil
}

func (e *testEngine) objectCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.objects)
}

func testProvider(t *testing.T) (pluginv1.ProviderServiceClient, *testEngine, func()) {
	t.Helper()
	engine := newTestEngine()
	service, err := dockerprovider.NewService(engine)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginv1.RegisterProviderServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient(
		"passthrough:///controlplane-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pluginv1.NewProviderServiceClient(connection), engine, func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}
