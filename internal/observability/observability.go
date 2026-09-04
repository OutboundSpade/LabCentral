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

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
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
	durableJobs        *prometheus.GaugeVec
	durableJobAge      *prometheus.GaugeVec
	outboxPending      *prometheus.GaugeVec
	outboxAge          *prometheus.GaugeVec
	operationState     *prometheus.GaugeVec
	operationCompleted *prometheus.CounterVec
	operationDuration  *prometheus.HistogramVec
	accessSessions     *prometheus.GaugeVec
	resourceState      *prometheus.GaugeVec
	cleanupLeftovers   *prometheus.GaugeVec
	targetSnapshots    prometheus.Gauge
	targetStale        prometheus.Gauge
	targetOldestAge    prometheus.Gauge
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
		durableJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "durable", Name: "jobs", Help: "Durable jobs by bounded operational condition.",
		}, []string{"state"}),
		durableJobAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "durable", Name: "job_oldest_age_seconds", Help: "Age of the oldest queued job or expired lease.",
		}, []string{"condition"}),
		outboxPending: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "outbox", Name: "pending", Help: "Pending durable outbox deliveries by bounded stream.",
		}, []string{"stream"}),
		outboxAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "outbox", Name: "oldest_age_seconds", Help: "Age of the oldest pending durable outbox delivery.",
		}, []string{"stream"}),
		operationState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "operation", Name: "state", Help: "Durable operations by bounded lifecycle state.",
		}, []string{"state"}),
		operationCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "labcentral", Subsystem: "operation", Name: "completed_total", Help: "Operations completed by bounded kind and result in this process.",
		}, []string{"kind", "result"}),
		operationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "labcentral", Subsystem: "operation", Name: "duration_seconds", Help: "Worker execution duration for completed operations by bounded kind and result.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
		}, []string{"kind", "result"}),
		accessSessions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "access", Name: "sessions", Help: "Controlled access sessions by bounded lifecycle state.",
		}, []string{"state"}),
		resourceState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "resource", Name: "state", Help: "Durable resources by bounded lifecycle phase.",
		}, []string{"phase"}),
		cleanupLeftovers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "cleanup", Name: "leftovers", Help: "Resources and allocations requiring cleanup or ownership review.",
		}, []string{"kind"}),
		targetSnapshots: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "target", Name: "capability_snapshots", Help: "Persisted provider target capability snapshots.",
		}),
		targetStale: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "target", Name: "capability_stale", Help: "Persisted provider target capability snapshots past their validity bound.",
		}),
		targetOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "labcentral", Subsystem: "target", Name: "capability_oldest_age_seconds", Help: "Age of the oldest persisted provider target capability snapshot.",
		}),
	}
	registry.MustRegister(
		prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "labcentral", Name: "build_info", Help: "Static build and replica information.", ConstLabels: prometheus.Labels{"version": version, "replica": replicaID},
		}, func() float64 { return 1 }),
		system.ready, system.httpRequests, system.httpDuration, system.backgroundRuns,
		system.backgroundErrors, system.pluginUp, system.readinessCheckUp, system.readinessCheckTime,
		system.durableJobs, system.durableJobAge, system.outboxPending, system.outboxAge,
		system.operationState, system.operationCompleted, system.operationDuration,
		system.accessSessions, system.resourceState, system.cleanupLeftovers,
		system.targetSnapshots, system.targetStale, system.targetOldestAge,
	)
	system.initializeOperationalLabels()
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

// SetOperationalSnapshot replaces every aggregate runtime gauge. Only closed
// lifecycle values are accepted, so corrupt rows cannot create metric labels.
func (s *System) SetOperationalSnapshot(snapshot application.OperationalSnapshot) {
	s.durableJobs.WithLabelValues("queued").Set(float64(snapshot.Jobs.Queued))
	s.durableJobs.WithLabelValues("running").Set(float64(snapshot.Jobs.Running))
	s.durableJobs.WithLabelValues("recovery_required").Set(float64(snapshot.Jobs.RecoveryRequired))
	s.durableJobAge.WithLabelValues("queued").Set(nonNegativeSeconds(snapshot.Jobs.OldestQueuedAge))
	s.durableJobAge.WithLabelValues("expired_lease").Set(nonNegativeSeconds(snapshot.Jobs.OldestExpiredLeaseAge))
	s.outboxPending.WithLabelValues("job").Set(float64(snapshot.Outbox.JobPending))
	s.outboxPending.WithLabelValues("event").Set(float64(snapshot.Outbox.EventPending))
	s.outboxAge.WithLabelValues("job").Set(nonNegativeSeconds(snapshot.Outbox.OldestJobAge))
	s.outboxAge.WithLabelValues("event").Set(nonNegativeSeconds(snapshot.Outbox.OldestEventAge))

	resetGaugeLabels(s.operationState, operationStates)
	for _, count := range snapshot.Operations {
		if knownOperationState(count.State) {
			s.operationState.WithLabelValues(string(count.State)).Add(float64(count.Count))
		}
	}
	resetGaugeLabels(s.accessSessions, accessStates)
	for _, count := range snapshot.Access {
		if knownAccessState(count.State) {
			s.accessSessions.WithLabelValues(string(count.State)).Add(float64(count.Count))
		}
	}
	resetGaugeLabels(s.resourceState, resourcePhases)
	for _, count := range snapshot.Resources {
		if knownResourcePhase(count.Phase) {
			s.resourceState.WithLabelValues(string(count.Phase)).Add(float64(count.Count))
		}
	}
	s.cleanupLeftovers.WithLabelValues("resource_unknown").Set(float64(snapshot.Cleanup.UnknownResources))
	s.cleanupLeftovers.WithLabelValues("resource_cleanup_failed").Set(float64(snapshot.Cleanup.FailedResources))
	s.cleanupLeftovers.WithLabelValues("allocation_orphaned").Set(float64(snapshot.Cleanup.OrphanedAllocations))
	s.targetSnapshots.Set(float64(snapshot.Targets.Total))
	s.targetStale.Set(float64(snapshot.Targets.Stale))
	s.targetOldestAge.Set(nonNegativeSeconds(snapshot.Targets.OldestAge))
}

