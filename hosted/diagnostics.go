package hosted

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"strings"
	"time"
)

func validateDiagnostics(address string) error {
	if address == "" {
		return nil
	}
	parsed, err := netip.ParseAddrPort(address)
	if err != nil || !parsed.Addr().IsLoopback() || parsed.Addr().Zone() != "" {
		return fmt.Errorf("diagnostics must be a literal loopback IP:port")
	}
	return nil
}

// NewDiagnosticsServer binds before returning so configuration and port failures
// stop startup. No profiling handler is registered on the public service mux.
func NewDiagnosticsServer(address string) (*http.Server, net.Listener, error) {
	if address == "" {
		return nil, nil, nil
	}
	if err := validateDiagnostics(address); err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}, listener, nil
}

type apiResponse struct {
	http.ResponseWriter
	status int
}

func (w *apiResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *apiResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *apiResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(p)
}
func (w *apiResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
func (w *apiResponse) Flush() {
	if w.status == 0 {
		w.status = 200
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func slowAPI(next http.Handler, threshold time.Duration, report func(string, ...any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		response := &apiResponse{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(response, r)
		if elapsed := time.Since(start); elapsed >= threshold {
			status := response.status
			if status == 0 {
				status = 200
			}
			// Pattern is the registered route, never a URL, query, user or session ID.
			report("slow API request route=%q status=%d duration=%s", r.Pattern, status, elapsed)
		}
	})
}

var apiRequestLog = log.Printf
