package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/launch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Services struct {
	Info                     *pluginv1.PluginInfo
	Runtime                  pluginv1.PluginRuntimeServiceServer
	Provider                 pluginv1.ProviderServiceServer
	Access                   pluginv1.AccessServiceServer
	ConfigureAccessCallbacks func(AccessCallbacks) error
}

// AccessCallbacks contains the host-authenticated callback channel supplied to
// one access-only plugin process. Credential is launch scoped and must never be
// logged, persisted, or sent to a browser.
type AccessCallbacks struct {
	Authorization pluginv1.AccessAuthorizationServiceClient
	Credential    string
	Audience      string
	Protocol      *pluginv1.ProtocolVersion
}

func Serve(ctx context.Context, services Services) error {
	if err := validateServices(services); err != nil {
		return err
	}
	registrationPath, err := environmentUnixPath(launch.RegistrationEndpoint)
	if err != nil {
		return err
	}
	servicePath, err := environmentUnixPath(launch.ServiceEndpoint)
	if err != nil {
		return err
	}
	runtimeDir := os.Getenv(launch.RuntimeDirectory)
	if err := requireConfinedPath(servicePath, runtimeDir); err != nil {
		return err
	}
	socketMode, err := configuredSocketMode()
	if err != nil {
		return err
	}
	if err := prepareRuntimeDirectory(runtimeDir, socketMode); err != nil {
		return err
	}
	if err := removeStaleSocket(servicePath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", servicePath)
	if err != nil {
		return fmt.Errorf("listen on plugin service socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(servicePath)
	}()
	if err := os.Chmod(servicePath, socketMode); err != nil {
		return fmt.Errorf("restrict plugin service socket: %w", err)
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(8<<20), grpc.MaxSendMsgSize(8<<20))
	pluginv1.RegisterPluginRuntimeServiceServer(server, services.Runtime)
	if services.Provider != nil {
		pluginv1.RegisterProviderServiceServer(server, services.Provider)
	}
	if services.Access != nil {
		pluginv1.RegisterAccessServiceServer(server, services.Access)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer server.Stop()

	connection, err := grpc.NewClient(
		"passthrough:///labcentral-registration",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", registrationPath)
		}),
	)
	if err != nil {
		return fmt.Errorf("create registration client: %w", err)
	}
	defer connection.Close()
	registration := pluginv1.NewPluginRegistrationServiceClient(connection)
	launchToken, err := configuredLaunchToken()
	if err != nil {
		return err
	}
	registrationTimeout := 8 * time.Second
	if socketMode == 0o660 {
		registrationTimeout = 2 * time.Minute
	}
	registerContext, cancel := context.WithTimeout(ctx, registrationTimeout)
	response, err := registration.RegisterPlugin(registerContext, &pluginv1.RegisterPluginRequest{
		LaunchToken:        launchToken,
		ConfiguredPluginId: os.Getenv(launch.ConfiguredPluginID),
		PluginInstanceId:   os.Getenv(launch.PluginInstanceID),
		ServiceEndpoint:    "unix://" + servicePath,
		Plugin:             services.Info,
	}, grpc.WaitForReady(true))
	cancel()
	if err != nil {
		return fmt.Errorf("register plugin: %w", err)
	}
	selected, ranges := selectedProtocol(services.Info, response)
	if !supportsProtocol(ranges, selected) {
		return fmt.Errorf("host selected unsupported protocol")
	}
	if services.Access != nil {
		if response.GetAccessCallbackCredential() == "" || response.GetAccessAudience() == "" {
			return fmt.Errorf("host omitted access callback binding")
		}
		if err := services.ConfigureAccessCallbacks(AccessCallbacks{
			Authorization: pluginv1.NewAccessAuthorizationServiceClient(connection),
			Credential:    response.GetAccessCallbackCredential(),
			Audience:      response.GetAccessAudience(),
			Protocol:      selected,
		}); err != nil {
			return fmt.Errorf("configure access callbacks: %w", err)
		}
	}
	heartbeatInterval := response.GetHeartbeatInterval().AsDuration()
	if heartbeatInterval <= 0 {
		return fmt.Errorf("host returned invalid heartbeat interval")
	}
	heartbeatDone := make(chan error, 1)
	go heartbeatLoop(ctx, registration, os.Getenv(launch.PluginInstanceID), heartbeatInterval, heartbeatDone)

	select {
	case <-ctx.Done():
		server.GracefulStop()
		return nil
	case err := <-heartbeatDone:
		return err
	case err := <-serverDone:
		return err
	}
}

func configuredSocketMode() (os.FileMode, error) {
	switch os.Getenv(launch.SocketMode) {
	case "", "0600":
		return 0o600, nil
	case "0660":
		return 0o660, nil
	default:
		return 0, fmt.Errorf("plugin socket mode is invalid")
	}
}

func configuredLaunchToken() (string, error) {
	direct, path := os.Getenv(launch.Token), os.Getenv(launch.TokenFile)
	if (direct == "") == (path == "") {
		return "", fmt.Errorf("exactly one plugin launch token source is required")
	}
	if direct != "" {
		if !validLaunchToken(direct) {
			return "", fmt.Errorf("plugin launch token is invalid")
		}
		return direct, nil
	}
	info, err := os.Lstat(path)
	mode := os.FileMode(0)
	if err == nil {
		mode = info.Mode().Perm()
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || mode&0o400 == 0 || mode&0o027 != 0 || info.Size() < 64 || info.Size() > 65 {
		return "", fmt.Errorf("plugin launch token file is not a protected bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open plugin launch token file: %w", err)
	}
	after, statErr := file.Stat()
	contents, readErr := io.ReadAll(io.LimitReader(file, 66))
	closeErr := file.Close()
	if statErr != nil || !os.SameFile(info, after) || after.Mode().Perm() != mode || readErr != nil || closeErr != nil || len(contents) > 65 {
		clear(contents)
		return "", fmt.Errorf("read plugin launch token file")
	}
	token := strings.TrimSuffix(string(contents), "\n")
	clear(contents)
	if !validLaunchToken(token) {
		return "", fmt.Errorf("plugin launch token file is invalid")
	}
	return token, nil
}

func validLaunchToken(value string) bool {
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

func prepareRuntimeDirectory(path string, socketMode os.FileMode) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("plugin runtime directory is invalid")
	}
	if socketMode == 0o660 {
		parent := filepath.Dir(path)
		resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
		parentInfo, parentErr := os.Lstat(parent)
		if resolveErr != nil || parentErr != nil || resolvedParent != parent || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("shared plugin runtime parent must be a protected real directory")
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(path, 0o770); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create shared plugin runtime directory: %w", err)
			}
			if err := os.Chmod(path, 0o770); err != nil {
				return fmt.Errorf("protect shared plugin runtime directory: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("inspect shared plugin runtime directory: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o007 != 0 {
		return fmt.Errorf("plugin runtime directory is not a protected real directory")
	}
	if socketMode == 0o600 && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("private plugin runtime directory mode must be 0700")
	}
	if socketMode == 0o660 && info.Mode().Perm()&0o070 != 0o070 {
		return fmt.Errorf("shared plugin runtime directory must grant group access")
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect plugin service socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("plugin service path contains a non-socket entry")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale plugin service socket: %w", err)
	}
	return nil
}

func validateServices(services Services) error {
	if services.Info == nil || services.Runtime == nil {
		return fmt.Errorf("plugin info and runtime service are required")
	}
	provider, access := services.Provider != nil, services.Access != nil
	if provider == access {
		return fmt.Errorf("exactly one provider or access service is required")
	}
	if provider {
		if !contains(services.Info.GetCategories(), "provider") || services.ConfigureAccessCallbacks != nil {
			return fmt.Errorf("provider service metadata is invalid")
		}
		return nil
	}
	if !contains(services.Info.GetCategories(), "access") || services.ConfigureAccessCallbacks == nil {
		return fmt.Errorf("access service metadata and callback configuration are required")
	}
	return nil
}

func selectedProtocol(info *pluginv1.PluginInfo, response *pluginv1.RegisterPluginResponse) (*pluginv1.ProtocolVersion, []*pluginv1.ProtocolVersionRange) {
	if contains(info.GetCategories(), "access") {
		return response.GetSelectedAccessProtocol(), info.GetSupportedAccessProtocols()
	}
	ranges := info.GetSupportedProviderProtocols()
	if len(ranges) == 0 {
		ranges = info.GetSupportedProtocols()
	}
	selected := response.GetSelectedProviderProtocol()
	if selected == nil {
		selected = response.GetSelectedProtocol()
	}
	return selected, ranges
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func supportsProtocol(ranges []*pluginv1.ProtocolVersionRange, selected *pluginv1.ProtocolVersion) bool {
	if selected == nil {
		return false
	}
	for _, versionRange := range ranges {
		if versionRange != nil && versionRange.GetMajor() == selected.GetMajor() &&
			versionRange.GetMinMinor() <= selected.GetMinor() && selected.GetMinor() <= versionRange.GetMaxMinor() {
			return true
		}
	}
	return false
}

func heartbeatLoop(ctx context.Context, client pluginv1.PluginRegistrationServiceClient, instanceID string, interval time.Duration, done chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			sequence++
			heartbeatContext, cancel := context.WithTimeout(ctx, interval)
			_, err := client.Heartbeat(heartbeatContext, &pluginv1.HeartbeatRequest{
				PluginInstanceId: instanceID,
				Sequence:         sequence,
				SentAt:           timestamppb.Now(),
			})
			cancel()
			if err != nil {
				done <- fmt.Errorf("plugin heartbeat failed: %w", err)
				return
			}
		}
	}
}

func environmentUnixPath(key string) (string, error) {
	value := os.Getenv(key)
	if !strings.HasPrefix(value, "unix://") {
		return "", fmt.Errorf("%s must contain a Unix socket endpoint", key)
	}
	value = strings.TrimPrefix(value, "unix://")
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s socket path must be absolute", key)
	}
	return value, nil
}

func requireConfinedPath(value, runtimeDir string) error {
	if runtimeDir == "" || !filepath.IsAbs(runtimeDir) {
		return fmt.Errorf("plugin runtime directory is invalid")
	}
	relative, err := filepath.Rel(runtimeDir, value)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("plugin service path escapes the private runtime directory")
	}
	return nil
}
