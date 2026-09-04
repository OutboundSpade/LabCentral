// Package observability exposes LabCentral's bounded operational health and
// telemetry surface. It deliberately avoids tenant, user, resource, target,
// and request identifiers in metric labels.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const checkTimeout = 2 * time.Second

// Check is one bounded readiness dependency. Name is operator-visible and
// must be a static low-cardinality identifier, never a tenant or resource ID.
type Check struct {
	Name string
	Run  func(context.Context) error
}

// System owns one isolated Prometheus registry and the health state for one
// labd process.
type System struct {
	registry *prometheus.Registry
	checks   []Check

	startupComplete atomic.Bool

	ready              prometheus.Gauge
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
	backgroundRuns     *prometheus.CounterVec
	backgroundErrors   *prometheus.CounterVec
	pluginUp           *prometheus.GaugeVec
	readinessCheckUp   *prometheus.GaugeVec
	readinessCheckTime *prometheus.HistogramVec
}

func New(version, replicaID string, checks []Check) (*System, error) {
	if version == "" || replicaID == "" {
		return nil, fmt.Errorf("observability version and replica identity are required")
	}
	seen := make(map[string]bool, len(checks))
	for _, check := range checks {
		if check.Name == "" || check.Run == nil || seen[check.Name] || !safeLabel(check.Name) {
			return nil, fmt.Errorf("readiness checks require unique safe names and functions")
		}
		seen[check.Name] = true
	}
	registry := prometheus.NewRegistry()
	system := &System{
		registry: registry,
		checks:   append([]Check(nil), checks...),
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "labcentral", Name: "ready", Help: "Whether this labd replica currently passes every readiness check.",
		}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "labcentral", Subsystem: "http", Name: "requests_total", Help: "Completed public HTTP requests by bounded method, endpoint family, and status class.",
		}, []string{"method", "endpoint_family", "status_class"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "labcentral", Subsystem: "http", Name: "request_duration_seconds", Help: "Public HTTP request duration by bounded method and endpoint family.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"method", "endpoint_family"}),
		backgroundRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "labcentral", Subsystem: "background", Name: "runs_total", Help: "Completed background iterations by bounded component and outcome.",
		}, []string{"component", "outcome"}),
		backgroundErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "labcentral", Subsystem: "background", Name: "errors_total", Help: "Background iteration errors by bounded component.",
		}, []string{"component"}),
		pluginUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "plugin", Name: "up", Help: "Whether the configured plugin category is running.",
		}, []string{"category", "kind"}),
		readinessCheckUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "readiness", Name: "check_up", Help: "Whether a bounded readiness dependency passed its latest check.",
		}, []string{"check"}),
		readinessCheckTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "labcentral", Subsystem: "readiness", Name: "check_duration_seconds", Help: "Readiness dependency check duration.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
		}, []string{"check"}),
	}
	registry.MustRegister(
		prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "labcentral", Name: "build_info", Help: "Static build and replica information.", ConstLabels: prometheus.Labels{"version": version, "replica": replicaID},
		}, func() float64 { return 1 }),
		system.ready, system.httpRequests, system.httpDuration, system.backgroundRuns,
		system.backgroundErrors, system.pluginUp, system.readinessCheckUp, system.readinessCheckTime,
	)
	return system, nil
}

func safeLabel(value string) bool {
	if len(value) > 48 {
		return false
	}
	for _, character := range value {
		if character != '_' && character != '-' && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

// MarkStartupComplete allows readiness only after all startup preflights,
// plugin handshakes, target refreshes, and worker construction have succeeded.
func (s *System) MarkStartupComplete() { s.startupComplete.Store(true) }

func (s *System) SetPlugin(category, kind string, up bool) {
	value := 0.0
	if up {
		value = 1
	}
	s.pluginUp.WithLabelValues(category, kind).Set(value)
}

func (s *System) ObserveBackground(component string, err error) {
	if !safeLabel(component) {
		component = "invalid"
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		s.backgroundErrors.WithLabelValues(component).Inc()
	}
	s.backgroundRuns.WithLabelValues(component, outcome).Inc()
}

func (s *System) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live", func(response http.ResponseWriter, _ *http.Request) {
		writeHealth(response, http.StatusOK, "live", nil)
	})
	mux.HandleFunc("GET /ready", s.serveReady)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.HTTPErrorOnError,
		Timeout:       10 * time.Second,
	}))
	return noStore(mux)
}

