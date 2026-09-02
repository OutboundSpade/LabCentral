package pluginhost

import (
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

func TestProtocolMinorNegotiation(t *testing.T) {
	tests := []struct {
		name   string
		ranges []*pluginv1.ProtocolVersionRange
		minor  uint32
		ok     bool
	}{
		{name: "old plugin", ranges: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 0}}, minor: 0, ok: true},
		{name: "allocation protocol", ranges: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 1}}, minor: 1, ok: true},
		{name: "future upper bound", ranges: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 1, MaxMinor: 9}}, minor: 1, ok: true},
		{name: "future only", ranges: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 2, MaxMinor: 9}}, ok: false},
		{name: "invalid range", ranges: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 1, MaxMinor: 0}}, ok: false},
		{name: "other major", ranges: []*pluginv1.ProtocolVersionRange{{Major: 2, MinMinor: 0, MaxMinor: 1}}, ok: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			selected, ok := selectProtocol(testCase.ranges)
			if ok != testCase.ok {
				t.Fatalf("negotiation ok = %t, want %t", ok, testCase.ok)
			}
			if ok && (selected.GetMajor() != 1 || selected.GetMinor() != testCase.minor) {
				t.Fatalf("selected protocol = %s, want 1.%d", selected, testCase.minor)
			}
		})
	}
}

func TestProtocolFeatureGate(t *testing.T) {
	client := &Client{
		SelectedProtocol: &pluginv1.ProtocolVersion{Major: 1, Minor: 1},
		Info: &pluginv1.PluginInfo{Features: []string{
			"opaque-allocation-claims-v1", "dependency-resource-input-v1",
		}},
	}
	if !client.SupportsFeatures(1, "opaque-allocation-claims-v1", "dependency-resource-input-v1") {
		t.Fatal("negotiated allocation features were rejected")
	}
	if client.SupportsFeatures(2, "opaque-allocation-claims-v1") {
		t.Fatal("future protocol minor was accepted")
	}
	if client.SupportsFeatures(1, "missing-feature") {
		t.Fatal("missing plugin feature was accepted")
	}
}
