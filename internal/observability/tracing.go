package observability

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type TraceConfig struct {
	Endpoint       string
	ServiceVersion string
	ReplicaID      string
	SampleRatio    float64
	TLSConfig      *tls.Config
	OnError        func(error)
}

// ConfigureTracing installs W3C propagation in all modes. An empty endpoint
// intentionally leaves the no-op provider installed and starts no exporter.
func ConfigureTracing(ctx context.Context, config TraceConfig) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if config.Endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	if config.ServiceVersion == "" || config.ReplicaID == "" || config.SampleRatio < 0 || config.SampleRatio > 1 {
		return nil, fmt.Errorf("trace service version, replica identity, and sample ratio are invalid")
	}
	if config.OnError != nil {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(config.OnError))
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     config.TLSConfig,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(config.Endpoint),
		otlptracehttp.WithHTTPClient(&http.Client{Transport: transport, Timeout: 10 * time.Second}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithTimeout(10*time.Second),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("configure OTLP trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second), sdktrace.WithExportTimeout(10*time.Second), sdktrace.WithMaxQueueSize(2048)),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "labcentral"),
			attribute.String("service.version", config.ServiceVersion),
			attribute.String("service.instance.id", config.ReplicaID),
		)),
	)
	otel.SetTracerProvider(provider)
	return func(shutdownContext context.Context) error {
		defer transport.CloseIdleConnections()
		return provider.Shutdown(shutdownContext)
	}, nil
}