// ObserveOperation records one terminal worker execution. Kind and result are
// closed sets; unexpected values are rejected instead of becoming labels.
func (s *System) ObserveOperation(kind string, result domain.OperationState, duration time.Duration) {
	if kind != "apply" && kind != "destroy" || !terminalOperationState(result) {
		return
	}
	s.operationCompleted.WithLabelValues(kind, string(result)).Inc()
	s.operationDuration.WithLabelValues(kind, string(result)).Observe(nonNegativeSeconds(duration))
}

var operationStates = []string{
	string(domain.OperationQueued), string(domain.OperationRunning), string(domain.OperationCancelRequested),
	string(domain.OperationCompensating), string(domain.OperationSucceeded), string(domain.OperationFailed),
	string(domain.OperationCanceled), string(domain.OperationTimedOut), string(domain.OperationPartial),
}

var terminalOperationStates = []string{
	string(domain.OperationSucceeded), string(domain.OperationFailed), string(domain.OperationCanceled),
	string(domain.OperationTimedOut), string(domain.OperationPartial),
}

var accessStates = []string{
	string(domain.AccessSessionPending), string(domain.AccessSessionReady), string(domain.AccessSessionActive),
	string(domain.AccessSessionExpired), string(domain.AccessSessionRevoked), string(domain.AccessSessionFailed),
}

var resourcePhases = []string{
	string(domain.ResourcePending), string(domain.ResourceApplying), string(domain.ResourceReady),
	string(domain.ResourceFailed), string(domain.ResourceUnknown), string(domain.ResourceStopped),
	string(domain.ResourcePaused), string(domain.ResourceDestroying), string(domain.ResourceDestroyed),
	string(domain.ResourceCleanupFailed),
}

func (s *System) initializeOperationalLabels() {
	resetGaugeLabels(s.durableJobs, []string{"queued", "running", "recovery_required"})
	resetGaugeLabels(s.durableJobAge, []string{"queued", "expired_lease"})
	resetGaugeLabels(s.outboxPending, []string{"job", "event"})
	resetGaugeLabels(s.outboxAge, []string{"job", "event"})
	resetGaugeLabels(s.operationState, operationStates)
	resetGaugeLabels(s.accessSessions, accessStates)
	resetGaugeLabels(s.resourceState, resourcePhases)
	resetGaugeLabels(s.cleanupLeftovers, []string{"resource_unknown", "resource_cleanup_failed", "allocation_orphaned"})
	for _, kind := range []string{"apply", "destroy"} {
		for _, result := range terminalOperationStates {
			s.operationCompleted.WithLabelValues(kind, result)
			s.operationDuration.WithLabelValues(kind, result)
		}
	}
}

func resetGaugeLabels(gauge *prometheus.GaugeVec, labels []string) {
	for _, label := range labels {
		gauge.WithLabelValues(label).Set(0)
	}
}

func nonNegativeSeconds(duration time.Duration) float64 {
	if duration < 0 {
		return 0
	}
	return duration.Seconds()
}

func knownOperationState(state domain.OperationState) bool {
	for _, candidate := range operationStates {
		if string(state) == candidate {
			return true
		}
	}
	return false
}

func terminalOperationState(state domain.OperationState) bool {
	for _, candidate := range terminalOperationStates {
		if string(state) == candidate {
			return true
		}
	}
	return false
}

func knownAccessState(state domain.AccessSessionState) bool {
	for _, candidate := range accessStates {
		if string(state) == candidate {
			return true
		}
	}
	return false
}

func knownResourcePhase(phase domain.ResourcePhase) bool {
	for _, candidate := range resourcePhases {
		if string(phase) == candidate {
			return true
		}
	}
	return false
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
