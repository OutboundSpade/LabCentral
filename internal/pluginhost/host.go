package pluginhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var localIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Config struct {
	Executable             string
	ExecutableSHA256       string
	Args                   []string
	RuntimeParent          string
	ConfiguredPluginID     string
	ConfiguredProviderKind string
	ConfiguredAccessKind   string
	AccessAudience         string
	AccessAuthorization    application.AccessAuthorizationHandler
	PluginInstanceID       string
	RegistrationTimeout    time.Duration
	RPCMaxBytes            int
	LogMaxBytes            int
	LaunchToken            string
	SocketMode             os.FileMode
}

type Client struct {
	Provider         pluginv1.ProviderServiceClient
	Access           pluginv1.AccessServiceClient
	Runtime          pluginv1.PluginRuntimeServiceClient
	Info             *pluginv1.PluginInfo
	SelectedProtocol *pluginv1.ProtocolVersion

	connection    *grpc.ClientConn
	command       *exec.Cmd
	cancel        context.CancelFunc
	server        *grpc.Server
	listener      net.Listener
	runtimeDir    string
	process       *processState
	closeOnce     sync.Once
	stdout        *boundedWriter
	stderr        *boundedWriter
	removeRuntime bool
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
	callbackCredential := ""
	if config.ConfiguredAccessKind != "" {
		callbackCredential, err = randomToken(32)
		if err != nil {
			return nil, err
		}
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
	registration := newRegistrationServer(token, callbackCredential, config, runtimeDir)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(config.RPCMaxBytes),
		grpc.MaxSendMsgSize(config.RPCMaxBytes),
	)
	pluginv1.RegisterPluginRegistrationServiceServer(server, registration)
	if config.AccessAuthorization != nil {
		pluginv1.RegisterAccessAuthorizationServiceServer(server, &accessAuthorizationServer{
			registration: registration, handler: config.AccessAuthorization,
		})
	}
	go func() { _ = server.Serve(listener) }()

	processContext, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(processContext, executable, config.Args...)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + runtimeDir,
		launch.RegistrationEndpoint + "=unix://" + registrationPath,
		launch.ServiceEndpoint + "=unix://" + servicePath,
		launch.Token + "=" + token,
		launch.ConfiguredPluginID + "=" + config.ConfiguredPluginID,
		launch.PluginInstanceID + "=" + config.PluginInstanceID,
		launch.RuntimeDirectory + "=" + runtimeDir,
	}
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
	runtimeClient := pluginv1.NewPluginRuntimeServiceClient(connection)
	_, err = waitForHealthy(ctx, runtimeClient, process, 5*time.Second)
	if err != nil {
		_ = connection.Close()
		cancel()
		server.Stop()
		return nil, fmt.Errorf("plugin did not become healthy: %w", err)
	}
	client := &Client{
		Provider:         pluginv1.NewProviderServiceClient(connection),
		Access:           pluginv1.NewAccessServiceClient(connection),
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
		removeRuntime:    true,
	}
	cleanupDir = false
	return client, nil
}

