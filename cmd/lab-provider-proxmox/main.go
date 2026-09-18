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
	"path/filepath"
	"syscall"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	plugincontract "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/contract"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	"github.com/OutboundSpade/LabCentral/providers/proxmox"
	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/qualification"
)

var sourceTreeDigest = "unavailable"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	flags := flag.NewFlagSet("lab-provider-proxmox", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cleanupDocument := flags.String("cleanup-authorization", "", "explicit signed exact-resource cleanup grant")
	qualificationDocument := flags.String("qualification-document", "", "signed qualification evidence")
	qualificationTrust := flags.String("qualification-trust", "", "qualification reviewer trust policy")
	journalDirectory := flags.String("initialize-journal-directory", "", "create and verify the configured shared invocation journal directory")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 {
		logger.Error("parse Proxmox provider arguments")
		os.Exit(1)
	}
	if *journalDirectory != "" {
		if err := prepareJournalDirectory(*journalDirectory); err != nil {
			logger.Error("prepare Proxmox invocation journal")
			os.Exit(1)
		}
	}
	buildDigest := executableDigest()
	service := proxmox.NewService()
	if (*qualificationDocument == "") != (*qualificationTrust == "") || (*cleanupDocument != "" && *qualificationDocument == "") {
		logger.Error("qualification document and trust policy must be configured together")
		os.Exit(1)
	}
	if *qualificationDocument != "" {
		authorizer, err := loadProductionAuthorizer(*qualificationDocument, *qualificationTrust, buildDigest, sourceTreeDigest)
		if err != nil {
			logger.Error("load Proxmox production qualification", "error", errors.New("qualification inputs are invalid"))
			os.Exit(1)
		}
		if *cleanupDocument != "" {
			authorizer.cleanupDocument, err = readPublicInput(*cleanupDocument, qualification.MaxDocumentBytes)
			if err != nil {
				logger.Error("invalid cleanup grant file")
				os.Exit(1)
			}
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
			{Name: "proxmox.target-config", Major: 1, Minor: 3},
			{Name: "proxmox.target-config", Major: 1, Minor: 4},
			{Name: "proxmox.api-token-credentials", Major: 1, Minor: 0},
			{Name: "proxmox.capacity", Major: 1, Minor: 0},
			{Name: "proxmox.validation-plan", Major: 1, Minor: 0},
			{Name: "proxmox.validation-plan", Major: 1, Minor: 1},
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
	services := pluginruntime.Services{Info: info, Runtime: service, Provider: service}
	if *journalDirectory != "" {
		images, err := proxmox.NewImageService(service, *journalDirectory)
		if err != nil {
			logger.Error("configure image import journal")
			os.Exit(1)
		}
		services.ImageImports = images
		services.Artifacts = images
		info.Features = append(info.Features, "image-import-v1", "image-import-reservations-v1", "image-retirement-v1", "artifact-transfer-v1")
	}
	if err := pluginruntime.Serve(ctx, services); err != nil {
		logger.Error("serve Proxmox provider plugin", "error", err)
		os.Exit(1)
	}
}

func prepareJournalDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("journal path is invalid")
	}
	parent := filepath.Dir(path)
	resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
	parentInfo, parentErr := os.Lstat(parent)
	if resolveErr != nil || parentErr != nil || resolvedParent != parent || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("journal parent must be a protected real directory")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create journal directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect journal directory: %w", err)
	}
	if _, err := proxmox.OpenInvocationJournal(path); err != nil {
		return err
	}
	return nil
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
