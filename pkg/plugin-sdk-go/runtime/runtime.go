package runtime

import (
	"context"
	"fmt"
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
	Info     *pluginv1.PluginInfo
	Runtime  pluginv1.PluginRuntimeServiceServer
	Provider pluginv1.ProviderServiceServer
}

func Serve(ctx context.Context, services Services) error {
	if services.Info == nil || services.Runtime == nil || services.Provider == nil {
		return fmt.Errorf("plugin info, runtime service, and provider service are required")
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
	listener, err := net.Listen("unix", servicePath)
	if err != nil {
		return fmt.Errorf("listen on plugin service socket: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(servicePath, 0o600); err != nil {
		return fmt.Errorf("restrict plugin service socket: %w", err)
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(8<<20), grpc.MaxSendMsgSize(8<<20))
	pluginv1.RegisterPluginRuntimeServiceServer(server, services.Runtime)
	pluginv1.RegisterProviderServiceServer(server, services.Provider)
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
	registerContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	response, err := registration.RegisterPlugin(registerContext, &pluginv1.RegisterPluginRequest{
		LaunchToken:        os.Getenv(launch.Token),
		ConfiguredPluginId: os.Getenv(launch.ConfiguredPluginID),
		PluginInstanceId:   os.Getenv(launch.PluginInstanceID),
		ServiceEndpoint:    "unix://" + servicePath,
		Plugin:             services.Info,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("register plugin: %w", err)
	}
	if response.GetSelectedProtocol().GetMajor() != 1 {
		return fmt.Errorf("host selected unsupported protocol")
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
