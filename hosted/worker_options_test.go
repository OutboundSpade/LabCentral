package hosted

import (
	"context"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
	"testing"
	"time"
)

func TestWorkerOptionsRejectInvalidBeforeIdentityOrState(t *testing.T) {
	for _, options := range []WorkerOptions{
		{MaxProvisioningConcurrency: -1}, {MaxProvisioningConcurrency: 33}, {MaxBulkJobs: -1}, {MaxGuestJobs: 33},
		{MaxImageBuildJobs: 4, MaxBulkJobs: 3}, {JobTimeoutMinutes: -1}, {JobTimeoutMinutes: 1441},
	} {
		if _, err := New(context.Background(), Config{MemberGroup: "members", AdminGroup: "admins", Worker: options}, nil); err == nil {
			t.Fatal("invalid startup accepted", options)
		}
	}
	if err := (WorkerOptions{MaxBulkJobs: 1}).Validate(); err != nil {
		t.Fatal("omitted image limit must adapt to smaller bulk budget", err)
	}
}

func TestConfiguredLanesKeepGuestCapacityAndCleanupUnderSaturation(t *testing.T) {
	s, teams := checkFixture(t)
	s.Config.Worker = WorkerOptions{MaxBulkJobs: 1, MaxGuestJobs: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := &guestLaneEngine{started: make(chan string, 2)}
	s.Engine = e
	s.activeJobs = map[string]runningJob{"bulk": {job: Job{Kind: "prepare"}, cancel: func() {}}}
	for _, team := range teams[:2] {
		j := failedCheck(t, s, team)
		if _, err := s.Store.DB.Exec(`UPDATE jobs SET status='queued' WHERE id=?`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	done := make(chan error, 3)
	defer func() { cancel(); workers.Wait() }()
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.started:
	case <-ctx.Done():
		t.Fatal("configured guest lane blocked")
	}
	var queued int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='check' AND status='queued'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("guest budget ignored", queued, err)
	}
	cleanup, err := s.Store.Enqueue(ctx, "access-cleanup", "startup")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("cleanup capacity consumed")
	}
	var state string
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, cleanup.ID).Scan(&state); err != nil || state != "complete" {
		t.Fatal(state, err)
	}
}

func TestProvisioningBudgetSharedByPracticeAndTeamsAndCancellationRetainsIdentity(t *testing.T) {
	s, v, p := learningFixture(t)
	s.Config.Worker.MaxProvisioningConcurrency = 1
	teams := setupMeeting(t, s.Store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := &blockedPracticeEngine{started: make(chan string, 3), release: make(chan struct{})}
	s.Engine = e
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	practiceDone := make(chan error, 1)
	go func() { practiceDone <- s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}) }()
	defer func() {
		cancel()
		if err := <-practiceDone; err != nil && !t.Failed() {
			t.Error(err)
		}
	}()
	select {
	case <-e.started:
	case <-ctx.Done():
		t.Fatal("practice never started")
	}
	var src lc.Source
	if err = s.Store.Get(ctx, "source", p.Template, &src); err != nil {
		t.Fatal(err)
	}
	plan, err := lc.PlanLab(src, s.env, nil)
	if err != nil {
		t.Fatal(err)
	}
	teamCtx, stopTeam := context.WithCancel(ctx)
	teamDone := make(chan error, 1)
	go func() { teamDone <- s.prepareTeam(teamCtx, "meeting", teams[0], plan) }()
	select {
	case <-e.started:
		t.Fatal("team bypassed shared budget")
	case <-time.After(30 * time.Millisecond):
	}
	stopTeam()
	if err = <-teamDone; !errors.Is(err, context.Canceled) {
		t.Fatal("queued native work ignored cancellation", err)
	}
	stored, err := s.Store.Teams(ctx, "meeting")
	if err != nil || stored[0].InstanceID != teams[0].InstanceID || stored[0].Status != teams[0].Status {
		t.Fatal("waiting team lost durable intent", stored, err)
	}
	close(e.release)
	// The gate becomes available to a retry with the original reserved identity.
	if err = s.prepareTeam(ctx, "meeting", teams[0], plan); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-e.started:
		if id != teams[0].InstanceID {
			t.Fatal(id)
		}
	case <-ctx.Done():
		t.Fatal("released provisioning slot leaked")
	}
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err = reopened.Teams(ctx, "meeting")
	if err != nil || stored[0].InstanceID != teams[0].InstanceID || stored[0].Status != "ready" {
		t.Fatal("configured preparation not durable", stored, err)
	}
}

type configuredBuildEngine struct {
	Engine
	started chan time.Duration
}

func (e *configuredBuildEngine) Build(ctx context.Context, _ lc.Plan) ([]lc.ImageRecord, error) {
	deadline, _ := ctx.Deadline()
	e.started <- time.Until(deadline)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConfiguredImageJobLimitAndTimeout(t *testing.T) {
	s, _ := prepareFixture(t)
	s.Config.Worker = WorkerOptions{MaxBulkJobs: 3, MaxImageBuildJobs: 1, JobTimeoutMinutes: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var src lc.Source
	if err := s.Store.Get(ctx, "source", "", &src); err != nil {
		t.Fatal(err)
	}
	for n := range 3 {
		id := fmt.Sprint(n)
		if err := s.Store.Put(ctx, "template", id, Template{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Put(ctx, "source", id, src); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Put(ctx, "template-images", id, []string{id}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Store.Enqueue(ctx, "build", id); err != nil {
			t.Fatal(err)
		}
	}
	e := &configuredBuildEngine{started: make(chan time.Duration, 3)}
	s.Engine = e
	var workers sync.WaitGroup
	done := make(chan error, 4)
	defer func() { cancel(); workers.Wait() }()
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case remaining := <-e.started:
		if remaining <= 0 || remaining > time.Minute {
			t.Fatal("configured timeout ignored", remaining)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("build did not start")
	}
	var queued int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='build' AND status='queued'`).Scan(&queued); err != nil || queued != 2 {
		t.Fatal("image job limit ignored", queued, err)
	}
}

func TestStoppedPracticeWaiterCannotProvisionAndCleanupBypassesGate(t *testing.T) {
	s, v, p := learningFixture(t)
	s.Config.Worker.MaxProvisioningConcurrency = 1
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e := &blockedPracticeEngine{started: make(chan string, 2), release: make(chan struct{})}
	s.Engine = e
	hold, err := s.acquireProvisioning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now().UTC())
	if err != nil {
		hold()
		t.Fatal(err)
	}
	initial, err := s.Store.practice(ctx, run.ID)
	if err != nil {
		hold()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}) }()
	time.Sleep(30 * time.Millisecond)
	if _, err = s.Store.endPractice(ctx, run.ID, run.UserID); err != nil {
		hold()
		cancel()
		<-done
		t.Fatal(err)
	}
	cleanupCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
	err = s.runPractice(cleanupCtx, Job{Kind: "practice-end", Target: run.ID})
	stop()
	hold()
	if err != nil {
		cancel()
		<-done
		t.Fatal("cleanup waited for provisioning capacity", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.started:
		t.Fatal("stopped waiter created native resources")
	default:
	}
	stored, err := s.Store.practice(ctx, run.ID)
	if err != nil || stored.Status != "ended" || stored.InstanceID != initial.InstanceID || stored.EndedAt == nil {
		t.Fatal("stop/cleanup lost durable allocation identity", stored, err)
	}
}
