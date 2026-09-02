package runtime

import (
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func TestSelectedProtocolMustBeInsideAdvertisedRange(t *testing.T) {
	ranges := []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 1}}
	if !supportsProtocol(ranges, &pluginv1.ProtocolVersion{Major: 1, Minor: 1}) {
		t.Fatal("advertised protocol 1.1 was rejected")
	}
	for _, selected := range []*pluginv1.ProtocolVersion{
		nil,
		{Major: 1, Minor: 2},
		{Major: 2, Minor: 0},
	} {
		if supportsProtocol(ranges, selected) {
			t.Fatalf("unadvertised protocol was accepted: %v", selected)
		}
	}
}
