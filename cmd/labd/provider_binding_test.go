package main

import (
	"context"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"google.golang.org/grpc"
	"testing"
)

type boundImportClient struct {
	wire.ImageImportServiceClient
	called bool
}

func (c *boundImportClient) ValidateBinding(_ context.Context, r *wire.ImageImportServiceValidateBindingRequest, _ ...grpc.CallOption) (*wire.ImageImportServiceValidateBindingResponse, error) {
	c.called = true
	return &wire.ImageImportServiceValidateBindingResponse{Valid: true}, nil
}

type boundArtifactClient struct {
	wire.ArtifactTransferServiceClient
}

func TestProviderRegistrationCarriesNegotiatedImportServices(t *testing.T) {
	imports := &boundImportClient{}
	artifacts := &boundArtifactClient{}
	client := &pluginhost.Client{Info: &wire.PluginInfo{BuildDigest: "sha256:qualified-provider", Features: []string{"image-import-v1", "artifact-transfer-v1"}}, ImageImports: imports, Artifacts: artifacts}
	provider := boundProvider(client)
	if provider.ExtensionIdentity() != client.Info.BuildDigest || provider.Artifacts != artifacts {
		t.Fatal("provider identity or artifact service lost")
	}
	if err := provider.ValidateImageBinding(context.Background(), app.ImportRequest{}, app.ImageBinding{}); err != nil || !imports.called {
		t.Fatal("registered provider cannot validate an imported image", err)
	}
	client.Info.Features = []string{"image-import-v1"}
	provider = boundProvider(client)
	if provider.Artifacts != nil || provider.ImageImports != nil {
		t.Fatal("partial negotiation enabled image transfer")
	}
}