// Attach accepts a plugin started by an external service manager, such as a
// Kubernetes sidecar running under a distinct UID. The two processes share
// only one group-restricted runtime directory and a rollout-scoped
// registration token; target credentials continue to travel solely in
// bounded protocol calls.
func Attach(ctx context.Context, config Config) (*Client, error) {
	config = withDefaults(config)
	if config.SocketMode == 0 {
		config.SocketMode = 0o660
	}
	if err := validateAttachConfig(config); err != nil {
		return nil, err
	}
	runtimeDir := config.RuntimeParent
	if err := prepareAttachedRuntimeDirectory(runtimeDir); err != nil {
		return nil, err
	}
	registrationPath := filepath.Join(runtimeDir, "registration.sock")
	if err := removeStaleSocket(registrationPath); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", registrationPath)
	if err != nil {
		return nil, fmt.Errorf("listen on attached plugin registration socket: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = listener.Close()
			_ = os.Remove(registrationPath)
		}
	}()
	if err := os.Chmod(registrationPath, config.SocketMode); err != nil {
		return nil, fmt.Errorf("restrict attached plugin registration socket: %w", err)
	}
	callbackCredential := ""
	if config.ConfiguredAccessKind != "" {
		callbackCredential, err = randomToken(32)
		if err != nil {
			return nil, err
		}
	}
	registration := newRegistrationServer(config.LaunchToken, callbackCredential, config, runtimeDir)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(config.RPCMaxBytes), grpc.MaxSendMsgSize(config.RPCMaxBytes))
	pluginv1.RegisterPluginRegistrationServiceServer(server, registration)
	if config.AccessAuthorization != nil {
		pluginv1.RegisterAccessAuthorizationServiceServer(server, &accessAuthorizationServer{registration: registration, handler: config.AccessAuthorization})
	}
	go func() { _ = server.Serve(listener) }()
	timer := time.NewTimer(config.RegistrationTimeout)
	defer timer.Stop()
	var accepted registrationResult
	select {
	case accepted = <-registration.accepted:
	case <-timer.C:
		server.Stop()
		return nil, fmt.Errorf("attached plugin registration timed out after %s", config.RegistrationTimeout)
	case <-ctx.Done():
		server.Stop()
		return nil, ctx.Err()
	}
	connection, err := dialUnix(ctx, accepted.servicePath, config.RPCMaxBytes)
	if err != nil {
		server.Stop()
		return nil, fmt.Errorf("connect to attached plugin service: %w", err)
	}
	runtimeClient := pluginv1.NewPluginRuntimeServiceClient(connection)
	if _, err := waitForHealthy(ctx, runtimeClient, nil, 5*time.Second); err != nil {
		_ = connection.Close()
		server.Stop()
		return nil, fmt.Errorf("attached plugin did not become healthy: %w", err)
	}
	_, cancel := context.WithCancel(context.Background())
	client := &Client{
		Provider: pluginv1.NewProviderServiceClient(connection), Access: pluginv1.NewAccessServiceClient(connection), Runtime: runtimeClient,
		Info: accepted.info, SelectedProtocol: accepted.selectedProtocol, connection: connection, cancel: cancel,
		server: server, listener: listener, runtimeDir: runtimeDir, stdout: newBoundedWriter(config.LogMaxBytes), stderr: newBoundedWriter(config.LogMaxBytes),
	}
	cleanup = false
	return client, nil
}

func waitForHealthy(ctx context.Context, client pluginv1.PluginRuntimeServiceClient, process *processState, timeout time.Duration) (*pluginv1.HealthResponse, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastMessage string
	var processDone <-chan struct{}
	if process != nil {
		processDone = process.done
	}
	for {
		callContext, cancel := context.WithTimeout(ctx, time.Second)
		health, err := client.Health(callContext, &pluginv1.HealthRequest{})
		cancel()
		if err == nil && health.GetStatus() == pluginv1.HealthResponse_STATUS_HEALTHY {
			return health, nil
		}
		if err != nil {
			lastMessage = "health RPC failed"
		} else {
			lastMessage = health.GetSafeMessage()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-processDone:
			return nil, fmt.Errorf("plugin exited: %v", process.result())
		case <-deadline.C:
			return nil, fmt.Errorf("health timeout: %s", lastMessage)
		case <-ticker.C:
		}
	}
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
	if config.LaunchToken != "" || config.SocketMode != 0 {
		return fmt.Errorf("local plugin launch cannot configure attached-plugin credentials")
	}
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
	return validateIdentityConfig(config)
}

func validateAttachConfig(config Config) error {
	if config.Executable != "" || config.ExecutableSHA256 != "" || len(config.Args) != 0 {
		return fmt.Errorf("attached plugins cannot configure a local executable")
	}
	if !filepath.IsAbs(config.RuntimeParent) || filepath.Clean(config.RuntimeParent) != config.RuntimeParent || config.RuntimeParent == "/" {
		return fmt.Errorf("attached plugin runtime directory must be an absolute clean non-root path")
	}
	if !validToken(config.LaunchToken) {
		return fmt.Errorf("attached plugin launch token must be 64 lowercase hexadecimal characters")
	}
	if config.SocketMode != 0o660 {
		return fmt.Errorf("attached plugin socket mode must be 0660")
	}
	return validateIdentityConfig(config)
}

