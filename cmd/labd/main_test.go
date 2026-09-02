package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoAuthListenerClassification(t *testing.T) {
	for _, testCase := range []struct {
		name string
		ip   net.IP
		want bool
	}{
		{"IPv4 loopback", net.ParseIP("127.0.0.1"), true},
		{"IPv6 loopback", net.ParseIP("::1"), true},
		{"IPv4 unspecified", net.ParseIP("0.0.0.0"), false},
		{"private interface", net.ParseIP("192.168.10.2"), false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			listener := stubListener{address: &net.TCPAddr{IP: testCase.ip, Port: 8080}}
			if got := listenerIsLoopback(listener); got != testCase.want {
				t.Fatalf("listenerIsLoopback(%s) = %t, want %t", testCase.ip, got, testCase.want)
			}
		})
	}
}

func TestHealthcheckRequiresHealthyV1ServerInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/server-info" {
			t.Errorf("healthcheck path = %q", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"apiVersion":"v1"}`))
	}))
	defer server.Close()
	t.Setenv("LABCENTRAL_HEALTHCHECK_URL", server.URL+"/api/v1/server-info")
	if err := healthcheck(); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheckRejectsWrongVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"apiVersion":"v2"}`))
	}))
	defer server.Close()
	t.Setenv("LABCENTRAL_HEALTHCHECK_URL", server.URL)
	if err := healthcheck(); err == nil {
		t.Fatal("healthcheck accepted an unexpected API version")
	}
}

type stubListener struct{ address net.Addr }

func (stubListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (stubListener) Close() error              { return nil }
func (s stubListener) Addr() net.Addr          { return s.address }
