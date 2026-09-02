package application

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const MaximumAllocationClaims = 256

var (
	allocationNamespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	allocationValuePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// ValidateAllocationClaims applies the core's bounded opaque-claim contract
// and returns a stable copy suitable for persistence or protocol delivery.
func ValidateAllocationClaims(binding AllocationBinding, source []AllocationKey) ([]AllocationKey, error) {
	if _, err := domain.NewInstanceID(string(binding.InstanceID)); err != nil {
		return nil, err
	}
	if _, err := domain.NewLogicalResourceID(string(binding.ResourceID)); err != nil {
		return nil, err
	}
	if _, err := domain.ParseSHA256Digest(binding.PlanDigest.Hex()); err != nil {
		return nil, err
	}
	if len(source) == 0 || len(source) > MaximumAllocationClaims {
		return nil, fmt.Errorf("allocation claim count must be between 1 and %d", MaximumAllocationClaims)
	}
	claims := append([]AllocationKey(nil), source...)
	seen := make(map[string]bool, len(claims))
	var target domain.ProviderTargetID
	for _, key := range claims {
		if _, err := domain.NewProviderTargetID(string(key.TargetID)); err != nil {
			return nil, err
		}
		if target == "" {
			target = key.TargetID
		} else if key.TargetID != target {
			return nil, fmt.Errorf("one allocation reservation cannot span provider targets")
		}
		if !allocationNamespacePattern.MatchString(key.Namespace) {
			return nil, fmt.Errorf("allocation namespace must be 1-128 lowercase safe characters")
		}
		if !utf8.ValidString(key.Value) || !allocationValuePattern.MatchString(key.Value) || strings.TrimSpace(key.Value) != key.Value {
			return nil, fmt.Errorf("allocation value must be 1-128 bounded opaque safe characters")
		}
		identity := string(key.TargetID) + "\x00" + key.Namespace + "\x00" + key.Value
		if seen[identity] {
			return nil, fmt.Errorf("duplicate allocation claim in one reservation")
		}
		seen[identity] = true
	}
	sort.Slice(claims, func(left, right int) bool {
		if claims[left].TargetID != claims[right].TargetID {
			return claims[left].TargetID < claims[right].TargetID
		}
		if claims[left].Namespace != claims[right].Namespace {
			return claims[left].Namespace < claims[right].Namespace
		}
		return claims[left].Value < claims[right].Value
	})
	return claims, nil
}
