package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

const MaximumAllocationPlanAttempts = 8

type AllocationProposal struct {
	Binding AllocationBinding
	Claims  []AllocationKey
}

type AllocationPlanner func(context.Context, []AllocationKey) (AllocationProposal, error)

// ReserveAllocationPlan retries only explicit uniqueness conflicts. Exclusions
// are stable, de-duplicated, and bounded so a provider can deterministically
// select its next candidate without turning planning into an unbounded search.
func ReserveAllocationPlan(ctx context.Context, repository AllocationRepository, planner AllocationPlanner) (AllocationProposal, error) {
	if repository == nil || planner == nil {
		return AllocationProposal{}, fmt.Errorf("allocation repository and planner are required")
	}
	excluded := make([]AllocationKey, 0)
	for attempt := 0; attempt < MaximumAllocationPlanAttempts; attempt++ {
		proposal, err := planner(ctx, append([]AllocationKey(nil), excluded...))
		if err != nil {
			return AllocationProposal{}, err
		}
		if len(proposal.Claims) == 0 {
			return proposal, nil
		}
		err = repository.ReserveClaims(ctx, ReserveAllocationClaimsRequest{Binding: proposal.Binding, Claims: proposal.Claims})
		if err == nil {
			return proposal, nil
		}
		var conflict *AllocationConflictError
		if !errors.As(err, &conflict) || len(conflict.Conflicts) == 0 {
			return AllocationProposal{}, err
		}
		excluded = mergeAllocationExclusions(excluded, conflict.Conflicts)
	}
	return AllocationProposal{}, fmt.Errorf("allocation planning exhausted %d conflict attempts", MaximumAllocationPlanAttempts)
}

func mergeAllocationExclusions(existing, additions []AllocationKey) []AllocationKey {
	seen := make(map[string]bool, len(existing)+len(additions))
	result := make([]AllocationKey, 0, len(existing)+len(additions))
	for _, key := range append(append([]AllocationKey(nil), existing...), additions...) {
		identity := string(key.TargetID) + "\x00" + key.Namespace + "\x00" + key.Value
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, key)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].TargetID != result[right].TargetID {
			return result[left].TargetID < result[right].TargetID
		}
		if result[left].Namespace != result[right].Namespace {
			return result[left].Namespace < result[right].Namespace
		}
		return result[left].Value < result[right].Value
	})
	return result
}
