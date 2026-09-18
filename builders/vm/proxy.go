package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// EgressProxy is the only guest-initiated connection exposed by QEMU's
// restricted user network. It never accepts an arbitrary forwarding target.
type EgressProxy struct {
	charge      func(int) error
	hosts       map[string]bool
	budget      atomic.Int64
	connections chan struct{}
	resolver    *net.Resolver
	transport   *http.Transport
}

func NewEgressProxy(hosts []string, maximumBytes int64) (*EgressProxy, error) {
	if len(hosts) > 64 || maximumBytes <= 0 {
		return nil, fmt.Errorf("explicit bounded egress policy required")
	}
	p := &EgressProxy{hosts: map[string]bool{}, connections: make(chan struct{}, 16), resolver: net.DefaultResolver}
	p.budget.Store(maximumBytes)
	for _, host := range hosts {
		if !validEgressHost(host) || p.hosts[host] {
			return nil, fmt.Errorf("invalid or duplicate egress host")
		}
		p.hosts[host] = true
	}
	p.transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := p.dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &meteredConnection{Conn: c, charge: p.account}, nil
	}, ForceAttemptHTTP2: false, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second}
	return p, nil
}
func validEgressHost(host string) bool {
	if host != strings.ToLower(host) || strings.TrimSpace(host) != host || len(host) > 253 || !strings.Contains(host, ".") || strings.ContainsAny(host, "/@:%\\?#\x00") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return net.ParseIP(host) == nil
}
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func (p *EgressProxy) target(address string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !p.hosts[host] || (port != "80" && port != "443") {
		return "", "", fmt.Errorf("egress target denied")
	}
	return host, port, nil
}
func (p *EgressProxy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := p.target(address)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return nil, fmt.Errorf("egress resolution failed")
	}
	for _, ip := range addresses {
		if !publicAddress(ip) {
			return nil, fmt.Errorf("nonpublic egress address denied")
		}
	}
	// Dial the checked address directly; never resolve it a second time.
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range addresses {
		connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
	}
	return nil, fmt.Errorf("egress connection failed")
}
func (p *EgressProxy) account(n int) error {
	if p.charge != nil {
		if err := p.charge(n); err != nil {
			p.budget.Store(0)
			return err
		}
	}
	if p.budget.Add(-int64(n)) < 0 {
		return fmt.Errorf("egress byte budget exhausted")
	}
	return nil
}

// HTTP accounting includes request and response headers, including empty-body
// responses. CONNECT accounting remains on its opaque bidirectional stream.
type meteredConnection struct {
	net.Conn
	charge func(int) error
}

func (c *meteredConnection) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b[:min(len(b), 32<<10)])
	if n > 0 {
		if e := c.charge(n); e != nil {
			c.Close()
			return 0, e
		}
	}
	return n, err
}
func (c *meteredConnection) Write(b []byte) (int, error) {
	if err := c.charge(len(b)); err != nil {
		c.Close()
		return 0, err
	}
	return c.Conn.Write(b)
}
func (p *EgressProxy) copy(dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if err := p.account(n); err != nil {
				return err
			}
			if written, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return writeErr
			} else if written != n {
				return io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.connections <- struct{}{}:
		defer func() { <-p.connections }()
	default:
		http.Error(w, "egress connection limit", 429)
		return
	}
	if p.budget.Load() <= 0 {
		http.Error(w, "egress byte limit", 429)
		return
	}
	if r.Method == http.MethodConnect {
		if _, port, err := p.target(r.Host); err != nil || port != "443" {
			http.Error(w, "egress target denied", 403)
			return
		}
		upstream, err := p.dial(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, "egress unavailable", 502)
			return
		}
		defer upstream.Close()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "tunneling unavailable", 500)
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		stop := context.AfterFunc(r.Context(), func() { client.Close(); upstream.Close() })
		defer stop()
		deadline := time.Now().Add(5 * time.Minute)
		client.SetDeadline(deadline)
		upstream.SetDeadline(deadline)
		if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err = buffered.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { defer close(done); _ = p.copy(client, upstream); client.Close() }()
		_ = p.copy(upstream, buffered)
		upstream.Close()
		<-done
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "egress method denied", 403)
		return
	}
	destination := r.URL
	if destination.Scheme != "http" || destination.User != nil || destination.Fragment != "" {
		http.Error(w, "egress URL denied", 403)
		return
	}
	port := destination.Port()
	if port == "" {
		port = "80"
	}
	if _, _, err := p.target(net.JoinHostPort(destination.Hostname(), port)); err != nil {
		http.Error(w, "egress target denied", 403)
		return
	}
	// Construct a fresh request; guest proxy credentials and hop-by-hop headers
	// must never cross the configured mirror boundary.
	target := &url.URL{Scheme: "http", Host: destination.Host, Path: destination.Path, RawPath: destination.RawPath, RawQuery: destination.RawQuery}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	for _, name := range []string{"Accept", "Accept-Encoding", "Range", "If-None-Match", "If-Modified-Since", "User-Agent"} {
		if value := r.Header.Get(name); len(value) <= 8192 {
			request.Header.Set(name, value)
		}
	}
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "egress unavailable", 502)
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Range", "ETag", "Last-Modified", "Location"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
func (p *EgressProxy) Close() { p.transport.CloseIdleConnections() }