func (s *System) serveReady(response http.ResponseWriter, request *http.Request) {
	failed := make([]string, 0, len(s.checks)+1)
	if !s.startupComplete.Load() {
		failed = append(failed, "startup")
	}
	type result struct {
		name     string
		failed   bool
		duration time.Duration
	}
	checkContext, cancel := context.WithTimeout(request.Context(), checkTimeout)
	defer cancel()
	results := make(chan result, len(s.checks))
	for _, check := range s.checks {
		check := check
		go func() {
			started := time.Now()
			err := check.Run(checkContext)
			results <- result{name: check.Name, failed: err != nil, duration: time.Since(started)}
		}()
	}
	failedByName := make(map[string]bool, len(s.checks))
	for range s.checks {
		select {
		case check := <-results:
			failedByName[check.name] = check.failed
			s.readinessCheckTime.WithLabelValues(check.name).Observe(check.duration.Seconds())
			if check.failed {
				s.readinessCheckUp.WithLabelValues(check.name).Set(0)
			} else {
				s.readinessCheckUp.WithLabelValues(check.name).Set(1)
			}
		case <-checkContext.Done():
			for _, check := range s.checks {
				if _, exists := failedByName[check.Name]; !exists {
					failedByName[check.Name] = true
					s.readinessCheckUp.WithLabelValues(check.Name).Set(0)
				}
			}
		}
	}
	for _, check := range s.checks {
		if failedByName[check.Name] {
			failed = append(failed, check.Name)
		}
	}
	status := http.StatusOK
	state := "ready"
	if len(failed) > 0 {
		status, state = http.StatusServiceUnavailable, "not_ready"
		s.ready.Set(0)
	} else {
		s.ready.Set(1)
	}
	writeHealth(response, status, state, failed)
}

func writeHealth(response http.ResponseWriter, status int, state string, failed []string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(struct {
		SchemaVersion string   `json:"schemaVersion"`
		Status        string   `json:"status"`
		FailedChecks  []string `json:"failedChecks,omitempty"`
	}{SchemaVersion: "labcentral.health/v1", Status: state, FailedChecks: failed})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}

// InstrumentHTTP records only bounded labels and creates a manually controlled
// server span so request paths, query strings, tenant IDs, and tokens cannot
// become telemetry attributes.
func (s *System) InstrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method := boundedMethod(request.Method)
		family := endpointFamily(request.URL.Path)
		ctx := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
		ctx, span := otel.Tracer("labcentral/http").Start(ctx, "HTTP "+strings.ToUpper(family),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", method), attribute.String("labcentral.endpoint_family", family)),
		)
		metrics := httpsnoop.CaptureMetrics(next, response, request.WithContext(ctx))
		statusClass := strconv.Itoa(metrics.Code/100) + "xx"
		s.httpRequests.WithLabelValues(method, family, statusClass).Inc()
		s.httpDuration.WithLabelValues(method, family).Observe(metrics.Duration.Seconds())
		span.SetAttributes(attribute.Int("http.response.status_code", metrics.Code))
		if metrics.Code >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server error")
		}
		span.End()
	})
}

func boundedMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func endpointFamily(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/auth/"):
		return "auth"
	case strings.HasPrefix(path, "/api/v1/"):
		return "api"
	case strings.HasPrefix(path, "/access-proxy/"):
		return "access"
	default:
		return "ui"
	}
}
