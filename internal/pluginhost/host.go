package pluginhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Config struct {
	Executable             string
	ExecutableSHA256       string
	Args                   []string
	RuntimeParent          string
	ConfiguredPluginID     string
	ConfiguredProviderKind string
	PluginInstanceID       string
	RegistrationTimeout    time.Duration
	RPCMaxBytes            int
	LogMaxBytes            int
}

type Client struct {
	Provider         pluginv1.ProviderServiceClient
	Runtime          pluginv1.PluginRuntimeServiceClient
	Info             *pluginv1.PluginInfo
	SelectedProtocol *pluginv1.ProtocolVersion

	connection *grpc.ClientConn
	command    *exec.Cmd
	cancel     context.CancelFunc
	server     *grpc.Server
	listener   net.Listener
	runtimeDir string
	process    *processState
	closeOnce  sync.Once
	stdout     *boundedWriter
	stderr     *boundedWriter
}

type processState struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newProcessState() *processState { return &processState{done: make(chan struct{})} }

func (state *processState) wait(command *exec.Cmd) {
	err := command.Wait()
	state.mu.Lock()
	state.err = err
	state.mu.Unlock()
	close(state.done)
}

func (state *processState) result() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.err
}

func Launch(ctx context.Context, config Config) (*Client, error) {
	config = withDefaults(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	runtimeDir, err := os.MkdirTemp(config.RuntimeParent, "labcentral-plugin-")
	if err != nil {
		return nil, fmt.Errorf("create private plugin runtime directory: %w", err)
	}
	cleanupDir := true
	defer func() {
		if cleanupDir {
			_ = os.RemoveAll(runtimeDir)
		}
	}()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict plugin runtime directory: %w", err)
	}
	executable, err := snapshotExecutable(config.Executable, config.ExecutableSHA256, runtimeDir)
	if err != nil {
		return nil, err
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	registrationPath := filepath.Join(runtimeDir, "registration.sock")
	servicePath := filepath.Join(runtimeDir, "service.sock")
	listener, err := net.Listen("unix", registrationPath)
	if err != nil {
		return nil, fmt.Errorf("listen on private plugin registration socket: %w", err)
	}
	defer func() {
		if cleanupDir {
			_ = listener.Close()
		}
	}()
	if err := os.Chmod(registrationPath, 0o600); err != nil {
		return nil, fmt.Errorf("restrict plugin registration socket: %w", err)
	}
	registration := newRegistrationServer(token, config, runtimeDir)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(config.RPCMaxBytes),
		grpc.MaxSendMsgSize(config.RPCMaxBytes),
	)
	pluginv1.RegisterPluginRegistrationServiceServer(server, registration)
	go func() { _ = server.Serve(listener) }()

	processContext, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(processContext, executable, config.Args...)
	command.Env = append(filteredEnvironment(os.Environ()),
		launch.RegistrationEndpoint+"=unix://"+registrationPath,
		launch.ServiceEndpoint+"=unix://"+servicePath,
		launch.Token+"="+token,
		launch.ConfiguredPluginID+"="+config.ConfiguredPluginID,
		launch.PluginInstanceID+"="+config.PluginInstanceID,
		launch.RuntimeDirectory+"="+runtimeDir,
	)
	stdout := newBoundedWriter(config.LogMaxBytes)
	stderr := newBoundedWriter(config.LogMaxBytes)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		server.Stop()
		return nil, fmt.Errorf("start configured plugin: %w", err)
	}
	process := newProcessState()
	go process.wait(command)

	timer := time.NewTimer(config.RegistrationTimeout)
	defer timer.Stop()
	var accepted registrationResult
	select {
	case accepted = <-registration.accepted:
	case <-process.done:
		cancel()
		server.Stop()
		return nil, fmt.Errorf("plugin exited before registration: %v; stderr=%q", process.result(), stderr.String())
	case <-timer.C:
		cancel()
		server.Stop()
		return nil, fmt.Errorf("plugin registration timed out after %s", config.RegistrationTimeout)
	case <-ctx.Done():
		cancel()
		server.Stop()
		return nil, ctx.Err()
	}

	connection, err := dialUnix(ctx, accepted.servicePath, config.RPCMaxBytes)
	if err != nil {
		cancel()
		server.Stop()
		return nil, fmt.Errorf("connect to registered plugin service: %w", err)
	}
	healthContext, healthCancel := context.WithTimeout(ctx, 5*time.Second)
	defer healthCancel()
	runtimeClient := pluginv1.NewPluginRuntimeServiceClient(connection)
	health, err := runtimeClient.Health(healthContext, &pluginv1.HealthRequest{})
	if err != nil || health.GetStatus() != pluginv1.HealthResponse_STATUS_HEALTHY {
		_ = connection.Close()
		cancel()
		server.Stop()
		if err != nil {
			return nil, fmt.Errorf("plugin health check failed: %w", err)
		}
		return nil, fmt.Errorf("plugin did not become healthy: %s", health.GetSafeMessage())
	}
	client := &Client{
		Provider:         pluginv1.NewProviderServiceClient(connection),
		Runtime:          runtimeClient,
		Info:             accepted.info,
		SelectedProtocol: accepted.selectedProtocol,
		connection:       connection,
		command:          command,
		cancel:           cancel,
		server:           server,
		listener:         listener,
		runtimeDir:       runtimeDir,
		process:          process,
		stdout:           stdout,
		stderr:           stderr,
	}
	cleanupDir = false
	return client, nil
}

