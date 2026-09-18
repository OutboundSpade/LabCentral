package vm

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestHTTPHeaderTrafficConsumesEgressBudget(t *testing.T) {
	proxy, err := NewEgressProxy([]string{"mirror.example.com"}, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()
	proxy.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return &meteredConnection{Conn: client, charge: proxy.account}, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := http.ReadRequest(bufio.NewReader(server)); err == nil {
			_, _ = io.WriteString(server, "HTTP/1.1 204 No Content\r\nX-Padding: "+strings.Repeat("x", 512)+"\r\n\r\n")
		}
	}()
	out := httptest.NewRecorder()
	proxy.ServeHTTP(out, httptest.NewRequest("GET", "http://mirror.example.com/", nil))
	client.Close()
	<-done
	if out.Code != http.StatusBadGateway || proxy.budget.Load() >= 0 {
		t.Fatal("empty-body response bypassed egress budget", out.Code, proxy.budget.Load())
	}
}

func TestBuilderProxyRejectsInternalAndUnconfiguredTargets(t *testing.T) {
	proxy, err := NewEgressProxy([]string{"registry.example.com"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	for _, target := range []string{"https://registry.example.com/", "http://127.0.0.1/", "http://registry.example.com:8006/", "http://unconfigured.example.com/", "http://user:password@registry.example.com/"} {
		request := httptest.NewRequest("GET", target, nil)
		out := httptest.NewRecorder()
		proxy.ServeHTTP(out, request)
		if out.Code != http.StatusForbidden {
			t.Fatalf("accepted %s: %d", target, out.Code)
		}
	}
	for _, value := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "192.168.1.1", "100.64.0.1", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "192.0.2.1"} {
		if publicAddress(netip.MustParseAddr(value)) {
			t.Fatalf("internal address accepted: %s", value)
		}
	}
	if !publicAddress(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address rejected")
	}
}
