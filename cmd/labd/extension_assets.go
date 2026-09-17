package main

import (
	"context"
	"fmt"
	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	"github.com/OutboundSpade/LabCentral/internal/adapter/browserassets"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
	"net"
	"net/http"
	"net/url"
	"time"
)

func startExtensionAssets(ctx context.Context, cfg config, extensions loadedExtensions) ([]httpapi.BrowserExtensionDTO, func() []httpapi.ExtensionInventoryDTO, func(), error) {
	views := []httpapi.BrowserExtensionDTO{}
	inventory := func() []httpapi.ExtensionInventoryDTO {
		items := []httpapi.ExtensionInventoryDTO{}
		for _, backend := range extensions.Backends {
			c := backend.Client
			bindings := []httpapi.ExtensionBindingDTO{}
			for _, binding := range backend.Entry.Bindings {
				bindings = append(bindings, httpapi.ExtensionBindingDTO{ID: binding.ID, ProjectIDs: binding.ProjectIDs, HasCredentials: binding.CredentialFile != ""})
			}
			items = append(items, httpapi.ExtensionInventoryDTO{Bindings: bindings, TargetIDs: backend.Entry.TargetIDs, Methods: backend.Entry.Methods, Permissions: backend.Entry.GrantedPermissions, InstanceID: backend.Entry.InstanceID, Kind: backend.Entry.Kind, ConfigurationSchema: backend.Manifest.ConfigurationSchema, PluginID: c.Info.GetPluginId(), Version: c.Info.GetPluginVersion(), Category: c.Info.GetCategories()[0], ProtocolMajor: c.SelectedProtocol.GetMajor(), ProtocolMinor: c.SelectedProtocol.GetMinor(), Available: c.Available()})
		}
		for _, b := range extensions.Browsers {
			items = append(items, httpapi.ExtensionInventoryDTO{InstanceID: b.InstanceID, PluginID: b.Manifest.ID, Version: b.Manifest.Version, Category: "browser", ProtocolMajor: 1, Available: true})
		}
		return items
	}
	if len(extensions.Browsers) == 0 {
		return views, inventory, func() {}, nil
	}
	origin, err := url.Parse(cfg.extensionAssetOrigin)
	if err != nil || origin.Host == "" || (cfg.authMode != "none" && origin.Scheme != "https") {
		return nil, nil, func() {}, fmt.Errorf("browser extensions require an explicit HTTPS asset origin")
	}
	core := cfg.publicOrigin
	if core == "" {
		core = "http://" + cfg.listen
	}
	files := map[string]map[string]manifest.AssetSnapshot{}
	for _, b := range extensions.Browsers {
		files[b.InstanceID] = b.Assets
		for _, v := range b.Manifest.Browser.Contributions {
			views = append(views, httpapi.BrowserExtensionDTO{ID: v.ID, PluginInstance: b.InstanceID, Slot: v.Slot, Title: v.Title, URL: cfg.extensionAssetOrigin + "/" + url.PathEscape(b.InstanceID) + "/" + v.Entrypoint, Permissions: v.Permissions})
		}
	}
	handler, err := browserassets.Handler(cfg.extensionAssetOrigin, core, files)
	if err != nil {
		return nil, nil, func() {}, err
	}
	listener, err := net.Listen("tcp", cfg.extensionAssetListen)
	if err != nil {
		return nil, nil, func() {}, err
	}
	if !listenerIsLoopback(listener) {
		_ = listener.Close()
		return nil, nil, func() {}, fmt.Errorf("asset listener must be loopback behind its configured origin proxy")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	return views, inventory, func() { _ = server.Close() }, nil
}
