package labcentral

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Provider gates exercise library fences and retained intents, not native DCV.
type desktopFenceProvider struct {
	*fakeProvider
	opened chan Command
	gate   chan struct{}
}

func (p *desktopFenceProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	switch c.Action {
	case "access.open":
		p.opened <- c
		if c.Access.Principal == "blocked" {
			select {
			case <-p.gate:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		b, err := json.Marshal(AccessInfo{ID: c.Operation, URL: "http://127.0.0.1:1", Expires: time.Now().Add(time.Hour)})
		return Result{Resource: Resource{Data: b}}, err
	case "access.read":
		return Result{Absent: true}, nil
	case "access.close":
		if c.Access.Principal == "unknown-close" {
			return Result{}, errors.New("unconfirmed guest cleanup")
		}
		return Result{Absent: true}, nil
	}
	return p.fakeProvider.Execute(ctx, c, o)
}

func desktopFenceFixture(t *testing.T) (*Engine, *Engine, Instance, *desktopFenceProvider) {
	t.Helper()
	dir := t.TempDir()
	source := strings.Replace(sample, "  one:", "  web:", 1)
	source = strings.Replace(source, "    base: ubuntu-24.04", "    base: ubuntu-24.04\n    build:\n      playbook: base.yml", 1) + `  workstation:
    image: base
    cpu: 2
    memory: 2Gi
    disk: 20Gi
    access: [ssh, desktop]
`
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.yml"), []byte("- hosts: all\n  tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := imagePlan(t, "desktop-fences").Environment
	env.AWS.DesktopProfile = "fixture-license-only"
	plan, err := PlanLab(src, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &desktopFenceProvider{fakeProvider: freshFake(), opened: make(chan Command, 8), gate: make(chan struct{})}
	root := t.TempDir()
	first, err := Open(root, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := Open(root, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	inst, err := first.Deploy(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return first, second, inst, p
}

func TestWebCheckAllowsDesktopLaunchAndCloseAcrossClients(t *testing.T) {
	first, second, inst, p := desktopFenceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.WithSSHMaterial(ctx, inst.ID, "web", func(_ Instance, _ PlannedMachine, _ Resource, key []byte) error {
			if len(key) == 0 {
				return errors.New("missing local credentials")
			}
			close(entered)
			select {
			case <-release:
				return errors.New("guest check failed")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("web check did not start")
	}
	launchCtx, stop := context.WithTimeout(ctx, time.Second)
	info, err := second.OpenDesktopWithOptions(launchCtx, inst.ID, "workstation", AccessOptions{Principal: "student"})
	stop()
	if err != nil || info.ID == "" {
		t.Fatal("web check blocked workstation launch", info, err)
	}
	if c := <-p.opened; c.Machine.Name != "workstation" || len(c.ClientPrivateKey) == 0 {
		t.Fatal("wrong machine or missing provider credentials")
	}
	waitCtx, stop := context.WithTimeout(ctx, 35*time.Millisecond)
	err = second.WithSSHMaterial(waitCtx, inst.ID, "web", func(Instance, PlannedMachine, Resource, []byte) error {
		t.Error("same-machine checks overlapped")
		return nil
	})
	stop()
	var busy *Error
	if !errors.As(err, &busy) || busy.Code != Busy || !errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		t.Fatal("same-machine wait did not cancel", err)
	}
	destroyWait, stopDestroy := context.WithTimeout(ctx, 35*time.Millisecond)
	err = second.Destroy(destroyWait, inst.ID)
	stopDestroy()
	if err == nil {
		t.Fatal("destruction overlapped active web check")
	}
	if _, err = second.Recover(ctx, inst.ID); err == nil {
		t.Fatal("recovery overlapped active web check")
	}
	if err = second.CloseAccess(ctx, info.ID); err != nil {
		t.Fatal("web check blocked workstation cleanup", err)
	}
	if err = first.state.Get(ctx, "access", info.ID, new(Command)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("confirmed desktop cleanup was not persisted", err)
	}
	// Failed revocation still retains cleanup intent with the narrower fences.
	info, err = second.OpenDesktopWithOptions(ctx, inst.ID, "workstation", AccessOptions{Principal: "unknown-close"})
	if err != nil {
		t.Fatal(err)
	}
	if err = second.CloseAccess(ctx, info.ID); err == nil {
		t.Fatal("unconfirmed cleanup was accepted")
	}
	if err = first.state.Get(ctx, "access", info.ID, new(Command)); err != nil {
		t.Fatal("unconfirmed cleanup lost its intent", err)
	}
	close(release)
	// The callback's failure must release both its machine and lifecycle guards.
	if err = second.WithSSHMaterial(ctx, inst.ID, "web", func(Instance, PlannedMachine, Resource, []byte) error { return nil }); err != nil {
		t.Fatal("failed check retained its machine fence", err)
	}
	if err = second.Destroy(ctx, inst.ID); err != nil {
		t.Fatal("failed/cancelled operations retained a lifecycle fence", err)
	}
}

func TestDesktopLaunchFencesSameMachineAndLifecycleAcrossClients(t *testing.T) {
	first, second, inst, p := desktopFenceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prior, err := first.OpenDesktopWithOptions(ctx, inst.ID, "workstation", AccessOptions{Principal: "prior"})
	if err != nil {
		t.Fatal(err)
	}
	<-p.opened
	done := make(chan error, 1)
	go func() {
		_, err := first.OpenDesktopWithOptions(ctx, inst.ID, "workstation", AccessOptions{Principal: "blocked"})
		done <- err
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-p.opened:
	case <-ctx.Done():
		t.Fatal("desktop launch did not start")
	}
	for _, operation := range []func(context.Context) error{
		func(ctx context.Context) error {
			return second.WithSSHMaterial(ctx, inst.ID, "workstation", func(Instance, PlannedMachine, Resource, []byte) error {
				t.Error("check crossed workstation launch fence")
				return nil
			})
		},
		func(ctx context.Context) error {
			_, err := second.OpenDesktopWithOptions(ctx, inst.ID, "workstation", AccessOptions{Principal: "another-student"})
			return err
		},
		func(ctx context.Context) error { return second.CloseAccess(ctx, prior.ID) },
	} {
		waitCtx, stop := context.WithTimeout(ctx, 35*time.Millisecond)
		err = operation(waitCtx)
		stop()
		var busy *Error
		if !errors.As(err, &busy) || busy.Code != Busy || !errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			t.Fatal("same-machine operation did not respect cancellation", err)
		}
	}
	if len(p.opened) != 0 {
		t.Fatal("another desktop reached provider during launch")
	}
	if err = second.state.Get(ctx, "access", prior.ID, new(Command)); err != nil {
		t.Fatal("cancelled close lost desktop intent", err)
	}
	if err = second.WithSSHMaterial(ctx, inst.ID, "web", func(Instance, PlannedMachine, Resource, []byte) error { return nil }); err != nil {
		t.Fatal("workstation launch blocked a different machine", err)
	}
	destroyWait, stopDestroy := context.WithTimeout(ctx, 35*time.Millisecond)
	err = second.Destroy(destroyWait, inst.ID)
	stopDestroy()
	if err == nil {
		t.Fatal("destruction overlapped native desktop startup")
	}
	if _, err = second.Recover(ctx, inst.ID); err == nil {
		t.Fatal("recovery overlapped native desktop startup")
	}
	close(p.gate)
	if err = second.CloseAccess(ctx, prior.ID); err != nil {
		t.Fatal("cancelled waits prevented subsequent cleanup", err)
	}
	if err = second.Destroy(ctx, inst.ID); err != nil {
		t.Fatal("native launch or cancelled waits leaked lifecycle guard", err)
	}
}

// The provider cache can retire independently of the saved successful open.
type expiringDesktopProvider struct {
	*fakeProvider
	cached     *Command
	tombstones map[string]bool
	opens      []string
}

func (p *expiringDesktopProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	switch c.Action {
	case "access.open":
		if p.tombstones[c.Access.Generation] {
			return Result{}, errors.New("generation was revoked")
		}
		p.opens = append(p.opens, c.Access.Generation)
		saved := c
		p.cached = &saved
		data, _ := json.Marshal(AccessInfo{ID: c.Operation, URL: "http://127.0.0.1:1", Expires: time.Now().Add(time.Hour)})
		return Result{Resource: Resource{Data: data}}, nil
	case "access.read":
		if p.cached == nil {
			return Result{Absent: true}, nil
		}
		data, _ := json.Marshal(AccessInfo{ID: c.Operation, URL: "http://127.0.0.1:1", Expires: time.Now().Add(time.Hour)})
		return Result{Resource: Resource{Data: data}}, nil
	case "access.close":
		p.tombstones[c.Access.Generation] = true
		p.cached = nil
		return Result{Absent: true}, nil
	}
	return p.fakeProvider.Execute(ctx, c, o)
}
func TestDesktopReopenAfterProviderExpiryAllocatesFreshGeneration(t *testing.T) {
	first, _, inst, _ := desktopFenceFixture(t)
	p := &expiringDesktopProvider{fakeProvider: freshFake(), tombstones: map[string]bool{}}
	first.provider = p
	ctx := context.Background()
	opts := AccessOptions{Principal: "example-principal"}
	info, err := first.OpenDesktopWithOptions(ctx, inst.ID, "workstation", opts)
	if err != nil {
		t.Fatal(err)
	}
	old := p.cached.Access.Generation
	if _, err = first.OpenDesktopWithOptions(ctx, inst.ID, "workstation", opts); err != nil || len(p.opens) != 1 {
		t.Fatal("confirmed cache was not reused", err, p.opens)
	}
	p.tombstones[old] = true
	p.cached = nil
	if _, err = first.OpenDesktopWithOptions(ctx, inst.ID, "workstation", opts); err != nil {
		t.Fatal("reopen after autonomous expiry", err)
	}
	if len(p.opens) != 2 || p.opens[1] == old || !p.tombstones[old] {
		t.Fatal("retired generation reused", p.opens)
	}
	var saved Command
	if err = first.state.Get(ctx, "access", info.ID, &saved); err != nil || saved.Access.Generation != p.opens[1] || saved.Recovery {
		t.Fatal(saved, err)
	}
	if err = first.CloseAccess(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
}

func TestProxmoxRetainedDesktopGrantAndRevoke(t *testing.T) {
	engine, _, inst, _ := desktopFenceFixture(t)
	inst.Plan.Environment.Platform = "proxmox"
	if err := engine.state.Put(context.Background(), "instance", inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	provider := &expiringDesktopProvider{fakeProvider: freshFake(), tombstones: map[string]bool{}}
	engine.provider = provider
	info, err := engine.OpenDesktopWithOptions(context.Background(), inst.ID, "workstation", AccessOptions{Principal: "student", RetainWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	if provider.cached == nil || !provider.cached.Access.RetainWorkspace || provider.cached.Environment.Platform != "proxmox" {
		t.Fatal("retention lost at provider boundary")
	}
	generation := provider.cached.Access.Generation
	if err = engine.CloseAccess(context.Background(), info.ID); err != nil || !provider.tombstones[generation] {
		t.Fatal("generation not revoked", err)
	}
}

func TestInteractiveSSHAllowsSameMachineStepAndRetainsLifecycleGuard(t *testing.T) {
	first, second, inst, _ := desktopFenceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.WithSSHSession(ctx, inst.ID, "web", func(i Instance, m PlannedMachine, r Resource, key []byte) error {
			if i.ID != inst.ID || m.Name != "web" || r.Name != "web" || len(key) == 0 {
				return errors.New("unpinned SSH material")
			}
			close(entered)
			select {
			case <-release:
				return errors.New("terminal ended")
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("terminal did not start")
	}
	// Use a second engine on the same state root: these are process-wide file
	// locks, not just hosted in-memory admission or an SSH fixture's behavior.
	stepCtx, stop := context.WithTimeout(ctx, time.Second)
	err := second.WithSSHMaterial(stepCtx, inst.ID, "web", func(Instance, PlannedMachine, Resource, []byte) error { return nil })
	stop()
	if err != nil {
		t.Fatal("terminal blocked same-machine step", err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	err = second.Destroy(waitCtx, inst.ID)
	stop()
	if err == nil {
		t.Fatal("destruction overlapped an interactive session")
	}
	if _, err = second.Recover(ctx, inst.ID); err == nil {
		t.Fatal("recovery overlapped an interactive session")
	}
	close(release)
	// Acquiring the exclusive lifecycle guard waits for callback and fence release.
	if err = second.Destroy(ctx, inst.ID); err != nil {
		t.Fatal("terminal retained lifecycle guard", err)
	}
}

func TestInteractiveSSHRejectsUndeclaredAndUnreadyAccess(t *testing.T) {
	first, _, inst, _ := desktopFenceFixture(t)
	ctx := context.Background()
	called := false
	run := func() error {
		return first.WithSSHSession(ctx, inst.ID, "web", func(Instance, PlannedMachine, Resource, []byte) error { called = true; return nil })
	}
	for n := range inst.Plan.Machines {
		if inst.Plan.Machines[n].Name == "web" {
			inst.Plan.Machines[n].Spec.Access = nil
		}
	}
	if err := first.state.Put(ctx, "instance", inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || called {
		t.Fatal("undeclared SSH access admitted", err)
	}
	for n := range inst.Plan.Machines {
		if inst.Plan.Machines[n].Name == "web" {
			inst.Plan.Machines[n].Spec.Access = []string{"ssh"}
		}
	}
	inst.Status = "ending"
	if err := first.state.Put(ctx, "instance", inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || called {
		t.Fatal("unready instance admitted", err)
	}
}
