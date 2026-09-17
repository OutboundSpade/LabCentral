package main

import (
	"cmp"
	"slices"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
)

// Inventory reports the same registered processes regardless of configuration
// format. It contains identity/bindings only, never credentials or file paths.
type registeredPlugin struct {
	Info      httpapi.ExtensionInventoryDTO
	Available func() bool
}

func legacyPluginInventory(client *pluginhost.Client, instance, category, kind string, targets []string) registeredPlugin {
	return registeredPlugin{Info: httpapi.ExtensionInventoryDTO{InstanceID: instance, PluginID: client.Info.GetPluginId(), Version: client.Info.GetPluginVersion(), Category: category, Kind: kind, TargetIDs: targets, Methods: client.Info.GetSupportedAccessMethods(), ProtocolMajor: client.SelectedProtocol.GetMajor(), ProtocolMinor: client.SelectedProtocol.GetMinor()}, Available: client.Available}
}

func combinedPluginInventory(installed func() []httpapi.ExtensionInventoryDTO, registered []registeredPlugin) func() []httpapi.ExtensionInventoryDTO {
	return func() []httpapi.ExtensionInventoryDTO {
		items := []httpapi.ExtensionInventoryDTO{}
		if installed != nil {
			items = append(items, installed()...)
		}
		for _, entry := range registered {
			item := entry.Info
			item.Available = entry.Available != nil && entry.Available()
			items = append(items, item)
		}
		slices.SortFunc(items, func(a, b httpapi.ExtensionInventoryDTO) int { return cmp.Compare(a.InstanceID, b.InstanceID) })
		return items
	}
}
