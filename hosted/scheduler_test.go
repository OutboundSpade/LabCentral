package hosted

import (
	"context"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"sync"
	"testing"
	"time"
)

type scheduledEngine struct {
	Engine
	started chan string
	release chan struct{}
}

type guestLaneEngine struct {
	Engine
	started chan string
}

func (e *guestLaneEngine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.started <- instance
	<-ctx.Done()
	return ctx.Err()
}

func TestGuestCapacitySurvivesBulkSaturationAndKeepsCleanupSlot(t *testing.T) {
	s, teams := checkFixture(t)
	s.Config.Worker.MaxGuestJobs = 2
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := &guestLaneEngine{started: make(chan string, 3)}
	s.Engine = e
	// Three unrelated long operations fully occupy the bulk budget.
	s.activeJobs = map[string]runningJob{}
	for n, kind := range []string{"build", "build", "prepare"} {
		id := teams[n].ID
		s.activeJobs[id] = runningJob{job: Job{Kind: kind}, claims: []jobClaim{{"unrelated:" + id, true}}, cancel: func() {}}
	}
	var jobs []Job
	for _, team := range teams[:3] {
		j := failedCheck(t, s, team)
		if _, err := s.Store.DB.Exec(`UPDATE jobs SET status='queued' WHERE id=?`, j.ID); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	var workers sync.WaitGroup
	done := make(chan error, 6)
	defer func() { cancel(); workers.Wait() }()
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		select {
		case <-e.started:
		case <-ctx.Done():
			t.Fatal("bulk work blocked independent guest checks")
		}
	}
	var queued int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='check' AND status='queued'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("guest limit ignored", queued, err)
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
		t.Fatal("cleanup slot consumed")
	}
	var state string
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, cleanup.ID).Scan(&state); err != nil || state != "complete" {
		t.Fatal(state, err)
	}
}

func (e *scheduledEngine) BuildWithRecovery(ctx context.Context, p lc.Plan) ([]lc.ImageRecord, error) {
	e.started <- "build"
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return nil, nil
	}
}
func (e *scheduledEngine) Inspect(context.Context, string) (lc.Instance, error) {
	return lc.Instance{Status: "ready"}, nil
}
func TestSchedulerDeploysOldRevisionWhileUnrelatedBuildRuns(t *testing.T) {
	s, _ := prepareFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	src := new(lc.Source)
	if err := s.Store.Get(ctx, "source", "", src); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"new", "shared"} {
		if err := s.Store.Put(ctx, "template", id, Template{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Put(ctx, "source", id, src); err != nil {
			t.Fatal(err)
		}
	}
	for id, keys := range map[string][]string{"": {"old-image"}, "new": {"new-image"}, "shared": {"new-image"}} {
		if err := s.Store.Put(ctx, "template-images", id, keys); err != nil {
			t.Fatal(err)
		}
	}
	engine := &scheduledEngine{started: make(chan string, 3), release: make(chan struct{})}
	s.Engine = engine
	build, err := s.Store.Enqueue(ctx, "build", "new")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan error, 4)
	defer func() { cancel(); workers.Wait() }()
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.started:
	case <-ctx.Done():
		t.Fatal("build not dispatched")
	}
	shared, err := s.Store.Enqueue(ctx, "build", "shared")
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := s.Store.Enqueue(ctx, "prepare", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("old lab deployment blocked by unrelated build")
	}
	var state string
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, prepare.ID).Scan(&state); err != nil || state != "complete" {
		t.Fatal(state, err)
	}
	jobs := []Job{shared}
	if err = s.jobDetails(ctx, jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs[0].WaitingOn) != 1 || jobs[0].WaitingOn[0] != build.ID {
		t.Fatal("missing shared-image dependency", jobs)
	}
	select {
	case <-engine.started:
		t.Fatal("overlapping image jobs ran together")
	default:
	}
	close(engine.release)
}
func TestEndWaitsForCancelledMeetingWorkButNotBuild(t *testing.T) {
	s, _ := prepareFixture(t)
	ctx := context.Background()
	prepareCtx, stopPrepare := context.WithCancel(ctx)
	defer stopPrepare()
	buildCtx, stopBuild := context.WithCancel(ctx)
	defer stopBuild()
	s.activeJobs = map[string]runningJob{"prepare": {job: Job{Kind: "prepare"}, claims: []jobClaim{{"meeting:meeting", false}}, cancel: stopPrepare}, "build": {job: Job{Kind: "build"}, claims: []jobClaim{{"image:unrelated", true}}, cancel: stopBuild}}
	j, err := s.Store.Enqueue(ctx, "end", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	if err = s.dispatchJobs(ctx, &workers, make(chan error, 4)); err != nil {
		t.Fatal(err)
	}
	if prepareCtx.Err() == nil || buildCtx.Err() != nil {
		t.Fatal("wrong work cancelled")
	}
	var state string
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, j.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatal("end ran before dependency drained", state, err)
	}
}

