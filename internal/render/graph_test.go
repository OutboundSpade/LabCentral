package render

import (
	"errors"
	"reflect"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestGraphBuildsStableApplyAndReverseDestroyOrder(t *testing.T) {
	t.Parallel()
	networkA := testNetwork(t, "network.a", nil)
	networkB := testNetwork(t, "network.b", nil)
	networkC := testNetwork(t, "network.c", []domain.LogicalResourceID{"network.a", "network.b"})
	graph, err := BuildResourceGraph([]domain.LogicalResource{networkC, networkB, networkA})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(graph.ApplyOrder, []domain.LogicalResourceID{"network.a", "network.b", "network.c"}) {
		t.Fatalf("apply order = %v", graph.ApplyOrder)
	}
	if !reflect.DeepEqual(graph.DestroyOrder, []domain.LogicalResourceID{"network.c", "network.b", "network.a"}) {
		t.Fatalf("destroy order = %v", graph.DestroyOrder)
	}
}

func TestRenderContract002DependencyCycle(t *testing.T) {
	t.Parallel()
	networkA := testNetwork(t, "network.a", []domain.LogicalResourceID{"network.b"})
	networkB := testNetwork(t, "network.b", []domain.LogicalResourceID{"network.a"})
	_, err := BuildResourceGraph([]domain.LogicalResource{networkA, networkB})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "dependency_cycle" || renderError.Path != "network.a -> network.b -> network.a" {
		t.Fatalf("unexpected cycle error: %v", err)
	}
}

func testNetwork(t *testing.T, id domain.LogicalResourceID, dependencies []domain.LogicalResourceID) domain.LogicalResource {
	t.Helper()
	name, err := domain.NewName("test")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := domain.NewLogicalResource(id, dependencies, domain.CapabilityExpression{}, domain.ResourceSpec{
		Network: &domain.NetworkSpec{
			Name:   name,
			Kind:   domain.NetworkIsolated,
			IPAM:   domain.IPAMProviderManaged,
			Egress: domain.EgressPolicy{Mode: domain.EgressDeny},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resource
}
