package hosted

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/coder/websocket"
)

func TestCoachTerminalAllowanceOverWebSocket(t *testing.T) {
	for _, participants := range []int{0, 8, 12} {
		t.Run(fmt.Sprintf("participants-%d", participants), func(t *testing.T) {
			s, v, teams, engine := rangeAccessFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			v.User.Admin = true
			if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
				t.Fatal(err)
			}
			// An existing template has no participant-terminal opt-in.
			var bundle InstructorBundle
			if err := s.Store.Get(ctx, "template-instructor", "pinned", &bundle); err != nil {
				t.Fatal(err)
			}
			bundle.Visibility.ParticipantTerminals = nil
			if err := s.Store.Put(ctx, "template-instructor", "pinned", bundle); err != nil {
				t.Fatal(err)
			}
			for n := range participants {
				team := teams[0].ID
				if n >= 8 {
					team = teams[1].ID
				}
				release, err := s.acquireTerminal(ctx, fmt.Sprint(n/2), team)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			server := httptest.NewUnstartedServer(nil)
			s.Config.Origin = "https://" + server.Listener.Addr().String()
			server.Config.Handler = s.Handler()
			server.StartTLS()
			defer server.Close()
			dial := func(machine string) *websocket.Conn {
				t.Helper()
				conn, _, err := websocket.Dial(ctx, server.URL+"/api/admin/teams/"+teams[0].ID+"/machines/"+machine+"/terminal", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.CloseNow() })
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"csrf":"`+v.CSRF+`"}`)); err != nil {
					t.Fatal(err)
				}
				return conn
			}
			waitAdmission := func(machine string) {
				t.Helper()
				select {
				case got := <-engine.terminal:
					if got != machine {
						t.Fatal("wrong machine", got)
					}
				case <-ctx.Done():
					t.Fatal("coach admission failed", ctx.Err())
				}
			}
			var connections []*websocket.Conn
			for _, machine := range lc.SortedMachineNames(engine.src.Lab)[:4] {
				connections = append(connections, dial(machine))
				waitAdmission(machine)
			}
			// All four are the same coach and allocation, including a third machine.
			extra := dial("server")
			refusal, stop := context.WithTimeout(ctx, time.Second)
			_, _, err := extra.Read(refusal)
			stop()
			if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
				t.Fatal("full coach allowance did not refuse promptly", err)
			}
			if len(engine.terminal) != 0 || len(s.sshSlots) != participants+4 {
				t.Fatal("refused terminal consumed SSH capacity")
			}
			controlCtx, stop := context.WithTimeout(ctx, time.Second)
			_, releaseControl, err := s.sshContext(controlCtx)
			stop()
			if err != nil {
				t.Fatal("coach/participant saturation blocked control", err)
			}
			releaseControl()
			connections[0].CloseNow()
			waitCount := func(want int) {
				t.Helper()
				deadline := time.Now().Add(time.Second)
				for {
					s.terminalMu.Lock()
					count := s.coachTerminals
					s.terminalMu.Unlock()
					if count == want {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("coach leases: got %d, want %d", count, want)
					}
					time.Sleep(time.Millisecond)
				}
			}
			waitCount(3)
			reconnected := dial("server")
			waitAdmission("server")
			reconnected.CloseNow()
			for _, conn := range connections {
				conn.CloseNow()
			}
			waitCount(0)
			if len(s.sshSlots) != participants {
				t.Fatal("coach disconnect leaked or released participant slots")
			}
		})
	}
}

func TestCoachTerminalBoundPreservesControlForEveryPoolSize(t *testing.T) {
	for size := 1; size <= 64; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := &Service{Config: Config{Aggregate: AggregateOptions{MaxSSHSessions: size}}}
			ctx := context.Background()
			var releases []func()
			defer func() {
				for _, release := range releases {
					release()
				}
			}()
			for n := range size / 2 {
				release, err := s.acquireTerminal(ctx, fmt.Sprint(n), fmt.Sprint(n))
				if err != nil {
					t.Fatal(err)
				}
				releases = append(releases, release)
			}
			leases := make(chan func(), 20)
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					if release, err := s.acquireTerminalForRole(ctx, "coach", "team", true); err == nil {
						leases <- release
					}
				})
			}
			wg.Wait()
			close(leases)
			for release := range leases {
				// Double release must not free another active lease.
				releases = append(releases, func() { release(); release() })
			}
			if s.coachTerminals != min(4, size-size/2-1) || len(s.sshSlots) >= size {
				t.Fatal("coach allowance consumed control reserve", s.coachTerminals, len(s.sshSlots))
			}
			control, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			release, err := s.acquireSSH(control)
			if err != nil {
				t.Fatal(err)
			}
			release()
			for _, release := range releases {
				release()
			}
			if len(s.sshSlots) != 0 || s.coachTerminals != 0 || s.terminalCount != 0 || len(s.terminalUsers) != 0 || len(s.terminalTeams) != 0 {
				t.Fatal("release leaked admission state")
			}
		})
	}
}

func TestCoachTerminalRespectsAggregateOccupancyAndCancellation(t *testing.T) {
	s := &Service{Config: Config{Aggregate: AggregateOptions{MaxSSHSessions: 6}}}
	ctx := context.Background()
	for range 6 {
		release, err := s.acquireSSH(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	// The coach allowance is free, but control work already owns the entire pool.
	start := time.Now()
	if release, err := s.acquireTerminalForRole(ctx, "coach", "team", true); err == nil {
		release()
		t.Fatal("coach bypassed overall SSH limit")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("coach admission queued behind control work")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.acquireTerminalForRole(cancelled, "coach", "team", true); err != context.Canceled {
		t.Fatal("coach cancellation ignored", err)
	}
	if s.coachTerminals != 0 || len(s.sshSlots) != 6 {
		t.Fatal("failed coach admission changed occupancy")
	}
}

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
