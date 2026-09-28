package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/wire"
	"github.com/OutboundSpade/LabCentral/v2/internal/wireconvert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestTypedProtocolRoundTrip(t *testing.T) {
	c := lc.Command{ClientPrivateKey: []byte("transient"), Operation: "op", Environment: lc.Environment{Version: 2, Platform: "aws", Bases: map[string]lc.Base{"ubuntu": {ID: "ami", DiskGiB: 8}}}, Source: lc.Source{Files: []lc.File{{Path: "lab.yaml", Content: []byte("version: 2")}}}, Prior: lc.Resource{Data: json.RawMessage(`{"native":"state"}`)}}
	w := &wire.Command{}
	if e := wireconvert.Encode(c, w); e != nil {
		t.Fatal(e)
	}
	var got lc.Command
	if e := wireconvert.Decode(w, &got); e != nil {
		t.Fatal(e)
	}
	if got.Environment.Bases["ubuntu"].ID != "ami" || string(got.Prior.Data) != string(c.Prior.Data) || string(got.ClientPrivateKey) != "transient" {
		t.Fatalf("lossy protocol %+v", got)
	}
}

type fake struct{}

func (fake) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	o(lc.Event{Version: 2, Stage: "test"})
	return lc.Result{Message: "complete"}, nil
}
func TestSocketProtocolRejectsWrongVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(t.TempDir(), "rpc")
	go Serve(ctx, sock, fake{})
	conn, e := grpc.NewClient("passthrough:///"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	deadline, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	stream, e := wire.NewPlatformClient(conn).Execute(deadline, &wire.Request{Version: 1}, grpc.WaitForReady(true))
	if e == nil {
		_, e = stream.Recv()
	}
	if e == nil {
		t.Fatal("wrong version accepted")
	}
}

type blockedProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p blockedProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	if c.Action == "machine.create" {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return lc.Result{}, ctx.Err()
		}
	}
	return lc.Result{}, nil
}

type testStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s testStream) Context() context.Context { return s.ctx }
func (s testStream) Send(r *wire.Reply) error {
	if r.Error != nil {
		return fmt.Errorf("provider error: %v", r.Error)
	}
	return nil
}
func TestAccessProceedsDuringUnrelatedProvisioning(t *testing.T) {
	p := blockedProvider{make(chan struct{}), make(chan struct{})}
	server := &Server{Provider: p}
	env := lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	execute := func(ctx context.Context, action, owner string) error {
		c := &wire.Command{}
		if err := wireconvert.Encode(lc.Command{Action: action, Owner: owner, Environment: env}, c); err != nil {
			return err
		}
		return server.Execute(&wire.Request{Version: 2, Command: c}, testStream{ctx: ctx})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- execute(ctx, "machine.create", "new-lab") }()
	select {
	case <-p.started:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := execute(ctx, "access.open", "running-lab"); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type deletionProvider struct {
	entered chan string
	release chan struct{}
}

func (p deletionProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.entered <- c.Prior.Kind
	if c.Prior.Kind == "machine" {
		select {
		case <-p.release:
		case <-ctx.Done():
			return lc.Result{}, ctx.Err()
		}
	}
	return lc.Result{Absent: true}, nil
}
func TestMachineDeletionConcurrencyAndNetworkFence(t *testing.T) {
	p := deletionProvider{make(chan string, 4), make(chan struct{})}
	server := &Server{Provider: p}
	env := lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 4)
	execute := func(kind, id string) {
		c := &wire.Command{}
		if err := wireconvert.Encode(lc.Command{Action: "resource.destroy", Owner: "lab", Environment: env, Prior: lc.Resource{Kind: kind, ID: id, Owner: "lab"}}, c); err != nil {
			done <- err
			return
		}
		done <- server.Execute(&wire.Request{Version: 2, Command: c}, testStream{ctx: ctx})
	}
	go execute("machine", "one")
	go execute("machine", "two")
	for n := 0; n < 2; n++ {
		select {
		case kind := <-p.entered:
			if kind != "machine" {
				t.Fatal(kind)
			}
		case <-ctx.Done():
			t.Fatal("machine deletions serialized")
		}
	}
	go execute("machine", "one")
	select {
	case <-p.entered:
		t.Fatal("retry crossed same-machine fence")
	case <-time.After(30 * time.Millisecond):
	}
	go execute("network", "net")
	select {
	case <-p.entered:
		t.Fatal("network crossed machine fence")
	case <-time.After(30 * time.Millisecond):
	}
	close(p.release)
	for n := 0; n < 4; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	for n := 0; n < 2; n++ {
		seen[<-p.entered]++
	}
	if seen["network"] != 1 || seen["machine"] != 1 {
		t.Fatal(seen)
	}
}
