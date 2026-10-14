package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/wire"
	"github.com/OutboundSpade/LabCentral/v2/internal/wireconvert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSelectedImageFilesAndHostnamePeersCrossProviderProtocol(t *testing.T) {
	root := t.TempDir()
	declaration := []byte("version: 3\nname: private-source\nimages:\n  base:\n    base: ubuntu\n    build:\n      playbook: build.yml\n      files: [build.yml]\nmachines:\n  bank:\n    image: base\n    cpu: 1\n    memory: 1Gi\n    disk: 8Gi\n    access: [ssh]\n    hostnamePeers: [bank]\n  coach:\n    image: base\n    cpu: 1\n    memory: 1Gi\n    disk: 8Gi\n    access: [ssh]\n")
	declaration = append(declaration, []byte("    hostnamePeers: []\n")...)
	declaration = []byte(strings.ReplaceAll(string(declaration), "    base: ubuntu\n", "    base: ubuntu\n    os: windows\n    platform: {aws: {buildInstanceType: t3.large}, proxmox: {}}\n    extensions: {org.example.image: [null, false, 1]}\n"))
	declaration = []byte(strings.ReplaceAll(string(declaration), "    hostnamePeers: [bank]", "    hostnamePeers: [bank]\n    nics: [{network: lab, address: 250}]\n    forwarding: true\n    platform: {aws: {instanceType: t3.large, volume: {type: gp3, iops: 4000, throughput: 1000}, cpuCredits: unlimited}, proxmox: {cpuType: host, nicModel: e1000e, memoryBalloon: false}}\n    extensions: {org.example.machine: {key: value}}"))
	declaration = append(declaration, []byte("platforms: [aws, proxmox]\nnetworks: {lab: {egress: internet, cidr: 10.0.0.0/24, extensions: {org.example.network: 2}}}\nextensions: {org.example.lab: true}\ninputs: {count: {type: integer, default: 0, description: Count}, enabled: {type: boolean, default: false}}\n")...)
	if err := os.WriteFile(filepath.Join(root, "lab.yaml"), declaration, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.yml"), []byte("- hosts: all\n  tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := lc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	command := lc.Command{Source: source, Machine: lc.PlannedMachine{Spec: source.Lab.Machines["bank"]}, Image: lc.PlannedImage{BuildInstanceType: "t3.large"}, Environment: lc.Environment{Proxmox: lc.ProxmoxConfig{CPUTypes: []string{"host"}}}}
	wireCommand := &wire.Command{}
	if err := wireconvert.Encode(command, wireCommand); err != nil {
		t.Fatal(err)
	}
	var received lc.Command
	if err := wireconvert.Decode(wireCommand, &received); err != nil {
		t.Fatal(err)
	}
	if err := lc.Validate(received.Source); err != nil {
		t.Fatal("provider cannot validate retained declaration:", err)
	}
	if !reflect.DeepEqual(command.Source.Lab.Images["base"].Platform, received.Source.Lab.Images["base"].Platform) || !reflect.DeepEqual(command.Machine.Spec.Platform, received.Machine.Spec.Platform) || received.Image.BuildInstanceType != "t3.large" || !reflect.DeepEqual(command.Environment.Proxmox.CPUTypes, received.Environment.Proxmox.CPUTypes) {
		t.Fatal("platform options were lost across provider protocol")
	}
	if !received.Source.Lab.Machines["coach"].HostnamePeers.Explicit || len(received.Source.Lab.Machines["coach"].HostnamePeers.Names) != 0 {
		t.Fatal("empty peer inventory was lost in transport")
	}
	if len(received.Source.Lab.Images["base"].BuildFiles()) != 1 || len(received.Machine.Spec.HostnamePeers.Names) != 1 || received.Machine.Spec.HostnamePeers.Names[0] != "bank" {
		t.Fatal("source privacy selections were lost across provider protocol")
	}
}

