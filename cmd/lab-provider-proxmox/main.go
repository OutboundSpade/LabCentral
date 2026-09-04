package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
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

var sourceTreeDigest = "unavailable"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	flags := flag.NewFlagSet("lab-provider-proxmox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	qualificationDocument := flags.String("qualification-document", "", "signed qualification evidence")
	qualificationTrust := flags.String("qualification-trust", "", "qualification reviewer trust policy")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 {
		logger.Error("parse Proxmox provider arguments")
		os.Exit(1)
	}
	buildDigest := executableDigest()
	service := proxmox.NewService()
	if (*qualificationDocument == "") != (*qualificationTrust == "") {
		logger.Error("qualification document and trust policy must be configured together")
		os.Exit(1)
	}
	if *qualificationDocument != "" {
		authorizer, err := loadProductionAuthorizer(*qualificationDocument, *qualificationTrust, buildDigest, sourceTreeDigest)
		if err != nil {
			logger.Error("load Proxmox production qualification", "error", errors.New("qualification inputs are invalid"))
			os.Exit(1)
		}
		service, err = proxmox.NewProductionService(authorizer)
		if err != nil {
			logger.Error("configure Proxmox production service", "error", err)
			os.Exit(1)
		}
	}
	info := &pluginv1.PluginInfo{
		PluginId:      proxmox.PluginID,
		PluginVersion: proxmox.PluginVersion,
		BuildDigest:   buildDigest,
		SupportedProtocols: []*pluginv1.ProtocolVersionRange{
			{Major: 1, MinMinor: 0, MaxMinor: 1},
		},
		SupportedProviderProtocols: []*pluginv1.ProtocolVersionRange{{Major: 1, MinMinor: 0, MaxMinor: 1}},
		Categories:                 []string{"provider"},
		ProviderKinds:              []string{"proxmox"},
		Schemas: []*pluginv1.SchemaVersion{
			{Name: "proxmox.target-config", Major: 1, Minor: 0},
			{Name: "proxmox.target-config", Major: 1, Minor: 1},
			{Name: "proxmox.target-config", Major: 1, Minor: 2},
			{Name: "proxmox.api-token-credentials", Major: 1, Minor: 0},
			{Name: "proxmox.capacity", Major: 1, Minor: 0},
			{Name: "proxmox.validation-plan", Major: 1, Minor: 0},
			{Name: "proxmox.qemu-plan", Major: 1, Minor: 0},
			{Name: "proxmox.qemu-state", Major: 1, Minor: 0},
			{Name: "proxmox.invocation-journal", Major: 1, Minor: 0},
			{Name: "proxmox.vnc-endpoint", Major: 1, Minor: 0},
		},
		Features: []string{
			"target-discovery",
			"qualification-gated-qemu-mutation-t0",
			"target-scoped-api-token",
			plugincontract.FeatureOpaqueAllocationClaimsV1,
			plugincontract.FeatureDependencyResourceInputV1,
			"proxmox-vnc-endpoint-v1",
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
