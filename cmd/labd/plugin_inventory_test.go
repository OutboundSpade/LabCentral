package main

import (
	"testing"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
)

func TestInventoryCombinesConfigurationFormatsAndLiveAvailability(t *testing.T) {
	client := &pluginhost.Client{Info: &pluginv1.PluginInfo{PluginId: "io.labcentral.access.novnc", PluginVersion: "0.1.0", SupportedAccessMethods: []string{"vnc"}}, SelectedProtocol: &pluginv1.ProtocolVersion{Major: 1, Minor: 1}}
	legacy := legacyPluginInventory(client, "plugin.legacy-access", "access", "browser-vnc", nil)
	available := true
	legacy.Available = func() bool { return available }
	installed := func() []httpapi.ExtensionInventoryDTO {
		return []httpapi.ExtensionInventoryDTO{{InstanceID: "plugin.source", Category: "source", Available: true}}
	}
	inventory := combinedPluginInventory(installed, []registeredPlugin{legacy})
	first := inventory()
	if len(first) != 2 || first[0].Category != "access" || first[0].Methods[0] != "vnc" || !first[0].Available || first[1].Category != "source" {
		t.Fatalf("missing installed/legacy contributions: %+v", first)
	}
	available = false
	second := inventory()
	if second[0].Available || !first[0].Available {
		t.Fatal("availability must be current without mutating previous snapshots")
	}
}
