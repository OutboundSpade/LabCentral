package controlplane

import (
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"testing"
)

func TestAccessResolutionRequiresUnambiguousCompatibleBinding(t *testing.T) {
	a := validAccessContractBinding(&accessContractClient{})
	b := validAccessContractBinding(&accessContractClient{})
	b.PluginInstance = "other.access"
	registry, err := NewAccessRegistry([]AccessBinding{a, b})
	if err != nil {
		t.Fatal(err)
	}
	schema := application.ProviderSchema{Name: "proxmox.vnc-endpoint", Major: 1}
	if _, err = registry.Resolve("vnc", "target.a", "VM", schema); err == nil {
		t.Fatal("ambiguous adapter accepted")
	}
	b.TargetIDs = []string{"target.b"}
	a.TargetIDs = []string{"target.a"}
	registry, err = NewAccessRegistry([]AccessBinding{a, b})
	if err != nil {
		t.Fatal(err)
	}
	b.TargetIDs[0] = "target.a" // Registration owns its input, including nested slices.
	got, err := registry.Resolve("vnc", "target.a", "Container", schema)
	if err != nil || got.PluginInstance != a.PluginInstance {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, kind := range []string{"Network", "Volume"} {
		if _, err := registry.Resolve("vnc", "target.a", kind, schema); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
}

func TestAccessDescriptorsCannotBroadenSchemaOrEnforcement(t *testing.T) {
	base := validAccessContractBinding(&accessContractClient{})
	base.Methods = []string{"fixture.network"}
	base.Features = append(base.Features, "access-credential-delivery-v1", "access-enforcement-inspection-v1", "access-issuance-recovery-v1")
	descriptor := domain.AccessDescriptor{Method: "fixture.network", Title: "Lab network", TargetKinds: []string{"Network"}, Presentation: "credential-download", MaximumOfflineSeconds: 30, EndpointSchemas: []domain.AccessEndpointSchema{{Name: "proxmox.vnc-endpoint", Major: 1}}}
	base.Descriptors = []domain.AccessDescriptor{descriptor}
	if _, err := NewAccessRegistry([]AccessBinding{base}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.AccessDescriptor){
		func(d *domain.AccessDescriptor) { d.MaximumOfflineSeconds = 31 },
		func(d *domain.AccessDescriptor) { d.Method = "unqualified" },
		func(d *domain.AccessDescriptor) { d.Presentation = "arbitrary-policy" },
		func(d *domain.AccessDescriptor) {
			d.EndpointSchemas = []domain.AccessEndpointSchema{{Name: "forged.endpoint", Major: 1}}
		},
	} {
		changed := descriptor
		change(&changed)
		base.Descriptors = []domain.AccessDescriptor{changed}
		if _, err := NewAccessRegistry([]AccessBinding{base}); err == nil {
			t.Fatal("forged descriptor accepted")
		}
	}
}
