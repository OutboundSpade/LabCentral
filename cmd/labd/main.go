package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/internal/render"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
	"github.com/OutboundSpade/LabCentral/schemas"
	webassets "github.com/OutboundSpade/LabCentral/web"
)

var version = "dev"

type config struct {
	listen           string
	dataDir          string
	dockerPlugin     string
	dockerPluginHash string
	unsafeNoAuth     bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("labd stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configuration := parseFlags()
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
	pluginHash, err := configuredPluginDigest(configuration)
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
		ConfiguredPluginID: dockerprovider.PluginID, PluginInstanceID: "plugin.local-docker",
	})
	if err != nil {
		return fmt.Errorf("launch Docker provider: %w", err)
	}
	defer plugin.Close()

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
	control, err := controlplane.NewService(renderer, store, plugin.Provider, ids)
	if err != nil {
		return err
	}
	worker, err := controlplane.NewWorker(store, plugin.Provider, ids, controlplane.WorkerOptions{
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
	flag.BoolVar(&result.unsafeNoAuth, "unsafe-listen-non-loopback-without-auth", false, "explicitly allow no-auth mode on a non-loopback listener")
	flag.Parse()
	return result
}

func listenerIsLoopback(listener net.Listener) bool {
	tcp, ok := listener.Addr().(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

func configuredPluginDigest(configuration config) (string, error) {
	value := strings.TrimSpace(configuration.dockerPluginHash)
	if value == "" {
		contents, err := os.ReadFile(configuration.dockerPlugin + ".sha256")
		if err != nil {
			return "", fmt.Errorf("read configured Docker plugin digest: %w", err)
		}
		value, _, _ = strings.Cut(strings.TrimSpace(string(contents)), " ")
	}
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return "", fmt.Errorf("Docker plugin SHA-256 must contain 64 hexadecimal characters")
	}
	return strings.ToLower(value), nil
}
