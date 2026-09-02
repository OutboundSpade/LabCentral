package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	plugincontract "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/OutboundSpade/LabCentral/providers/proxmox"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := proxmox.NewService()
	info := &pluginv1.PluginInfo{
		PluginId:      proxmox.PluginID,
		PluginVersion: proxmox.PluginVersion,
		BuildDigest:   executableDigest(),
		SupportedProtocols: []*pluginv1.ProtocolVersionRange{
			{Major: 1, MinMinor: 0, MaxMinor: 1},
		},
		Categories:    []string{"provider"},
		ProviderKinds: []string{"proxmox"},
		Schemas: []*pluginv1.SchemaVersion{
			{Name: "proxmox.target-config", Major: 1, Minor: 0},
			{Name: "proxmox.target-config", Major: 1, Minor: 1},
			{Name: "proxmox.api-token-credentials", Major: 1, Minor: 0},
			{Name: "proxmox.capacity", Major: 1, Minor: 0},
			{Name: "proxmox.validation-plan", Major: 1, Minor: 0},
			{Name: "proxmox.qemu-plan", Major: 1, Minor: 0},
			{Name: "proxmox.qemu-state", Major: 1, Minor: 0},
			{Name: "proxmox.invocation-journal", Major: 1, Minor: 0},
		},
		Features: []string{
			"target-discovery",
			"qualification-gated-qemu-mutation-t0",
			"target-scoped-api-token",
			plugincontract.FeatureOpaqueAllocationClaimsV1,
			plugincontract.FeatureDependencyResourceInputV1,
		},
	}
	if err := pluginruntime.Serve(ctx, pluginruntime.Services{Info: info, Runtime: service, Provider: service}); err != nil {
		logger.Error("serve Proxmox provider plugin", "error", err)
		os.Exit(1)
	}
}

func executableDigest() string {
	path, err := os.Executable()
	if err != nil {
		return "unavailable"
	}
	file, err := os.Open(path)
	if err != nil {
		return "unavailable"
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(hash.Sum(nil)))
}
