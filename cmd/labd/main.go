package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	dockerlaptop "github.com/OutboundSpade/LabCentral/examples/docker-laptop"
	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localsecret"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/internal/render"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
	proxmoxprovider "github.com/OutboundSpade/LabCentral/providers/proxmox"
	"github.com/OutboundSpade/LabCentral/schemas"
	webassets "github.com/OutboundSpade/LabCentral/web"
)

var version = "dev"

type config struct {
	listen                string
	dataDir               string
	dockerPlugin          string
	dockerPluginHash      string
	proxmoxPlugin         string
	proxmoxPluginHash     string
	proxmoxTargetID       string
	proxmoxTargetConfig   string
	proxmoxCredentialFile string
	unsafeNoAuth          bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			logger.Error("LabCentral healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("labd stopped", "error", err)
		os.Exit(1)
	}
}

func healthcheck() error {
	endpoint := strings.TrimSpace(os.Getenv("LABCENTRAL_HEALTHCHECK_URL"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/api/v1/server-info"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("request server info: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server info returned HTTP %d", response.StatusCode)
	}
	var info struct {
		APIVersion string `json:"apiVersion"`
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return fmt.Errorf("decode server info: %w", err)
	}
	if info.APIVersion != "v1" {
		return fmt.Errorf("server reported unexpected API version %q", info.APIVersion)
	}
	return nil
}

func run(logger *slog.Logger) error {
	configuration := parseFlags()
	if configuration.proxmoxPlugin == "" && (configuration.proxmoxTargetConfig != "" || configuration.proxmoxCredentialFile != "" || configuration.proxmoxPluginHash != "") {
		return fmt.Errorf("Proxmox target options require --proxmox-plugin")
	}
	if err := os.MkdirAll(configuration.dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	listener, err := net.Listen("tcp", configuration.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	loopback := listenerIsLoopback(listener)
	if !loopback && !configuration.unsafeNoAuth {
		return fmt.Errorf("no-auth mode refuses non-loopback listener %s; use --unsafe-listen-non-loopback-without-auth only after accepting the exposure", listener.Addr())
	}
	warning := httpapi.NoAuthWarning
	if !loopback {
		warning = "UNSAFE: authentication is disabled on a non-loopback listener. Every reachable client has installation administrator privileges. " + warning
	}
	pluginHash, err := configuredPluginDigest(configuration.dockerPlugin, configuration.dockerPluginHash, "Docker")
	if err != nil {
		return err
	}
	pluginPath, err := filepath.Abs(configuration.dockerPlugin)
	if err != nil {
		return err
	}
	runtimeParent := filepath.Join(configuration.dataDir, "run")
	if err := os.MkdirAll(runtimeParent, 0o700); err != nil {
		return fmt.Errorf("create plugin runtime parent: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	plugin, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: pluginPath, ExecutableSHA256: pluginHash, RuntimeParent: runtimeParent,
		ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin.local-docker",
	})
	if err != nil {
		return fmt.Errorf("launch Docker provider: %w", err)
	}
	defer plugin.Close()
	targetBindings := []controlplane.TargetBinding{{
		ID: controlplane.LocalDockerTarget, ProviderKind: "docker", Provider: plugin.Provider,
		ProtocolMajor: plugin.SelectedProtocol.GetMajor(), ProtocolMinor: plugin.SelectedProtocol.GetMinor(), Features: plugin.Info.GetFeatures(),
	}}
	var proxmoxPlugin *pluginhost.Client
	if configuration.proxmoxPlugin != "" {
		proxmoxBinding, client, err := launchProxmoxTarget(ctx, configuration, runtimeParent)
		if err != nil {
			return err
		}
		proxmoxPlugin = client
		defer proxmoxPlugin.Close()
		targetBindings = append(targetBindings, proxmoxBinding)
	}
	targets, err := controlplane.NewTargetRegistry(targetBindings)
	if err != nil {
		return fmt.Errorf("construct provider target registry: %w", err)
	}

	store, err := localdb.Open(ctx, filepath.Join(configuration.dataDir, "labcentral.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	artifacts, err := artifactfs.New(filepath.Join(configuration.dataDir, "artifacts"))
	if err != nil {
		return fmt.Errorf("open filesystem artifact store: %w", err)
	}
	defer artifacts.Close()
	renderer, err := render.NewRenderer(schemas.LabV1Alpha1(), render.DefaultLimits())
	if err != nil {
		return err
	}
	ids := controlplane.CryptoIDs{}
	control, err := controlplane.NewServiceWithTargets(renderer, store, targets, ids)
	if err != nil {
		return err
	}
	if proxmoxPlugin != nil {
		if err := control.RefreshTarget(ctx, domain.ProviderTargetID(configuration.proxmoxTargetID)); err != nil {
			logger.Error("configured Proxmox target discovery failed; cached evidence remains authoritative", "error", err)
		}
	}
	worker, err := controlplane.NewWorkerWithTargets(store, targets, ids, controlplane.WorkerOptions{
		OnError: func(err error) { logger.Error("durable worker iteration failed", "error", err) },
	})
	if err != nil {
		return err
	}
	workerContext, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerContext) }()

	apiServer, err := httpapi.New(control, dockerlaptop.LabYAML(), webassets.FS(), httpapi.Options{NoAuthWarning: warning})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           apiServer.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	logger.Warn("LabCentral started in no-auth trusted-laptop mode", "listen", listener.Addr().String(), "version", version, "warning", warning)

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cancelWorker()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		if err := <-workerDone; err != nil {
			return err
		}
		return nil
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-workerDone:
		return fmt.Errorf("durable worker stopped: %w", err)
	}
}

func parseFlags() config {
	executable, _ := os.Executable()
	defaultPlugin := filepath.Join(filepath.Dir(executable), "lab-provider-docker")
	var result config
	flag.StringVar(&result.listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&result.dataDir, "data-dir", "./labcentral-data", "local SQLite, artifact, and runtime directory")
	flag.StringVar(&result.dockerPlugin, "docker-plugin", defaultPlugin, "absolute or relative path to lab-provider-docker")
	flag.StringVar(&result.dockerPluginHash, "docker-plugin-sha256", "", "pinned Docker plugin SHA-256; defaults to <plugin>.sha256")
	flag.StringVar(&result.proxmoxPlugin, "proxmox-plugin", "", "path to lab-provider-proxmox; empty keeps Proxmox disabled")
	flag.StringVar(&result.proxmoxPluginHash, "proxmox-plugin-sha256", "", "pinned Proxmox plugin SHA-256; defaults to <plugin>.sha256")
	flag.StringVar(&result.proxmoxTargetID, "proxmox-target-id", "target.local-proxmox", "stable provider target ID for the optional Proxmox target")
	flag.StringVar(&result.proxmoxTargetConfig, "proxmox-target-config", "", "path to the strict Proxmox target-config JSON envelope")
	flag.StringVar(&result.proxmoxCredentialFile, "proxmox-credential-file", "", "mode-0600 file containing the Proxmox credential JSON envelope")
	flag.BoolVar(&result.unsafeNoAuth, "unsafe-listen-non-loopback-without-auth", false, "explicitly allow no-auth mode on a non-loopback listener")
	flag.Parse()
	return result
}

func listenerIsLoopback(listener net.Listener) bool {
	tcp, ok := listener.Addr().(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

func configuredPluginDigest(pluginPath, configuredHash, label string) (string, error) {
	value := strings.TrimSpace(configuredHash)
	if value == "" {
		contents, err := os.ReadFile(pluginPath + ".sha256")
		if err != nil {
			return "", fmt.Errorf("read configured %s plugin digest: %w", label, err)
		}
		value, _, _ = strings.Cut(strings.TrimSpace(string(contents)), " ")
	}
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return "", fmt.Errorf("%s plugin SHA-256 must contain 64 hexadecimal characters", label)
	}
	return strings.ToLower(value), nil
}

func launchProxmoxTarget(ctx context.Context, configuration config, runtimeParent string) (controlplane.TargetBinding, *pluginhost.Client, error) {
	targetID, err := domain.NewProviderTargetID(configuration.proxmoxTargetID)
	if err != nil {
		return controlplane.TargetBinding{}, nil, fmt.Errorf("invalid Proxmox target ID: %w", err)
	}
	if configuration.proxmoxTargetConfig == "" || configuration.proxmoxCredentialFile == "" {
		return controlplane.TargetBinding{}, nil, fmt.Errorf("the optional Proxmox target requires --proxmox-target-config and --proxmox-credential-file")
	}
	pluginPath, err := filepath.Abs(configuration.proxmoxPlugin)
	if err != nil {
		return controlplane.TargetBinding{}, nil, err
	}
	configPath, err := filepath.Abs(configuration.proxmoxTargetConfig)
	if err != nil {
		return controlplane.TargetBinding{}, nil, err
	}
	credentialPath, err := filepath.Abs(configuration.proxmoxCredentialFile)
	if err != nil {
		return controlplane.TargetBinding{}, nil, err
	}
	configEnvelope, err := readRegularFile(configPath, 8<<20)
	if err != nil {
		return controlplane.TargetBinding{}, nil, fmt.Errorf("read Proxmox target configuration: %w", err)
	}
	digest, err := configuredPluginDigest(configuration.proxmoxPlugin, configuration.proxmoxPluginHash, "Proxmox")
	if err != nil {
		return controlplane.TargetBinding{}, nil, err
	}
	client, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: pluginPath, ExecutableSHA256: digest, RuntimeParent: runtimeParent,
		ConfiguredPluginID: proxmoxprovider.PluginID, ConfiguredProviderKind: "proxmox", PluginInstanceID: "plugin.local-proxmox",
	})
	if err != nil {
		return controlplane.TargetBinding{}, nil, fmt.Errorf("launch Proxmox provider: %w", err)
	}
	resolver, err := localsecret.NewFileResolver([]string{credentialPath}, 64<<10)
	if err != nil {
		_ = client.Close()
		return controlplane.TargetBinding{}, nil, err
	}
	return controlplane.TargetBinding{
		ID: targetID, ProviderKind: "proxmox", Provider: client.Provider, ConfigEnvelope: configEnvelope,
		CredentialRef: &domain.SecretRef{Source: "file", Name: credentialPath, Key: "value"}, CredentialResolver: resolver,
		ProtocolMajor: client.SelectedProtocol.GetMajor(), ProtocolMinor: client.SelectedProtocol.GetMinor(), Features: client.Info.GetFeatures(),
	}, client, nil
}

func readRegularFile(path string, maximumBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() < 1 || before.Size() > maximumBytes {
		return nil, fmt.Errorf("configured path is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configured file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, fmt.Errorf("configured file identity changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read configured file: %w", err)
	}
	if len(contents) == 0 || int64(len(contents)) > maximumBytes {
		return nil, fmt.Errorf("configured file is empty or exceeds its size limit")
	}
	return contents, nil
}
