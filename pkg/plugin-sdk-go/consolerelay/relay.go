// Package consolerelay transports one provider-issued console over protected
// same-host IPC. It does not authorize users or accept remote target addresses.
package consolerelay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const MaxConnections = 128
const connectWindow = 10 * time.Second

type Grant struct {
	ID    string
	Token string
}

type Server struct {
	directory string
	peerUID   uint32
	mu        sync.Mutex
	closed    bool
	active    map[*relay]struct{}
	wg        sync.WaitGroup
	lock      *os.File
}

type relay struct {
	listener *net.UnixListener
	upstream net.Conn
	cancel   context.CancelFunc
}

func NewServer(directory string, peerUID uint32) (*Server, error) {
	if err := validateDirectory(directory, uint32(os.Getuid())); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("console relay directory is already in use or invalid")
	}
	entries, err := os.ReadDir(directory)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if len(name) != 64 || strings.ToLower(name) != name {
				continue
			}
			if _, decodeErr := hex.DecodeString(name); decodeErr != nil {
				continue
			}
			info, statErr := os.Lstat(filepath.Join(directory, name))
			if statErr != nil || info.Mode()&os.ModeSocket == 0 || !ownedBy(info, uint32(os.Getuid())) {
				err = fmt.Errorf("invalid stale relay entry")
				break
			}
			if err = os.Remove(filepath.Join(directory, name)); err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("console relay directory cleanup failed")
	}
	return &Server{directory: directory, peerUID: peerUID, active: make(map[*relay]struct{}), lock: lock}, nil
}

// Publish takes ownership of upstream, including on error. Only one authenticated
// peer can consume the grant. Expiry bounds the complete stream, not just launch.
func (s *Server) Publish(upstream net.Conn, expires time.Time) (Grant, error) {
	if upstream == nil {
		return Grant{}, fmt.Errorf("console upstream is absent")
	}
	fail := func() (Grant, error) { _ = upstream.Close(); return Grant{}, fmt.Errorf("console relay unavailable") }
	if !expires.After(time.Now()) || expires.After(time.Now().Add(time.Hour)) {
		return fail()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.active) >= MaxConnections {
		return fail()
	}
	var id, token [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fail()
	}
	if _, err := rand.Read(token[:]); err != nil {
		return fail()
	}
	grant := Grant{ID: hex.EncodeToString(id[:]), Token: base64.RawURLEncoding.EncodeToString(token[:])}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(s.directory, grant.ID), Net: "unix"})
	if err != nil {
		return fail()
	}
	if err := os.Chmod(listener.Addr().String(), 0o660); err != nil {
		_ = listener.Close()
		return fail()
	}
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	r := &relay{listener: listener, upstream: upstream, cancel: cancel}
	s.active[r] = struct{}{}
	s.wg.Add(1)
	go s.serve(ctx, r, grant.Token)
	return grant, nil
}

func (s *Server) serve(ctx context.Context, r *relay, token string) {
	defer s.wg.Done()
	defer func() {
		r.cancel()
		_ = r.listener.Close()
		_ = r.upstream.Close()
		s.mu.Lock()
		delete(s.active, r)
		s.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { _ = r.listener.Close(); _ = r.upstream.Close() })
	defer stop()
	deadline := time.Now().Add(connectWindow)
	if expiry, ok := ctx.Deadline(); ok && expiry.Before(deadline) {
		deadline = expiry
	}
	_ = r.listener.SetDeadline(deadline)
	for {
		peer, err := r.listener.AcceptUnix()
		if err != nil {
			return
		}
		closePeer := context.AfterFunc(ctx, func() { _ = peer.Close() })
		_ = peer.SetDeadline(deadline)
		if err := verifyPeer(peer, s.peerUID); err != nil {
			_ = peer.Close()
			closePeer()
			continue
		}
		expected := []byte("LCR1 " + token + "\n")
		received := make([]byte, len(expected))
		if _, err := io.ReadFull(peer, received); err != nil || subtle.ConstantTimeCompare(expected, received) != 1 {
			_ = peer.Close()
			closePeer()
			continue
		}
		_ = r.listener.Close() // removes the path and prevents replay
		if _, err := peer.Write([]byte("OK\n")); err != nil {
			_ = peer.Close()
			closePeer()
			return
		}
		_ = peer.SetDeadline(time.Time{})
		done := make(chan struct{})
		go func() { _, _ = io.Copy(r.upstream, peer); _ = r.upstream.Close(); _ = peer.Close(); close(done) }()
		_, _ = io.Copy(peer, r.upstream)
		_ = peer.Close()
		_ = r.upstream.Close()
		<-done
		closePeer()
		return
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for r := range s.active {
		r.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
	s.mu.Unlock()
	return nil
}

type Client struct {
	Directory   string
	ProviderUID uint32
}

func (c Client) Validate() error { return validateDirectory(c.Directory, c.ProviderUID) }

func (c Client) Connect(ctx context.Context, grant Grant) (net.Conn, error) {
	if c.Validate() != nil || len(grant.ID) != 64 || strings.ToLower(grant.ID) != grant.ID {
		return nil, fmt.Errorf("console relay binding is invalid")
	}
	if _, err := hex.DecodeString(grant.ID); err != nil {
		return nil, fmt.Errorf("console relay binding is invalid")
	}
	token, err := base64.RawURLEncoding.DecodeString(grant.Token)
	if err != nil || len(token) != 32 || base64.RawURLEncoding.EncodeToString(token) != grant.Token {
		return nil, fmt.Errorf("console relay binding is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(c.Directory, grant.ID))
	if err != nil {
		return nil, fmt.Errorf("console relay connection failed")
	}
	peer, ok := raw.(*net.UnixConn)
	if !ok || verifyPeer(peer, c.ProviderUID) != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("console relay peer rejected")
	}
	deadline, _ := ctx.Deadline()
	_ = peer.SetDeadline(deadline)
	fail := func() (net.Conn, error) { _ = peer.Close(); return nil, fmt.Errorf("console relay grant rejected") }
	if _, err := io.WriteString(peer, "LCR1 "+grant.Token+"\n"); err != nil {
		return fail()
	}
	var ack [3]byte
	if _, err := io.ReadFull(peer, ack[:]); err != nil || string(ack[:]) != "OK\n" {
		return fail()
	}
	_ = peer.SetDeadline(time.Time{})
	return peer, nil
}

func validateDirectory(directory string, owner uint32) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || len(directory) > 40 {
		return fmt.Errorf("console relay directory is invalid")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	info, statErr := os.Lstat(directory)
	if err != nil || statErr != nil || resolved != directory || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || !ownedBy(info, owner) {
		return errors.New("console relay directory must be protected and owned by the provider")
	}
	return nil
}
