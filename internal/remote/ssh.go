package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"sync"
	"time"
)

// Dialer owns one pinned gateway connection for a bounded operation. Close it
// after its machine clients and TCP channels. Reconnecting a failed transport
// opens new channels only; it never replays guest commands or existing sessions.
type Dialer struct {
	GatewayAddress, GatewayHostKey string
	PrivateKey                     []byte
	mu                             sync.Mutex
	gateway                        *ssh.Client
	closed                         bool
}

func sshAddress(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "22")
}
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.gateway != nil {
		return d.gateway.Close()
	}
	return nil
}
func (d *Dialer) DialTCP(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("SSH dialer closed")
	}
	if d.GatewayAddress == "" {
		d.mu.Unlock()
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, address)
	}
	if d.gateway == nil {
		client, err := Dial(ctx, d.GatewayAddress, "lab", d.PrivateKey, d.GatewayHostKey)
		if err != nil {
			d.mu.Unlock()
			return nil, fmt.Errorf("gateway SSH: %w", err)
		}
		d.gateway = client
	}
	gateway := d.gateway
	d.mu.Unlock()
	conn, err := gateway.DialContext(ctx, network, address)
	if err == nil {
		return conn, nil
	}
	var denied *ssh.OpenChannelError
	if errors.As(err, &denied) || ctx.Err() != nil {
		return nil, err
	}
	// A new read/access operation may outlive a gateway reboot. Re-establish
	// its transport with the same pin; channel rejection never triggers this.
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("SSH dialer closed")
	}
	if d.gateway == gateway {
		gateway.Close()
		d.gateway = nil
		replacement, e := Dial(ctx, d.GatewayAddress, "lab", d.PrivateKey, d.GatewayHostKey)
		if e != nil {
			d.mu.Unlock()
			return nil, fmt.Errorf("gateway SSH: %w", e)
		}
		d.gateway = replacement
	}
	gateway = d.gateway
	d.mu.Unlock()
	return gateway.DialContext(ctx, network, address)
}
func (d *Dialer) Dial(ctx context.Context, address, user string, private []byte, host string) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	config, err := clientConfig(user, private, host)
	if err != nil {
		return nil, err
	}
	conn, err := d.DialTCP(ctx, "tcp", sshAddress(address))
	if err != nil {
		return nil, err
	}
	return handshake(ctx, conn, address, config)
}
func clientConfig(user string, private []byte, host string) (*ssh.ClientConfig, error) {
	key, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return nil, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host))
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(pub), HostKeyAlgorithms: []string{pub.Type()}}, nil
}
func handshake(ctx context.Context, conn net.Conn, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	// SSH channels do not implement deadlines. Cancellation closes this channel,
	// never the reused gateway connection or an unrelated machine's channel.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cc, ch, r, err := ssh.NewClientConn(conn, address, config)
	if !stop() {
		conn.Close()
		if cc != nil {
			cc.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(cc, ch, r), nil
}
func Dial(ctx context.Context, address, user string, private []byte, host string) (*ssh.Client, error) {
	return (&Dialer{}).Dial(ctx, address, user, private, host)
}
func Run(ctx context.Context, c *ssh.Client, command string) ([]byte, error) {
	s, e := c.NewSession()
	if e != nil {
		return nil, e
	}
	defer s.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-done:
		}
	}()
	var out limitedOutput
	s.Stdout = &out
	s.Stderr = &out
	e = s.Run(command)
	b := out.Bytes()
	if out.Exceeded {
		return nil, fmt.Errorf("guest output exceeds bound")
	}
	return b, e
}
func Wait(ctx context.Context, address, user string, private []byte, host string) error {
	return WaitWithDialer(ctx, &Dialer{}, address, user, private, host)
}

// cloud-init exits 2 for "done" with recoverable warnings, such as an unreachable IPv6
// metadata endpoint on a network without a default route. Only a hard error (exit 1)
// or a missing provisioned user leaves the guest unready.
const readinessCommand = `cloud-init status --wait >/dev/null; rc=$?; { [ "$rc" -eq 0 ] || [ "$rc" -eq 2 ]; } && test -d /home/lab`

func WaitWithDialer(ctx context.Context, d *Dialer, address, user string, private []byte, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		c, e := d.Dial(ctx, address, user, private, host)
		if e == nil {
			_, e = Run(ctx, c, readinessCommand)
			c.Close()
			if e == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("SSH readiness: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

type limitedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	Exceeded bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := (1 << 20) - w.buffer.Len()
	if remaining < n {
		w.Exceeded = true
		p = p[:remaining]
	}
	_, e := w.buffer.Write(p)
	return n, e
}

func (w *limitedOutput) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}
