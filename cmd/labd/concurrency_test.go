package main

import (
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"testing"
)

func TestConfigureProviderConcurrency(t *testing.T) {
	for _, input := range []string{"docker=0", "docker=17", "docker=x", "docker", "docker=2,docker=3", "missing=2", "docker=2,"} {
		t.Run(input, func(t *testing.T) {
			targets := []controlplane.TargetBinding{{ProviderKind: "docker"}}
			if configureProviderConcurrency(targets, input) == nil {
				t.Fatal("accepted invalid limit")
			}
		})
	}
	targets := []controlplane.TargetBinding{{ProviderKind: "docker"}, {ProviderKind: "proxmox"}, {ProviderKind: "proxmox"}}
	if err := configureProviderConcurrency(targets, "proxmox=2"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint32{0, 2, 2} {
		if targets[i].Policy.MaxConcurrentDeployments != want {
			t.Fatalf("target %d policy: %+v", i, targets[i].Policy)
		}
	}
	if err := configureProviderConcurrency(targets, ""); err != nil {
		t.Fatal(err)
	}
}
