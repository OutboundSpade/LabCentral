package controlplane

import (
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func TestFailedResourceLeftoverClassificationIsHonest(t *testing.T) {
	failure := &pluginv1.StructuredError{Code: "provider_failure", ErrorClass: "provider_failure", SafeMessage: "Provider operation failed"}
	for _, testCase := range []struct {
		name   string
		result *pluginv1.ResourceResult
		want   bool
	}{
		{name: "no provider result", result: nil, want: false},
		{name: "prewrite failure without leftovers", result: &pluginv1.ResourceResult{Error: failure, ObservedStateJson: []byte(`{"leftovers":[]}`)}, want: false},
		{name: "reported owned leftover", result: &pluginv1.ResourceResult{Error: failure, ObservedStateJson: []byte(`{"leftovers":["qemu/101"]}`)}, want: true},
		{name: "incomplete attempted compensation", result: &pluginv1.ResourceResult{Error: failure, CompensationAttempted: true}, want: true},
		{name: "confirmed compensation overrides stale observation", result: &pluginv1.ResourceResult{Error: failure, CompensationAttempted: true, CompensationComplete: true, ObservedStateJson: []byte(`{"leftovers":["qemu/101"]}`)}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resultHasOwnedLeftovers(testCase.result); got != testCase.want {
				t.Fatalf("resultHasOwnedLeftovers() = %t, want %t", got, testCase.want)
			}
		})
	}
}
