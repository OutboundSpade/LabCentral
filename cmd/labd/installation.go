package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/OutboundSpade/LabCentral/internal/adapter/localsecret"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
)

type installation struct {
	APIVersion string              `json:"apiVersion"`
	PluginRoot string              `json:"pluginRoot"`
	Providers  []installedProvider `json:"providers"`
}
type installedProvider struct {
	Manifest           string            `json:"manifest"`
	InstanceID         string            `json:"instanceId"`
	Mode               string            `json:"mode"`
	RuntimeDirectory   string            `json:"runtimeDirectory,omitempty"`
	TokenFile          string            `json:"tokenFile,omitempty"`
	Args               []string          `json:"args,omitempty"`
	GrantedPermissions []string          `json:"grantedPermissions"`
	Targets            []installedTarget `json:"targets"`
}
type installedTarget struct {
	ID             string                    `json:"id"`
	Kind           string                    `json:"kind"`
	ConfigFile     string                    `json:"configFile,omitempty"`
	CredentialFile string                    `json:"credentialFile,omitempty"`
	Policy         controlplane.TargetPolicy `json:"policy"`
}

func loadInstalledProviders(ctx context.Context, cfg config, runtimeParent string) ([]controlplane.TargetBinding, []*pluginhost.Client, error) {
	b, err := manifest.ProtectedFile(cfg.installationFile, 1<<20)
	if err != nil {
		return nil, nil, err
	}
	var install installation
	if err := manifest.Decode(b, &install); err != nil {
		return nil, nil, err
	}
	if install.APIVersion != "labcentral.io/installation/v1" || len(install.Providers) < 1 || len(install.Providers) > 32 {
		return nil, nil, fmt.Errorf("installation must use v1 and contain 1-32 providers")
	}
	var bindings []controlplane.TargetBinding
	var clients []*pluginhost.Client
	fail := func(err error) ([]controlplane.TargetBinding, []*pluginhost.Client, error) {
		for _, c := range clients {
			_ = c.Close()
		}
		return nil, nil, err
	}
	seenPlugins := map[string]bool{}
	seenTargets := map[string]bool{}
	for _, entry := range install.Providers {
		m, executable, err := manifest.Load(install.PluginRoot, entry.Manifest, entry.GrantedPermissions)
		if err != nil {
			return fail(err)
		}
		if seenPlugins[entry.InstanceID] || entry.InstanceID == "" || !slices.Contains(m.Categories, "provider") || len(entry.Targets) < 1 || len(entry.Targets) > 64 {
			return fail(fmt.Errorf("invalid provider installation binding"))
		}
		seenPlugins[entry.InstanceID] = true
		// Production third-party providers attach under separately provisioned OS
		// identities. Local execution is permitted only in explicit no-auth mode.
		if entry.Mode != "attach" && (entry.Mode != "local" || cfg.authMode != "none") {
			return fail(fmt.Errorf("production installed providers require attach mode and isolated operator-managed identities"))
		}
		if entry.Mode == "attach" && len(entry.Args) > 0 {
			return fail(fmt.Errorf("attached provider arguments belong to its external service"))
		}
		if entry.Mode == "local" && (entry.RuntimeDirectory != "" || entry.TokenFile != "") {
			return fail(fmt.Errorf("local provider cannot contain attach inputs"))
		}
		if len(entry.Args) > 32 {
			return fail(fmt.Errorf("too many provider arguments"))
		}
		for _, arg := range entry.Args {
			if len(arg) > 4096 {
				return fail(fmt.Errorf("provider argument exceeds limit"))
			}
		}
		for _, target := range entry.Targets {
			if seenTargets[target.ID] || !slices.Contains(m.ProviderKinds, target.Kind) {
				return fail(fmt.Errorf("duplicate target or unsupported provider kind"))
			}
			if _, err := domain.NewProviderTargetID(target.ID); err != nil {
				return fail(err)
			}
			seenTargets[target.ID] = true
		}
		launch := providerPluginConfig{Label: m.ID, Executable: executable, ExecutableSHA256: m.SHA256, Args: entry.Args, RuntimeParent: runtimeParent, PluginID: m.ID, ProviderKind: entry.Targets[0].Kind, PluginInstanceID: entry.InstanceID}
		if entry.Mode == "attach" {
			if !filepath.IsAbs(entry.RuntimeDirectory) || !filepath.IsAbs(entry.TokenFile) {
				return fail(fmt.Errorf("attach inputs must be absolute"))
			}
			launch.AttachRuntimeDirectory = entry.RuntimeDirectory
			launch.AttachTokenFile = entry.TokenFile
		}
		client, err := startProviderPlugin(ctx, launch)
		if err != nil {
			return fail(err)
		}
		clients = append(clients, client)
		if client.Info.GetBuildDigest() != "sha256:"+m.SHA256 || client.Info.GetPluginVersion() != m.Version || client.SelectedProtocol.GetMajor() != m.ProtocolMajor || client.SelectedProtocol.GetMinor() < m.MinimumProtocolMinor || client.SelectedProtocol.GetMinor() > m.MaximumProtocolMinor {
			return fail(fmt.Errorf("plugin handshake does not match installed manifest"))
		}
		for _, target := range entry.Targets {
			if !slices.Contains(client.Info.GetProviderKinds(), target.Kind) {
				return fail(fmt.Errorf("plugin did not advertise configured target kind"))
			}
			binding := controlplane.TargetBinding{ID: domain.ProviderTargetID(target.ID), ProviderKind: target.Kind, Provider: providergrpc.NewLive(client.Provider, client.Available), Policy: target.Policy, ProtocolMajor: client.SelectedProtocol.GetMajor(), ProtocolMinor: client.SelectedProtocol.GetMinor(), Features: client.Info.GetFeatures()}
			if target.ConfigFile != "" {
				if !filepath.IsAbs(target.ConfigFile) {
					return fail(fmt.Errorf("target configuration path must be absolute"))
				}
				binding.ConfigEnvelope, err = manifest.ProtectedFile(target.ConfigFile, 8<<20)
				if err != nil {
					return fail(err)
				}
			}
			if target.CredentialFile != "" {
				resolver, err := localsecret.NewFileResolverWithOptions([]string{target.CredentialFile}, 64<<10, localsecret.Options{AllowGroupRead: cfg.allowGroupReadableSecrets})
				if err != nil {
					return fail(err)
				}
				binding.CredentialResolver = resolver
				binding.CredentialRef = &domain.SecretRef{Source: "file", Name: target.CredentialFile, Key: "value"}
			}
			bindings = append(bindings, binding)
		}
	}
	if _, err := controlplane.NewTargetRegistry(bindings); err != nil {
		return fail(err)
	}
	return bindings, clients, nil
}
