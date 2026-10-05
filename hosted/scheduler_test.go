package hosted

import (
	"context"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
	"testing"
	"time"
)

type scheduledEngine struct {
	Engine
	started chan string
	release chan struct{}
}

func (e *scheduledEngine) Build(ctx context.Context, p lc.Plan) ([]lc.ImageRecord, error) {
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
	s.activeJobs = map[string]runningJob{"prepare": {job: Job{Kind: "prepare"}, claims: []jobClaim{{"meeting:meeting", true}}, cancel: stopPrepare}, "build": {job: Job{Kind: "build"}, claims: []jobClaim{{"image:unrelated", true}}, cancel: stopBuild}}
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
