package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	accessnovnc "github.com/OutboundSpade/LabCentral/access/novnc"
	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	dockerlaptop "github.com/OutboundSpade/LabCentral/examples/docker-laptop"
	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localsecret"
	"github.com/OutboundSpade/LabCentral/internal/authentication"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/internal/observability"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/internal/render"
	dockerprovider "github.com/OutboundSpade/LabCentral/providers/docker"
	proxmoxprovider "github.com/OutboundSpade/LabCentral/providers/proxmox"
	"github.com/OutboundSpade/LabCentral/schemas"
	webassets "github.com/OutboundSpade/LabCentral/web"
)

var version = "dev"
var authoringAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var replicaIDPattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

const (
	deploymentModeSingleNode   = "single-node"
	deploymentModeMultiReplica = "multi-replica"
	artifactModeLocal          = "local-filesystem"
	artifactModeShared         = "shared-filesystem"
)

type config struct {
	listen                       string
	observabilityListen          string
	otlpTracesEndpoint           string
	otlpTracesCAFile             string
	otlpTracesClientCertificate  string
	otlpTracesClientKey          string
	traceSampleRatio             float64
	dataDir                      string
	deploymentMode               string
	postgresDSNFile              string
	replicaID                    string
	artifactMode                 string
	artifactDir                  string
	dockerPlugin                 string
	dockerPluginHash             string
	proxmoxPlugin                string
	proxmoxPluginHash            string
	proxmoxTargetID              string
	proxmoxTargetConfig          string
	proxmoxCredentialFile        string
	proxmoxQualificationDocument string
	proxmoxQualificationTrust    string
	accessPlugin                 string
	accessPluginHash             string
	accessProxyListen            string
	authMode                     string
	publicOrigin                 string
	tlsCertificateFile           string
	tlsKeyFile                   string
	trustedProxyCIDRs            string
	sessionKeyringFile           string
	bootstrapUserID              string
	bootstrapUsername            string
	bootstrapDisplayName         string
	bootstrapPasswordFile        string
	bootstrapOIDCSubject         string
	oidcIssuer                   string
	oidcClientID                 string
	oidcClientSecretFile         string
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
		endpoint = "http://127.0.0.1:9090/ready"
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
		Status     string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return fmt.Errorf("decode server info: %w", err)
	}
	if info.APIVersion != "v1" && info.Status != "ready" {
		return fmt.Errorf("server reported an unexpected health contract")
	}
	return nil
}

func run(logger *slog.Logger) error {
	configuration := parseFlags()
	return runConfigured(logger, configuration)
}

