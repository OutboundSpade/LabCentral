package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAccessContract001UnauthorizedRequestStopsBeforePlugins(t *testing.T) {
	provider := &accessContractProvider{}
	accessClient := &accessContractClient{}
	targets, err := NewTargetRegistry([]TargetBinding{{
		ID: "target.test", ProviderKind: "proxmox", Provider: provider, ProtocolMajor: 1, ProtocolMinor: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := validAccessContractRegistry(t, accessClient)
	service := &Service{
		store: &accessContractStore{}, targets: targets, access: registry,
		authorizer: accessContractAuthorizer{deny: true}, tokenHasher: accessContractHasher{}, ids: accessContractIDs{},
	}
	_, err = service.CreateAccessSession(context.Background(), AccessSessionCommand{
		SubjectID: "user.attacker", InstanceID: "instance.victim", ResourceID: "vm.victim", Method: "vnc",
		IdempotencyKey: "access-denied",
	})
	if err == nil {
		t.Fatal("unauthorized access request was accepted")
	}
	if provider.calls != 0 || accessClient.calls != 0 || service.store.(*accessContractStore).calls != 0 {
		t.Fatalf("unauthorized request crossed a protected boundary: provider=%d access=%d store=%d", provider.calls, accessClient.calls, service.store.(*accessContractStore).calls)
	}
}

func TestAccessContract002ExactEndpointBinding(t *testing.T) {
	opaque := []byte(`{"schemaVersion":"endpoint/v1"}`)
	digest := sha256.Sum256(opaque)
	stateDigest := domain.SHA256Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("provider-state"))))
	session := AccessSessionRecord{TargetID: "target.test", InstanceID: "instance.test", ResourceID: "vm.test", Method: "vnc"}
	valid := &pluginv1.DiscoverAccessEndpointResponse{
		OpaqueConnectionData: opaque, EndpointSchema: &pluginv1.SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 1},
		EndpointDigest: fmt.Sprintf("%x", digest), TargetId: string(session.TargetID), LabInstanceId: string(session.InstanceID),
		LogicalResourceId: string(session.ResourceID), Method: session.Method, ProviderStateDigest: stateDigest.String(),
		ValidUntil: timestamppb.New(time.Now().UTC().Add(time.Minute)),
	}
	if _, err := validateDiscoveredAccessEndpoint(valid, session, stateDigest, opaque); err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}
	for name, mutate := range map[string]func(*pluginv1.DiscoverAccessEndpointResponse){
		"target":   func(value *pluginv1.DiscoverAccessEndpointResponse) { value.TargetId = "target.other" },
		"instance": func(value *pluginv1.DiscoverAccessEndpointResponse) { value.LabInstanceId = "instance.other" },
		"resource": func(value *pluginv1.DiscoverAccessEndpointResponse) { value.LogicalResourceId = "vm.other" },
		"method":   func(value *pluginv1.DiscoverAccessEndpointResponse) { value.Method = "ssh" },
		"state": func(value *pluginv1.DiscoverAccessEndpointResponse) {
			value.ProviderStateDigest = domain.SHA256Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("other")))).String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.Clone(valid).(*pluginv1.DiscoverAccessEndpointResponse)
			mutate(changed)
			if _, err := validateDiscoveredAccessEndpoint(changed, session, stateDigest, opaque); err == nil {
				t.Fatal("substituted endpoint binding was accepted")
			}
		})
	}
}

func TestAccessContract003TightestExpiryWins(t *testing.T) {
	now := time.Now().UTC()
	policy, endpoint, launch := now.Add(30*time.Minute), now.Add(2*time.Minute), now.Add(5*time.Minute)
	if got := earliestAccessExpiry(policy, endpoint, launch); !got.Equal(endpoint) {
		t.Fatalf("effective expiry = %v, want %v", got, endpoint)
	}
	if got := earliestAccessExpiry(endpoint, now.Add(time.Minute)); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("launch expiry = %v, want one-minute bound", got)
	}
}

func TestAccessContract013ProtocolAndSchemaCompatibility(t *testing.T) {
	client := &accessContractClient{}
	valid := validAccessContractBinding(client)
	for name, mutate := range map[string]func(*AccessBinding){
		"old-protocol":    func(binding *AccessBinding) { binding.ProtocolMinor = 1 },
		"missing-recheck": func(binding *AccessBinding) { binding.Features = nil },
		"missing-method":  func(binding *AccessBinding) { binding.Methods = nil },
		"missing-schema":  func(binding *AccessBinding) { binding.EndpointSchemas = nil },
	} {
		t.Run(name, func(t *testing.T) {
			binding := valid
			mutate(&binding)
			if _, err := NewAccessRegistry([]AccessBinding{binding}); err == nil {
				t.Fatal("incompatible access registration was accepted")
			}
		})
	}
	registry := validAccessContractRegistry(t, client)
	binding, err := registry.Select("vnc")
	if err != nil {
		t.Fatal(err)
	}
	if registry.Accepts(binding, &pluginv1.SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 2}) ||
		registry.Accepts(binding, &pluginv1.SchemaVersion{Name: "other.endpoint", Major: 1}) {
		t.Fatal("incompatible endpoint schema was accepted")
	}
}

func TestAccessContract015NoURLToken(t *testing.T) {
	token := "launch-token-secret-canary"
	for _, value := range []string{
		"/access/access.test?token=" + token,
		"/access/access.test#" + token,
		"https://console.example.test/access/access.test/" + token,
		"https://management.example.test/access/access.test",
	} {
		if err := validateAccessLaunchURL(value, "https://console.example.test", "access.test", token); err == nil {
			t.Fatalf("unsafe launch URL accepted: %s", value)
		}
	}
	if err := validateAccessLaunchURL("https://console.example.test/access/access.test", "https://console.example.test", "access.test", token); err != nil {
		t.Fatalf("safe token-free launch URL rejected: %v", err)
	}
}

