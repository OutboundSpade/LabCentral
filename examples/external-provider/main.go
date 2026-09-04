// This independent module demonstrates the public plugin boundary without infrastructure access.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type provider struct {
	wire.UnimplementedProviderServiceServer
	wire.UnimplementedPluginRuntimeServiceServer
}

func (*provider) Health(context.Context, *wire.HealthRequest) (*wire.HealthResponse, error) {
	return &wire.HealthResponse{Status: wire.HealthResponse_STATUS_HEALTHY}, nil
}
func (*provider) DiscoverTarget(_ context.Context, in *wire.DiscoverTargetRequest) (*wire.DiscoverTargetResponse, error) {
	return &wire.DiscoverTargetResponse{Capabilities: []*wire.Capability{{FeatureId: "workload.vm", Support: wire.Capability_SUPPORT_UNSUPPORTED, Evidence: "External SDK fixture: discovery only; no infrastructure execution"}}, DiscoveredAt: timestamppb.Now(), ValidUntil: timestamppb.New(time.Now().Add(time.Minute))}, nil
}
func main() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	d := sha256.Sum256(b)
	p := &provider{}
	err = pluginruntime.Serve(context.Background(), pluginruntime.Services{Info: &wire.PluginInfo{PluginId: "org.example.provider", PluginVersion: "0.1.0", BuildDigest: "sha256:" + hex.EncodeToString(d[:]), Categories: []string{"provider"}, ProviderKinds: []string{"example"}, SupportedProtocols: []*wire.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 1}}}, Runtime: p, Provider: p})
	if err != nil {
		panic(err)
	}
}
