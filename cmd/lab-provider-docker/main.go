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
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine, err := dockerprovider.NewEngineClientFromEnvironment()
	if err != nil {
		logger.Error("configure Docker Engine client", "error", err)
		os.Exit(1)
	}
	service, err := dockerprovider.NewService(engine)
	if err != nil {
		logger.Error("configure Docker provider", "error", err)
		os.Exit(1)
	}
	info := &pluginv1.PluginInfo{
		PluginId:      dockerprovider.PluginID,
		PluginVersion: dockerprovider.PluginVersion,
		BuildDigest:   executableDigest(),
		SupportedProtocols: []*pluginv1.ProtocolVersionRange{
			{Major: 1, MinMinor: 0, MaxMinor: 0},
		},
		Categories:    []string{"provider"},
		ProviderKinds: []string{"docker"},
		Schemas: []*pluginv1.SchemaVersion{
			{Name: "docker.resource-state", Major: 1, Minor: 0},
			{Name: "docker.provider-override", Major: 1, Minor: 0},
		},
		Features: []string{"streaming-progress", "ownership-label-fencing", "idempotent-ensure"},
	}
	if err := pluginruntime.Serve(ctx, pluginruntime.Services{Info: info, Runtime: service, Provider: service}); err != nil {
		logger.Error("serve Docker provider plugin", "error", err)
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
