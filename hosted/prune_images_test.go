package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

type pruneTestEngine struct {
	*imageTestEngine
	refuse  string
	started chan struct{}
	release chan struct{}
}

func (e *pruneTestEngine) RemoveImage(ctx context.Context, key string) error {
	if key == e.refuse {
		return &lc.Error{Code: lc.Invalid, Message: "image is referenced by a retained instance"}
	}
	if e.started != nil {
		select {
		case e.started <- struct{}{}:
		default:
		}
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return e.imageTestEngine.RemoveImage(ctx, key)
}
func pruneJob(t *testing.T, s *Service) Job {
	t.Helper()
	w := request(s, "POST", "/api/admin/images/prune", `{}`, s.Config.Origin, "csrf")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var j Job
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	return j
}
func pruneState(t *testing.T, s *Service, j Job) imagePrune {
	t.Helper()
	var p imagePrune
	if err := s.Store.Get(context.Background(), "image-prune", j.Target, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func imageRevision(t *testing.T, s *Service, id string, keys ...string) {
	t.Helper()
	ctx := context.Background()
	if err := s.Store.Put(ctx, "template", id, Template{ID: id, Name: id, Built: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "template-images", id, keys); err != nil {
		t.Fatal(err)
	}
}
func TestPruneImagesAuthorizationAndCSRF(t *testing.T) {
	for _, tc := range []struct {
		admin        bool
		origin, csrf string
		want         int
	}{{false, "https://app.test", "csrf", 403}, {true, "https://app.test", "bad", 403}, {true, "https://elsewhere.test", "csrf", 403}, {true, "https://app.test", "", 403}} {
		s, _ := templateFixture(t)
		var session Session
		if err := s.Store.Get(context.Background(), "session", digest("cookie"), &session); err != nil {
			t.Fatal(err)
		}
		session.User.Admin = tc.admin
		if err := s.Store.Put(context.Background(), "session", session.ID, session); err != nil {
			t.Fatal(err)
		}
		origin := tc.origin
		if origin == "https://app.test" {
			origin = s.Config.Origin
		}
		w := request(s, http.MethodPost, "/api/admin/images/prune", `{}`, origin, tc.csrf)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		jobs, err := s.Store.Jobs(context.Background())
		if err != nil || len(jobs) != 0 {
			t.Fatal(jobs, err)
		}
	}
	s, _ := templateFixture(t)
	r := request(s, "POST", "/api/admin/images/prune", `{}`, s.Config.Origin, "csrf")
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = request(s, "POST", "/api/admin/images/prune", `{}`, s.Config.Origin, "csrf")
	if r.Code != 409 {
		t.Fatal("duplicate admitted", r.Code)
	}
}
func TestPruneImagesSelectionHistoryAndPrivateProgress(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	imageRevision(t, s, "meeting", "meeting-image")
	imageRevision(t, s, "active", "active-image")
	imageRevision(t, s, "pending", "pending-image")
	imageRevision(t, s, "cleanup", "cleanup-image")
	imageRevision(t, s, "ended", "ended-image")
	imageRevision(t, s, "queued-build", "queued-image")
	for _, key := range []string{"new-web", "new-workstation", "meeting-image", "active-image", "pending-image", "cleanup-image", "ended-image", "orphan-from-rebuild", "building-image", "queued-image"} {
		e.images = append(e.images, lc.ImageRecord{Key: key, Name: key})
	}
	e.builds = []lc.Command{{Image: lc.PlannedImage{CacheKey: "building-image"}}}
	if err := s.Store.Put(ctx, "meeting", "current", Meeting{ID: "m", Template: "meeting", Status: "ending"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, status string }{{"active", "ready"}, {"pending", "queued"}, {"cleanup", "ending"}, {"ended", "ended"}} {
		if err := s.Store.Put(ctx, "learning-publication", tc.id, LearningPublication{ID: tc.id, Template: tc.id}); err != nil {
			t.Fatal(err)
		}
		r, err := s.Store.reservePractice(ctx, tc.id, tc.id, 180, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.DB.Exec(`UPDATE practice_runs SET status=? WHERE id=?`, tc.status, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Store.Enqueue(ctx, "build", "queued-build"); err != nil {
		t.Fatal(err)
	}
	summary, _, err := s.imageInventory(ctx)
	if err != nil || summary.Images != 12 || summary.UnusedImages != 4 {
		t.Fatal(summary, err)
	}
	response := request(s, "GET", "/api/admin", "", "", "")
	var view AdminView
	if err = json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != 200 || view.ImageSummary == nil || *view.ImageSummary != summary {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	j := pruneJob(t, s)
	if err = s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, j, nil)
	slices.Sort(e.removed)
	if !slices.Equal(e.removed, []string{"ended-image", "orphan-from-rebuild", "web", "workstation"}) {
		t.Fatal(e.removed)
	}
	templates, err := s.templates(ctx)
	if err != nil || len(templates) != 8 {
		t.Fatal(len(templates), err)
	}
	for _, rev := range templates {
		if rev.ID == "old" && (rev.Built || rev.ImagesReady != 0 || rev.ImagesTotal != 2) {
			t.Fatal(rev)
		}
	}
	var old Template
	if err = s.Store.Get(ctx, "template", "old", &old); err != nil || old.Built {
		t.Fatal("record not reconciled", old, err)
	}
	jobs := []Job{j}
	if err = s.jobDetails(ctx, jobs); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(jobs)
	if len(jobs[0].ImageOutcomes) != 4 || strings.Contains(string(raw), `"key"`) {
		t.Fatal(string(raw))
	}
	if jobLane("prune-images") != "bulk" {
		t.Fatal("wrong lane")
	}
}
func TestPruneImagesUnknownIntentSurvivesRestartAndRetries(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	e.fail = "workstation"
	j := pruneJob(t, s)
	err := s.runJob(ctx, j)
	if err == nil {
		t.Fatal("unknown removal reported complete")
	}
	finishImageJob(t, s, j, err)
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered := &Service{Store: reopened, Engine: e, Config: s.Config}
	p := pruneState(t, recovered, j)
	if p.Images[1].Outcome.Status != "unknown" {
		t.Fatal(p)
	}
	blocked, attention, err := recovered.retryDisposition(ctx, j)
	if err != nil || blocked != "" || !attention {
		t.Fatal(blocked, attention, err)
	}
	if err = recovered.revisionPruneBlock(ctx, "old"); err == nil {
		t.Fatal("unresolved cleanup allowed allocation")
	}
	if _, err = recovered.queueTemplateOperation(ctx, "build", "old"); err == nil {
		t.Fatal("unresolved cleanup allowed build")
	}
	e.fail = ""
	response := request(recovered, "POST", "/api/admin/jobs/retry", jsonValue(TargetRequest{ID: j.ID}), recovered.Config.Origin, "csrf")
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	var retry Job
	if err = json.Unmarshal(response.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if err = recovered.runJob(ctx, retry); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, recovered, retry, nil)
	if len(e.removed) != 2 {
		t.Fatal("confirmed removal repeated", e.removed)
	}
	if err = recovered.revisionPruneBlock(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	blocked, attention, err = recovered.retryDisposition(ctx, j)
	if err != nil || blocked == "" || attention {
		t.Fatal(blocked, attention, err)
	}
}
func TestPruneImagesReferencedSkipAndRevalidation(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	s.Engine = &pruneTestEngine{imageTestEngine: e, refuse: "web"}
	j := pruneJob(t, s)
	// A practice allocation committed after queuing is rechecked before effects.
	if err := s.Store.Put(ctx, "meeting", "current", Meeting{ID: "m", Template: "old", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if len(e.removed) != 0 {
		t.Fatal(e.removed)
	}
	if err := s.Store.Delete(ctx, "meeting", "current"); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, j, nil)
	j = pruneJob(t, s)
	if err := s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	p := pruneState(t, s, j)
	if p.Images[0].Outcome.Status != "skipped" || !strings.Contains(p.Images[0].Outcome.Message, "retained instance") || !slices.Equal(e.removed, []string{"workstation"}) {
		t.Fatal(p, e.removed)
	}
}
func TestPruneClaimsFenceConcurrentBuildAndPreparation(t *testing.T) {
	s, e := templateFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	engine := &pruneTestEngine{imageTestEngine: e, started: make(chan struct{}, 1), release: make(chan struct{})}
	s.Engine = engine
	j := pruneJob(t, s)
	var workers sync.WaitGroup
	done := make(chan error, 4)
	defer func() { cancel(); workers.Wait() }()
	if err := s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.started:
	case <-ctx.Done():
		t.Fatal("prune did not start")
	}
	// A real API allocation must refuse a reserved revision during native deletion.
	w := request(s, "POST", "/api/admin/meeting", `{"name":"Test","template":"old","hours":1}`, s.Config.Origin, "csrf")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "cleanup") {
		t.Fatal(w.Code, w.Body.String())
	}
	build, err := s.Store.Enqueue(ctx, "build", "old")
	if err != nil {
		t.Fatal(err)
	}
	// Bypass API admission deliberately to also exercise scheduler preparation claims.
	if err = s.Store.Put(ctx, "meeting", "current", Meeting{ID: "m", Template: "old", Status: "waiting"}); err != nil {
		t.Fatal(err)
	}
	prep, err := s.Store.Enqueue(ctx, "prepare", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchJobs(ctx, &workers, done); err != nil {
		t.Fatal(err)
	}
	for _, job := range []Job{build, prep} {
		var status string
		if err = s.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, job.ID).Scan(&status); err != nil || status != "queued" {
			t.Fatal(job.Kind, status, err)
		}
		claims, err := s.jobClaims(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		pruneClaims, err := s.jobClaims(ctx, j)
		if err != nil {
			t.Fatal(err)
		}
		if !claimsConflict(claims, pruneClaims) {
			t.Fatal("missing dependency", job.Kind)
		}
	}
	close(engine.release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestPruneMissingNeededIdentitiesFailsClosed(t *testing.T) {
	s, e := templateFixture(t)
	if err := s.Store.Delete(context.Background(), "template-images", "new"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/admin/images/prune", `{}`, s.Config.Origin, "csrf")
	if w.Code != 503 || len(e.removed) != 0 {
		t.Fatal(w.Code, w.Body.String(), e.removed)
	}
	if _, _, err := s.imageInventory(context.Background()); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPrunePreservesCurrentPlanAndRemovesSupersededBuildKeys(t *testing.T) {
	s, v, pub := learningFixture(t)
	ctx := context.Background()
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "template-head", "workstation", pub.Template); err != nil {
		t.Fatal(err)
	}
	engine := &imageTestEngine{}
	s.Engine = engine
	first, err := s.queueTemplateOperation(ctx, "build", pub.Template)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, first, nil)
	before, err := s.templateKeys(ctx, pub.Template)
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	// A toolchain change produces new current planned keys, even before building.
	s.env.Build.ToolchainID = "updated-toolchain"
	plan, err := s.plan(ctx, pub.Template)
	if err != nil {
		t.Fatal(err)
	}
	planned := ""
	for _, im := range plan.Images {
		if im.Playbook != "" {
			planned = im.CacheKey
		}
	}
	if planned == "" || planned == before[0] {
		t.Fatal("fixture did not change identity")
	}
	engine.images = append(engine.images, lc.ImageRecord{Key: planned, Name: "workstation"})
	summary, _, err := s.imageInventory(ctx)
	if err != nil || summary.UnusedImages != 0 {
		t.Fatal("current planned or saved key unprotected", summary, err)
	}
	// The successful rebuild replaces saved keys. The old native image becomes
	// an orphan and is discovered from inventory rather than revision records.
	rebuild, err := s.queueTemplateOperation(ctx, "build", pub.Template)
	if err != nil {
		t.Fatal(err)
	}
	engine.images = engine.images[:1]
	if err = s.runJob(ctx, rebuild); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, rebuild, nil)
	after, err := s.templateKeys(ctx, pub.Template)
	if err != nil || !slices.Equal(after, []string{planned}) {
		t.Fatal(after, err)
	}
	j := pruneJob(t, s)
	if err = s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(engine.removed, before) {
		t.Fatal("orphan removal", engine.removed, before)
	}
	var revision Template
	if err = s.Store.Get(ctx, "template", pub.Template, &revision); err != nil || !revision.Built {
		t.Fatal(revision, err)
	}
}
func TestPruneConfirmedRemovalStillFencesAllocationUntilRecordsCommit(t *testing.T) {
	s, v, pub := learningFixture(t)
	ctx := context.Background()
	intent := imagePrune{Images: []pruneImage{{Key: "shared-workstation", Outcome: ImagePruneOutcome{Status: "removed"}}}}
	if err := s.Store.Put(ctx, "image-prune", "interrupted", intent); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/learning/"+pub.ID+"/start", `{}`, s.Config.Origin, v.CSRF)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "cleanup") {
		t.Fatal(w.Code, w.Body.String())
	}
	run, err := s.Store.activePractice(ctx, v.User.ID)
	if err != nil || run != nil {
		t.Fatal("allocated during incomplete record reconciliation", run, err)
	}
}
