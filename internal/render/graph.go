package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func BuildResourceGraph(resources []domain.LogicalResource) (domain.ResourceGraph, error) {
	byID := make(map[domain.LogicalResourceID]domain.LogicalResource, len(resources))
	indegree := make(map[domain.LogicalResourceID]int, len(resources))
	dependents := make(map[domain.LogicalResourceID][]domain.LogicalResourceID, len(resources))
	for _, resource := range resources {
		if resource == nil {
			return domain.ResourceGraph{}, renderError(StageGraph, "nil_resource", "", "resource graph contains a nil resource", nil)
		}
		id := resource.ID()
		if _, duplicate := byID[id]; duplicate {
			return domain.ResourceGraph{}, renderError(StageGraph, "duplicate_resource_id", string(id), "resource ID is repeated", nil)
		}
		byID[id] = resource
		indegree[id] = len(resource.Dependencies())
	}
	for _, resource := range resources {
		for _, dependency := range resource.Dependencies() {
			if _, exists := byID[dependency]; !exists {
				return domain.ResourceGraph{}, renderError(
					StageReferenceResolution,
					"unresolved_reference",
					string(resource.ID()),
					fmt.Sprintf("dependency %q does not exist", dependency),
					nil,
				)
			}
			dependents[dependency] = append(dependents[dependency], resource.ID())
		}
	}
	ready := make([]domain.LogicalResourceID, 0)
	for id, count := range indegree {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sortReadyResources(ready, byID)
	applyOrder := make([]domain.LogicalResourceID, 0, len(resources))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		applyOrder = append(applyOrder, current)
		children := append([]domain.LogicalResourceID(nil), dependents[current]...)
		sortResourceIDs(children)
		for _, child := range children {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sortReadyResources(ready, byID)
			}
		}
	}
	if len(applyOrder) != len(resources) {
		cycle := findDependencyCycle(byID)
		parts := make([]string, len(cycle))
		for index := range cycle {
			parts[index] = string(cycle[index])
		}
		return domain.ResourceGraph{}, renderError(StageGraph, "dependency_cycle", strings.Join(parts, " -> "), "resource dependency graph contains a cycle", nil)
	}
	destroyOrder := append([]domain.LogicalResourceID(nil), applyOrder...)
	for left, right := 0, len(destroyOrder)-1; left < right; left, right = left+1, right-1 {
		destroyOrder[left], destroyOrder[right] = destroyOrder[right], destroyOrder[left]
	}
	orderedResources := make([]domain.LogicalResource, len(applyOrder))
	for index, id := range applyOrder {
		orderedResources[index] = byID[id]
	}
	return domain.ResourceGraph{
		Resources:    orderedResources,
		ApplyOrder:   applyOrder,
		DestroyOrder: destroyOrder,
	}, nil
}

func sortResourceIDs(ids []domain.LogicalResourceID) {
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
}

func sortReadyResources(ids []domain.LogicalResourceID, resources map[domain.LogicalResourceID]domain.LogicalResource) {
	sort.Slice(ids, func(left, right int) bool {
		leftPriority := resourceKindPriority(resources[ids[left]].Kind())
		rightPriority := resourceKindPriority(resources[ids[right]].Kind())
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return ids[left] < ids[right]
	})
}

func resourceKindPriority(kind domain.ResourceKind) int {
	switch kind {
	case domain.ResourceNetwork:
		return 0
	case domain.ResourceVolume:
		return 1
	case domain.ResourceVM, domain.ResourceContainer:
		return 2
	case domain.ResourceAccessEndpoint:
		return 3
	case domain.ResourceOutput:
		return 4
	default:
		return 5
	}
}

func findDependencyCycle(resources map[domain.LogicalResourceID]domain.LogicalResource) []domain.LogicalResourceID {
	ids := make([]domain.LogicalResourceID, 0, len(resources))
	for id := range resources {
		ids = append(ids, id)
	}
	sortResourceIDs(ids)
	colors := make(map[domain.LogicalResourceID]uint8, len(resources))
	stack := make([]domain.LogicalResourceID, 0, len(resources))
	positions := make(map[domain.LogicalResourceID]int, len(resources))
	var cycle []domain.LogicalResourceID
	var visit func(domain.LogicalResourceID) bool
	visit = func(id domain.LogicalResourceID) bool {
		colors[id] = 1
		positions[id] = len(stack)
		stack = append(stack, id)
		dependencies := append([]domain.LogicalResourceID(nil), resources[id].Dependencies()...)
		sortResourceIDs(dependencies)
		for _, dependency := range dependencies {
			switch colors[dependency] {
			case 0:
				if visit(dependency) {
					return true
				}
			case 1:
				start := positions[dependency]
				cycle = append(cycle, stack[start:]...)
				cycle = append(cycle, dependency)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		delete(positions, id)
		colors[id] = 2
		return false
	}
	for _, id := range ids {
		if colors[id] == 0 && visit(id) {
			return cycle
		}
	}
	return nil
}
