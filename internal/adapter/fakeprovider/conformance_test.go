package fakeprovider

import (
	"testing"

	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contracttest"
)

var factory contract.Factory = New

func TestFakeProviderConformance(t *testing.T) {
	contracttest.Run(t, factory)
}

func TestProviderContract001IdempotentApplyReplay(t *testing.T) {
	contracttest.Case001IdempotentApplyReplay(t, factory)
}

func TestProviderContract002StaleGenerationIsFenced(t *testing.T) {
	contracttest.Case002StaleGenerationIsFenced(t, factory)
}

func TestProviderContract003NameCollisionIsNotAdopted(t *testing.T) {
	contracttest.Case003NameCollisionIsNotAdopted(t, factory)
}

func TestProviderContract004CrashAfterCreateBeforeResponse(t *testing.T) {
	contracttest.Case004CrashAfterCreateBeforeResponse(t, factory)
}

func TestProviderContract005LostResponseAfterMutation(t *testing.T) {
	contracttest.Case005LostResponseAfterMutation(t, factory)
}

func TestProviderContract006PartialCreationCompensates(t *testing.T) {
	contracttest.Case006PartialCreationCompensates(t, factory)
}

func TestProviderContract007CompensationFailureReportsLeftovers(t *testing.T) {
	contracttest.Case007CompensationFailureReportsLeftovers(t, factory)
}

func TestProviderContract008DestroyContinuesAfterFailure(t *testing.T) {
	contracttest.Case008DestroyContinuesAfterFailure(t, factory)
}

func TestProviderContract009OldStateMigratesExplicitly(t *testing.T) {
	contracttest.Case009OldStateMigratesExplicitly(t, factory)
}

func TestProviderContract010UnknownStateFailsClosed(t *testing.T) {
	contracttest.Case010UnknownStateFailsClosed(t, factory)
}

func TestProviderContract011CapabilityDowngradeInvalidatesPlan(t *testing.T) {
	contracttest.Case011CapabilityDowngradeInvalidatesPlan(t, factory)
}

func TestProviderContract012CancellationBeforeUnsafeBoundary(t *testing.T) {
	contracttest.Case012CancellationBeforeUnsafeBoundary(t, factory)
}

func TestProviderContract013CancellationAfterSideEffect(t *testing.T) {
	contracttest.Case013CancellationAfterSideEffect(t, factory)
}

func TestProviderContract014SecretCanaryAbsent(t *testing.T) {
	contracttest.Case014SecretCanaryAbsent(t, factory)
}

func TestProviderContract015PlanIsDeterministic(t *testing.T) {
	contracttest.Case015PlanIsDeterministic(t, factory)
}

func TestProviderContract016RestartPreservesInvocation(t *testing.T) {
	contracttest.Case016RestartPreservesInvocation(t, factory)
}

func TestProviderContract017ReverseDependencyDestroyOrder(t *testing.T) {
	contracttest.Case017ReverseDependencyDestroyOrder(t, factory)
}

func TestProviderContract018OwnershipMismatchBlocksDestroy(t *testing.T) {
	contracttest.Case018OwnershipMismatchBlocksDestroy(t, factory)
}