func runConfigured(logger *slog.Logger, configuration config) error {
	configuration = withRuntimeDefaults(configuration)
	if err := validateConfig(configuration); err != nil {
		return err
	}
	otlpTLS, err := loadOTLPTLSConfiguration(configuration)
	if err != nil {
		return err
	}
	shutdownTracing, err := observability.ConfigureTracing(context.Background(), observability.TraceConfig{
		Endpoint: configuration.otlpTracesEndpoint, ServiceVersion: version, ReplicaID: configuration.replicaID,
		SampleRatio: configuration.traceSampleRatio, TLSConfig: otlpTLS,
		OnError: func(error) { logger.Warn("OTLP trace export failed") },
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownContext); err != nil {
			logger.Warn("OTLP trace shutdown failed")
		}
	}()
	if configuration.dockerPlugin == "" && configuration.dockerPluginHash != "" {
		return fmt.Errorf("--docker-plugin-sha256 requires --docker-plugin")
	}
	if configuration.proxmoxPlugin == "" && (configuration.proxmoxTargetConfig != "" || configuration.proxmoxCredentialFile != "" || configuration.proxmoxPluginHash != "" || configuration.proxmoxQualificationDocument != "" || configuration.proxmoxQualificationTrust != "") {
		return fmt.Errorf("Proxmox target options require --proxmox-plugin")
	}
	if configuration.accessPlugin == "" && configuration.accessPluginHash != "" {
		return fmt.Errorf("--access-plugin-sha256 requires --access-plugin")
	}
	if (configuration.proxmoxQualificationDocument == "") != (configuration.proxmoxQualificationTrust == "") {
		return fmt.Errorf("Proxmox qualification document and trust policy must be configured together")
	}
	if err := ensurePrivateDirectory(configuration.dataDir); err != nil {
		return fmt.Errorf("prepare data directory: %w", err)
	}
	listener, err := net.Listen("tcp", configuration.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	loopback := listenerIsLoopback(listener)
	if err := validateListenerSecurity(configuration, loopback); err != nil {
		return fmt.Errorf("listener %s: %w", listener.Addr(), err)
	}
	warning := httpapi.NoAuthWarning
	runtimeParent := filepath.Join(configuration.dataDir, "run")
	if err := ensurePrivateDirectory(runtimeParent); err != nil {
		return fmt.Errorf("prepare plugin runtime parent: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openControlPlaneStore(ctx, configuration)
	if err != nil {
		return err
	}
	defer store.Close()
	authenticationOptions, err := configureHTTPAuthentication(ctx, configuration, store)
	if err != nil {
		return err
	}

	targetBindings := make([]controlplane.TargetBinding, 0, 2)
	providerExamples := make(map[string]httpapi.ProviderExample, 2)
	var dockerPlugin *pluginhost.Client
	if configuration.dockerPlugin != "" {
		pluginHash, err := configuredPluginDigest(configuration.dockerPlugin, configuration.dockerPluginHash, "Docker")
		if err != nil {
			return err
		}
		pluginPath, err := filepath.Abs(configuration.dockerPlugin)
		if err != nil {
			return err
		}
		dockerPlugin, err = pluginhost.Launch(ctx, pluginhost.Config{
			Executable: pluginPath, ExecutableSHA256: pluginHash, RuntimeParent: runtimeParent,
			ConfiguredPluginID: dockerprovider.PluginID, ConfiguredProviderKind: "docker", PluginInstanceID: "plugin.local-docker",
		})
		if err != nil {
			return fmt.Errorf("launch Docker provider: %w", err)
		}
		defer dockerPlugin.Close()
		targetBindings = append(targetBindings, controlplane.TargetBinding{
			ID: controlplane.LocalDockerTarget, ProviderKind: "docker", Provider: dockerPlugin.Provider,
			ProtocolMajor: dockerPlugin.SelectedProtocol.GetMajor(), ProtocolMinor: dockerPlugin.SelectedProtocol.GetMinor(), Features: dockerPlugin.Info.GetFeatures(),
		})
		providerExamples[string(controlplane.LocalDockerTarget)] = httpapi.ProviderExample{Source: string(dockerlaptop.LabYAML())}
	}
	var proxmoxPlugin *pluginhost.Client
	if configuration.proxmoxPlugin != "" {
		proxmoxBinding, client, example, err := launchProxmoxTarget(ctx, configuration, runtimeParent)
		if err != nil {
			return err
		}
		proxmoxPlugin = client
		defer proxmoxPlugin.Close()
		targetBindings = append(targetBindings, proxmoxBinding)
		providerExamples[string(proxmoxBinding.ID)] = example
	}
	if len(targetBindings) == 0 {
		return fmt.Errorf("at least one provider target must be configured")
	}
	targets, err := controlplane.NewTargetRegistry(targetBindings)
	if err != nil {
		return fmt.Errorf("construct provider target registry: %w", err)
	}
	defaultTargetID := string(targetBindings[0].ID)
	if dockerPlugin != nil {
		defaultTargetID = string(controlplane.LocalDockerTarget)
	}

	artifactDirectory := configuration.artifactDir
	if artifactDirectory == "" {
		artifactDirectory = filepath.Join(configuration.dataDir, "artifacts")
	}
	var artifacts *artifactfs.Store
	if configuration.artifactMode == artifactModeShared {
		artifacts, err = artifactfs.NewShared(artifactDirectory)
	} else {
		artifacts, err = artifactfs.New(artifactDirectory)
	}
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
	if err := control.SetArtifactStore(artifacts); err != nil {
		return err
	}
	var accessPlugin *pluginhost.Client
	if configuration.accessPlugin != "" {
		accessHash, err := configuredPluginDigest(configuration.accessPlugin, configuration.accessPluginHash, "noVNC access")
		if err != nil {
			return err
		}
		accessPath, err := filepath.Abs(configuration.accessPlugin)
		if err != nil {
			return err
		}
		accessPlugin, err = pluginhost.Launch(ctx, pluginhost.Config{
			Executable: accessPath, ExecutableSHA256: accessHash,
			Args:          []string{"--listen", configuration.accessProxyListen, "--public-origin", configuration.publicOrigin, "--websocket-base-path", "/access-proxy"},
			RuntimeParent: runtimeParent, ConfiguredPluginID: accessnovnc.PluginID, ConfiguredAccessKind: "browser-vnc",
			AccessAudience: configuration.publicOrigin, AccessAuthorization: control, PluginInstanceID: "plugin.local-novnc",
		})
		if err != nil {
			return fmt.Errorf("launch noVNC access plugin: %w", err)
		}
		defer accessPlugin.Close()
		accessRegistry, err := controlplane.NewAccessRegistry([]controlplane.AccessBinding{{
			PluginID: accessPlugin.Info.GetPluginId(), PluginVersion: accessPlugin.Info.GetPluginVersion(), PluginInstance: "plugin.local-novnc",
			AccessKind: "browser-vnc", Audience: configuration.publicOrigin, Access: accessPlugin.Access,
			ProtocolMajor: accessPlugin.SelectedProtocol.GetMajor(), ProtocolMinor: accessPlugin.SelectedProtocol.GetMinor(),
			Features: accessPlugin.Info.GetFeatures(), Methods: accessPlugin.Info.GetSupportedAccessMethods(), EndpointSchemas: accessPlugin.Info.GetAcceptedAccessEndpointSchemas(),
		}})
		if err != nil {
			return fmt.Errorf("construct access plugin registry: %w", err)
		}
		if err := control.SetAccessRuntime(accessRegistry, authenticationOptions.Authorizer, authenticationOptions.TokenHasher); err != nil {
			return err
		}
	}
	for _, target := range targets.IDs() {
		if err := control.RefreshTarget(ctx, target); err != nil {
			logger.Error("configured provider target discovery failed; cached evidence remains authoritative", "target", target, "error", err)
		}
	}
	readinessChecks := []observability.Check{
		{Name: "database", Run: store.Ping},
		{Name: "artifacts", Run: artifacts.Check},
	}
	if dockerPlugin != nil {
		client := dockerPlugin
		readinessChecks = append(readinessChecks, observability.Check{Name: "docker_plugin", Run: client.CheckHealth})
	}
	if proxmoxPlugin != nil {
		client := proxmoxPlugin
		readinessChecks = append(readinessChecks, observability.Check{Name: "proxmox_plugin", Run: client.CheckHealth})
	}
	if accessPlugin != nil {
		client := accessPlugin
		readinessChecks = append(readinessChecks, observability.Check{Name: "access_plugin", Run: client.CheckHealth})
	}
	telemetry, err := observability.New(version, configuration.replicaID, readinessChecks)
	if err != nil {
		return err
	}
	if dockerPlugin != nil {
		telemetry.SetPlugin("provider", "docker", true)
	}
	if proxmoxPlugin != nil {
		telemetry.SetPlugin("provider", "proxmox", true)
	}
	if accessPlugin != nil {
		telemetry.SetPlugin("access", "novnc", true)
	}
	operationsListener, err := net.Listen("tcp", configuration.observabilityListen)
	if err != nil {
		return fmt.Errorf("listen for operations: %w", err)
	}
	defer operationsListener.Close()
	operationsServer := &http.Server{
		Handler: telemetry.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	operationsDone := make(chan error, 1)
	go func() { operationsDone <- operationsServer.Serve(operationsListener) }()
	refreshContext, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	go refreshProviderTargets(refreshContext, control, targets.IDs(), logger, telemetry)
	expirationContext, cancelExpiration := context.WithCancel(ctx)
	defer cancelExpiration()
	go sweepExpiredInstances(expirationContext, control, logger, telemetry)
	if control.AccessAvailable() {
		go sweepAccessSessions(expirationContext, control, logger, telemetry)
	}
	worker, err := controlplane.NewWorkerWithTargets(store, targets, ids, controlplane.WorkerOptions{
		Owner: configuration.replicaID, Authorizer: authenticationOptions.Authorizer,
		OnError: func(err error) {
			telemetry.ObserveBackground("durable_worker", err)
			logger.Error("durable worker iteration failed", "error", err)
		},
	})
	if err != nil {
		return err
	}
	workerContext, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerContext) }()

	apiServer, err := httpapi.New(control, dockerlaptop.LabYAML(), webassets.FS(), httpapi.Options{
		NoAuthWarning: warning, DefaultProviderTargetID: defaultTargetID, ProviderExamples: providerExamples, Authentication: authenticationOptions,
	})
	if err != nil {
		return err
	}
	handler := apiServer.Handler
	if accessPlugin != nil {
		handler = withAccessProxy(handler, configuration.accessProxyListen)
	}
	handler = telemetry.InstrumentHTTP(handler)
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	tlsConfiguration, err := loadTLSConfiguration(configuration)
	if err != nil {
		return err
	}
	server.TLSConfig = tlsConfiguration
	serveDone := make(chan error, 1)
	go func() {
		if tlsConfiguration == nil {
			serveDone <- server.Serve(listener)
			return
		}
		serveDone <- server.ServeTLS(listener, "", "")
	}()
	telemetry.MarkStartupComplete()
	providerFailure := make(chan error, 3)
	monitorProvider := func(label, category, kind string, client *pluginhost.Client) {
		if client == nil {
			return
		}
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-client.Done():
				telemetry.SetPlugin(category, kind, false)
				providerFailure <- fmt.Errorf("%s plugin process exited: %v", label, client.ExitError())
			}
		}()
	}
	monitorProvider("Docker", "provider", "docker", dockerPlugin)
	monitorProvider("Proxmox", "provider", "proxmox", proxmoxPlugin)
	monitorProvider("noVNC access", "access", "novnc", accessPlugin)
	if configuration.authMode == httpapi.AuthModeNone {
		logger.Warn("LabCentral started in no-auth development mode", "listen", listener.Addr().String(), "version", version, "warning", warning)
	} else {
		logger.Info("LabCentral started", "listen", listener.Addr().String(), "version", version, "authMode", configuration.authMode, "publicOrigin", configuration.publicOrigin, "directTLS", tlsConfiguration != nil)
	}

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cancelWorker()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		if err := operationsServer.Shutdown(shutdownContext); err != nil {
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
	case err := <-operationsDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("operations server stopped: %w", err)
	case err := <-workerDone:
		return fmt.Errorf("durable worker stopped: %w", err)
	case err := <-providerFailure:
		if ctx.Err() != nil {
			return nil
		}
		stop()
		cancelWorker()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownContext); shutdownErr != nil {
			return errors.Join(err, shutdownErr)
		}
		if shutdownErr := operationsServer.Shutdown(shutdownContext); shutdownErr != nil {
			return errors.Join(err, shutdownErr)
		}
		if workerErr := <-workerDone; workerErr != nil {
			return errors.Join(err, workerErr)
		}
		return err
	}
}