func TestTypedProtocolRoundTrip(t *testing.T) {
	c := lc.Command{ClientPrivateKey: []byte("transient"), Operation: "op", Environment: lc.Environment{Version: 2, Platform: "aws", Bases: map[string]lc.Base{"ubuntu": {ID: "ami", DiskGiB: 8}}}, Source: lc.Source{Files: []lc.File{{Path: "lab.yaml", Content: []byte("version: 2")}}}, Prior: lc.Resource{Data: json.RawMessage(`{"native":"state"}`)}}
	c.Environment.AWS.Gateway = lc.AWSGatewayConfig{Base: "ubuntu-24.04", InstanceType: "t3.micro"}
	c.Environment.AWS.AllowEgress = true
	c.Environment.AWS.Types = []lc.InstanceType{{Name: "t3.large", CPU: 2, MemoryMiB: 8192, MaxNICs: 3}}
	c.Networks = []lc.PlannedNetwork{{Name: "a", CIDR: "10.77.0.0/24", VLAN: 101, Routes: []lc.PlannedRoute{{Network: "b", CIDR: "10.77.1.0/24", Via: "10.77.0.12", Router: "router", RouterNIC: 0}}}}
	c.Machine.NICs = []lc.PlannedNIC{{Network: "a", CIDR: "10.77.0.0/24", Address: "10.77.0.11", Default: true, Gateway: "10.77.0.1", Routes: c.Networks[0].Routes}}
	c.Machine.Resolver = "10.77.0.2"

	c.Environment.Proxmox.AllowEgress = true
	c.Gateway = lc.Resource{Kind: "gateway", Owner: "owner", ID: "i-gateway", Address: "192.0.2.1", Complete: true, Data: json.RawMessage(`{"hostPublicKey":"pinned","eni":"eni-owned"}`)}
	w := &wire.Command{}
	if e := wireconvert.Encode(c, w); e != nil {
		t.Fatal(e)
	}
	var got lc.Command
	if e := wireconvert.Decode(w, &got); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.Networks, got.Networks) || !reflect.DeepEqual(c.Machine, got.Machine) || !reflect.DeepEqual(c.Gateway, got.Gateway) || !reflect.DeepEqual(c.Environment.AWS, got.Environment.AWS) || !got.Environment.Proxmox.AllowEgress || got.Environment.Bases["ubuntu"].ID != "ami" || string(got.Prior.Data) != string(c.Prior.Data) || string(got.ClientPrivateKey) != "transient" {
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

type concurrentCreateProvider struct {
	entered chan string
	release chan struct{}
}

func (p concurrentCreateProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.entered <- c.Owner
	select {
	case <-p.release:
		return lc.Result{}, nil
	case <-ctx.Done():
		return lc.Result{}, ctx.Err()
	}
}
func TestIndependentCreatesOverlapWithOwnerFence(t *testing.T) {
	p := concurrentCreateProvider{make(chan string, 3), make(chan struct{})}
	server := &Server{Provider: p}
	env := lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 3)
	execute := func(owner, action string) {
		c := &wire.Command{}
		if err := wireconvert.Encode(lc.Command{Action: action, Owner: owner, Environment: env}, c); err != nil {
			done <- err
			return
		}
		done <- server.Execute(&wire.Request{Version: 2, Command: c}, testStream{ctx: ctx})
	}
	go execute("one", "network.create")
	go execute("two", "machine.create")
	for n := 0; n < 2; n++ {
		select {
		case <-p.entered:
		case <-ctx.Done():
			t.Fatal("independent creates serialized")
		}
	}
	go execute("one", "network.create")
	select {
	case <-p.entered:
		t.Fatal("same-owner retry crossed fence")
	case <-time.After(30 * time.Millisecond):
	}
	close(p.release)
	for n := 0; n < 3; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildDoesNotBlockOtherOwnerAndMachineRetriesStayFenced(t *testing.T) {
	p := concurrentCreateProvider{make(chan string, 6), make(chan struct{})}
	server := &Server{Provider: p}
	env := lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	source, err := lc.Load("../examples/single")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	done := make(chan error, 6)
	execute := func(owner, action, machine string) {
		workers.Go(func() {
			cmd := lc.Command{Action: action, Owner: owner, Environment: env, Source: source, Machine: lc.PlannedMachine{Name: machine}}
			wireCmd := new(wire.Command)
			if err := wireconvert.Encode(cmd, wireCmd); err != nil {
				done <- err
				return
			}
			done <- server.Execute(&wire.Request{Version: 2, Command: wireCmd}, testStream{ctx: ctx})
		})
	}
	defer func() { cancel(); workers.Wait() }()
	execute("build-owner", "image.build", "")
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal("build did not enter")
	}
	execute("lab-owner", "network.create", "")
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal("build blocked unrelated network")
	}
	// Release the network, then hold the machines together under a new provider gate.
	close(p.release)
	workers.Wait()
	for n := 0; n < 2; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	p = concurrentCreateProvider{make(chan string, 6), make(chan struct{})}
	server.Provider = p
	execute("lab-owner", "machine.create", "web")
	execute("lab-owner", "machine.create", "database")
	for n := 0; n < 2; n++ {
		select {
		case <-p.entered:
		case <-ctx.Done():
			t.Fatal("independent machines serialized")
		}
	}
	execute("lab-owner", "discover", "web")
	select {
	case <-p.entered:
		t.Fatal("response-lost same-machine retry crossed fence")
	case <-time.After(30 * time.Millisecond):
	}
	close(p.release)
	workers.Wait()
	for n := 0; n < 3; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmptyPlatformRestrictionSurvivesProtocol(t *testing.T) {
	c := lc.Command{Source: lc.Source{Lab: lc.Lab{Platforms: &lc.PlatformList{}}}}
	w := &wire.Command{}
	if e := wireconvert.Encode(c, w); e != nil {
		t.Fatal(e)
	}
	var got lc.Command
	if e := wireconvert.Decode(w, &got); e != nil {
		t.Fatal(e)
	}
	if got.Source.Lab.Platforms == nil || len(got.Source.Lab.Platforms.Names) != 0 {
		t.Fatal("empty restriction was broadened to any platform")
	}
	for _, platform := range []string{"aws", "proxmox"} {
		e := lc.CheckCapabilities(got.Source.Lab, platform)
		var p *lc.Error
		if !errors.As(e, &p) || p.Code != lc.Unsupported || p.Field != "platforms" {
			t.Fatal(platform, e)
		}
	}
}

func TestOwnerWaitRespectsCancellation(t *testing.T) {
	var owner sync.RWMutex
	owner.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := waitOwner(ctx, &owner, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	owner.Unlock()
	owner.RLock()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel2()
	if err := waitOwner(ctx2, &owner, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	owner.RUnlock()
	if err := waitOwner(context.Background(), &owner, false); err != nil {
		t.Fatal(err)
	}
	owner.Unlock()
}
