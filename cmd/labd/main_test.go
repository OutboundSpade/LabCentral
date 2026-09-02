package main

import (
	"io"
	"net"
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

type stubListener struct{ address net.Addr }

func (stubListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (stubListener) Close() error              { return nil }
func (s stubListener) Addr() net.Addr          { return s.address }
