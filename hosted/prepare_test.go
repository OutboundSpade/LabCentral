package hosted

import (
	"context"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"testing"
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
	checks := 0
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
		checks++
		return lc.Instance{Status: "ready"}, nil
	}}
	if err := s.meetingOperation(ctx, Job{Kind: "prepare", Target: "meeting"}); err != nil {
		t.Fatal(err)
	}
	if checks != len(teams) {
		t.Fatal("did not reconcile every team", checks)
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