func validateIdentityConfig(config Config) error {
	if !localIdentityPattern.MatchString(config.ConfiguredPluginID) || !localIdentityPattern.MatchString(config.PluginInstanceID) {
		return fmt.Errorf("configured plugin and instance IDs are required")
	}
	provider, access := config.ConfiguredProviderKind != "", config.ConfiguredAccessKind != ""
	if provider == access {
		return fmt.Errorf("exactly one configured provider or access kind is required")
	}
	if access {
		if len(config.AccessAudience) == 0 || len(config.AccessAudience) > 512 || strings.ContainsAny(config.AccessAudience, "\r\n\t ") || config.AccessAuthorization == nil {
			return fmt.Errorf("configured access audience and authorization callback are required")
		}
	} else if config.AccessAudience != "" || config.AccessAuthorization != nil {
		return fmt.Errorf("provider plugins cannot receive access callback configuration")
	}
	if config.RPCMaxBytes < 1024 || config.RPCMaxBytes > 64<<20 {
		return fmt.Errorf("plugin RPC size limit must be between 1 KiB and 64 MiB")
	}
	return nil
}

func validToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func prepareAttachedRuntimeDirectory(path string) error {
	parent := filepath.Dir(path)
	resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
	parentInfo, parentErr := os.Lstat(parent)
	if resolveErr != nil || parentErr != nil || resolvedParent != parent || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("attached plugin runtime parent must be a protected real directory")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o770); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create attached plugin runtime directory: %w", err)
		}
		if err := os.Chmod(path, 0o770); err != nil {
			return fmt.Errorf("protect attached plugin runtime directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect attached plugin runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o770 {
		return fmt.Errorf("attached plugin runtime directory must be a real mode-0770 directory")
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect stale plugin socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("plugin socket path contains a non-socket entry")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale plugin socket: %w", err)
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

type registrationResult struct {
	servicePath      string
	info             *pluginv1.PluginInfo
	selectedProtocol *pluginv1.ProtocolVersion
}

type registrationServer struct {
	pluginv1.UnimplementedPluginRegistrationServiceServer

	token              string
	callbackCredential string
	config             Config
	runtimeDir         string
	accepted           chan registrationResult
	mu                 sync.Mutex
	used               bool
	sequence           uint64
	selectedProtocol   *pluginv1.ProtocolVersion
}

func newRegistrationServer(token, callbackCredential string, config Config, runtimeDir string) *registrationServer {
	return &registrationServer{token: token, callbackCredential: callbackCredential, config: config, runtimeDir: runtimeDir, accepted: make(chan registrationResult, 1)}
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
	if info == nil || info.GetPluginId() != s.config.ConfiguredPluginID || len(info.GetCategories()) != 1 {
		return nil, fmt.Errorf("plugin registration metadata mismatch")
	}
	var selectedProtocol *pluginv1.ProtocolVersion
	var selectedProvider, selectedAccess *pluginv1.ProtocolVersion
	if s.config.ConfiguredProviderKind != "" {
		if info.GetCategories()[0] != "provider" || !contains(info.GetProviderKinds(), s.config.ConfiguredProviderKind) {
			return nil, fmt.Errorf("plugin registration metadata mismatch")
		}
		ranges := info.GetSupportedProviderProtocols()
		if len(ranges) == 0 {
			ranges = info.GetSupportedProtocols()
		}
		var ok bool
		selectedProvider, ok = selectProtocol(ranges)
		if !ok {
			return nil, fmt.Errorf("plugin does not support provider protocol 1.0")
		}
		selectedProtocol = selectedProvider
	} else {
		if info.GetCategories()[0] != "access" || !contains(info.GetAccessKinds(), s.config.ConfiguredAccessKind) ||
			!contains(info.GetFeatures(), "access-authorization-recheck-v1") || len(info.GetSupportedAccessMethods()) == 0 || len(info.GetAcceptedAccessEndpointSchemas()) == 0 {
			return nil, fmt.Errorf("plugin registration metadata mismatch")
		}
		var ok bool
		selectedAccess, ok = selectAccessProtocol(info.GetSupportedAccessProtocols())
		if !ok {
			return nil, fmt.Errorf("plugin does not support required access protocol 1.2")
		}
		selectedProtocol = selectedAccess
	}
	expectedSocketMode := s.config.SocketMode
	if expectedSocketMode == 0 {
		expectedSocketMode = 0o600
	}
	servicePath, err := confinedUnixEndpoint(request.GetServiceEndpoint(), s.runtimeDir, expectedSocketMode)
	if err != nil {
		return nil, err
	}
	infoCopy := clonePluginInfo(info)
	s.used = true
	s.selectedProtocol = proto.Clone(selectedProtocol).(*pluginv1.ProtocolVersion)
	s.accepted <- registrationResult{servicePath: servicePath, info: infoCopy, selectedProtocol: selectedProtocol}
	return &pluginv1.RegisterPluginResponse{
		SelectedProtocol:         selectedProtocol,
		HeartbeatInterval:        durationpb.New(2 * time.Second),
		MaxRequestBytes:          uint64(s.config.RPCMaxBytes),
		MaxResponseBytes:         uint64(s.config.RPCMaxBytes),
		SelectedProviderProtocol: selectedProvider,
		SelectedAccessProtocol:   selectedAccess,
		AccessCallbackCredential: s.callbackCredential,
		AccessAudience:           s.config.AccessAudience,
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
	return selectProtocolThrough(ranges, 1, 0)
}

func selectAccessProtocol(ranges []*pluginv1.ProtocolVersionRange) (*pluginv1.ProtocolVersion, bool) {
	return selectProtocolThrough(ranges, 2, 2)
}

func selectProtocolThrough(ranges []*pluginv1.ProtocolVersionRange, hostMaxMinor, hostMinMinor uint32) (*pluginv1.ProtocolVersion, bool) {
	var selected *pluginv1.ProtocolVersion
	for _, versionRange := range ranges {
		if versionRange == nil || versionRange.GetMajor() != 1 || versionRange.GetMinMinor() > versionRange.GetMaxMinor() || versionRange.GetMinMinor() > hostMaxMinor || versionRange.GetMaxMinor() < hostMinMinor {
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

type accessAuthorizationServer struct {
	pluginv1.UnimplementedAccessAuthorizationServiceServer
	registration *registrationServer
	handler      application.AccessAuthorizationHandler
}

func (s *accessAuthorizationServer) callbackIdentity(credential, audience string) (application.AccessCallbackIdentity, error) {
	if len(credential) == 0 || len(credential) > 256 || audience != s.registration.config.AccessAudience {
		return application.AccessCallbackIdentity{}, fmt.Errorf("access callback binding is invalid")
	}
	s.registration.mu.Lock()
	defer s.registration.mu.Unlock()
	if !s.registration.used || s.registration.selectedProtocol == nil ||
		subtle.ConstantTimeCompare([]byte(credential), []byte(s.registration.callbackCredential)) != 1 {
		return application.AccessCallbackIdentity{}, fmt.Errorf("access callback binding is invalid")
	}
	identity := application.AccessCallbackIdentity{
		PluginID: s.registration.config.ConfiguredPluginID, PluginInstanceID: s.registration.config.PluginInstanceID,
		Audience: s.registration.config.AccessAudience, ProtocolMajor: s.registration.selectedProtocol.GetMajor(),
		ProtocolMinor: s.registration.selectedProtocol.GetMinor(),
	}
	return identity, nil
}

func (s *accessAuthorizationServer) ExchangeAccessToken(ctx context.Context, request *pluginv1.ExchangeAccessTokenRequest) (*pluginv1.ExchangeAccessTokenResponse, error) {
	identity, err := s.callbackIdentity(request.GetCallbackCredential(), request.GetAudience())
	if err != nil {
		return nil, err
	}
	decision, err := s.handler.ExchangeAccessToken(ctx, identity, request.GetAccessSessionId(), request.GetOneTimeToken())
	if err != nil {
		return nil, err
	}
	return exchangeDecision(decision), nil
}

func (s *accessAuthorizationServer) RecheckAccessLease(ctx context.Context, request *pluginv1.RecheckAccessLeaseRequest) (*pluginv1.RecheckAccessLeaseResponse, error) {
	identity, err := s.callbackIdentity(request.GetCallbackCredential(), request.GetAudience())
	if err != nil {
		return nil, err
	}
	decision, err := s.handler.RecheckAccessLease(ctx, identity, request.GetAccessSessionId(), request.GetAuthorizationLease())
	if err != nil {
		return nil, err
	}
	response := &pluginv1.RecheckAccessLeaseResponse{
		AuthorizationLease: decision.Lease, ValidUntil: timestamppb.New(decision.ValidUntil),
		RecheckAfter: durationpb.New(decision.RecheckAfter),
	}
	if !decision.Granted {
		response.AuthorizationLease = ""
		response.ValidUntil = nil
		response.RecheckAfter = nil
		response.Error = accessDecisionError(decision)
	}
	return response, nil
}

func (s *accessAuthorizationServer) EndAccessLease(ctx context.Context, request *pluginv1.EndAccessLeaseRequest) (*pluginv1.EndAccessLeaseResponse, error) {
	identity, err := s.callbackIdentity(request.GetCallbackCredential(), request.GetAudience())
	if err != nil {
		return nil, err
	}
	ended, err := s.handler.EndAccessLease(ctx, identity, request.GetAccessSessionId(), request.GetAuthorizationLease(), request.GetReason())
	if err != nil {
		return nil, err
	}
	return &pluginv1.EndAccessLeaseResponse{Ended: ended}, nil
}

func exchangeDecision(decision application.AccessAuthorizationDecision) *pluginv1.ExchangeAccessTokenResponse {
	response := &pluginv1.ExchangeAccessTokenResponse{
		AuthorizationLease: decision.Lease, ValidUntil: timestamppb.New(decision.ValidUntil),
		RecheckAfter: durationpb.New(decision.RecheckAfter),
	}
	if !decision.Granted {
		response.AuthorizationLease = ""
		response.ValidUntil = nil
		response.RecheckAfter = nil
		response.Error = accessDecisionError(decision)
	}
	return response
}

func accessDecisionError(decision application.AccessAuthorizationDecision) *pluginv1.StructuredError {
	code, message := decision.Code, decision.SafeMessage
	if code == "" {
		code = "access_denied"
	}
	if message == "" {
		message = "Access authorization was denied"
	}
	return &pluginv1.StructuredError{Code: code, ErrorClass: "authorization_denied", SafeMessage: message}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func confinedUnixEndpoint(endpoint, runtimeDir string, expectedMode os.FileMode) (string, error) {
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
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != expectedMode {
		return "", fmt.Errorf("registered plugin service endpoint type or mode is invalid")
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
		if c.connection != nil {
			_ = c.connection.Close()
		}
		if c.cancel != nil {
			c.cancel()
		}
		if c.process != nil {
			select {
			case <-c.process.done:
			case <-time.After(3 * time.Second):
				if c.command != nil && c.command.Process != nil {
					_ = c.command.Process.Kill()
				}
				<-c.process.done
			}
		}
		if c.server != nil {
			c.server.Stop()
		}
		if c.listener != nil {
			_ = c.listener.Close()
		}
		if c.removeRuntime {
			if err := os.RemoveAll(c.runtimeDir); err != nil && result == nil {
				result = err
			}
		} else if c.runtimeDir != "" {
			if err := os.Remove(filepath.Join(c.runtimeDir, "registration.sock")); err != nil && !errors.Is(err, os.ErrNotExist) && result == nil {
				result = err
			}
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

// CheckHealth performs one bounded runtime health RPC for private readiness
// evaluation. It reports a terminated child even if the gRPC connection has
// not observed closure yet.
func (c *Client) CheckHealth(ctx context.Context) error {
	if c == nil || c.Runtime == nil {
		return fmt.Errorf("plugin runtime is unavailable")
	}
	if c.process != nil {
		select {
		case <-c.process.done:
			return fmt.Errorf("plugin process exited")
		default:
		}
	}
	health, err := c.Runtime.Health(ctx, &pluginv1.HealthRequest{})
	if err != nil {
		return fmt.Errorf("plugin health RPC failed: %w", err)
	}
	if health.GetStatus() != pluginv1.HealthResponse_STATUS_HEALTHY {
		return fmt.Errorf("plugin is not healthy")
	}
	return nil
}

func (c *Client) Logs() (stdout, stderr string) {
	if c == nil || c.stdout == nil || c.stderr == nil {
		return "", ""
	}
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
var _ pluginv1.AccessAuthorizationServiceServer = (*accessAuthorizationServer)(nil)