func withDefaults(config Config) Config {
	if config.RegistrationTimeout <= 0 {
		config.RegistrationTimeout = 10 * time.Second
	}
	if config.RPCMaxBytes <= 0 {
		config.RPCMaxBytes = 8 << 20
	}
	if config.LogMaxBytes <= 0 {
		config.LogMaxBytes = 256 << 10
	}
	return config
}

func validateConfig(config Config) error {
	if config.Executable == "" || !filepath.IsAbs(config.Executable) {
		return fmt.Errorf("plugin executable must be an absolute path")
	}
	if len(config.ExecutableSHA256) != sha256.Size*2 {
		return fmt.Errorf("configured plugin executable SHA-256 is required")
	}
	if len(config.Args) > 16 {
		return fmt.Errorf("configured plugin argument count exceeds 16")
	}
	for _, argument := range config.Args {
		if argument == "" || len(argument) > 4096 || strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("configured plugin argument is invalid")
		}
	}
	if config.ConfiguredPluginID == "" || config.ConfiguredProviderKind == "" || config.PluginInstanceID == "" {
		return fmt.Errorf("configured plugin, provider kind, and instance IDs are required")
	}
	if config.RPCMaxBytes < 1024 || config.RPCMaxBytes > 64<<20 {
		return fmt.Errorf("plugin RPC size limit must be between 1 KiB and 64 MiB")
	}
	return nil
}

