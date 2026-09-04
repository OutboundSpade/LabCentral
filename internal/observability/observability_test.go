package observability

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHealthSeparatesLivenessAndReadiness(t *testing.T) {
	databaseReady := false
	system, err := New("test", "replica-test", []Check{{Name: "database", Run: func(context.Context) error {
		if !databaseReady {
			return errors.New("canary detail must not be exposed")
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	assertHealth(t, system.Handler(), "/live", http.StatusOK, `"status":"live"`)
	assertHealth(t, system.Handler(), "/ready", http.StatusServiceUnavailable, `"failedChecks":["startup","database"]`)
	system.MarkStartupComplete()
	databaseReady = true
	assertHealth(t, system.Handler(), "/ready", http.StatusOK, `"status":"ready"`)
}

func TestReadinessChecksRunWithinOneSharedDeadline(t *testing.T) {
	system, err := New("test", "replica-test", []Check{
		{Name: "first", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		{Name: "second", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	})
	if err != nil {
		t.Fatal(err)
	}
	system.MarkStartupComplete()
	started := time.Now()
	assertHealth(t, system.Handler(), "/ready", http.StatusServiceUnavailable, `"failedChecks":["first","second"]`)
	if elapsed := time.Since(started); elapsed > checkTimeout+time.Second {
		t.Fatalf("parallel readiness took %s", elapsed)
	}
}

func TestHTTPInstrumentationPreservesFlushing(t *testing.T) {
	system, err := New("test", "replica-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var flushed atomic.Bool
	handler := system.InstrumentHTTP(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(response).Flush(); err != nil {
			t.Errorf("flush through instrumentation: %v", err)
		}
		flushed.Store(true)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/operations/example/events", nil))
	if !flushed.Load() {
		t.Fatal("instrumented streaming handler did not flush")
	}
	writer := &streamingWriter{header: make(http.Header)}
	hijackHandler := system.InstrumentHTTP(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _, err := http.NewResponseController(response).Hijack()
		if !errors.Is(err, errTestHijack) {
			t.Errorf("hijack through instrumentation: %v", err)
		}
	}))
	hijackHandler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/access-proxy/example", nil))
	if !writer.hijacked {
		t.Fatal("instrumented WebSocket handler did not preserve hijacking")
	}
}

var errTestHijack = errors.New("test hijack reached")

type streamingWriter struct {
	header   http.Header
	hijacked bool
}

func (writer *streamingWriter) Header() http.Header         { return writer.header }
func (*streamingWriter) Write(contents []byte) (int, error) { return len(contents), nil }
func (*streamingWriter) WriteHeader(int)                    {}
func (writer *streamingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	writer.hijacked = true
	return nil, nil, errTestHijack
}

func TestHTTPTraceOmitsPathQueryAndIdentifiers(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	system, err := New("test", "replica-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := system.InstrumentHTTP(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/projects/tenant-canary?token=secret-canary", nil))
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "HTTP API" {
		t.Fatalf("spans = %#v", spans)
	}
	serialized := spans[0].Name
	for _, field := range spans[0].Attributes {
		serialized += string(field.Key) + field.Value.Emit()
	}
	for _, forbidden := range []string{"tenant-canary", "secret-canary", "/api/v1/projects"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("trace contains forbidden value %q", forbidden)
		}
	}
}

func TestMetricsAndHTTPInstrumentationUseOnlyBoundedLabels(t *testing.T) {
	system, err := New("test", "replica-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := system.InstrumentHTTP(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusCreated)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/tenant-secret?token=token-secret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	response := httptest.NewRecorder()
	system.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`labcentral_http_requests_total{endpoint_family="api",method="POST",status_class="2xx"} 1`,
		`labcentral_build_info{replica="replica-test",version="test"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	for _, forbidden := range []string{"tenant-secret", "token-secret", "/api/v1/projects"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics contain forbidden value %q", forbidden)
		}
	}
}

func TestBackgroundAndPluginMetricsAreBounded(t *testing.T) {
	system, err := New("test", "replica-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	system.ObserveBackground("target_refresh", errors.New("secret failure"))
	system.SetPlugin("provider", "proxmox", true)
	response := httptest.NewRecorder()
	system.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`labcentral_background_errors_total{component="target_refresh"} 1`,
		`labcentral_plugin_up{category="provider",kind="proxmox"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	if strings.Contains(body, "secret failure") {
		t.Fatal("metrics exposed an error string")
	}
}

func assertHealth(t *testing.T, handler http.Handler, path string, status int, body string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != status || !strings.Contains(response.Body.String(), body) {
		t.Fatalf("%s response = %d %s", path, response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s response is cacheable", path)
	}
}
