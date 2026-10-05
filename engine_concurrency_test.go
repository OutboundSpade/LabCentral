package labcentral

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type blockingBuildProvider struct {
	*fakeProvider
	key     string
	entered chan struct{}
	release chan struct{}
}

func (p *blockingBuildProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action == "image.build" && c.Image.CacheKey == p.key {
		p.entered <- struct{}{}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-p.release:
		}
	}
	return p.fakeProvider.Execute(ctx, c, o)
}
func imagePlan(t *testing.T, name string) Plan {
	t.Helper()
	src, err := Load("examples/single")
	if err != nil {
		t.Fatal(err)
	}
	env := testEnvironment()
	env.Name = name
	env.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: name, AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	plan, err := PlanLab(src, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func TestUnrelatedBuildDoesNotBlockCachedDeployOrDestroy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old, newPlan := imagePlan(t, "old"), imagePlan(t, "new")
	provider := &blockingBuildProvider{fakeProvider: freshFake(), key: newPlan.Images[0].CacheKey, entered: make(chan struct{}, 1), release: make(chan struct{})}
	engine, err := Open(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err = engine.Build(ctx, old); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := engine.Build(ctx, newPlan); done <- err }()
	defer func() { close(provider.release); <-done }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("build did not start")
	}
	inst, err := engine.Deploy(ctx, old)
	if err != nil {
		t.Fatal("cached deployment blocked by unrelated image", err)
	}
	if err = engine.Destroy(ctx, inst.ID); err != nil {
		t.Fatal("cleanup blocked by unrelated image", err)
	}
}
func TestImageFenceCoordinatesClientsAndCancellation(t *testing.T) {
	plan := imagePlan(t, "shared")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := &blockingBuildProvider{fakeProvider: freshFake(), key: plan.Images[0].CacheKey, entered: make(chan struct{}, 2), release: make(chan struct{})}
	root := t.TempDir()
	first, _ := Open(root, provider, nil)
	defer first.Close()
	second, _ := Open(root, provider, nil)
	defer second.Close()
	done := make(chan error, 1)
	go func() { _, err := first.Build(ctx, plan); done <- err }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("build did not start")
	}
	waiting, stop := context.WithCancel(ctx)
	stop()
	if _, err := second.Build(waiting, plan); !errors.Is(err, context.Canceled) {
		t.Fatal("image wait did not respect cancellation", err)
	}
	removeCtx, stopRemove := context.WithTimeout(ctx, 30*time.Millisecond)
	if err := second.AbandonBuild(removeCtx, plan.Images[0].CacheKey); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cleanup overlapped active image", err)
	}
	stopRemove()
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := second.Build(ctx, plan); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.creates != 1 {
		t.Fatal("duplicate image generation", provider.creates)
	}
}

type blockingMachinesProvider struct {
	*fakeProvider
	entered chan string
	release chan struct{}
}

func (p *blockingMachinesProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action == "machine.create" {
		p.mu.Lock()
		network := p.resources[c.Owner+"/network"].Complete
		p.mu.Unlock()
		if !network {
			return Result{}, errors.New("machine started before network")
		}
		p.entered <- c.Machine.Name
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-p.release:
		}
		if c.Machine.Name == "database" {
			return Result{}, errors.New("machine response lost")
		}
	}
	return p.fakeProvider.Execute(ctx, c, o)
}
func TestMachinesOverlapAfterNetworkAndRetainSuccessfulSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source, err := Load("examples/web-database")
	if err != nil {
		t.Fatal(err)
	}
	env := imagePlan(t, "parallel").Environment
	plan, err := PlanLab(source, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &blockingMachinesProvider{fakeProvider: freshFake(), entered: make(chan string, 2), release: make(chan struct{})}
	engine, err := Open(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	done := make(chan error, 1)
	var inst Instance
	go func() { var err error; inst, err = engine.Deploy(ctx, plan); done <- err }()
	for n := 0; n < 2; n++ {
		select {
		case <-provider.entered:
		case <-ctx.Done():
			close(provider.release)
			<-done
			t.Fatal("independent machines serialized")
		}
	}
	close(provider.release)
	if err := <-done; err == nil {
		t.Fatal("response loss hidden")
	}
	retained, err := engine.Inspect(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Status != "recovery_required" || len(retained.Resources) != 2 {
		t.Fatal("successful network/machine sibling lost", retained)
	}
}
