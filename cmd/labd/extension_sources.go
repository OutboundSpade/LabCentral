package main

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
	"slices"
)

func configureSourceExtensions(control *controlplane.Service, extensions loadedExtensions) error {
	bindings := []controlplane.SourceBinding{}
	for _, backend := range extensions.Backends {
		if backend.Manifest.Categories[0] != "source" {
			continue
		}
		for _, entry := range backend.Entry.Bindings {
			projects := []domain.ProjectID{}
			for _, id := range entry.ProjectIDs {
				projects = append(projects, domain.ProjectID(id))
			}
			var credential func(context.Context) ([]byte, error)
			if entry.CredentialFile != "" {
				file := entry.CredentialFile
				credential = func(context.Context) ([]byte, error) { return manifest.ProtectedFile(file, 64<<10) }
			}
			bindings = append(bindings, controlplane.SourceBinding{Collections: slices.Contains(backend.Client.Info.GetFeatures(), "source-collection-v1"), InstanceID: backend.Entry.InstanceID, ID: entry.ID, PluginDigest: backend.Client.Info.GetBuildDigest(), Projects: projects, Configuration: entry.Configuration, Credential: credential, Source: &providergrpc.Source{Client: backend.Client.Source, Artifacts: backend.Client.Artifacts}, Available: backend.Client.Available})
		}
	}
	return control.SetSources(bindings)
}
