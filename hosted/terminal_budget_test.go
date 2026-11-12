package hosted

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

type terminalControlProvider struct{ lc.Provider }

func (terminalControlProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	return lc.Result{}, ctx.Err()
}

func TestTerminalSaturationPreservesSharedControlCapacity(t *testing.T) {
	s, _, _ := serviceFor(t)
	s.Config.Aggregate = AggregateOptions{MaxNativeOperations: 2, MaxSSHSessions: 2}
	wrapped, err := NewBudgetedProvider(terminalControlProvider{}, s.Config.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	s.sshSlots = wrapped.sshSlots
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	leases := make(chan func(), 30)
	var wg sync.WaitGroup
	for n := range 30 {
		wg.Go(func() {
			if release, err := s.acquireTerminal(ctx, fmt.Sprint(n), "team"); err == nil {
				leases <- release
			}
		})
	}
	wg.Wait()
	if len(leases) != 1 || len(s.sshSlots) != 1 {
		t.Fatal("terminal admission exceeded share", len(leases), len(s.sshSlots))
	}
	// The same channel backs provider readiness and all hosted SSH operations.
	for _, action := range []string{"machine.create", "discover", "access.open", "access.close"} {
		if _, err := wrapped.Execute(ctx, lc.Command{Action: action, Machine: lc.PlannedMachine{Name: "server"}}, nil); err != nil {
			t.Fatalf("%s blocked by terminals: %v", action, err)
		}
	}
	_, control, err := s.sshContext(ctx)
	if err != nil {
		t.Fatal("desktop/check/score lease blocked", err)
	}
	if len(s.sshSlots) != 2 {
		t.Fatal("control did not share overall limit")
	}
	control()
	release := <-leases
	release()
	release() // cleanup is idempotent even after revocation/disconnect.
	if len(s.sshSlots) != 0 {
		t.Fatal("terminal lease leaked")
	}
	reconnected, err := s.acquireTerminal(ctx, "user", "team")
	if err != nil {
		t.Fatal("reconnect refused after release", err)
	}
	reconnected()
	first, _ := s.acquireSSH(ctx)
	second, _ := s.acquireSSH(ctx)
	start := time.Now()
	if release, err := s.acquireTerminal(ctx, "user", "team"); err == nil {
		release()
		t.Fatal("terminal exceeded total budget")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("terminal queued instead of refusing")
	}
	first()
	second()
}

func TestTerminalUserTeamCapsAndCancelledAdmission(t *testing.T) {
	s, _, _ := serviceFor(t)
	ctx := context.Background()
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for n := range 8 {
		user := fmt.Sprint(n / 2)
		release, err := s.acquireTerminal(ctx, user, "team")
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
		if n == 1 {
			if release, err := s.acquireTerminal(ctx, user, "other-team"); err == nil {
				release()
				t.Fatal("per-user cap bypassed")
			}
		}
	}
	if release, err := s.acquireTerminal(ctx, "another-user", "team"); err == nil {
		release()
		t.Fatal("per-team cap bypassed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if release, err := s.acquireTerminal(cancelled, "another-user", "other-team"); err == nil {
		release()
		t.Fatal("cancelled admission consumed capacity")
	}
	small := &Service{Config: Config{Aggregate: AggregateOptions{MaxSSHSessions: 1}}}
	if release, err := small.acquireTerminal(ctx, "user", "team"); err == nil {
		release()
		t.Fatal("one-slot budget lost control reserve")
	}
}

func TestParticipantTerminalsRequirePinnedOptIn(t *testing.T) {
	s, v, _, engine := rangeAccessFixture(t)
	ctx := context.Background()
	var bundle InstructorBundle
	if err := s.Store.Get(ctx, "template-instructor", "pinned", &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Visibility.ParticipantTerminals = nil
	if err := s.Store.Put(ctx, "template-instructor", "pinned", bundle); err != nil {
		t.Fatal(err)
	}
	assertSSH := func(want bool) {
		t.Helper()
		inventory := decodeLearning[CoachMachines](t, request(s, "GET", "/api/machines", "", "", ""), 200)
		overview := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
		for _, machines := range [][]Machine{inventory.Machines, overview.Lab.Machines} {
			for _, machine := range machines {
				if machine.SSH != (want && machine.Name == "server") {
					t.Fatal("incorrect participant terminal projection", machine)
				}
			}
		}
	}
	assertSSH(false)
	if w := request(s, "GET", "/api/machines/server/terminal", "", s.Config.Origin, v.CSRF); w.Code != 404 {
		t.Fatal("legacy SSH became participant shell", w.Code)
	}
	if len(engine.terminal) != 0 || len(s.sshSlots) != 0 {
		t.Fatal("denied terminal reached SSH")
	}
	bundle.Visibility.ParticipantTerminals = []string{"server"}
	if err := s.Store.Put(ctx, "template-instructor", "pinned", bundle); err != nil {
		t.Fatal(err)
	}
	assertSSH(true)
	if !s.machineAllowed(ctx, "pinned", engine.src, "server", "ssh", false) {
		t.Fatal("opted-in terminal denied")
	}
	bundle.Visibility.Audience = "coaches"
	bundle.Visibility.HiddenMachines = []string{"server"}
	bundle.Visibility.ParticipantTerminals = nil
	if err := s.Store.Put(ctx, "template-instructor", "pinned", bundle); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", "/api/machines/server/terminal", "", s.Config.Origin, v.CSRF); w.Code != 404 {
		t.Fatal("hidden shell exposed", w.Code)
	}
	if !s.machineAllowed(ctx, "pinned", engine.src, "server", "ssh", true) {
		t.Fatal("coach lost access")
	}
}