// Native work is deliberately gated. Guest execution is unavailable in this
// fixture; these tests establish scheduling and persistence, not live health.
type preparingCheckEngine struct {
	*concurrentPrepareEngine
	releases     map[string]chan struct{}
	checked      chan string
	checkRelease chan struct{}
}

func (e *preparingCheckEngine) DeployWithID(ctx context.Context, _ lc.Plan, id string) (lc.Instance, error) {
	e.started <- id
	select {
	case <-e.releases[id]:
	case <-ctx.Done():
		return lc.Instance{}, ctx.Err()
	}
	inst := lc.Instance{ID: id, Status: "ready"}
	e.mu.Lock()
	e.instances[id] = inst
	e.mu.Unlock()
	return inst, nil
}
func (e *preparingCheckEngine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.checked <- instance
	select {
	case <-e.checkRelease:
		return errors.New("fixture guest execution unavailable")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestChecksRunAsEachTeamBecomesReadyDuringPreparation(t *testing.T) {
	s, teams := prepareFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bundle := InstructorBundle{Visibility: instructor.Visibility{Audience: "students"}, Practice: &instructor.Practice{DesktopMachine: "workstation"}, Version: 3, Checks: []TemplateCheck{{ID: "website", Name: "Website", Points: 100, Steps: []GuestPlaybook{{Machine: "server", Playbook: "check.yml"}}}}}
	if err := s.Store.Put(ctx, "template-instructor", "", bundle); err != nil {
		t.Fatal(err)
	}
	e := &preparingCheckEngine{concurrentPrepareEngine: &concurrentPrepareEngine{instances: map[string]lc.Instance{}, started: make(chan string, 8)}, releases: map[string]chan struct{}{}, checked: make(chan string, 2), checkRelease: make(chan struct{})}
	for _, team := range teams {
		e.releases[team.InstanceID] = make(chan struct{})
	}
	s.Engine = e
	prepare, err := s.Store.Enqueue(ctx, "prepare", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan error, 4)
	defer func() { cancel(); workers.Wait() }()
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		select {
		case <-e.started:
		case <-ctx.Done():
			t.Fatal("preparation did not start")
		}
	}
	// A stale queued operation for a still-preparing team must remain excluded.
	blockedRun := CheckRun{ID: "waiting-team-check", MeetingID: "meeting", TeamID: teams[1].ID, Status: "queued"}
	if err = s.Store.Put(ctx, "check", blockedRun.ID, blockedRun); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.Store.Enqueue(ctx, "check", blockedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	details := []Job{blocked}
	if err = s.jobDetails(ctx, details); err != nil {
		t.Fatal(err)
	}
	if len(details[0].WaitingOn) != 1 || details[0].WaitingOn[0] != prepare.ID {
		t.Fatal("missing same-team preparation dependency", details)
	}
	// Starting the fourth deployment proves the first team's readiness was saved
	// and its claim released, while the other deployments are still gated.
	close(e.releases[teams[0].InstanceID])
	select {
	case <-e.started:
	case <-ctx.Done():
		t.Fatal("ready team did not release its worker")
	}
	current, err := s.Store.Teams(ctx, "meeting")
	if err != nil || current[0].Status != "ready" {
		t.Fatal(current, err)
	}
	// A restarted scheduler reconstructs exactly the remaining team dependencies.
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := &Service{Store: reopened, env: s.env}
	claims, err := restored.jobClaims(ctx, prepare)
	if err != nil {
		t.Fatal(err)
	}
	readyClaims := []jobClaim{{"meeting:meeting", false}, {"team:" + teams[0].ID, true}}
	waitingClaims := []jobClaim{{"meeting:meeting", false}, {"team:" + teams[1].ID, true}}
	if claimsConflict(claims, readyClaims) || !claimsConflict(claims, waitingClaims) {
		t.Fatal("restart restored an overly broad dependency", claims)
	}
	// Scheduling ready-team checks retains admin/CSRF policy and only persists
	// the ready team's run, despite a meeting-wide prepare job being active.
	var session Session
	if err = s.Store.Get(ctx, "session", digest("cookie"), &session); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/admin/checks/run", "{}", s.Config.Origin, session.CSRF); w.Code != 403 {
		t.Fatal("student controlled concurrent checks", w.Code)
	}
	session.User.Admin = true
	if err = s.Store.Put(ctx, "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/admin/checks/run", "{}", s.Config.Origin, "wrong"); w.Code != 403 {
		t.Fatal("concurrent checks bypassed CSRF", w.Code)
	}
	if w := request(s, "POST", "/api/admin/checks/run", "{}", s.Config.Origin, session.CSRF); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var readyID string
	if err = s.Store.Get(ctx, "check-current", teams[0].ID, &readyID); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var readyJob Job
	for _, job := range jobs {
		if job.Target == readyID {
			readyJob = job
		}
	}
	if readyJob.ID == "" {
		t.Fatal("missing persisted ready-team check job")
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-e.checked:
		if id != teams[0].InstanceID {
			t.Fatal("checked a preparing team", id)
		}
	case <-ctx.Done():
		t.Fatal("ready-team check blocked by unrelated preparation")
	}
	// Exercises on that same ready team still wait for its in-flight check.
	exercise := BreakRun{ID: "same-team-exercise", MeetingID: "meeting", TeamID: teams[0].ID}
	if err = s.Store.Put(ctx, "exercise", exercise.ID, exercise); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Store.Enqueue(ctx, "exercise", exercise.ID)
	if err != nil {
		t.Fatal(err)
	}
	details = []Job{queued}
	if err = s.jobDetails(ctx, details); err != nil {
		t.Fatal(err)
	}
	if len(details[0].WaitingOn) != 1 || details[0].WaitingOn[0] != readyJob.ID {
		t.Fatal("exercise waited on unrelated preparation or overlapped its check", details)
	}
	close(e.checkRelease)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("check did not finish during preparation")
	}
	var run CheckRun
	if err = s.Store.Get(ctx, "check", readyID, &run); err != nil || run.Status != "complete" || len(run.Results) != 1 || run.Results[0].Status != "unavailable" {
		t.Fatal("missing truthful durable check result", run, err)
	}
	var status string
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, prepare.ID).Scan(&status); err != nil || status != "running" {
		t.Fatal("test did not overlap active preparation", status, err)
	}
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, blocked.ID).Scan(&status); err != nil || status != "queued" {
		t.Fatal("preparing team's operation was dispatched", status, err)
	}
	// End must cancel shared meeting operations and wait for all durable work.
	end, err := s.Store.Enqueue(ctx, "end", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, end.ID).Scan(&status); err != nil || status != "queued" {
		t.Fatal("end overlapped undrained preparation", status, err)
	}
}
