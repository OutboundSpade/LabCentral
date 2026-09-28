package remote

import (
	"bytes"
	"context"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"sync"
	"time"
)

func Dial(ctx context.Context, address, user string, private []byte, host string) (*ssh.Client, error) {
	key, e := ssh.ParsePrivateKey(private)
	if e != nil {
		return nil, e
	}
	pub, _, _, _, e := ssh.ParseAuthorizedKey([]byte(host))
	if e != nil {
		return nil, e
	}
	config := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(pub), Timeout: 15 * time.Second}
	conn, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(address, "22"))
	if e != nil {
		return nil, e
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	cc, ch, r, e := ssh.NewClientConn(conn, address, config)
	if e != nil {
		conn.Close()
		return nil, e
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, ch, r), nil
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		c, e := Dial(ctx, address, user, private, host)
		if e == nil {
			_, e = Run(ctx, c, "cloud-init status --wait >/dev/null && test -d /home/lab")
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
