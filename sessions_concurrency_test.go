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