func TestAccessContract017SSHRequiresTrustedUserCA(t *testing.T) {
	registry := validAccessContractRegistry(t, &accessContractClient{})
	if _, err := registry.Select("ssh"); !errors.Is(err, ErrAccessUnsupported) {
		t.Fatalf("SSH selection error = %v, want unsupported", err)
	}
}

func TestAccessReconciliationRetriesDurableClosure(t *testing.T) {
	client := &accessReconciliationClient{}
	registry := validAccessContractRegistry(t, client)
	store := &accessReconciliationStore{record: AccessSessionRecord{
		ID: "access.reconcile", InstanceID: "instance.test", ResourceID: "vm.test", TargetID: "target.test",
		AccessPluginInstance: "access.instance", State: domain.AccessSessionRevoked, ClosureRequired: true,
		AccessState: []byte(`{"schemaVersion":"state/v1"}`),
	}}
	service := &Service{store: store, access: registry, authorizer: accessContractAuthorizer{}, tokenHasher: accessContractHasher{}, ids: accessContractIDs{}}
	result, err := service.RunAccessReconciliation(context.Background(), 10)
	if err != nil || result.ClosuresRetried != 1 || result.ClosuresClosed != 1 || client.revokes != 1 || !store.marked {
		t.Fatalf("access reconciliation result=%#v revokes=%d marked=%v err=%v", result, client.revokes, store.marked, err)
	}
}

func validAccessContractRegistry(t *testing.T, client pluginv1.AccessServiceClient) *AccessRegistry {
	t.Helper()
	registry, err := NewAccessRegistry([]AccessBinding{validAccessContractBinding(client)})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func validAccessContractBinding(client pluginv1.AccessServiceClient) AccessBinding {
	return AccessBinding{
		PluginID: "access.novnc", PluginVersion: "1.0.0", PluginInstance: "access.instance", AccessKind: "browser-vnc",
		Audience: "https://console.example.test", Access: client, ProtocolMajor: 1, ProtocolMinor: 2,
		Features: []string{AccessAuthorizationFeature}, Methods: []string{"vnc"},
		EndpointSchemas: []*pluginv1.SchemaVersion{{Name: "proxmox.vnc-endpoint", Major: 1}},
	}
}

type accessContractProvider struct {
	pluginv1.ProviderServiceClient
	calls int
}

func (provider *accessContractProvider) DiscoverAccessEndpoint(context.Context, *pluginv1.DiscoverAccessEndpointRequest, ...grpc.CallOption) (*pluginv1.DiscoverAccessEndpointResponse, error) {
	provider.calls++
	return nil, errors.New("unexpected provider call")
}

type accessContractClient struct {
	pluginv1.AccessServiceClient
	calls int
}

type accessReconciliationClient struct {
	pluginv1.AccessServiceClient
	revokes int
}

func (client *accessReconciliationClient) RevokeAccess(context.Context, *pluginv1.RevokeAccessRequest, ...grpc.CallOption) (*pluginv1.RevokeAccessResponse, error) {
	client.revokes++
	return &pluginv1.RevokeAccessResponse{ActiveConnectionsClosed: true}, nil
}

type accessReconciliationStore struct {
	Store
	record AccessSessionRecord
	marked bool
}

func (store *accessReconciliationStore) ListAccessSessionsForReconciliation(context.Context, uint32) ([]AccessSessionRecord, error) {
	return []AccessSessionRecord{store.record}, nil
}

func (store *accessReconciliationStore) MarkAccessClosureComplete(context.Context, domain.AccessSessionID) error {
	store.marked = true
	return nil
}

type accessContractStore struct {
	Store
	calls int
}

type accessContractAuthorizer struct{ deny bool }

func (authorizer accessContractAuthorizer) Authorize(context.Context, application.AuthorizationRequest) error {
	if authorizer.deny {
		return errors.New("denied")
	}
	return nil
}
func (authorizer accessContractAuthorizer) AuthorizeInstance(context.Context, domain.UserID, application.PermissionAction, domain.InstanceID) (application.InstanceAccessRecord, error) {
	if authorizer.deny {
		return application.InstanceAccessRecord{}, errors.New("denied")
	}
	return application.InstanceAccessRecord{}, nil
}
func (accessContractAuthorizer) AuthorizeOperation(context.Context, domain.UserID, application.PermissionAction, domain.OperationID) (application.InstanceAccessRecord, error) {
	return application.InstanceAccessRecord{}, nil
}
func (accessContractAuthorizer) AuthorizeInstanceBatch(context.Context, domain.UserID, application.PermissionAction, domain.InstanceBatchID) (application.InstanceBatchAccessRecord, error) {
	return application.InstanceBatchAccessRecord{}, nil
}
func (accessContractAuthorizer) AuthorizedInstanceIDs(context.Context, domain.UserID, application.PermissionAction) ([]domain.InstanceID, error) {
	return nil, nil
}

type accessContractHasher struct{}

func (accessContractHasher) HashCurrent(string, string) application.KeyedTokenHash {
	return application.KeyedTokenHash{KeyID: "key", Digest: string(make([]byte, 64))}
}
func (accessContractHasher) HashCandidates(string, string) []application.KeyedTokenHash { return nil }

type accessContractIDs struct{}

func (accessContractIDs) New(prefix string) (string, error) { return prefix + ".test", nil }