func refreshProviderTargets(ctx context.Context, control *controlplane.Service, targets []domain.ProviderTargetID, logger *slog.Logger, telemetry *observability.System) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, target := range targets {
				err := control.RefreshTarget(ctx, target)
				telemetry.ObserveBackground("target_refresh", err)
				if err != nil {
					logger.Error("provider target refresh failed; cached evidence remains authoritative", "target", target, "error", err)
				}
			}
		}
	}
}

func sweepExpiredInstances(ctx context.Context, control *controlplane.Service, logger *slog.Logger, telemetry *observability.System) {
	const batchSize = uint32(200)
	run := func() {
		result, err := control.RunExpirationSweep(ctx, batchSize)
		telemetry.ObserveBackground("expiration_sweep", err)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("instance expiration sweep failed", "error", err)
			}
			return
		}
		if result.ReservationsExpired > 0 || result.WarningsEmitted > 0 || result.DestroysEnqueued > 0 {
			logger.Info("instance expiration sweep completed", "reservationsExpired", result.ReservationsExpired,
				"warningsEmitted", result.WarningsEmitted, "destroysEnqueued", result.DestroysEnqueued)
		}
	}
	run()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func sweepAccessSessions(ctx context.Context, control *controlplane.Service, logger *slog.Logger, telemetry *observability.System) {
	const batchSize = uint32(200)
	run := func() {
		result, err := control.RunAccessReconciliation(ctx, batchSize)
		telemetry.ObserveBackground("access_sweep", err)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("access session reconciliation failed", "error", err)
			}
			return
		}
		if result.Expired > 0 || result.ClosuresRetried > 0 {
			logger.Info("access session reconciliation completed", "expired", result.Expired,
				"closuresRetried", result.ClosuresRetried, "closuresClosed", result.ClosuresClosed)
		}
	}
	run()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func parseFlags() config {
	executable, _ := os.Executable()
	defaultPlugin := filepath.Join(filepath.Dir(executable), "lab-provider-docker")
	var result config
	flag.StringVar(&result.listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&result.observabilityListen, "observability-listen", "127.0.0.1:9090", "private operations listen address for /live, /ready, and /metrics")
	flag.StringVar(&result.otlpTracesEndpoint, "otlp-traces-endpoint", "", "optional exact OTLP/HTTP traces endpoint")
	flag.StringVar(&result.otlpTracesCAFile, "otlp-traces-ca-file", "", "optional protected PEM CA for the OTLP/HTTP endpoint")
	flag.StringVar(&result.otlpTracesClientCertificate, "otlp-traces-client-certificate-file", "", "optional protected PEM client certificate for OTLP mTLS")
	flag.StringVar(&result.otlpTracesClientKey, "otlp-traces-client-key-file", "", "optional owner-only PEM client key for OTLP mTLS")
	flag.Float64Var(&result.traceSampleRatio, "trace-sample-ratio", 0.05, "root trace sample ratio from 0 to 1")
	flag.StringVar(&result.dataDir, "data-dir", "./labcentral-data", "local SQLite, artifact, and runtime directory")
	flag.StringVar(&result.deploymentMode, "deployment-mode", deploymentModeSingleNode, "deployment topology: single-node or multi-replica")
	flag.StringVar(&result.postgresDSNFile, "postgres-dsn-file", "", "owner-only file containing the PostgreSQL connection URL")
	flag.StringVar(&result.replicaID, "replica-id", "local-worker", "stable unique worker identity; required explicitly in multi-replica mode")
	flag.StringVar(&result.artifactMode, "artifact-mode", artifactModeLocal, "artifact topology: local-filesystem or shared-filesystem")
	flag.StringVar(&result.artifactDir, "artifact-dir", "", "artifact root; defaults beneath data-dir in single-node mode")
	flag.StringVar(&result.dockerPlugin, "docker-plugin", defaultPlugin, "path to lab-provider-docker; empty disables Docker")
	flag.StringVar(&result.dockerPluginHash, "docker-plugin-sha256", "", "pinned Docker plugin SHA-256; defaults to <plugin>.sha256")
	flag.StringVar(&result.proxmoxPlugin, "proxmox-plugin", "", "path to lab-provider-proxmox; empty keeps Proxmox disabled")
	flag.StringVar(&result.proxmoxPluginHash, "proxmox-plugin-sha256", "", "pinned Proxmox plugin SHA-256; defaults to <plugin>.sha256")
	flag.StringVar(&result.proxmoxTargetID, "proxmox-target-id", "target.local-proxmox", "stable provider target ID for the optional Proxmox target")
	flag.StringVar(&result.proxmoxTargetConfig, "proxmox-target-config", "", "path to the strict Proxmox target-config JSON envelope")
	flag.StringVar(&result.proxmoxCredentialFile, "proxmox-credential-file", "", "mode-0600 file containing the Proxmox credential JSON envelope")
	flag.StringVar(&result.proxmoxQualificationDocument, "proxmox-qualification-document", "", "signed exact-target qualification evidence; requires the trust policy")
	flag.StringVar(&result.proxmoxQualificationTrust, "proxmox-qualification-trust", "", "reviewer trust policy for signed exact-target qualification evidence")
	flag.StringVar(&result.accessPlugin, "access-plugin", "", "path to lab-access-novnc; empty keeps browser console access disabled")
	flag.StringVar(&result.accessPluginHash, "access-plugin-sha256", "", "pinned noVNC access plugin SHA-256; defaults to <plugin>.sha256")
	flag.StringVar(&result.accessProxyListen, "access-proxy-listen", "127.0.0.1:8091", "private loopback address for the access plugin HTTP bridge")
	flag.StringVar(&result.authMode, "auth-mode", "", "required authentication mode: none, local, or oidc")
	flag.StringVar(&result.publicOrigin, "public-origin", "", "exact public HTTPS origin, for example https://labs.example.edu")
	flag.StringVar(&result.tlsCertificateFile, "tls-certificate-file", "", "protected PEM certificate chain for direct HTTPS")
	flag.StringVar(&result.tlsKeyFile, "tls-key-file", "", "owner-only PEM private key for direct HTTPS")
	flag.StringVar(&result.trustedProxyCIDRs, "trusted-proxy-cidrs", "", "comma-separated proxy network CIDRs allowed to assert HTTPS")
	flag.StringVar(&result.sessionKeyringFile, "session-keyring-file", "", "owner-only versioned session keyring JSON")
	flag.StringVar(&result.bootstrapUserID, "bootstrap-user-id", "user.bootstrap-admin", "initial administrator user ID")
	flag.StringVar(&result.bootstrapUsername, "bootstrap-username", "bootstrap-admin", "initial administrator username")
	flag.StringVar(&result.bootstrapDisplayName, "bootstrap-display-name", "Bootstrap administrator", "initial administrator display name")
	flag.StringVar(&result.bootstrapPasswordFile, "bootstrap-password-file", "", "owner-only initial local administrator password file")
	flag.StringVar(&result.bootstrapOIDCSubject, "bootstrap-oidc-subject", "", "initial administrator's exact pre-linked OIDC subject")
	flag.StringVar(&result.oidcIssuer, "oidc-issuer", "", "exact HTTPS OIDC issuer")
	flag.StringVar(&result.oidcClientID, "oidc-client-id", "", "OIDC client ID")
	flag.StringVar(&result.oidcClientSecretFile, "oidc-client-secret-file", "", "owner-only OIDC client secret file")
	flag.Parse()
	return result
}

