package controlplane

import (
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"testing"
)

func TestContainerVMReservesBothWorkloadDimensions(t *testing.T) {
	c := domain.Container{Spec: domain.ContainerSpec{Isolation: domain.IsolationVirtualMachine, RealizationPreference: []domain.ContainerRealization{domain.ContainerViaVM}}}
	amounts, err := quotaAmountsForCanonical(domain.CanonicalLab{Resources: []domain.LogicalResource{c}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[application.QuotaDimension]uint64{}
	for _, a := range amounts {
		got[a.Dimension] = a.Value
	}
	if got[application.QuotaVMs] != 1 || got[application.QuotaContainers] != 1 {
		t.Fatal(got)
	}
	c.Spec.Isolation = domain.IsolationProcess
	c.Spec.RealizationPreference = []domain.ContainerRealization{domain.ContainerNative}
	amounts, err = quotaAmountsForCanonical(domain.CanonicalLab{Resources: []domain.LogicalResource{c}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range amounts {
		if a.Dimension == application.QuotaVMs && a.Value != 0 {
			t.Fatal("native container charged a VM")
		}
	}
}
