package hosted

import (
	"context"
	"database/sql"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recoveredEngine struct {
	Engine
	inspect func(context.Context, string) (lc.Instance, error)
	result  string
}

func (e recoveredEngine) Inspect(ctx context.Context, id string) (lc.Instance, error) {
	return e.inspect(ctx, id)
}
func (e recoveredEngine) Recover(context.Context, string) (lc.Instance, error) {
	return lc.Instance{Status: e.result}, nil
}
func prepareFixture(t *testing.T) (*Service, []Team) {
	t.Helper()
	s, _, _ := serviceFor(t)
	teams := setupMeeting(t, s.Store)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte("version: 2\nname: recovery\nimages:\n  base:\n    base: ubuntu-24.04\nmachines:\n  server:\n    image: base\n    cpu: 2\n    memory: 2Gi\n    disk: 20Gi\n    access: [ssh]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Put(context.Background(), "source", "", source); err != nil {
		t.Fatal(err)
	}
	s.env = lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	return s, teams
}
func TestPrepareRetryNeverWithdrawsReadyTeamAccess(t *testing.T) {
	s, teams := prepareFixture(t)
	ctx := context.Background()
	if _, err := s.Store.DB.Exec(`UPDATE teams SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	s.Engine = recoveredEngine{inspect: func(ctx context.Context, id string) (lc.Instance, error) {
		current, err := s.Store.Teams(ctx, "meeting")
		if err != nil {
			t.Fatal(err)
		}
		for _, team := range current {
			if team.Status != "ready" {
				t.Fatal("ready team made unavailable during retry", team.Name)
			}
		}
		checks.Add(1)
		return lc.Instance{Status: "ready"}, nil
	}}
	if err := s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}); err != nil {
		t.Fatal(err)
	}
	if int(checks.Load()) != len(teams) {
		t.Fatal("did not reconcile every team", checks.Load())
	}
}
func TestPrepareRejectsRecoveryThatDidNotReachReadiness(t *testing.T) {
	s, _ := prepareFixture(t)
	ctx := context.Background()
	meeting, _ := s.Store.Meeting(ctx)
	meeting.Status = "waiting"
	if err := s.Store.Put(ctx, "meeting", "current", meeting); err != nil {
		t.Fatal(err)
	}
	s.Engine = recoveredEngine{inspect: func(context.Context, string) (lc.Instance, error) { return lc.Instance{Status: "failed"}, nil }, result: "destroyed"}
	if err := s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}); err == nil {
		t.Fatal("unsuccessful recovery reported ready")
	}
	meeting, _ = s.Store.Meeting(ctx)
	if meeting.Status == "ready" {
		t.Fatal("meeting reported ready after failed recovery")
	}
}

type concurrentPrepareEngine struct {
	Engine
	mu              sync.Mutex
	instances       map[string]lc.Instance
	calls           map[string]int
	started         chan string
	release         chan struct{}
	fail            string
	active, maximum int
}

func (e *concurrentPrepareEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i, ok := e.instances[id]; ok {
		return i, nil
	}
	return lc.Instance{}, sql.ErrNoRows
}
func (e *concurrentPrepareEngine) DeployWithID(ctx context.Context, _ lc.Plan, id string) (lc.Instance, error) {
	return e.run(ctx, id)
}
func (e *concurrentPrepareEngine) Recover(ctx context.Context, id string) (lc.Instance, error) {
	return e.run(ctx, id)
}
func (e *concurrentPrepareEngine) run(ctx context.Context, id string) (lc.Instance, error) {
	e.mu.Lock()
	e.calls[id]++
	e.active++
	if e.active > e.maximum {
		e.maximum = e.active
	}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.active--; e.mu.Unlock() }()
	e.started <- id
	select {
	case <-e.release:
	case <-ctx.Done():
		return lc.Instance{}, ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	i := lc.Instance{ID: id, Status: "ready"}
	var err error
	if id == e.fail {
		i.Status = "recovery_required"
		err = fmt.Errorf("lost provider response")
	}
	e.instances[id] = i
	return i, err
}
func TestPrepareParallelFailureIsolationAndRetry(t *testing.T) {
	s, teams := prepareFixture(t)
	e := &concurrentPrepareEngine{instances: map[string]lc.Instance{}, calls: map[string]int{}, started: make(chan string, 10), release: make(chan struct{}), fail: teams[0].InstanceID}
	s.Engine = e
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}) }()
	for n := 0; n < 3; n++ {
		select {
		case <-e.started:
		case <-ctx.Done():
			t.Fatal("teams serialized")
		}
	}
	select {
	case <-e.started:
		t.Fatal("concurrency exceeded three")
	case <-time.After(30 * time.Millisecond):
	}
	close(e.release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), teams[0].Name) {
		t.Fatal("missing team failure", err)
	}
	current, err := s.Store.Teams(ctx, "meeting")
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range current {
		want := "ready"
		if team.ID == teams[0].ID {
			want = "failed"
		}
		if team.Status != want {
			t.Fatal(team)
		}
	}
	meeting, err := s.Store.Meeting(ctx)
	if err != nil || meeting.Status != "partial" {
		t.Fatal("missing partial meeting status", meeting, err)
	}
	// A student on a successful team can open access despite another failure.
	if err := s.Store.BeginSwitch(ctx, "student", teams[1].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, "student"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.readyTeam(ctx, "student"); err != nil {
		t.Fatal("ready lab access blocked", err)
	}
	// Retry uses the same reserved identity; only the failed lab is recovered.
	e.fail = ""
	if err := s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}); err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		want := 1
		if team.ID == teams[0].ID {
			want = 2
		}
		if e.calls[team.InstanceID] != want {
			t.Fatal("recreated ready team or changed retry identity", team)
		}
	}
	if e.maximum != 3 {
		t.Fatal(e.maximum)
	}
}
func TestPrepareCancellationStopsQueuedTeams(t *testing.T) {
	s, _ := prepareFixture(t)
	e := &concurrentPrepareEngine{instances: map[string]lc.Instance{}, calls: map[string]int{}, started: make(chan string, 10), release: make(chan struct{})}
	s.Engine = e
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}) }()
	for n := 0; n < 3; n++ {
		select {
		case <-e.started:
		case <-time.After(5 * time.Second):
			t.Fatal("teams serialized")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not drain")
	}
	if len(e.calls) != 3 {
		t.Fatal("started queued teams after cancellation", e.calls)
	}
}
