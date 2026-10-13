package labcentral

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type boundedProvider struct {
	Provider
	action          string
	started         chan string
	release         chan struct{}
	mu              sync.Mutex
	active, maximum int
}

func (p *boundedProvider) Execute(ctx context.Context, c Command, observer Observer) (Result, error) {
	if c.Action == p.action && (c.Action != "resource.destroy" || c.Prior.Kind == "machine") {
		p.mu.Lock()
		p.active++
		p.maximum = max(p.maximum, p.active)
		p.mu.Unlock()
		defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
		p.started <- c.Operation
		select {
		case <-p.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return p.Provider.Execute(ctx, c, observer)
}

func boundedPlan(t *testing.T, build bool) Plan {
	t.Helper()
	dir := t.TempDir()
	yaml := "version: 2\nname: bounded\nimages:\n"
	for _, name := range []string{"one", "two", "three"} {
		yaml += "  " + name + ":\n    base: ubuntu-24.04\n"
		if build {
			yaml += "    playbook: " + name + ".yml\n"
			if err := os.WriteFile(filepath.Join(dir, name+".yml"), []byte("- hosts: localhost\n  tasks: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	yaml += "machines:\n"
	for _, name := range []string{"one", "two", "three"} {
		yaml += "  " + name + ":\n    image: " + name + "\n    cpu: 2\n    memory: 2Gi\n    disk: 20Gi\n    access: [ssh]\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := testEnvironment()
	if build {
		env.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "bounded", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	}
	plan, err := PlanLab(source, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestConfiguredNativeConcurrencyBoundsEachOperation(t *testing.T) {
	for _, action := range []string{"image.build", "machine.create", "resource.destroy"} {
		for _, limit := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", action, limit), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				p := &boundedProvider{Provider: freshFake(), started: make(chan string, 4), release: make(chan struct{})}
				options := EngineOptions{}
				switch action {
				case "image.build":
					options.MaxImageBuildConcurrency = limit
				case "machine.create":
					options.MaxMachineConcurrency = limit
				case "resource.destroy":
					options.MaxCleanupConcurrency = limit
				}
				e, err := OpenWithOptions(t.TempDir(), p, nil, options)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				plan := boundedPlan(t, action == "image.build")
				var instance Instance
				if action == "resource.destroy" {
					instance, err = e.Deploy(ctx, plan)
					if err != nil {
						t.Fatal(err)
					}
				}
				p.action = action
				done := make(chan error, 1)
				go func() {
					var err error
					switch action {
					case "image.build":
						_, err = e.Build(ctx, plan)
					case "machine.create":
						_, err = e.Deploy(ctx, plan)
					default:
						err = e.Destroy(ctx, instance.ID)
					}
					done <- err
				}()
				finished := false
				defer func() {
					cancel()
					if !finished {
						<-done
					}
				}()
				for range limit {
					select {
					case <-p.started:
					case <-ctx.Done():
						t.Fatal("operation unexpectedly serialized")
					}
				}
				select {
				case <-p.started:
					t.Fatal("configured concurrency exceeded")
				case <-time.After(30 * time.Millisecond):
				}
				close(p.release)
				// Wait for completion before inspecting durable resource state.
				select {
				case err = <-done:
					finished = true
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("operation did not drain")
				}
				p.mu.Lock()
				maximum := p.maximum
				p.mu.Unlock()
				if maximum != limit {
					t.Fatal("native limit ignored", maximum, limit)
				}
				if action == "resource.destroy" {
					stored, err := e.Inspect(ctx, instance.ID)
					if err != nil || stored.Status != "destroyed" {
						t.Fatal("cleanup did not persist confirmation", stored, err)
					}
				}
			})
		}
	}
}

func TestInvalidEngineOptionsDoNotCreateState(t *testing.T) {
	for _, options := range []EngineOptions{{MaxImageBuildConcurrency: -1}, {MaxMachineConcurrency: 33}, {MaxCleanupConcurrency: -1}} {
		root := filepath.Join(t.TempDir(), "must-not-exist")
		_, err := OpenWithOptions(root, freshFake(), nil, options)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != Invalid {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid options created state", err)
		}
	}
}

func TestCancelledNativeWaitersDoNotCreateMoreMachines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &boundedProvider{Provider: freshFake(), action: "machine.create", started: make(chan string, 4), release: make(chan struct{})}
	e, err := OpenWithOptions(t.TempDir(), p, nil, EngineOptions{MaxMachineConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	plan := boundedPlan(t, false)
	done := make(chan error, 1)
	go func() { _, err := e.Deploy(ctx, plan); done <- err }()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("native operation not started")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled deployment reported success")
	}
	select {
	case <-p.started:
		t.Fatal("cancelled waiter created another machine")
	default:
	}
}
