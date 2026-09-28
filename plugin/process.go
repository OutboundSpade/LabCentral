// Package plugin connects the library to separately running platform executables.
package plugin

import (
	"context"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/wire"
	"github.com/OutboundSpade/LabCentral/v2/internal/wireconvert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const MaxMessage = 48 << 20

type Process struct {
	client wire.PlatformClient
	conn   *grpc.ClientConn
	cmd    *exec.Cmd
	dir    string
	done   chan error
	once   sync.Once
}

func Start(ctx context.Context, binary, state string) (*Process, error) {
	abs, e := filepath.Abs(binary)
	if e != nil {
		return nil, e
	}
	state, e = filepath.Abs(state)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(state, 0700); e != nil {
		return nil, e
	}
	dir, e := os.MkdirTemp("", "lc-v2-")
	if e != nil {
		return nil, e
	}
	socket := filepath.Join(dir, "rpc.sock")
	cmd := exec.Command(abs, "--socket", socket, "--state", state)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	log, e := os.OpenFile(filepath.Join(state, "plugin.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		os.RemoveAll(dir)
		return nil, e
	}
	cmd.Stderr = log
	cmd.Stdout = log
	if e = cmd.Start(); e != nil {
		log.Close()
		os.RemoveAll(dir)
		return nil, e
	}
	p := &Process{cmd: cmd, dir: dir, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait(); log.Close() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, e = os.Stat(socket); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			p.Close()
			return nil, ctx.Err()
		case e = <-p.done:
			p.Close()
			return nil, fmt.Errorf("provider exited during startup: %v", e)
		case <-deadline.C:
			p.Close()
			return nil, fmt.Errorf("provider startup timeout")
		case <-tick.C:
		}
	}
	conn, e := grpc.NewClient("passthrough:///"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessage), grpc.MaxCallSendMsgSize(MaxMessage)))
	if e != nil {
		p.Close()
		return nil, e
	}
	p.conn = conn
	p.client = wire.NewPlatformClient(conn)
	return p, nil
}
func (p *Process) Close() error {
	var err error
	p.once.Do(func() {
		if p.conn != nil {
			err = p.conn.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(3 * time.Second):
				_ = p.cmd.Process.Kill()
			}
		}
		os.RemoveAll(p.dir)
	})
	return err
}
func (p *Process) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	req := &wire.Request{Version: 2, Command: &wire.Command{}}
	if e := wireconvert.Encode(c, req.Command); e != nil {
		return lc.Result{}, e
	}
	stream, e := p.client.Execute(ctx, req)
	if e != nil {
		return lc.Result{}, e
	}
	var result lc.Result
	terminal := false
	for {
		reply, e := stream.Recv()
		if errors.Is(e, io.EOF) {
			if !terminal {
				return result, fmt.Errorf("recovery_required: provider closed without terminal result")
			}
			return result, nil
		}
		if e != nil {
			return result, e
		}
		if terminal {
			return result, fmt.Errorf("protocol: data after terminal result")
		}
		switch {
		case reply.Error != nil:
			var er lc.Error
			if e = wireconvert.Decode(reply.Error, &er); e != nil {
				return result, e
			}
			return result, &er
		case reply.Result != nil:
			if e = wireconvert.Decode(reply.Result, &result); e != nil {
				return result, e
			}
			terminal = true
		case reply.Event != nil:
			if o != nil {
				var ev lc.Event
				if e = wireconvert.Decode(reply.Event, &ev); e != nil {
					return result, e
				}
				o(ev)
			}
		default:
			return result, fmt.Errorf("protocol: empty reply")
		}
	}
}

type Server struct {
	wire.UnimplementedPlatformServer
	Provider lc.Provider
	mu       sync.RWMutex
	owners   sync.Map
}

func (s *Server) Execute(req *wire.Request, stream grpc.ServerStreamingServer[wire.Reply]) error {
	if req.Version != 2 || req.Command == nil {
		return fmt.Errorf("unsupported protocol version")
	}
	var c lc.Command
	if e := wireconvert.Decode(req.Command, &c); e != nil {
		return e
	}
	if e := lc.ValidateEnvironment(c.Environment); e != nil {
		return sendError(stream, e)
	}
	if c.Action == "image.build" {
		if e := lc.Validate(c.Source); e != nil {
			return sendError(stream, e)
		}
	}
	// Only independent machine deletions share infrastructure execution. Network
	// deletion, creation and builds remain exclusive. Access retains owner fencing.
	machineDelete := c.Action == "resource.destroy" && c.Prior.Kind == "machine"
	if c.Action != "access.open" && c.Action != "access.close" {
		if machineDelete {
			s.mu.RLock()
			defer s.mu.RUnlock()
		} else {
			s.mu.Lock()
			defer s.mu.Unlock()
		}
	}
	value, _ := s.owners.LoadOrStore(c.Owner, &sync.RWMutex{})
	ownerLock := value.(*sync.RWMutex)
	if machineDelete {
		ownerLock.RLock()
		defer ownerLock.RUnlock()
	} else {
		ownerLock.Lock()
		defer ownerLock.Unlock()
	}
	if err := stream.Context().Err(); err != nil {
		return err
	}
	var sendErr error
	result, e := s.Provider.Execute(stream.Context(), c, func(event lc.Event) {
		if sendErr != nil {
			return
		}
		ev := &wire.Event{}
		if sendErr = wireconvert.Encode(event, ev); sendErr == nil {
			sendErr = stream.Send(&wire.Reply{Event: ev})
		}
	})
	if sendErr != nil {
		return sendErr
	}
	if e != nil {
		return sendError(stream, e)
	}
	out := &wire.Result{}
	if e = wireconvert.Encode(result, out); e != nil {
		return e
	}
	return stream.Send(&wire.Reply{Result: out})
}
func sendError(stream grpc.ServerStreamingServer[wire.Reply], err error) error {
	e := &lc.Error{Code: lc.RecoveryRequired, Message: err.Error()}
	var typed *lc.Error
	if errors.As(err, &typed) {
		e = typed
	}
	out := &wire.Error{}
	if err = wireconvert.Encode(e, out); err != nil {
		return err
	}
	return stream.Send(&wire.Reply{Error: out})
}
func Serve(ctx context.Context, socket string, p lc.Provider) error {
	if _, e := os.Lstat(socket); !os.IsNotExist(e) {
		return fmt.Errorf("socket already exists")
	}
	l, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer l.Close()
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	s := grpc.NewServer(grpc.MaxRecvMsgSize(MaxMessage), grpc.MaxSendMsgSize(MaxMessage))
	wire.RegisterPlatformServer(s, &Server{Provider: p})
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-done:
		}
	}()
	return s.Serve(l)
}