func validateConfig(configuration config) error {
	if err := validateObservabilityConfig(configuration); err != nil {
		return err
	}
	if configuration.authMode == "" {
		return fmt.Errorf("--auth-mode must be explicitly set to none, local, or oidc")
	}
	switch configuration.deploymentMode {
	case deploymentModeSingleNode:
		if configuration.postgresDSNFile != "" {
			return fmt.Errorf("--postgres-dsn-file requires --deployment-mode=%s", deploymentModeMultiReplica)
		}
		if configuration.artifactMode != artifactModeLocal {
			return fmt.Errorf("single-node mode requires --artifact-mode=%s", artifactModeLocal)
		}
	case deploymentModeMultiReplica:
		if configuration.postgresDSNFile == "" {
			return fmt.Errorf("multi-replica mode requires --postgres-dsn-file")
		}
		if configuration.replicaID == "" || configuration.replicaID == "local-worker" || !replicaIDPattern.MatchString(configuration.replicaID) {
			return fmt.Errorf("multi-replica mode requires an explicit DNS-label-like --replica-id")
		}
		if configuration.artifactMode != artifactModeShared {
			return fmt.Errorf("multi-replica mode requires --artifact-mode=%s", artifactModeShared)
		}
		if !filepath.IsAbs(configuration.artifactDir) || filepath.Clean(configuration.artifactDir) != configuration.artifactDir || configuration.artifactDir == "/" {
			return fmt.Errorf("multi-replica mode requires an absolute clean non-root --artifact-dir")
		}
		if configuration.proxmoxPlugin != "" {
			return fmt.Errorf("multi-replica mode does not yet support the process-local Proxmox invocation journal")
		}
		if configuration.authMode == httpapi.AuthModeNone {
			return fmt.Errorf("multi-replica mode requires local or OIDC authentication")
		}
	default:
		return fmt.Errorf("unsupported --deployment-mode %q", configuration.deploymentMode)
	}
	if !replicaIDPattern.MatchString(configuration.replicaID) {
		return fmt.Errorf("--replica-id must be DNS-label-like")
	}
	if (configuration.tlsCertificateFile == "") != (configuration.tlsKeyFile == "") {
		return fmt.Errorf("--tls-certificate-file and --tls-key-file must be configured together")
	}
	switch configuration.authMode {
	case httpapi.AuthModeNone:
		if configuration.publicOrigin != "" || configuration.sessionKeyringFile != "" || configuration.bootstrapPasswordFile != "" || configuration.bootstrapOIDCSubject != "" || configuration.oidcIssuer != "" || configuration.oidcClientID != "" || configuration.oidcClientSecretFile != "" || configuration.trustedProxyCIDRs != "" {
			return fmt.Errorf("authentication and trusted-proxy options require --auth-mode=local or oidc")
		}
	case httpapi.AuthModeLocal, httpapi.AuthModeOIDC:
		if configuration.publicOrigin == "" || configuration.sessionKeyringFile == "" {
			return fmt.Errorf("authenticated mode requires --public-origin and --session-keyring-file")
		}
		parsed, err := url.Parse(configuration.publicOrigin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != configuration.publicOrigin {
			return fmt.Errorf("--public-origin must be an exact HTTPS origin without path, query, user information, or fragment")
		}
		if configuration.authMode == httpapi.AuthModeLocal && (configuration.bootstrapOIDCSubject != "" || configuration.oidcIssuer != "" || configuration.oidcClientID != "" || configuration.oidcClientSecretFile != "") {
			return fmt.Errorf("OIDC options require --auth-mode=oidc")
		}
		if configuration.authMode == httpapi.AuthModeOIDC && (configuration.oidcIssuer == "" || configuration.oidcClientID == "" || configuration.oidcClientSecretFile == "" || configuration.bootstrapPasswordFile != "") {
			return fmt.Errorf("OIDC mode requires issuer, client ID, client secret file, and no local bootstrap password")
		}
	default:
		return fmt.Errorf("unsupported --auth-mode %q", configuration.authMode)
	}
	if configuration.accessPlugin != "" {
		if configuration.proxmoxPlugin == "" || configuration.authMode == httpapi.AuthModeNone {
			return fmt.Errorf("browser console access requires the Proxmox provider and authenticated mode")
		}
		host, port, err := net.SplitHostPort(configuration.accessProxyListen)
		ip := net.ParseIP(host)
		if err != nil || port == "" || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("--access-proxy-listen must be an explicit loopback IP and port")
		}
	}
	if _, err := parseTrustedProxyCIDRs(configuration.trustedProxyCIDRs); err != nil {
		return err
	}
	return nil
}

