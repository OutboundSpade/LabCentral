package remote

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestJumpPinsBothHopsAndReusesGateway(t *testing.T) {
	machine := sshtest.New(t, nil, func(string) string { return "verified" })
	gateway := sshtest.New(t, func(dest string) string {
		if dest == "10.77.0.11:22" {
			return machine.Address
		}
		return ""
	}, nil)
	key, _ := sshtest.Key(t)
	d := &Dialer{GatewayAddress: gateway.Address, GatewayHostKey: gateway.HostKey, PrivateKey: key}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 3 {
		client, err := d.Dial(ctx, "10.77.0.11", "lab", key, machine.HostKey)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Run(ctx, client, "observational fixture")
		client.Close()
		if err != nil || string(out) != "verified" {
			t.Fatal(string(out), err)
		}
	}
	if gateway.Connections.Load() != 1 || gateway.Channels.Load() != 3 || machine.Connections.Load() != 3 {
		t.Fatal("transport was not reused", gateway.Connections.Load(), gateway.Channels.Load(), machine.Connections.Load())
	}
	_, wrong := sshtest.Key(t)
	client, err := d.Dial(ctx, "10.77.0.11", "lab", key, wrong)
	if client != nil {
		client.Close()
	}
	if err == nil {
		t.Fatal("wrong machine pin accepted")
	}
	bad := &Dialer{GatewayAddress: gateway.Address, GatewayHostKey: wrong, PrivateKey: key}
	defer bad.Close()
	if c, err := bad.Dial(ctx, "10.77.0.11", "lab", key, machine.HostKey); err == nil {
		c.Close()
		t.Fatal("wrong gateway pin accepted")
	}
	gateway.DropConnections()
	// The current transport has closed; a later channel may reconnect with the
	// same gateway pin, without replaying a previous command.
	client, err = d.Dial(ctx, "10.77.0.11", "lab", key, machine.HostKey)
	if err != nil {
		t.Fatal("fresh channel after outage", err)
	}
	if _, err := Run(ctx, client, "next operation"); err != nil {
		t.Fatal(err)
	}
	client.Close()
	if gateway.Connections.Load() != 2 {
		t.Fatal("gateway not re-established")
	}
	gateway.Close()
	if c, err := d.Dial(ctx, "10.77.0.11", "lab", key, machine.HostKey); err == nil {
		c.Close()
		t.Fatal("closed gateway accepted")
	}
	// Failure must not create a direct fallback to the machine.
	if machine.Connections.Load() != 4 {
		t.Fatal("unexpected machine handshake")
	}
}

func TestJumpTCPAndCancelledSecondHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
	}()
	gateway := sshtest.New(t, func(dest string) string {
		if dest == "10.77.0.11:22" {
			return listener.Addr().String()
		}
		return ""
	}, nil)
	key, host := sshtest.Key(t)
	d := &Dialer{GatewayAddress: gateway.Address, GatewayHostKey: gateway.HostKey, PrivateKey: key}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if c, err := d.Dial(ctx, "10.77.0.11", "lab", key, host); err == nil {
		c.Close()
		t.Fatal("hung handshake accepted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled channel leaked")
	}
	if _, err := d.DialTCP(context.Background(), "tcp", "10.77.0.99:8443"); err == nil {
		t.Fatal("denied forward accepted")
	}
	d.Close()
	if _, err := d.DialTCP(context.Background(), "tcp", "10.77.0.11:22"); err == nil {
		t.Fatal("closed dialer accepted")
	}
}
