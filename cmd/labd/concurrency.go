package main

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"strconv"
	"strings"
)

func configureProviderConcurrency(targets []controlplane.TargetBinding, input string) error {
	if input == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, item := range strings.Split(input, ",") {
		kind, value, ok := strings.Cut(item, "=")
		maximum, err := strconv.ParseUint(value, 10, 32)
		if !ok || err != nil || maximum < 1 || maximum > 16 || seen[kind] {
			return fmt.Errorf("invalid provider concurrency; use unique provider=1..16 entries")
		}
		seen[kind] = true
		found := false
		for i := range targets {
			if targets[i].ProviderKind == kind {
				targets[i].Policy.MaxConcurrentDeployments = uint32(maximum)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("concurrency configured for unregistered provider %q", kind)
		}
	}
	return nil
}
