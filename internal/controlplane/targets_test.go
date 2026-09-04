package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type targetTestClient struct{ pluginv1.ProviderServiceClient }

type targetTestSecrets struct{ value []byte }

func (resolver *targetTestSecrets) Resolve(_ context.Context, request application.AuthorizedSecretRef, scope application.InvocationScope) (*application.EphemeralSecret, error) {
	if request.SubjectID != LocalAdminID || request.Reference.Source != "file" || scope.InvocationID == "" {
		return nil, fmt.Errorf("invalid secret request")
	}
	return application.NewEphemeralSecret(resolver.value), nil
}

func TestTargetRegistryCopiesConfigurationAndDeliversScopedCredential(t *testing.T) {
	configuration := []byte(`{"endpoint":"https://pve.example:8006"}`)
	credential := []byte(`{"tokenID":"labcentral@pve!target","tokenSecret":"canary"}`)
	resolver := &targetTestSecrets{value: credential}
	registry, err := NewTargetRegistry([]TargetBinding{{
		ID: "target.proxmox", ProviderKind: "proxmox", Provider: providergrpc.New(targetTestClient{}), ConfigEnvelope: configuration,
		CredentialRef: &domain.SecretRef{Source: "file", Name: "/run/secrets/proxmox", Key: "value"}, CredentialResolver: resolver,
		ProtocolMajor: 1, ProtocolMinor: 1, Features: []string{"dependency-resource-input-v1", "opaque-allocation-claims-v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	configuration[0] = 'x'
	scope := application.InvocationScope{InvocationID: "inv_test", InstanceID: "inst_test", ResourceID: "vm.test", ExpiresAt: time.Now().Add(time.Minute)}
	session, err := registry.Acquire(context.Background(), "target.proxmox", scope, true)
	if err != nil {
		t.Fatal(err)
	}
	if session.ProviderKind != "proxmox" || string(session.Context.Config) != `{"endpoint":"https://pve.example:8006"}` || string(session.Context.Credential) != string(credential) || !session.Supports(1, "dependency-resource-input-v1", "opaque-allocation-claims-v1") {
		t.Fatalf("unexpected target session: %#v", session)
	}
	borrowed := session.Context.Credential
	session.Close()
	if session.Context.Credential != nil || strings.Trim(string(borrowed), "\x00") != "" {
		t.Fatal("target session did not clear its credential envelope")
	}
	if strings.Contains(fmt.Sprintf("%#v", application.NewEphemeralSecret(credential)), "canary") {
		t.Fatal("ephemeral credential formatting leaked its value")
	}
}

func TestTargetRegistryRejectsDuplicatesAndInvalidProtocol(t *testing.T) {
	client := targetTestClient{}
	for name, bindings := range map[string][]TargetBinding{
		"duplicate": {{ID: "target.one", ProviderKind: "docker", Provider: providergrpc.New(client), ProtocolMajor: 1}, {ID: "target.one", ProviderKind: "docker", Provider: providergrpc.New(client), ProtocolMajor: 1}},
		"protocol":  {{ID: "target.one", ProviderKind: "docker", Provider: providergrpc.New(client), ProtocolMajor: 2}},
		"pair":      {{ID: "target.one", ProviderKind: "docker", Provider: providergrpc.New(client), ProtocolMajor: 1, CredentialRef: &domain.SecretRef{Source: "file", Name: "/secret", Key: "value"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTargetRegistry(bindings); err == nil {
				t.Fatal("invalid target binding was accepted")
			}
		})
	}
}

var _ pluginv1.ProviderServiceClient = targetTestClient{}

func TestTargetPolicyIsImmutableAcrossSessions(t *testing.T) {
	policy := TargetPolicy{RequiredFeatures: []string{"feature.one"}, NativeCapabilities: []string{"workload.vm"}}
	registry, err := NewTargetRegistry([]TargetBinding{{ID: "target.policy", ProviderKind: "external", Provider: providergrpc.New(targetTestClient{}), ProtocolMajor: 1, Policy: policy}})
	if err != nil {
		t.Fatal(err)
	}
	policy.RequiredFeatures[0] = "feature.other"
	first, err := registry.Acquire(context.Background(), "target.policy", application.InvocationScope{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if first.Policy.RequiredFeatures[0] != "feature.one" {
		t.Fatal("caller mutated stored policy")
	}
	first.Policy.NativeCapabilities[0] = "workload.container"
	second, err := registry.Acquire(context.Background(), "target.policy", application.InvocationScope{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Policy.NativeCapabilities[0] != "workload.vm" {
		t.Fatal("session mutated stored policy")
	}
}