func validateObservabilityConfig(configuration config) error {
	configuration = withRuntimeDefaults(configuration)
	host, port, err := net.SplitHostPort(configuration.observabilityListen)
	if err != nil || port == "" || net.ParseIP(host) == nil {
		return fmt.Errorf("--observability-listen must be an explicit IP address and port")
	}
	if configuration.traceSampleRatio < 0 || configuration.traceSampleRatio > 1 {
		return fmt.Errorf("--trace-sample-ratio must be between 0 and 1")
	}
	clientPairConfigured := configuration.otlpTracesClientCertificate != "" || configuration.otlpTracesClientKey != ""
	if (configuration.otlpTracesClientCertificate == "") != (configuration.otlpTracesClientKey == "") {
		return fmt.Errorf("OTLP client certificate and key must be configured together")
	}
	if configuration.otlpTracesEndpoint == "" {
		if configuration.otlpTracesCAFile != "" || clientPairConfigured {
			return fmt.Errorf("OTLP TLS inputs require --otlp-traces-endpoint")
		}
		return nil
	}
	endpoint, err := url.Parse(configuration.otlpTracesEndpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.String() != configuration.otlpTracesEndpoint {
		return fmt.Errorf("--otlp-traces-endpoint must be an exact URL without user information, query, or fragment")
	}
	switch endpoint.Scheme {
	case "https":
		return nil
	case "http":
		hostname := endpoint.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("plaintext OTLP is allowed only to an explicit loopback collector")
		}
		if configuration.otlpTracesCAFile != "" || clientPairConfigured {
			return fmt.Errorf("OTLP TLS inputs cannot be used with a plaintext endpoint")
		}
		return nil
	default:
		return fmt.Errorf("--otlp-traces-endpoint must use HTTPS or loopback HTTP")
	}
}

