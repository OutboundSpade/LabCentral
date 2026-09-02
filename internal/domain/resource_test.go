package domain

import "testing"

func TestResourceSpecIsClosedSum(t *testing.T) {
	t.Parallel()
	if _, err := (ResourceSpec{}).Kind(); err == nil {
		t.Fatal("empty resource spec accepted")
	}
	if _, err := (ResourceSpec{Network: &NetworkSpec{}, VM: &VMSpec{}}).Kind(); err == nil {
		t.Fatal("multi-variant resource spec accepted")
	}
	kind, err := (ResourceSpec{Network: &NetworkSpec{}}).Kind()
	if err != nil {
		t.Fatalf("single resource variant rejected: %v", err)
	}
	if kind != ResourceNetwork {
		t.Fatalf("kind = %q, want %q", kind, ResourceNetwork)
	}
}

func TestLogicalResourceOwnsCopiesOfGraphMetadata(t *testing.T) {
	t.Parallel()
	dependencies := []LogicalResourceID{"network-red"}
	required := CapabilityExpression{AllOf: []CapabilityID{"compute.vm"}}
	resource, err := NewLogicalResource(
		"target-vm",
		dependencies,
		required,
		ResourceSpec{VM: &VMSpec{
			Name:     "target",
			Image:    "targetImage:debian-12",
			OSFamily: OSLinux,
			Compute: ComputeRequirements{
				CPUMillis:    1000,
				MemoryBytes:  ByteQuantity(1024 * 1024 * 1024),
				Architecture: ArchitectureAMD64,
			},
		}},
	)
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	dependencies[0] = "mutated"
	required.AllOf[0] = "mutated"
	if got := resource.Dependencies()[0]; got != "network-red" {
		t.Fatalf("dependency mutated through caller slice: %q", got)
	}
	if got := resource.RequiredCapabilities().AllOf[0]; got != "compute.vm" {
		t.Fatalf("capability mutated through caller slice: %q", got)
	}
}

func TestLogicalResourceRejectsUnsafeGraphAndSpecValues(t *testing.T) {
	t.Parallel()
	validNetwork := ResourceSpec{Network: &NetworkSpec{
		Name:   "red",
		Kind:   NetworkIsolated,
		IPAM:   IPAMProviderManaged,
		Egress: EgressPolicy{Mode: EgressDeny},
	}}
	for name, dependencies := range map[string][]LogicalResourceID{
		"self":      {"network.red"},
		"duplicate": {"network.blue", "network.blue"},
	} {
		if _, err := NewLogicalResource("network.red", dependencies, CapabilityExpression{}, validNetwork); err == nil {
			t.Fatalf("%s dependency set accepted", name)
		}
	}
	invalidNetwork := validNetwork
	invalidNetwork.Network.Egress.Mode = ""
	if _, err := NewLogicalResource("network.red", nil, CapabilityExpression{}, invalidNetwork); err == nil {
		t.Fatal("network without explicit canonical egress mode accepted")
	}
}

func TestEnvironmentValueExcludesSecretLiteralAmbiguity(t *testing.T) {
	t.Parallel()
	literal := "safe"
	secret := SecretRef{Source: "vault", Name: "credential", Key: "password"}
	for name, value := range map[string]EnvironmentValue{
		"neither": {},
		"both":    {Literal: &literal, Secret: &secret},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("%s environment value accepted", name)
		}
	}
	if err := (EnvironmentValue{Secret: &secret}).Validate(); err != nil {
		t.Fatalf("secret reference rejected: %v", err)
	}
}
