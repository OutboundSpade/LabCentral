package hosted

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"testing"
)

func loadBudgetProvider(t *testing.T, s *Service, p lc.Provider) lc.Provider {
	t.Helper()
	wrapped, err := NewBudgetedProvider(p, AggregateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.sshSlots = wrapped.sshSlots
	return wrapped
}
