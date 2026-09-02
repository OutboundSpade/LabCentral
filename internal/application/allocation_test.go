package application

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestAllocationContract013DeterministicBoundedReplan(t *testing.T) {
	repository := &allocationRepositoryStub{occupied: map[string]bool{"401": true, "402": true}}
	seenExclusions := make([][]AllocationKey, 0)
	planner := func(_ context.Context, excluded []AllocationKey) (AllocationProposal, error) {
		seenExclusions = append(seenExclusions, append([]AllocationKey(nil), excluded...))
		excludedValues := make(map[string]bool, len(excluded))
		for _, key := range excluded {
			excludedValues[key.Value] = true
		}
		for _, value := range []string{"401", "402", "403"} {
			if excludedValues[value] {
				continue
			}
			return allocationProposal(value), nil
		}
		return AllocationProposal{}, fmt.Errorf("no candidate")
	}
	proposal, err := ReserveAllocationPlan(context.Background(), repository, planner)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Claims[0].Value != "403" || repository.reserved != "403" {
		t.Fatalf("reserved proposal = %#v, repository value %q", proposal, repository.reserved)
	}
	want := [][]AllocationKey{
		nil,
		{{TargetID: "target-a", Namespace: "proxmox.qemu.vmid", Value: "401"}},
		{
			{TargetID: "target-a", Namespace: "proxmox.qemu.vmid", Value: "401"},
			{TargetID: "target-a", Namespace: "proxmox.qemu.vmid", Value: "402"},
		},
	}
	if !reflect.DeepEqual(seenExclusions, want) {
		t.Fatalf("exclusion sequence = %#v, want %#v", seenExclusions, want)
	}

	exhaustedRepository := &allocationRepositoryStub{alwaysConflict: true}
	calls := 0
	_, err = ReserveAllocationPlan(context.Background(), exhaustedRepository, func(_ context.Context, excluded []AllocationKey) (AllocationProposal, error) {
		calls++
		return allocationProposal(fmt.Sprintf("%d", 500+len(excluded))), nil
	})
	if err == nil || calls != MaximumAllocationPlanAttempts {
		t.Fatalf("bounded replan = %d calls, %v", calls, err)
	}
}

func allocationProposal(value string) AllocationProposal {
	return AllocationProposal{
		Binding: AllocationBinding{InstanceID: "instance-a", ResourceID: "vm.router", PlanDigest: domain.SHA256Digest(fmt.Sprintf("%064s", value))},
		Claims:  []AllocationKey{{TargetID: "target-a", Namespace: "proxmox.qemu.vmid", Value: value}},
	}
}

type allocationRepositoryStub struct {
	occupied       map[string]bool
	alwaysConflict bool
	reserved       string
}

func (repository *allocationRepositoryStub) ReserveClaims(_ context.Context, request ReserveAllocationClaimsRequest) error {
	key := request.Claims[0]
	if repository.alwaysConflict || repository.occupied[key.Value] {
		return &AllocationConflictError{Conflicts: []AllocationKey{key}}
	}
	repository.reserved = key.Value
	return nil
}

func (*allocationRepositoryStub) CommitClaims(context.Context, MutateAllocationClaimsRequest) error {
	return nil
}
func (*allocationRepositoryStub) RetainClaims(context.Context, MutateAllocationClaimsRequest) error {
	return nil
}
func (*allocationRepositoryStub) OrphanClaims(context.Context, MutateAllocationClaimsRequest) error {
	return nil
}
func (*allocationRepositoryStub) ReleaseClaims(context.Context, ReleaseAllocationClaimsRequest) error {
	return nil
}
func (*allocationRepositoryStub) ListClaims(context.Context, domain.InstanceID) ([]AllocationClaim, error) {
	return nil, nil
}
