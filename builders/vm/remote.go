package vm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type TLSFiles struct{ CA, Certificate, Key, ServerName string }

func (f TLSFiles) config(server bool) (*tls.Config, error) {
	for _, path := range []string{f.CA, f.Certificate, f.Key} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("TLS files must be protected bounded regular files")
		}
	}
	info, _ := os.Stat(f.Key)
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("TLS private key must be private")
	}
	certificate, err := tls.LoadX509KeyPair(f.Certificate, f.Key)
	if err != nil {
		return nil, fmt.Errorf("invalid worker TLS identity")
	}
	data, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("invalid worker CA")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, SessionTicketsDisabled: true}
	if server {
		config.ClientCAs = roots
		config.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		if f.ServerName == "" {
			return nil, fmt.Errorf("worker server name required")
		}
		config.RootCAs = roots
		config.ServerName = f.ServerName
	}
	return config, nil
}
func ServeWorker(ctx context.Context, address string, files TLSFiles, service *Service) error {
	config, err := files.config(true)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)), grpc.MaxRecvMsgSize(1<<20), grpc.MaxSendMsgSize(1<<20), grpc.MaxConcurrentStreams(32), grpc.ConnectionTimeout(10*time.Second))
	wire.RegisterPluginRuntimeServiceServer(server, service)
	wire.RegisterBuilderServiceServer(server, service)
	wire.RegisterArtifactTransferServiceServer(server, service)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Stop()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return err
	}
}

// Relay exposes exactly the installed builder protocol through the existing
// local plugin host. Only the authenticated worker connection leaves the core
// host, and no core credentials or database handle enter the worker protocol.
type Relay struct {
	kind string
	wire.UnimplementedPluginRuntimeServiceServer
	wire.UnimplementedBuilderServiceServer
	wire.UnimplementedArtifactTransferServiceServer
	connection *grpc.ClientConn
	runtime    wire.PluginRuntimeServiceClient
	builder    wire.BuilderServiceClient
	artifacts  wire.ArtifactTransferServiceClient
}

func NewRelay(address string, files TLSFiles, kind string) (*Relay, error) {
	if kind != "ansible" && kind != "dockerfile" && kind != "container-runtime" {
		return nil, fmt.Errorf("explicit builder kind required")
	}
	config, err := files.config(false)
	if err != nil {
		return nil, err
	}
	if _, _, err = net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("explicit worker host and port required")
	}
	conn, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20), grpc.MaxCallSendMsgSize(1<<20)))
	if err != nil {
		return nil, err
	}
	return &Relay{kind: kind, connection: conn, runtime: wire.NewPluginRuntimeServiceClient(conn), builder: wire.NewBuilderServiceClient(conn), artifacts: wire.NewArtifactTransferServiceClient(conn)}, nil
}
func (r *Relay) Close() error { return r.connection.Close() }
func (r *Relay) Health(ctx context.Context, in *wire.HealthRequest) (*wire.HealthResponse, error) {
	return r.runtime.Health(ctx, in)
}
func (r *Relay) Validate(ctx context.Context, in *wire.BuilderServiceValidateRequest) (*wire.BuilderServiceValidateResponse, error) {
	if err := r.requireKind(in.GetRecipe()); err != nil {
		return nil, err
	}
	return r.builder.Validate(ctx, in)
}
func (r *Relay) GetInvocation(ctx context.Context, in *wire.BuilderServiceGetInvocationRequest) (*wire.BuilderServiceGetInvocationResponse, error) {
	return r.builder.GetInvocation(ctx, in)
}
func (r *Relay) Cancel(ctx context.Context, in *wire.BuilderServiceCancelRequest) (*wire.BuilderServiceCancelResponse, error) {
	return r.builder.Cancel(ctx, in)
}
func (r *Relay) Build(in *wire.BuilderServiceBuildRequest, out grpc.ServerStreamingServer[wire.BuilderServiceBuildResponse]) error {
	if err := r.requireKind(in.GetRecipe()); err != nil {
		return err
	}
	stream, err := r.builder.Build(out.Context(), in)
	if err != nil {
		return err
	}
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err = out.Send(message); err != nil {
			return err
		}
	}
}
func (r *Relay) Read(in *wire.ArtifactTransferServiceReadRequest, out grpc.ServerStreamingServer[wire.ArtifactTransferServiceReadResponse]) error {
	stream, err := r.artifacts.Read(out.Context(), in)
	if err != nil {
		return err
	}
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err = out.Send(message); err != nil {
			return err
		}
	}
}
func (r *Relay) Write(in grpc.ClientStreamingServer[wire.ArtifactTransferServiceWriteRequest, wire.ArtifactTransferServiceWriteResponse]) error {
	stream, err := r.artifacts.Write(in.Context())
	if err != nil {
		return err
	}
	for {
		message, err := in.Recv()
		if err == io.EOF {
			result, err := stream.CloseAndRecv()
			if err != nil {
				return err
			}
			return in.SendAndClose(result)
		}
		if err != nil {
			return err
		}
		if err = stream.Send(message); err != nil {
			return err
		}
	}
}

func (r *Relay) requireKind(recipe *wire.BuildRecipe) error {
	schema := DockerfileSchema
	if r.kind == "ansible" {
		schema = AnsibleSchema
	}
	if r.kind == "container-runtime" {
		schema = containervm.RuntimeBuildSchema
	}
	if recipe.GetConfiguration().GetSchema().GetName() != schema {
		return status.Error(codes.InvalidArgument, "recipe does not match installed builder kind")
	}
	return nil
}
