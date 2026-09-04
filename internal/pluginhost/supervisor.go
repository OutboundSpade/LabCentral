package pluginhost

import (
	"context"
	"fmt"
	"sync"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Supervise keeps stable RPC clients while replacing only the failed plugin.
// A restart must negotiate exactly the original identity, schemas and features;
// upgrades require an explicit deployment restart, never an implicit state change.
func Supervise(ctx context.Context, initial *Client, start func(context.Context) (*Client, error)) *Client {
	ctx, cancel := context.WithCancel(ctx)
	state := &supervisedState{current: initial, cancel: cancel, done: make(chan struct{}), start: start, info: proto.Clone(initial.Info).(*wire.PluginInfo), protocol: proto.Clone(initial.SelectedProtocol).(*wire.ProtocolVersion)}
	proxy := &switchingConnection{state: state}
	out := &Client{Provider: wire.NewProviderServiceClient(proxy), Access: wire.NewAccessServiceClient(proxy), Runtime: wire.NewPluginRuntimeServiceClient(proxy), Info: state.info, SelectedProtocol: state.protocol, supervised: state}
	go state.run(ctx)
	return out
}

type supervisedState struct {
	mu       sync.RWMutex
	current  *Client
	cancel   context.CancelFunc
	done     chan struct{}
	start    func(context.Context) (*Client, error)
	info     *wire.PluginInfo
	protocol *wire.ProtocolVersion
	failures int
}

func (s *supervisedState) client() *Client { s.mu.RLock(); defer s.mu.RUnlock(); return s.current }
func (s *supervisedState) set(c *Client)   { s.mu.Lock(); s.current = c; s.mu.Unlock() }
func (s *supervisedState) run(ctx context.Context) {
	defer close(s.done)
	defer func() {
		if c := s.client(); c != nil {
			_ = c.Close()
		}
		s.set(nil)
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	unhealthy := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if c := s.client(); c != nil {
			check, cancel := context.WithTimeout(ctx, time.Second)
			err := c.CheckHealth(check)
			cancel()
			if err == nil {
				unhealthy = 0
				continue
			}
			unhealthy++
			if unhealthy < 3 {
				continue
			}
			s.set(nil)
			_ = c.Close()
			unhealthy = 0
		}
		// Five attempts per deployment, including repeatedly crashing children. A
		// circuit-open plugin stays unavailable until an explicit service restart.
		if s.failures >= 5 {
			continue
		}
		delay := time.Second * time.Duration(1<<s.failures)
		s.failures++
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		next, err := s.start(ctx)
		if err != nil {
			continue
		}
		if !proto.Equal(next.Info, s.info) || !proto.Equal(next.SelectedProtocol, s.protocol) {
			_ = next.Close()
			continue
		}
		s.set(next)
	}
}
func (c *Client) Available() bool {
	if c == nil {
		return false
	}
	if c.supervised != nil {
		return c.supervised.client() != nil
	}
	if done := c.Done(); done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	return true
}

type switchingConnection struct{ state *supervisedState }

func (c *switchingConnection) connection() (*grpc.ClientConn, error) {
	v := c.state.client()
	if v == nil {
		return nil, status.Error(codes.Unavailable, "plugin unavailable; recovery or operator restart required")
	}
	if v.connection == nil {
		return nil, fmt.Errorf("plugin connection is unavailable")
	}
	return v.connection, nil
}
func (c *switchingConnection) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	v, e := c.connection()
	if e != nil {
		return e
	}
	return v.Invoke(ctx, method, args, reply, opts...)
}
func (c *switchingConnection) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	v, e := c.connection()
	if e != nil {
		return nil, e
	}
	return v.NewStream(ctx, desc, method, opts...)
}