func withRuntimeDefaults(configuration config) config {
	if configuration.observabilityListen == "" {
		configuration.observabilityListen = "127.0.0.1:9090"
	}
	return configuration
}

func loadOTLPTLSConfiguration(configuration config) (*tls.Config, error) {
	if configuration.otlpTracesEndpoint == "" || strings.HasPrefix(configuration.otlpTracesEndpoint, "http://") {
		return nil, nil
	}
	result := &tls.Config{MinVersion: tls.VersionTLS13}
	if configuration.otlpTracesCAFile != "" {
		certificateBytes, err := readRegularFile(configuration.otlpTracesCAFile, 4<<20)
		if err != nil {
			return nil, fmt.Errorf("read OTLP CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certificateBytes) {
			return nil, fmt.Errorf("parse OTLP CA: no certificates found")
		}
		result.RootCAs = roots
	}
	if configuration.otlpTracesClientCertificate != "" {
		certificateBytes, err := readRegularFile(configuration.otlpTracesClientCertificate, 4<<20)
		if err != nil {
			return nil, fmt.Errorf("read OTLP client certificate: %w", err)
		}
		privateKeyBytes, err := readPrivateRegularFile(configuration.otlpTracesClientKey, 4<<20)
		if err != nil {
			return nil, fmt.Errorf("read OTLP client key: %w", err)
		}
		pair, err := tls.X509KeyPair(certificateBytes, privateKeyBytes)
		clear(privateKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse OTLP client certificate and key: %w", err)
		}
		result.Certificates = []tls.Certificate{pair}
	}
	return result, nil
}

func configureHTTPAuthentication(ctx context.Context, configuration config, store *localdb.Store) (httpapi.AuthenticationOptions, error) {
	if err := store.ValidateAuthenticationStartup(ctx, configuration.authMode); err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	if configuration.authMode == httpapi.AuthModeNone {
		return httpapi.AuthenticationOptions{Mode: httpapi.AuthModeNone}, nil
	}
	keyringBytes, err := readPrivateRegularFile(configuration.sessionKeyringFile, 64<<10)
	if err != nil {
		return httpapi.AuthenticationOptions{}, fmt.Errorf("read session keyring: %w", err)
	}
	keyring, err := authentication.ParseKeyring(keyringBytes)
	clear(keyringBytes)
	if err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	manager, err := authentication.NewManager(store, keyring, authentication.ManagerOptions{})
	if err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	hasUsers, err := store.HasUsers(ctx)
	if err != nil {
		return httpapi.AuthenticationOptions{}, fmt.Errorf("inspect authentication bootstrap state: %w", err)
	}
	bootstrapUserID, err := domain.NewUserID(configuration.bootstrapUserID)
	if err != nil {
		return httpapi.AuthenticationOptions{}, fmt.Errorf("invalid bootstrap user ID: %w", err)
	}
	if !hasUsers && configuration.authMode == httpapi.AuthModeOIDC && configuration.bootstrapOIDCSubject == "" {
		return httpapi.AuthenticationOptions{}, fmt.Errorf("the first OIDC startup requires --bootstrap-oidc-subject")
	}
	var oidcService *authentication.OIDCService
	if configuration.authMode == httpapi.AuthModeOIDC {
		secretBytes, err := readPrivateRegularFile(configuration.oidcClientSecretFile, 16<<10)
		if err != nil {
			return httpapi.AuthenticationOptions{}, fmt.Errorf("read OIDC client secret: %w", err)
		}
		secretBytes = trimOneLineEnding(secretBytes)
		clientSecret := string(secretBytes)
		clear(secretBytes)
		oidcService, err = authentication.NewOIDCService(ctx, store, manager, keyring, authentication.OIDCConfig{
			Issuer: configuration.oidcIssuer, ClientID: configuration.oidcClientID, ClientSecret: clientSecret,
			RedirectURL: configuration.publicOrigin + "/api/v1/auth/oidc/callback",
		})
		if err != nil {
			return httpapi.AuthenticationOptions{}, err
		}
	}
	if !hasUsers {
		switch configuration.authMode {
		case httpapi.AuthModeLocal:
			if configuration.bootstrapPasswordFile == "" {
				return httpapi.AuthenticationOptions{}, fmt.Errorf("the first local-authentication startup requires --bootstrap-password-file")
			}
			password, err := readPrivateRegularFile(configuration.bootstrapPasswordFile, 2048)
			if err != nil {
				return httpapi.AuthenticationOptions{}, fmt.Errorf("read bootstrap password: %w", err)
			}
			password = trimOneLineEnding(password)
			err = manager.BootstrapLocalAdministrator(ctx, bootstrapUserID, configuration.bootstrapUsername, configuration.bootstrapDisplayName, password)
			clear(password)
			if err != nil {
				return httpapi.AuthenticationOptions{}, fmt.Errorf("bootstrap local administrator: %w", err)
			}
		case httpapi.AuthModeOIDC:
			if err := manager.BootstrapOIDCAdministrator(ctx, bootstrapUserID, configuration.bootstrapUsername, configuration.bootstrapDisplayName, configuration.oidcIssuer, configuration.bootstrapOIDCSubject); err != nil {
				return httpapi.AuthenticationOptions{}, fmt.Errorf("bootstrap OIDC administrator: %w", err)
			}
		}
	}
	if err := store.EnsureAuthenticationMode(ctx, configuration.authMode); err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	evaluator, err := authorization.New(store)
	if err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	trustedProxies, err := parseTrustedProxyCIDRs(configuration.trustedProxyCIDRs)
	if err != nil {
		return httpapi.AuthenticationOptions{}, err
	}
	result := httpapi.AuthenticationOptions{
		Mode: configuration.authMode, Manager: manager, Repository: store, Authorizer: evaluator,
		TokenHasher:  keyring,
		PublicOrigin: configuration.publicOrigin, SecureCookies: true, TrustedProxies: trustedProxies, OIDC: oidcService, OIDCIssuer: configuration.oidcIssuer,
	}
	return result, nil
}

func withAccessProxy(next http.Handler, listen string) http.Handler {
	target := &url.URL{Scheme: "http", Host: listen}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "access proxy unavailable", http.StatusServiceUnavailable)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/access-proxy/") {
			// The API server intentionally has short request deadlines. Clear them
			// before ReverseProxy hijacks an upgraded connection so a valid console
			// is bounded by its Access Session instead of the HTTP read timeout.
			controller := http.NewResponseController(response)
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
			proxy.ServeHTTP(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func parseTrustedProxyCIDRs(value string) ([]*net.IPNet, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]*net.IPNet, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return nil, fmt.Errorf("--trusted-proxy-cidrs must contain clean comma-separated CIDRs")
		}
		ip, network, err := net.ParseCIDR(part)
		if err != nil || !ip.Equal(network.IP) || network.String() != part || seen[part] {
			return nil, fmt.Errorf("trusted proxy CIDR %q is invalid, non-canonical, or duplicated", part)
		}
		ones, bits := network.Mask.Size()
		if ones <= 0 || bits == 0 {
			return nil, fmt.Errorf("trusted proxy CIDR %q is too broad", part)
		}
		seen[part] = true
		result = append(result, network)
	}
	return result, nil
}

