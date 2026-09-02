package contract

import "testing"

func TestAllocationFeaturesMustBothBeAdvertised(t *testing.T) {
	features := []string{FeatureOpaqueAllocationClaimsV1, FeatureDependencyResourceInputV1}
	if !SupportsFeature(features, FeatureOpaqueAllocationClaimsV1) || !SupportsFeature(features, FeatureDependencyResourceInputV1) {
		t.Fatal("required allocation protocol feature was not found")
	}
	if SupportsFeature(features, "future-feature") {
		t.Fatal("unknown feature was accepted")
	}
}
