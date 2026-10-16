package hosted

import (
	"context"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func boolPointer(v bool) *bool { return &v }
func mustSource(t *testing.T, s *Service) lc.Source {
	t.Helper()
	var src lc.Source
	if e := s.Store.Get(context.Background(), "source", "pinned", &src); e != nil {
		t.Fatal(e)
	}
	return src
}
func baselineAttemptPublication(t *testing.T, s *Service, v Session) LearningPublication {
	t.Helper()
	lab := decodeLearning[LearningLab](t, request(s, "POST", "/api/templates/pinned/attempt", `{"requestID":"`+randomID(12)+`"}`, s.Config.Origin, v.CSRF), 201)
	var p LearningPublication
	if err := s.Store.Get(context.Background(), "learning-publication", lab.ID, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func importFixture(t *testing.T) (*Service, string) {
	t.Helper()
	s, _, _ := learningFixture(t)
	root := t.TempDir()
	if e := os.CopyFS(filepath.Join(root, "recovery"), os.DirFS("../templates/ccdc-service-recovery")); e != nil {
		t.Fatal(e)
	}
	return s, root
}
func TestTemplateImportIsolationAndAdminFailures(t *testing.T) {
	s, root := importFixture(t)
	bad := filepath.Join(root, "bad")
	if e := os.CopyFS(bad, os.DirFS("../templates/ccdc-service-recovery")); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(bad, "instructor.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	raw = []byte(strings.Replace(string(raw), "check: scheduler", "check: missing", 1))
	if e = os.WriteFile(filepath.Join(bad, "instructor.yaml"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	if e = s.importSnapshot(ctx, root, commit, time.Now().UTC(), "local fixture"); e != nil {
		t.Fatal(e)
	}
	id := digest(commit + ":recovery")[:24]
	var b InstructorBundle
	var src lc.Source
	var template Template
	var head string
	for _, o := range []struct {
		kind, id string
		out      any
	}{{"template-instructor", id, &b}, {"source", id, &src}, {"template", id, &template}, {"template-head", "ccdc-service-recovery", &head}} {
		if e = s.Store.Get(ctx, o.kind, o.id, o.out); e != nil {
			t.Fatal(o.kind, e)
		}
	}
	if head != id || b.Version != 3 || template.Instructor == nil {
		t.Fatal("incomplete revision")
	}
	for _, f := range src.Files {
		if strings.HasPrefix(f.Path, "instructor") {
			t.Fatal("private source persisted", f.Path)
		}
	}
	var report TemplateImportReport
	if e = s.Store.Get(ctx, "template-import-report", "current", &report); e != nil || len(report.Failures) != 1 || report.Failures[0].Directory != "bad" || !strings.Contains(report.Failures[0].Error, "stages[2].completion.check") {
		t.Fatal(report, e)
	}
	var v Session
	_ = s.Store.Get(ctx, "session", digest("cookie"), &v)
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	response := request(s, "GET", "/api/admin", "", "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"directory":"bad"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	if request(s, "GET", "/api/admin", "", "", "").Code != 403 {
		t.Fatal("student read import failures")
	}
}
func TestTemplateImportRollbackAndIdempotentRevision(t *testing.T) {
	s, root := importFixture(t)
	ctx := context.Background()
	src, b, e := instructor.LoadTemplate(filepath.Join(root, "recovery"), true)
	if e != nil {
		t.Fatal(e)
	}
	commit := strings.Repeat("b", 40)
	id := digest(commit + ":recovery")[:24]
	at := time.Now().UTC()
	if _, e = s.Store.DB.Exec(`CREATE TRIGGER interrupt_import BEFORE INSERT ON objects WHEN NEW.kind='template-head' BEGIN SELECT RAISE(ABORT,'interrupted'); END`); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.importRevision(ctx, id, src, b, "handover", commit, at, "test"); e == nil {
		t.Fatal("interruption ignored")
	}
	var count int
	if e = s.Store.DB.QueryRow(`SELECT count(*) FROM objects WHERE id=?`, id).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial revision persisted", count, e)
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER interrupt_import`); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.importRevision(ctx, id, src, b, "handover", commit, at, "test"); e != nil {
		t.Fatal(e)
	}
	var before Template
	_ = s.Store.Get(ctx, "template", id, &before)
	before.Built = true
	before.Removing = true
	_ = s.Store.Put(ctx, "template", id, before)
	if e = s.Store.importRevision(ctx, id, src, b, "handover", commit, at, "test"); e != nil {
		t.Fatal(e)
	}
	var after Template
	_ = s.Store.Get(ctx, "template", id, &after)
	if !after.Built || !after.Removing || !after.ImportedAt.Equal(*before.ImportedAt) {
		t.Fatal("re-import lost retained state", after)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = s.Store.importRevision(cancelled, "interrupted", src, b, "", commit, at, ""); e == nil {
		t.Fatal("cancelled import committed")
	}
}
func TestPublicTemplateRepositoryImport(t *testing.T) {
	s, _, _ := learningFixture(t)
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../templates")); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	if err := s.importSnapshot(context.Background(), root, commit, time.Now().UTC(), "public template fixture"); err != nil {
		t.Fatal(err)
	}
	var report TemplateImportReport
	if err := s.Store.Get(context.Background(), "template-import-report", "current", &report); err != nil || len(report.Failures) != 0 {
		t.Fatal(report, err)
	}
	var b InstructorBundle
	if err := s.Store.Get(context.Background(), "template-instructor", digest(commit + ":ccdc-service-recovery")[:24], &b); err != nil || b.Version != 3 {
		t.Fatal(b.Version, err)
	}
}

type timedStepEngine struct {
	Engine
	deadline time.Duration
}

func (e *timedStepEngine) WithSSHMaterial(ctx context.Context, _ string, _ string, _ func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	at, _ := ctx.Deadline()
	e.deadline = time.Until(at)
	return context.DeadlineExceeded
}
func TestGuestTimeoutIsUnknownAndUsesAuthoredDeadline(t *testing.T) {
	s, teams := exerciseFixture(t)
	engine := &timedStepEngine{}
	s.Engine = engine
	s.exerciseRunner = nil
	ctx := context.Background()
	b := InstructorBundle{Reset: []GuestPlaybook{{Machine: "web", Playbook: "reset.yml", TimeoutSeconds: 10}}, Breaks: []TemplateBreak{{ID: "stop-web"}}}
	_ = s.Store.Put(ctx, "template-instructor", "", b)
	run := queueExercise(t, s, teams[0].ID, "stop-web")
	if e := s.runExercise(ctx, Job{Target: run.ID}); !errors.Is(e, errGuestUnknown) {
		t.Fatal("timeout reported as failure", e)
	}
	var stored BreakRun
	_ = s.Store.Get(ctx, "exercise", run.ID, &stored)
	if stored.Status != "unknown" || engine.deadline < 24*time.Second || engine.deadline > 25*time.Second {
		t.Fatal(stored, engine.deadline)
	}
	if e := s.runExercise(ctx, Job{Target: run.ID}); e == nil {
		t.Fatal("unknown mutation replayed")
	}
	if strings.Contains(guestPlaybookLauncher, "timeout=90") || !strings.Contains(guestPlaybookLauncher, "timeout=p['timeoutSeconds']") {
		t.Fatal("launcher ignores timeout")
	}
}

func TestTemplateDesktopMachineAuthorization(t *testing.T) {
	s, v, _ := learningFixture(t)
	ctx := context.Background()
	b := InstructorBundle{Visibility: instructor.Visibility{Audience: "students"}, Practice: &instructor.Practice{DesktopMachine: "desk"}}
	root := t.TempDir()
	if e := os.CopyFS(filepath.Join(root, "lab"), os.DirFS("../examples/workstation")); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "lab", "lab.yaml")
	raw, _ := os.ReadFile(path)
	_ = os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "workstation", "desk")), 0600)
	if e := os.Rename(filepath.Join(root, "lab", "images", "workstation.yml"), filepath.Join(root, "lab", "images", "desk.yml")); e != nil {
		t.Fatal(e)
	}
	src, e := lc.Load(filepath.Join(root, "lab"))
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Store.Put(ctx, "source", "pinned", src)
	_ = s.Store.Put(ctx, "template-instructor", "pinned", b)
	if e = s.practiceTemplateReady(ctx, "pinned"); e != nil {
		t.Fatal("custom practice desktop rejected", e)
	}
	teams := setupMeeting(t, s.Store)
	m, _ := s.Store.Meeting(ctx)
	m.Template = "pinned"
	_ = s.Store.Put(ctx, "meeting", "current", m)
	_, _ = s.Store.DB.Exec(`UPDATE teams SET status='ready',instance_id='instance'`)
	id := digest("instance\x00desk\x00" + v.User.ID)[:24]
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	original := &desktopSession{record: accessRecord{ID: id, UserID: v.User.ID, SessionID: v.ID, TeamID: teams[0].ID, InstanceID: "instance", Expires: v.Expires}, info: lc.AccessInfo{ID: id, URL: "http://127.0.0.1:1/launch?token=old", Expires: v.Expires}, ctx: dc, cancel: cancel}
	s.desktops[id] = original
	engine := &reconnectEngine{info: original.info}
	s.Engine = engine
	launch := func() int {
		w := httptest.NewRecorder()
		s.launch(w, httptest.NewRequest("POST", "/api/access", nil), v)
		return w.Code
	}
	if code := launch(); code != 409 || engine.opens != 0 {
		t.Fatal("desktop without membership authorized", code)
	}
	if e = s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, m.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.FinishSwitch(ctx, v.User.ID); e != nil {
		t.Fatal(e)
	}
	if code := launch(); code != 200 || engine.machine != "desk" {
		t.Fatal("template desktop ignored", code, engine.machine)
	}
	run, e := s.Store.reservePractice(ctx, v.User.ID, "linux", 180, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.Store.DB.Exec(`UPDATE practice_runs SET status='ready',instance_id='instance' WHERE id=?`, run.ID)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/learning/linux/access", nil)
	r.SetPathValue("id", "linux")
	s.launchPractice(w, r, v)
	if w.Code != 200 || engine.machine != "desk" {
		t.Fatal("practice desktop ignored", w.Code)
	}
	b.Visibility.HiddenMachines = []string{"desk"}
	_ = s.Store.Put(ctx, "template-instructor", "pinned", b)
	before := engine.opens
	if code := launch(); code != 409 || engine.opens != before {
		t.Fatal("hidden desktop authorized", code)
	}
}

func TestTemplateAnswerAlternativesPersistAndRemainPrivate(t *testing.T) {
	s, v, b := stageFixture(t)
	ctx := context.Background()
	b.Stages[0].Completion.Answer = &instructor.Answer{Accepted: []string{"cron", "crond"}, CaseSensitive: false}
	_ = s.Store.Put(ctx, "template-instructor", "pinned", b)
	p := baselineAttemptPublication(t, s, v)
	root := s.Store.Root
	_ = s.Store.Close()
	var err error
	s.Store, err = OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, "GET", "/api/learning/"+p.ID, "", "", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "crond") || strings.Contains(response.Body.String(), "accepted") {
		t.Fatal("answer alternatives leaked", response.Body.String())
	}
	response = request(s, "POST", "/api/learning/"+p.ID+"/answer", `{"stepID":"inspect","answer":" CROND\n"}`, s.Config.Origin, v.CSRF)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	progress, e := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if e != nil || progress.CompletedSteps != 1 {
		t.Fatal("folded alternative not accepted", progress, e)
	}
}

func TestExpiredStepDoesNotScoreAsFailedOrAdvancePractice(t *testing.T) {
	s, teams := checkFixture(t)
	ctx := context.Background()
	engine := &timedStepEngine{}
	s.Engine = engine
	var b InstructorBundle
	_ = s.Store.Get(ctx, "template-instructor", "", &b)
	b.Checks[0].Steps[0].TimeoutSeconds = 10
	_ = s.Store.Put(ctx, "template-instructor", "", b)
	meeting, _ := s.Store.Meeting(ctx)
	if err := s.queueChecks(ctx, *meeting, nil); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Store.QueuedJobs(ctx)
	if len(jobs) != len(teams) {
		t.Fatal(jobs)
	}
	if err := s.runCheck(ctx, jobs[0]); err != nil {
		t.Fatal(err)
	}
	var scored CheckRun
	_ = s.Store.Get(ctx, "check", jobs[0].Target, &scored)
	if len(scored.Results) != 1 || scored.Results[0].Status != "unavailable" || scored.Results[0].Passed {
		t.Fatal("timeout scored as known failure/pass", scored)
	}
	s, v, _ := learningFixture(t)
	practice := practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	_ = run
	progress := LearningProgress{LabID: "linux", CompletedSteps: 1}
	_ = s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, "linux"), progress)
	response := request(s, "POST", "/api/learning/linux/check", `{"stepID":"restore"}`, s.Config.Origin, v.CSRF)
	queued := decodeLearning[LearningCheck](t, response, 202)
	// Keep allocation operations on the fixture, but expire the guest callback.
	s.Engine = &timedStepEngine{Engine: practice}
	if err := s.runLearningCheck(ctx, Job{Target: queued.ID}); !errors.Is(err, errGuestUnknown) {
		t.Fatal(err)
	}
	var record learningCheckRecord
	_ = s.Store.Get(ctx, "learning-check", queued.ID, &record)
	after, err := s.Store.learningProgress(ctx, v.User.ID, "linux")
	if err != nil || record.Status != "unavailable" || after.CompletedSteps != 1 {
		t.Fatal("unknown check advanced progress", record, after, err)
	}
}
