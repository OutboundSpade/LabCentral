package main

import (
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func configureBuilderExtensions(control *controlplane.Service, extensions loadedExtensions) error {
	bindings := []controlplane.BuilderBinding{}
	for _, backend := range extensions.Backends {
		if backend.Manifest.Categories[0] != "builder" {
			continue
		}
		for _, entry := range backend.Entry.Bindings {
			projects := []domain.ProjectID{}
			for _, p := range entry.ProjectIDs {
				projects = append(projects, domain.ProjectID(p))
			}
			l := entry.Limits
			bindings = append(bindings, controlplane.BuilderBinding{InstanceID: backend.Entry.InstanceID, ID: entry.ID, PluginDigest: backend.Client.Info.GetBuildDigest(), Projects: projects, Configuration: entry.Configuration, Limits: app.BuildLimits{TimeoutSeconds: l.TimeoutSeconds, CPUCount: l.CPUCount, MemoryBytes: l.MemoryBytes, InputBytes: l.InputBytes, OutputBytes: l.OutputBytes, Egress: l.Egress}, Builder: &providergrpc.Builder{Source: providergrpc.Source{Artifacts: backend.Client.Artifacts}, Client: backend.Client.Builder}, Available: backend.Client.Available})
		}
	}
	return control.SetBuilders(bindings)
}
