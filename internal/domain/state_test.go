package domain

import "testing"

func TestInstanceTransitions(t *testing.T) {
	t.Parallel()
	valid := [][2]InstancePhase{
		{InstancePending, InstanceValidating},
		{InstanceApplying, InstanceDegraded},
		{InstanceReady, InstanceDestroying},
		{InstanceDestroying, InstanceDestroyed},
	}
	for _, transition := range valid {
		if err := ValidateInstanceTransition(transition[0], transition[1]); err != nil {
			t.Errorf("valid transition %s -> %s rejected: %v", transition[0], transition[1], err)
		}
	}
	invalid := [][2]InstancePhase{
		{InstancePending, InstanceReady},
		{InstanceDestroyed, InstanceApplying},
		{InstanceFailed, InstanceReady},
	}
	for _, transition := range invalid {
		if err := ValidateInstanceTransition(transition[0], transition[1]); err == nil {
			t.Errorf("invalid transition %s -> %s accepted", transition[0], transition[1])
		}
	}
}

func TestResourceCleanupTransitions(t *testing.T) {
	t.Parallel()
	for _, transition := range [][2]ResourcePhase{
		{ResourceUnknown, ResourceDestroying},
		{ResourceDestroying, ResourceCleanupFailed},
		{ResourceCleanupFailed, ResourceDestroying},
	} {
		if err := ValidateResourceTransition(transition[0], transition[1]); err != nil {
			t.Errorf("valid cleanup transition %s -> %s rejected: %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateResourceTransition(ResourceDestroyed, ResourceApplying); err == nil {
		t.Fatal("destroyed resource was resurrected")
	}
}

func TestOperationTerminalStates(t *testing.T) {
	t.Parallel()
	for _, state := range []OperationState{OperationSucceeded, OperationFailed, OperationCanceled, OperationTimedOut, OperationPartial} {
		if !state.Terminal() {
			t.Errorf("%s should be terminal", state)
		}
	}
	for _, state := range []OperationState{OperationQueued, OperationRunning, OperationCancelRequested, OperationCompensating} {
		if state.Terminal() {
			t.Errorf("%s should not be terminal", state)
		}
	}
	if err := ValidateOperationTransition(OperationSucceeded, OperationRunning); err == nil {
		t.Fatal("terminal operation transitioned back to running")
	}
}
