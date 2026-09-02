package contract

const (
	FeatureOpaqueAllocationClaimsV1  = "opaque-allocation-claims-v1"
	FeatureDependencyResourceInputV1 = "dependency-resource-input-v1"
)

func SupportsFeature(features []string, required string) bool {
	for _, feature := range features {
		if feature == required {
			return true
		}
	}
	return false
}
