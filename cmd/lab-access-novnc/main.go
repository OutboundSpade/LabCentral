package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/OutboundSpade/LabCentral/access/novnc"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	flags := flag.NewFlagSet("lab-access-novnc", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "127.0.0.1:8091", "private HTTP listen address for the reverse proxy")
	publicOrigin := flags.String("public-origin", "", "browser-visible LabCentral HTTPS origin")
	websocketBase := flags.String("websocket-base-path", "/access-proxy", "same-origin reverse-proxy path")
	tailscaleProfileFile := flags.String("tailscale-upstream-profile", "", "optional operator-pinned Linux Tailscale console transport profile")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 || *publicOrigin == "" {
		logger.Error("parse noVNC access plugin arguments")
		os.Exit(1)
	}
	var tailnet *novnc.TailscaleProfile
	if *tailscaleProfileFile != "" {
		file, err := os.Open(*tailscaleProfileFile)
		if err != nil {
			logger.Error("open tailnet console profile")
			os.Exit(1)
		}
		contents, readErr := io.ReadAll(io.LimitReader(file, 4097))
		_ = file.Close()
		tailnet, err = novnc.ParseTailscaleProfile(contents)
		if readErr != nil || err != nil {
			logger.Error("invalid tailnet console profile")
			os.Exit(1)
		}
	}
	service, err := novnc.New(novnc.Config{PublicOrigin: *publicOrigin, WebSocketBasePath: *websocketBase, TailscaleProfile: tailnet})
	if err != nil {
		logger.Error("configure noVNC access plugin", "error", err)
		os.Exit(1)
	}
	httpDone := make(chan error, 1)
	go func() { httpDone <- service.RunHTTP(ctx, *listen) }()
	info := &pluginv1.PluginInfo{
		PluginId: novnc.PluginID, PluginVersion: novnc.PluginVersion, BuildDigest: executableDigest(),
		SupportedAccessProtocols: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 2, MaxMinor: 2}},
		Categories:               []string{"access"}, AccessKinds: []string{"browser-vnc"},
		SupportedAccessMethods:        []string{"vnc"},
		AcceptedAccessEndpointSchemas: []*pluginv1.SchemaVersion{{Name: "proxmox.vnc-endpoint", Major: 1, Minor: 0}},
		Schemas:                       []*pluginv1.SchemaVersion{{Name: "novnc.access-state", Major: 1, Minor: 0}},
		Features:                      []string{"access-authorization-recheck-v1", "shared-console-control-v1"},
	}
	pluginDone := make(chan error, 1)
	go func() {
		pluginDone <- pluginruntime.Serve(ctx, pluginruntime.Services{Info: info, Runtime: service, Access: service, ConfigureAccessCallbacks: service.ConfigureCallbacks})
	}()
	select {
	case err := <-httpDone:
		logger.Error("serve noVNC HTTP proxy", "error", err)
		stop()
	case err := <-pluginDone:
		if err != nil {
			logger.Error("serve noVNC access plugin", "error", err)
		}
		stop()
	case <-ctx.Done():
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