func loadTLSConfiguration(configuration config) (*tls.Config, error) {
	if configuration.tlsCertificateFile == "" {
		return nil, nil
	}
	certificateBytes, err := readRegularFile(configuration.tlsCertificateFile, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("read TLS certificate: %w", err)
	}
	privateKeyBytes, err := readPrivateRegularFile(configuration.tlsKeyFile, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("read TLS private key: %w", err)
	}
	pair, err := tls.X509KeyPair(certificateBytes, privateKeyBytes)
	clear(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse TLS certificate and key: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

func trimOneLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}

func openControlPlaneStore(ctx context.Context, configuration config) (*localdb.Store, error) {
	if configuration.deploymentMode == deploymentModeSingleNode {
		return localdb.Open(ctx, filepath.Join(configuration.dataDir, "labcentral.db"))
	}
	contents, err := readPrivateRegularFile(configuration.postgresDSNFile, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL connection secret: %w", err)
	}
	dsn := strings.TrimSpace(string(contents))
	if err := validateProductionPostgreSQLDSN(dsn); err != nil {
		for index := range contents {
			contents[index] = 0
		}
		return nil, err
	}
	store, err := localdb.OpenPostgreSQL(ctx, dsn)
	for index := range contents {
		contents[index] = 0
	}
	if err != nil {
		return nil, err
	}
	return store, nil
}

func validateProductionPostgreSQLDSN(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
		return fmt.Errorf("PostgreSQL connection secret must be a postgres URL with host and database")
	}
	if parsed.Query().Get("sslmode") != "verify-full" {
		return fmt.Errorf("multi-replica PostgreSQL requires sslmode=verify-full")
	}
	return nil
}

