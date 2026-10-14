// Package sshtest provides loopback-only SSH fixtures. It never executes commands.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

type Server struct {
	Address, HostKey      string
	Connections, Channels atomic.Int32
	listener              net.Listener
	mu                    sync.Mutex
	sockets               []net.Conn
	wg                    sync.WaitGroup
	// Forward maps the requested destination to a local fixture. Empty rejects it.
	Forward func(string) string
	// Exec returns fixture output; no shell is started.
	Exec func(string) string
}

func Key(t testing.TB) ([]byte, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(block)
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}
func New(t testing.TB, forward func(string) string, exec func(string) string) *Server {
	t.Helper()
	private, host := Key(t)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Address: listener.Addr().String(), HostKey: host, listener: listener, Forward: forward, Exec: exec}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	s.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.sockets = append(s.sockets, conn)
			s.mu.Unlock()
			s.wg.Go(func() {
				defer conn.Close()
				client, channels, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer client.Close()
				s.Connections.Add(1)
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					s.wg.Go(func() { s.channel(incoming) })
				}
			})
		}
	})
	t.Cleanup(s.Close)
	return s
}
func (s *Server) Close() {
	s.listener.Close()
	s.mu.Lock()
	for _, c := range s.sockets {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Server) channel(in ssh.NewChannel) {
	switch in.ChannelType() {
	case "direct-tcpip":
		var dest struct {
			Host       string
			Port       uint32
			Origin     string
			OriginPort uint32
		}
		if ssh.Unmarshal(in.ExtraData(), &dest) != nil || s.Forward == nil {
			in.Reject(ssh.Prohibited, "forward denied")
			return
		}
		target := s.Forward(net.JoinHostPort(dest.Host, fmt.Sprint(dest.Port)))
		if target == "" {
			in.Reject(ssh.Prohibited, "destination denied")
			return
		}
		conn, err := net.Dial("tcp", target)
		if err != nil {
			in.Reject(ssh.ConnectionFailed, "fixture closed")
			return
		}
		defer conn.Close()
		channel, reqs, err := in.Accept()
		if err != nil {
			return
		}
		defer channel.Close()
		go ssh.DiscardRequests(reqs)
		s.Channels.Add(1)
		done := make(chan struct{})
		go func() { io.Copy(conn, channel); conn.Close(); close(done) }()
		io.Copy(channel, conn)
		channel.Close()
		<-done
	case "session":
		ch, reqs, err := in.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for r := range reqs {
			if r.Type != "exec" {
				r.Reply(false, nil)
				continue
			}
			var cmd struct{ Command string }
			ssh.Unmarshal(r.Payload, &cmd)
			r.Reply(true, nil)
			if s.Exec != nil {
				io.WriteString(ch, s.Exec(cmd.Command))
			}
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		}
	default:
		in.Reject(ssh.UnknownChannelType, "unsupported")
	}
}

// DropConnections simulates a transport outage while retaining the same server
// identity and listener for a subsequent operation.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.sockets {
		c.Close()
	}
}
