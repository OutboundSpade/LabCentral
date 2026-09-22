//go:build linux

package consolerelay

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Server, Client) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lcr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := NewServer(dir, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, Client{Directory: dir, ProviderUID: uint32(os.Getuid())}
}

func TestRelaySingleUseBidirectionalAndDisconnect(t *testing.T) {
	s, c := fixture(t)
	up, remote := net.Pipe()
	defer remote.Close()
	g, err := s.Publish(up, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := c.Connect(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_ = remote.SetDeadline(time.Now().Add(time.Second))
	go func() { _, _ = io.WriteString(remote, "frame") }()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "frame" {
		t.Fatalf("frame: %q %v", buf, err)
	}
	go func() { _, _ = io.WriteString(conn, "input") }()
	if _, err := io.ReadFull(remote, buf); err != nil || string(buf) != "input" {
		t.Fatalf("input: %q %v", buf, err)
	}
	if replay, err := c.Connect(context.Background(), g); err == nil {
		_ = replay.Close()
		t.Fatal("grant replay accepted")
	}
	_ = conn.Close()
	if _, err := remote.Read(buf); err == nil {
		t.Fatal("disconnect retained upstream")
	}
	if _, err := os.Stat(filepath.Join(c.Directory, g.ID)); !os.IsNotExist(err) {
		t.Fatal("consumed socket path retained")
	}
}

func TestRelayWrongTokenDoesNotConsumeAndConcurrentReplay(t *testing.T) {
	s, c := fixture(t)
	up, remote := net.Pipe()
	defer remote.Close()
	g, err := s.Publish(up, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wrong := g
	wrong.Token = strings.Repeat("A", 43)
	if conn, err := c.Connect(context.Background(), wrong); err == nil {
		_ = conn.Close()
		t.Fatal("wrong token accepted")
	}
	var mu sync.Mutex
	var accepted []net.Conn
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := c.Connect(context.Background(), g)
			if err == nil {
				mu.Lock()
				accepted = append(accepted, conn)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	defer func() {
		for _, conn := range accepted {
			_ = conn.Close()
		}
	}()
	if len(accepted) != 1 {
		t.Fatalf("accepted %d peers", len(accepted))
	}
}

func TestRelayExpiryPeerIdentityAndShutdown(t *testing.T) {
	for _, scenario := range []string{"expired", "wrong-access-uid", "shutdown", "shutdown-during-auth", "live-expiry"} {
		t.Run(scenario, func(t *testing.T) {
			s, c := fixture(t)
			up, remote := net.Pipe()
			defer remote.Close()
			if scenario == "wrong-access-uid" {
				s.peerUID++
			}
			expires := time.Now().Add(time.Minute)
			if scenario == "expired" || scenario == "live-expiry" {
				expires = time.Now().Add(120 * time.Millisecond)
			}
			g, err := s.Publish(up, expires)
			if err != nil {
				t.Fatal(err)
			}
			var conn net.Conn
			switch scenario {
			case "expired":
				time.Sleep(180 * time.Millisecond)
			case "shutdown":
				conn, err = c.Connect(context.Background(), g)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = s.Close()
			case "shutdown-during-auth":
				conn, err = net.Dial("unix", filepath.Join(c.Directory, g.ID))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = s.Close()
			case "live-expiry":
				conn, err = c.Connect(context.Background(), g)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				time.Sleep(180 * time.Millisecond)
			}
			if conn, err := c.Connect(context.Background(), g); err == nil {
				_ = conn.Close()
				t.Fatal("invalid grant accepted")
			}
			_ = s.Close()
			_ = remote.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := remote.Read(make([]byte, 1)); err == nil {
				t.Fatal("upstream retained after invalidation")
			}
		})
	}
}

func TestRelayRejectsUnsafePathsAndBoundsPendingConnections(t *testing.T) {
	s, c := fixture(t)
	if _, err := NewServer("/tmp", uint32(os.Getuid())); err == nil {
		t.Fatal("writable directory accepted")
	}
	link := c.Directory + "-link"
	if err := os.Symlink(c.Directory, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	if _, err := NewServer(link, uint32(os.Getuid())); err == nil {
		t.Fatal("symlink accepted")
	}
	if conn, err := c.Connect(context.Background(), Grant{ID: "../../escape", Token: strings.Repeat("A", 43)}); err == nil {
		conn.Close()
		t.Fatal("path traversal accepted")
	}
	wrong := c
	wrong.ProviderUID++
	if wrong.Validate() == nil {
		t.Fatal("wrong provider identity accepted")
	}
	for range MaxConnections {
		up, remote := net.Pipe()
		defer remote.Close()
		if _, err := s.Publish(up, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	up, remote := net.Pipe()
	defer remote.Close()
	if _, err := s.Publish(up, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("relay capacity exceeded")
	}
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("rejected upstream leaked")
	}
}

func TestRelayRestartRemovesOnlyOwnedStaleSockets(t *testing.T) {
	s, c := fixture(t)
	if other, err := NewServer(c.Directory, uint32(os.Getuid())); err == nil {
		other.Close()
		t.Fatal("two provider processes acquired the same relay directory")
	}
	_ = s.Close()
	name := filepath.Join(c.Directory, strings.Repeat("a", 64))
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
	marker := filepath.Join(c.Directory, "operator-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(c.Directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("stale socket retained")
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "keep" {
		t.Fatal("unrelated operator file changed")
	}
}