func listenerIsLoopback(listener net.Listener) bool {
	tcp, ok := listener.Addr().(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

func validateListenerSecurity(configuration config, loopback bool) error {
	if configuration.authMode == httpapi.AuthModeNone {
		if !loopback {
			return fmt.Errorf("no-auth mode is development-only and loopback-only")
		}
		return nil
	}
	if !loopback && configuration.tlsCertificateFile == "" {
		return fmt.Errorf("authenticated non-loopback listeners require direct TLS")
	}
	if configuration.tlsCertificateFile == "" && configuration.trustedProxyCIDRs == "" {
		return fmt.Errorf("authenticated mode requires direct TLS or an explicitly trusted loopback reverse proxy")
	}
	return nil
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

func launchProxmoxTarget(ctx context.Context, configuration config, runtimeParent string) (controlplane.TargetBinding, *pluginhost.Client, httpapi.ProviderExample, error) {
	targetID, err := domain.NewProviderTargetID(configuration.proxmoxTargetID)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, fmt.Errorf("invalid Proxmox target ID: %w", err)
	}
	if configuration.proxmoxTargetConfig == "" || configuration.proxmoxCredentialFile == "" {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, fmt.Errorf("the optional Proxmox target requires --proxmox-target-config and --proxmox-credential-file")
	}
	pluginPath, err := filepath.Abs(configuration.proxmoxPlugin)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	configPath, err := filepath.Abs(configuration.proxmoxTargetConfig)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	credentialPath, err := filepath.Abs(configuration.proxmoxCredentialFile)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	providerArguments := make([]string, 0, 4)
	if configuration.proxmoxQualificationDocument != "" {
		documentPath, pathErr := filepath.Abs(configuration.proxmoxQualificationDocument)
		if pathErr != nil {
			return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, pathErr
		}
		trustPath, pathErr := filepath.Abs(configuration.proxmoxQualificationTrust)
		if pathErr != nil {
			return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, pathErr
		}
		providerArguments = append(providerArguments, "--qualification-document", documentPath, "--qualification-trust", trustPath)
	}
	configEnvelope, err := readRegularFile(configPath, 8<<20)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, fmt.Errorf("read Proxmox target configuration: %w", err)
	}
	example, err := proxmoxAuthoringExample(configEnvelope)
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	digest, err := configuredPluginDigest(configuration.proxmoxPlugin, configuration.proxmoxPluginHash, "Proxmox")
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	client, err := pluginhost.Launch(ctx, pluginhost.Config{
		Executable: pluginPath, ExecutableSHA256: digest, Args: providerArguments, RuntimeParent: runtimeParent,
		ConfiguredPluginID: proxmoxprovider.PluginID, ConfiguredProviderKind: "proxmox", PluginInstanceID: "plugin.local-proxmox",
	})
	if err != nil {
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, fmt.Errorf("launch Proxmox provider: %w", err)
	}
	resolver, err := localsecret.NewFileResolver([]string{credentialPath}, 64<<10)
	if err != nil {
		_ = client.Close()
		return controlplane.TargetBinding{}, nil, httpapi.ProviderExample{}, err
	}
	return controlplane.TargetBinding{
		ID: targetID, ProviderKind: "proxmox", Provider: client.Provider, ConfigEnvelope: configEnvelope,
		CredentialRef: &domain.SecretRef{Source: "file", Name: credentialPath, Key: "value"}, CredentialResolver: resolver,
		ProtocolMajor: client.SelectedProtocol.GetMajor(), ProtocolMinor: client.SelectedProtocol.GetMinor(), Features: client.Info.GetFeatures(),
	}, client, example, nil
}

func proxmoxAuthoringExample(configEnvelope []byte) (httpapi.ProviderExample, error) {
	var config proxmoxprovider.TargetConfig
	if err := json.Unmarshal(configEnvelope, &config); err != nil || len(config.Templates) == 0 || len(config.Storages) == 0 || len(config.Networks) == 0 {
		return httpapi.ProviderExample{}, fmt.Errorf("Proxmox target configuration cannot produce an authoring example")
	}
	template, storage, network := config.Templates[0], config.Storages[0], config.Networks[0]
	if !authoringAliasPattern.MatchString(template.Alias) || !authoringAliasPattern.MatchString(storage.Alias) || !authoringAliasPattern.MatchString(network.Alias) || !qualifiedSHA256(template.ContentDigest) {
		return httpapi.ProviderExample{}, fmt.Errorf("Proxmox target configuration contains unsafe authoring profile values")
	}
	override, err := json.Marshal(map[string]string{"templateAlias": template.Alias, "storageAlias": storage.Alias, "networkAlias": network.Alias})
	if err != nil {
		return httpapi.ProviderExample{}, err
	}
	source := fmt.Sprintf(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata:
  name: proxmox-vm-lab
  title: Qualified Proxmox VM lab
spec:
  requirements:
    isolationClass: virtualMachine
  resources:
    networks:
      - name: lab
        kind: isolated
        ipv4Cidr: 10.77.0.0/24
        ipam: static
        egress: {mode: deny}
    vms:
      - name: server
        imageRef: labcentral/proxmox/%s@%s
        osFamily: linux
        compute: {cpu: 0.5, memory: 512Mi, architecture: amd64}
        interfaces:
          - {networkRef: networks.lab, ipv4: 10.77.0.10}
`, template.Alias, template.ContentDigest)
	return httpapi.ProviderExample{Source: source, ProviderOverride: override}, nil
}

func qualifiedSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, character := range value[7:] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func readRegularFile(path string, maximumBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o022 != 0 || before.Size() < 1 || before.Size() > maximumBytes {
		return nil, fmt.Errorf("configured path is not a protected bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configured file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm()&0o022 != 0 {
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

func readPrivateRegularFile(path string, maximumBytes int64) ([]byte, error) {
	contents, err := readRegularFile(path, maximumBytes)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		for index := range contents {
			contents[index] = 0
		}
		return nil, fmt.Errorf("configured secret must have no group or other permissions")
	}
	return contents, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("path must be a private directory with no group or other permissions")
	}
	return nil
}
