package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// The adapter authenticates the first frame with the core-minted one-time
// session token and renews core authorization leases. Browser credentials are
// deliberately stripped at this process boundary.
func withExtensionStreamProxy(next http.Handler, entry installedExtension) http.Handler {
	target, _ := url.Parse(entry.StreamEndpoint) // Validated during installation.
	prefix := streamBasePath(entry)
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		r.Header.Del("X-CSRF-Token")
	}
	proxy.Transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "access stream unavailable", http.StatusServiceUnavailable)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			next.ServeHTTP(w, r)
			return
		}
		suffix := strings.TrimPrefix(r.URL.Path, prefix)
		if r.Method != "GET" || r.URL.RawQuery != "" || !strings.HasPrefix(suffix, "/v1/access/") || !strings.HasSuffix(suffix, "/websocket") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.NotFound(w, r)
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
		proxy.ServeHTTP(w, r)
	})
}
