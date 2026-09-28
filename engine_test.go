package labcentral

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProvider struct {
	mu         sync.Mutex
	resources  map[string]Resource
	creates    int
	loseOnce   bool
	failDelete bool
	block      chan struct{}
	entered    chan struct{}
}

func freshFake() *fakeProvider { return &fakeProvider{resources: map[string]Resource{}} }
func (f *fakeProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action == "network.create" && f.block != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-f.block:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch c.Action {
	case "check":
		return Result{}, nil
	case "network.create", "machine.create":
		key := c.Owner + "/network"
		kind, name := "network", "private"
		if c.Action == "machine.create" {
			key = c.Owner + "/machine/" + c.Machine.Name
			kind, name = "machine", c.Machine.Name
			if len(c.ClientPrivateKey) == 0 || c.Machine.HostPrivateKey == "" {
				return Result{}, fmt.Errorf("missing injected ephemeral credentials")
			}
		}
		r, exists := f.resources[key]
		if !exists {
			f.creates++
			r = Resource{Kind: kind, Name: name, ID: key, Owner: c.Owner, Address: "192.0.2.20", Complete: true}
			f.resources[key] = r
		}
		if c.Action == "machine.create" && f.loseOnce {
			f.loseOnce = false
			return Result{}, errors.New("lost response after native create")
		}
		return Result{Resource: r}, nil
	case "image.build":
		key := c.Owner + "/image"
		r, ok := f.resources[key]
		if !ok {
			f.creates++
			r = Resource{Kind: "image", Name: c.Image.Name, ID: key, Owner: c.Owner, Complete: true}
			f.resources[key] = r
		}
		if f.loseOnce {
			f.loseOnce = false
			return Result{}, errors.New("lost build response")
		}
		return Result{Resource: r}, nil
	case "image.read":
		r, ok := f.resources[c.Prior.ID]
		return Result{Resource: r, Absent: !ok}, nil
	case "discover":
		key := c.Owner + "/network"
		if c.Machine.Name != "" {
			key = c.Owner + "/machine/" + c.Machine.Name
		}
		r, ok := f.resources[key]
		return Result{Resource: r, Absent: !ok}, nil
	case "resource.destroy":
		if f.failDelete {
			return Result{}, errors.New("delete outcome uncertain")
		}
		if r, ok := f.resources[c.Prior.ID]; ok {
			if r.Owner != c.Owner {
				return Result{}, errors.New("ownership mismatch")
			}
			delete(f.resources, c.Prior.ID)
		}
		return Result{Absent: true}, nil
	}
	return Result{}, fmt.Errorf("unexpected fake action %s", c.Action)
}
func openFake(t *testing.T, root string, f *fakeProvider) *Engine {
	t.Helper()
	e, err := Open(root, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func TestValidatedPlanCanDeployIndependentInstances(t *testing.T) {
	ctx := context.Background()
	f := freshFake()
	e := openFake(t, t.TempDir(), f)
	p, err := PlanLab(testSource(t), testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	original := digest(p)
	hosts := map[string]bool{}
	clients := map[string]bool{}
	for range 5 {
		instance, err := e.Deploy(ctx, p)
		if err != nil || instance.Status != "ready" {
			t.Fatalf("reusing the validated plan failed: %s %v", instance.Status, err)
		}
		if digest(p) != original {
			t.Fatal("deployment changed the caller's validated plan")
		}
		machine := instance.Plan.Machines[0]
		if machine.NativeImage == "" || machine.Address == "" || machine.HostPublicKey == "" || machine.PublicKey == "" {
			t.Fatal("deployment did not resolve its own machine identity")
		}
		if hosts[machine.HostPublicKey] || clients[machine.PublicKey] {
			t.Fatal("independent instances shared guest or operator credentials")
		}
		hosts[machine.HostPublicKey], clients[machine.PublicKey] = true, true
	}
	if f.creates != 10 {
		t.Fatalf("expected five independent networks and machines, got %d resources", f.creates)
	}
}
func TestLostCreateResponseRecoversWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	f := freshFake()
	f.loseOnce = true
	e := openFake(t, root, f)
	p, err := PlanLab(testSource(t), testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err := e.Deploy(ctx, p)
	if err == nil || i.ID == "" {
		t.Fatal("expected interrupted deployment with durable identity")
	}
	stored, err := e.Inspect(ctx, i.ID)
	if err != nil || stored.Status != "recovery_required" {
		t.Fatalf("state was not retained: %+v %v", stored, err)
	}
	e.Close()
	e2 := openFake(t, root, f)
	ready, err := e2.Recover(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || f.creates != 2 {
		t.Fatalf("duplicate resources or failed recovery: %s, creates=%d", ready.Status, f.creates)
	}
	if err = e2.Destroy(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if err = e2.Destroy(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.resources) != 0 {
		t.Fatal("owned leftovers")
	}
}
func TestUnknownCleanupNeverReportsDestroyed(t *testing.T) {
	ctx := context.Background()
	f := freshFake()
	e := openFake(t, t.TempDir(), f)
	p, _ := PlanLab(testSource(t), testEnvironment(), nil)
	i, err := e.Deploy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	f.failDelete = true
	if err = e.Destroy(ctx, i.ID); err == nil {
		t.Fatal("uncertain cleanup reported success")
	}
	i, err = e.Inspect(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if i.Status != "destroying" {
		t.Fatalf("lost cleanup intent: %s", i.Status)
	}
	f.failDelete = false
	if _, err = e.Recover(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentMutationDoesNotBlockStatusReads(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	f := freshFake()
	f.block = make(chan struct{})
	f.entered = make(chan struct{}, 1)
	e := openFake(t, root, f)
	p, _ := PlanLab(testSource(t), testEnvironment(), nil)
	done := make(chan error, 1)
	go func() { _, err := e.Deploy(ctx, p); done <- err }()
	<-f.entered
	e2 := openFake(t, root, f)
	if _, err := e2.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.Deploy(ctx, p); err == nil {
		t.Fatal("simultaneous writer admitted")
	}
	close(f.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestPrivateKeysNeverEnterJournalOrPlan(t *testing.T) {
	ctx := context.Background()
	f := freshFake()
	e := openFake(t, t.TempDir(), f)
	p, _ := PlanLab(testSource(t), testEnvironment(), nil)
	i, err := e.Deploy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(i)
	if strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatal("private key in public result")
	}
	rows, err := e.state.DB.Query(`SELECT command FROM steps`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var c Command
		if err = json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		if len(c.ClientPrivateKey) != 0 || c.Machine.HostPrivateKey != "" {
			t.Fatal("private credentials persisted in journal")
		}
	}
}
func TestMutatedPlanRejectedBeforeProviderCalls(t *testing.T) {
	f := freshFake()
	e := openFake(t, t.TempDir(), f)
	p, _ := PlanLab(testSource(t), testEnvironment(), nil)
	p.Machines[0].InstanceType = "unauthorized"
	if _, err := e.Deploy(context.Background(), p); err == nil {
		t.Fatal("modified plan accepted")
	}
	if f.creates != 0 {
		t.Fatal("mutation happened before validation")
	}
}

func TestDestroyReconcilesLostCreateThenRepeatedCleanup(t *testing.T) {
	ctx := context.Background()
	f := freshFake()
	f.loseOnce = true
	e := openFake(t, t.TempDir(), f)
	p, _ := PlanLab(testSource(t), testEnvironment(), nil)
	i, err := e.Deploy(ctx, p)
	if err == nil {
		t.Fatal("expected response loss")
	}
	f.failDelete = true
	if err = e.Destroy(ctx, i.ID); err == nil {
		t.Fatal("expected cleanup failure")
	}
	f.failDelete = false
	if _, err = e.Recover(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Destroy(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.resources) != 0 {
		t.Fatal("owned leftovers")
	}
}

func TestBuildRecoveryAndNewGenerationAfterRemoval(t *testing.T) {
	ctx := context.Background()
	f := freshFake()
	e := openFake(t, t.TempDir(), f)
	s, err := Load("examples/single")
	if err != nil {
		t.Fatal(err)
	}
	env := testEnvironment()
	env.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	plan, err := PlanLab(s, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.loseOnce = true
	if _, err = e.Build(ctx, plan); err == nil {
		t.Fatal("expected response loss")
	}
	images, err := e.RecoverBuild(ctx, plan.Images[0].CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("duplicated build during recovery")
	}
	old := images[0]
	if _, err = e.Build(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("cache was ignored")
	}
	if err = e.RemoveImage(ctx, old.Key); err != nil {
		t.Fatal(err)
	}
	images, err = e.Build(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if images[0].ID == old.ID || f.creates != 2 {
		t.Fatal("deleted build generation was reused")
	}
}

type parallelDestroyProvider struct {
	entered  chan string
	release  chan struct{}
	mu       sync.Mutex
	failed   bool
	machines int
	network  bool
}

func (p *parallelDestroyProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action != "resource.destroy" {
		return Result{}, fmt.Errorf("unexpected action %s", c.Action)
	}
	if c.Prior.Kind == "machine" {
		p.entered <- c.Prior.ID
		select {
		case <-p.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if c.Prior.ID == "one" && !p.failed {
			p.failed = true
			return Result{}, errors.New("lost termination response")
		}
		p.machines++
		return Result{Absent: true}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.network = true
	return Result{Absent: true}, nil
}
func TestDestroyParallelMachinesPreservesFailedIntentAndNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &parallelDestroyProvider{entered: make(chan string, 4), release: make(chan struct{})}
	e, err := Open(t.TempDir(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	i := Instance{ID: "lab", Status: "ready", Resources: []Resource{{Kind: "network", Name: "private", ID: "net", Owner: "lab"}, {Kind: "machine", Name: "one", ID: "one", Owner: "lab"}, {Kind: "machine", Name: "two", ID: "two", Owner: "lab"}}}
	if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
		t.Fatal(err)
	}
	// Missing SSH material must not keep billable VMs alive, but access intent must
	// remain until their destruction is confirmed.
	if err = e.state.Put(ctx, "access", "session", Command{Operation: "session", Owner: "lab"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Destroy(ctx, "lab") }()
	for n := 0; n < 2; n++ {
		select {
		case <-p.entered:
		case <-ctx.Done():
			t.Fatal("machine deletions serialized")
		}
	}
	close(p.release)
	if err = <-done; err == nil {
		t.Fatal("unknown deletion reported successful")
	}
	if p.network {
		t.Fatal("network removed while a VM outcome was unknown")
	}
	if err = e.state.Get(ctx, "access", "session", new(Command)); err != nil {
		t.Fatal("access intent lost", err)
	}
	if err = e.Destroy(ctx, "lab"); err != nil {
		t.Fatal(err)
	}
	if !p.network || p.machines != 2 {
		t.Fatal("successful machine repeated or dependency skipped", p.machines)
	}
	if err = e.state.Get(ctx, "access", "session", new(Command)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("access not retired after VM absence", err)
	}
}