func snapshotExecutable(executable, expectedDigest, runtimeDir string) (string, error) {
	info, err := os.Lstat(executable)
	if err != nil {
		return "", fmt.Errorf("inspect configured plugin executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("configured plugin path is not a protected executable regular file")
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", fmt.Errorf("open configured plugin executable: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || after.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("configured plugin executable identity or permissions changed")
	}
	destination := filepath.Join(runtimeDir, "plugin")
	copy, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return "", fmt.Errorf("create private plugin executable snapshot: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(copy, hash), file); err != nil {
		_ = copy.Close()
		return "", fmt.Errorf("snapshot configured plugin executable: %w", err)
	}
	if err := copy.Sync(); err != nil {
		_ = copy.Close()
		return "", fmt.Errorf("sync private plugin executable snapshot: %w", err)
	}
	if err := copy.Chmod(0o500); err != nil {
		_ = copy.Close()
		return "", fmt.Errorf("protect private plugin executable snapshot: %w", err)
	}
	if err := copy.Close(); err != nil {
		return "", fmt.Errorf("close private plugin executable snapshot: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(actual)), []byte(strings.ToLower(expectedDigest))) != 1 {
		return "", fmt.Errorf("configured plugin executable digest mismatch")
	}
	return destination, nil
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate one-time plugin token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func filteredEnvironment(environment []string) []string {
	blocked := map[string]bool{
		launch.RegistrationEndpoint: true,
		launch.ServiceEndpoint:      true,
		launch.Token:                true,
		launch.ConfiguredPluginID:   true,
		launch.PluginInstanceID:     true,
		launch.RuntimeDirectory:     true,
	}
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			result = append(result, value)
		}
	}
	return result
}

type registrationResult struct {
	servicePath      string
	info             *pluginv1.PluginInfo
	selectedProtocol *pluginv1.ProtocolVersion
}

type registrationServer struct {
	pluginv1.UnimplementedPluginRegistrationServiceServer

	token      string
	config     Config
	runtimeDir string
	accepted   chan registrationResult
	mu         sync.Mutex
	used       bool
	sequence   uint64
}

func newRegistrationServer(token string, config Config, runtimeDir string) *registrationServer {
	return &registrationServer{token: token, config: config, runtimeDir: runtimeDir, accepted: make(chan registrationResult, 1)}
}

func (s *registrationServer) RegisterPlugin(_ context.Context, request *pluginv1.RegisterPluginRequest) (*pluginv1.RegisterPluginResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used {
		return nil, fmt.Errorf("launch token has already been used")
	}
	if subtle.ConstantTimeCompare([]byte(request.GetLaunchToken()), []byte(s.token)) != 1 {
		return nil, fmt.Errorf("launch token is invalid")
	}
	if request.GetConfiguredPluginId() != s.config.ConfiguredPluginID || request.GetPluginInstanceId() != s.config.PluginInstanceID {
		return nil, fmt.Errorf("plugin registration identity mismatch")
	}
	info := request.GetPlugin()
	if info == nil || info.GetPluginId() != s.config.ConfiguredPluginID || !contains(info.GetCategories(), "provider") || !contains(info.GetProviderKinds(), s.config.ConfiguredProviderKind) {
		return nil, fmt.Errorf("plugin registration metadata mismatch")
	}
	selectedProtocol, ok := selectProtocol(info.GetSupportedProtocols())
	if !ok {
		return nil, fmt.Errorf("plugin does not support protocol 1.0")
	}
	servicePath, err := confinedUnixEndpoint(request.GetServiceEndpoint(), s.runtimeDir)
	if err != nil {
		return nil, err
	}
	infoCopy := clonePluginInfo(info)
	s.used = true
	s.accepted <- registrationResult{servicePath: servicePath, info: infoCopy, selectedProtocol: selectedProtocol}
	return &pluginv1.RegisterPluginResponse{
		SelectedProtocol:  selectedProtocol,
		HeartbeatInterval: durationpb.New(2 * time.Second),
		MaxRequestBytes:   uint64(s.config.RPCMaxBytes),
		MaxResponseBytes:  uint64(s.config.RPCMaxBytes),
	}, nil
}

func (s *registrationServer) Heartbeat(_ context.Context, request *pluginv1.HeartbeatRequest) (*pluginv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.used || request.GetPluginInstanceId() != s.config.PluginInstanceID || request.GetSequence() <= s.sequence {
		return nil, fmt.Errorf("plugin heartbeat identity or sequence is invalid")
	}
	s.sequence = request.GetSequence()
	return &pluginv1.HeartbeatResponse{ReceivedAt: timestamppb.Now()}, nil
}

func selectProtocol(ranges []*pluginv1.ProtocolVersionRange) (*pluginv1.ProtocolVersion, bool) {
	const hostMaxMinor uint32 = 1
	var selected *pluginv1.ProtocolVersion
	for _, versionRange := range ranges {
		if versionRange == nil || versionRange.GetMajor() != 1 || versionRange.GetMinMinor() > versionRange.GetMaxMinor() || versionRange.GetMinMinor() > hostMaxMinor {
			continue
		}
		minor := versionRange.GetMaxMinor()
		if minor > hostMaxMinor {
			minor = hostMaxMinor
		}
		if selected == nil || minor > selected.GetMinor() {
			selected = &pluginv1.ProtocolVersion{Major: 1, Minor: minor}
		}
	}
	return selected, selected != nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func confinedUnixEndpoint(endpoint, runtimeDir string) (string, error) {
	const prefix = "unix://"
	if !strings.HasPrefix(endpoint, prefix) {
		return "", fmt.Errorf("local plugin service endpoint must use a Unix socket")
	}
	servicePath := strings.TrimPrefix(endpoint, prefix)
	if !filepath.IsAbs(servicePath) {
		return "", fmt.Errorf("plugin service socket path must be absolute")
	}
	relative, err := filepath.Rel(runtimeDir, servicePath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", fmt.Errorf("plugin service endpoint escapes its private runtime directory")
	}
	info, err := os.Lstat(servicePath)
	if err != nil {
		return "", fmt.Errorf("inspect registered plugin service endpoint: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("registered plugin service endpoint is not a socket")
	}
	return servicePath, nil
}

func dialUnix(ctx context.Context, socketPath string, maxBytes int) (*grpc.ClientConn, error) {
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	return grpc.NewClient(
		"passthrough:///labcentral-plugin",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxBytes), grpc.MaxCallSendMsgSize(maxBytes)),
	)
}

func clonePluginInfo(info *pluginv1.PluginInfo) *pluginv1.PluginInfo {
	if info == nil {
		return nil
	}
	return proto.Clone(info).(*pluginv1.PluginInfo)
}

func (c *Client) SupportsFeatures(minor uint32, required ...string) bool {
	if c == nil || c.SelectedProtocol.GetMajor() != 1 || c.SelectedProtocol.GetMinor() < minor || c.Info == nil {
		return false
	}
	for _, feature := range required {
		if !contains(c.Info.GetFeatures(), feature) {
			return false
		}
	}
	return true
}

func (c *Client) Close() error {
	var result error
	c.closeOnce.Do(func() {
		_ = c.connection.Close()
		c.cancel()
		select {
		case <-c.process.done:
		case <-time.After(3 * time.Second):
			if c.command.Process != nil {
				_ = c.command.Process.Kill()
			}
			<-c.process.done
		}
		c.server.Stop()
		_ = c.listener.Close()
		if err := os.RemoveAll(c.runtimeDir); err != nil && result == nil {
			result = err
		}
	})
	return result
}

// Done is closed when the provider child exits. Callers use this to make a
// terminal child failure visible to the deployment supervisor instead of
// silently retaining an unusable target.
func (c *Client) Done() <-chan struct{} {
	if c == nil || c.process == nil {
		return nil
	}
	return c.process.done
}

// ExitError reports the child wait result after Done closes.
func (c *Client) ExitError() error {
	if c == nil || c.process == nil {
		return nil
	}
	return c.process.result()
}

func (c *Client) Logs() (stdout, stderr string) {
	return c.stdout.String(), c.stderr.String()
}

type boundedWriter struct {
	mu        sync.Mutex
	remaining int
	contents  strings.Builder
	truncated bool
}

func newBoundedWriter(limit int) *boundedWriter { return &boundedWriter{remaining: limit} }

func (w *boundedWriter) Write(input []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLength := len(input)
	if len(input) > w.remaining {
		input = input[:w.remaining]
		w.truncated = true
	}
	_, _ = w.contents.Write(input)
	w.remaining -= len(input)
	return originalLength, nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := w.contents.String()
	if w.truncated {
		result += " [truncated]"
	}
	return result
}

var _ pluginv1.PluginRegistrationServiceServer = (*registrationServer)(nil)
