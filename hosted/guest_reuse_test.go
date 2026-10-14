package hosted

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"golang.org/x/crypto/ssh"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reuseGuestEngine struct {
	Engine
	instance lc.Instance
	machine  lc.PlannedMachine
	resource lc.Resource
	key      []byte
}

func (e *reuseGuestEngine) WithSSHMaterial(_ context.Context, _, _ string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	return fn(e.instance, e.machine, e.resource, e.key)
}

// A loopback SSH fixture verifies connection reuse across programs. Its exec
// handler never executes author commands; the injected dialer uses an ephemeral
// port with SSH's fixed-host-key verifier, without requiring a privileged port.
func TestGuestJobReusesPinnedSSHAcrossPrograms(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(credential)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	var connections, executions atomic.Int32
	var server sync.WaitGroup
	server.Go(func() {
		socket, err := listener.Accept()
		if err != nil {
			return
		}
		defer socket.Close()
		conn, channels, requests, err := ssh.NewServerConn(socket, config)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			for request := range requests {
				if request.Type == "exec" {
					executions.Add(1)
					request.Reply(true, nil)
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					channel.Close()
					break
				}
				request.Reply(false, nil)
			}
		}
	})
	s, _, p := learningFixture(t)
	s.Config.Aggregate.MaxSSHSessions = 1
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, closeJob := guestJobContext(ctx)
	cache, _ := guestCacheFor(ctx)
	cache.dial = func(ctx context.Context, _, user string, private []byte, host string) (*ssh.Client, error) {
		key, err := ssh.ParsePrivateKey(private)
		if err != nil {
			return nil, err
		}
		pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host))
		if err != nil {
			return nil, err
		}
		socket, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		if err != nil {
			return nil, err
		}
		socket.SetDeadline(time.Now().Add(5 * time.Second))
		cc, channels, requests, err := ssh.NewClientConn(socket, listener.Addr().String(), &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(pinned)})
		if err != nil {
			socket.Close()
			return nil, err
		}
		socket.SetDeadline(time.Time{})
		return ssh.NewClient(cc, channels, requests), nil
	}

	defer func() { closeJob(); listener.Close(); server.Wait() }()
	var src lc.Source
	var b InstructorBundle
	if err = s.Store.Get(ctx, "source", p.Template, &src); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Get(ctx, "template-instructor", p.Template, &b); err != nil {
		t.Fatal(err)
	}
	plan, err := lc.PlanLab(src, s.env, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := plan.Machines[0]
	m.HostPublicKey = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	engine := &reuseGuestEngine{Engine: s.Engine, instance: lc.Instance{ID: "instance", Status: "ready", Plan: plan}, machine: m, resource: lc.Resource{Kind: "machine", Name: m.Name, ID: "vm", Owner: "instance", Address: "127.0.0.1", Complete: true}, key: key}
	s.Engine = engine
	step := b.Checks[0].Steps[0]
	for range 2 {
		if err = s.runGuestPlaybooks(ctx, Team{InstanceID: "instance"}, src, b, []GuestPlaybook{step, step}); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 || executions.Load() != 4 {
		t.Fatal("programs redialed or steps were lost", connections.Load(), executions.Load())
	}
	// Identity changes cannot silently reuse a previously verified connection.
	engine.machine.HostPublicKey = "changed pin"
	if err = s.runGuestPlaybooks(ctx, Team{InstanceID: "instance"}, src, b, []GuestPlaybook{step}); err == nil {
		t.Fatal("changed host pin accepted")
	}
	if executions.Load() != 4 {
		t.Fatal("changed pin executed a guest program")
	}
}
