package hosted

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func learningFixture(t *testing.T) (*Service, Session, LearningPublication) {
	t.Helper()
	s, v, _ := serviceFor(t)
	src, err := lc.Load("../examples/workstation")
	if err != nil {
		t.Fatal(err)
	}
	s.env = lc.Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: lc.AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}}}
	s.env.Build = lc.BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	s.env.AWS.DesktopProfile = "license-profile"
	ctx := context.Background()
	for _, o := range []struct {
		kind  string
		value any
	}{{"source", src}, {"template", Template{ID: "pinned", Built: true}}, {"template-images", []string{"shared-workstation"}}, {"template-instructor", InstructorBundle{Visibility: instructor.Visibility{Audience: "students"}, Practice: &instructor.Practice{DesktopMachine: "workstation"}, Version: 3, Notes: "PRIVATE-INSTRUCTOR-SENTINEL", Files: []lc.File{{Path: "check.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")}}, Checks: []TemplateCheck{{ID: "service", Name: "Service", Points: 10, Steps: []GuestPlaybook{{Machine: "workstation", Playbook: "check.yml"}}}}}}} {
		if err = s.Store.Put(ctx, o.kind, "pinned", o.value); err != nil {
			t.Fatal(err)
		}
	}
	p := LearningPublication{ID: "linux", Template: "pinned", Title: "Linux service recovery", Description: "Investigate and restore a service.", Mode: "self-paced", EstimatedMinutes: 30, Published: true, PublishedAt: time.Now().UTC(), ReleasedSteps: 2, Steps: []LearningStepInput{{ID: "identify", Title: "Identify the service", Instructions: "Inspect the service unit.", Kind: "answer", Prompt: "Which service is stopped?", Answer: "PRIVATE-ANSWER-SENTINEL"}, {ID: "restore", Title: "Restore the service", Instructions: "Bring the service back up.", Kind: "action", CheckID: "service"}}}
	if err = s.Store.Put(ctx, "learning-publication", p.ID, p); err != nil {
		t.Fatal(err)
	}
	return s, v, p
}
func decodeLearning[T any](t *testing.T, w *httptest.ResponseRecorder, want int) T {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func TestLearningAuthorizationPublicationAndPrivateDownloads(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	body, _ := json.Marshal(p)
	for _, path := range []string{"/api/admin/learning"} {
		method := "POST"
		if path == "/api/admin/learning" {
			method = "GET"
		}
		if w := request(s, method, path, string(body), s.Config.Origin, v.CSRF); w.Code != 403 {
			t.Fatal("student administration", path, w.Code)
		}
	}
	for _, path := range []string{"answer", "check", "start", "stop", "access"} {
		if w := request(s, "POST", "/api/learning/linux/"+path, `{}`, "https://foreign.invalid", v.CSRF); w.Code != 403 {
			t.Fatal("foreign origin", path, w.Code)
		}
	}
	w := request(s, "GET", "/api/learning/linux", "", "", "")
	view := decodeLearning[LearningView](t, w, 200)
	if strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), "checkID") || !view.CloudReady {
		t.Fatal("private student view", w.Body.String())
	}
	download := request(s, "GET", "/api/learning/linux/download", "", "", "")
	if download.Code != 200 {
		t.Fatal(download.Body.String())
	}
	gz, err := gzip.NewReader(bytes.NewReader(download.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(h.Name, "instructor") || strings.Contains(string(data), "PRIVATE-") || strings.Contains(h.Name, "environment") {
			t.Fatal("private download", h.Name)
		}
		count++
	}
	if count < 2 {
		t.Fatal("empty download")
	}
	v.User.Admin = true
	if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"publish", "release", "unpublish"} {
		if w := request(s, "POST", "/api/admin/learning/"+path, string(body), s.Config.Origin, v.CSRF); w.Code != 404 {
			t.Fatal("removed authoring route remains", path, w.Code)
		}
	}
	_ = s.Store.Put(ctx, "learning-progress", progressKey("another", p.ID), LearningProgress{LabID: p.ID, CompletedSteps: 2, LastAnswer: "OTHER-STUDENT-SENTINEL"})
	if w := request(s, "GET", "/api/learning", "", "", ""); strings.Contains(w.Body.String(), "OTHER-STUDENT") {
		t.Fatal("foreign progress disclosed")
	}
}
func TestLearningAnswersConcurrentReplayAndPersistedInstructorGates(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	wrong := decodeLearning[LearningProgress](t, request(s, "POST", "/api/learning/linux/answer", `{"stepID":"identify","answer":"wrong"}`, s.Config.Origin, v.CSRF), 200)
	if wrong.CompletedSteps != 0 || wrong.LastAnswer != "wrong" {
		t.Fatal(wrong)
	}
	if w := request(s, "POST", "/api/learning/linux/answer", `{"stepID":"restore","answer":"PRIVATE-ANSWER-SENTINEL"}`, s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal("skipped stage")
	}
	var wg sync.WaitGroup
	codes := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			codes <- request(s, "POST", "/api/learning/linux/answer", `{"stepID":"identify","answer":" PRIVATE-ANSWER-SENTINEL "}`, s.Config.Origin, v.CSRF).Code
		})
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for code := range codes {
		if code == 200 {
			accepted++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	progress, err := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if err != nil || accepted != 1 || progress.CompletedSteps != 1 {
		t.Fatal("replay advanced progress", progress, accepted, err)
	}
	p.Mode = "instructor"
	p.ReleasedSteps = 1
	p.Steps[1].Kind = "instructor"
	p.Steps[1].CheckID = ""
	_ = s.Store.Put(ctx, "learning-publication", p.ID, p)
	if w := request(s, "POST", "/api/learning/linux/check", `{"stepID":"restore"}`, s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal("instructor gate bypassed")
	}
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	p.ReleasedSteps = 2
	_ = s.Store.Put(ctx, "learning-publication", p.ID, p) // historical instructor release
	root := s.Store.Root
	_ = s.Store.Close()
	s.Store, err = OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Store.Close()
	decodeLearning[LearningCheck](t, request(s, "POST", "/api/learning/linux/check", `{"stepID":"restore"}`, s.Config.Origin, v.CSRF), 200)
	progress, err = s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if err != nil || progress.CompletedSteps != 2 {
		t.Fatal("restart lost gate or progress", progress, err)
	}
}

type practiceTestEngine struct {
	Engine
	mu                                    sync.Mutex
	instances                             map[string]lc.Instance
	deployError, destroyError, guestError error
	deploys, destroys, checks             int
}

func (e *practiceTestEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.instances[id]
	if !ok {
		return i, sql.ErrNoRows
	}
	return i, nil
}
func (e *practiceTestEngine) DeployWithID(_ context.Context, p lc.Plan, id string) (lc.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deploys++
	i := lc.Instance{ID: id, Plan: p, Status: "ready"}
	if e.deployError != nil {
		i.Status = "recovery_required"
	}
	e.instances[id] = i
	return i, e.deployError
}
func (e *practiceTestEngine) Recover(_ context.Context, id string) (lc.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.instances[id]
	i.Status = "ready"
	e.instances[id] = i
	return i, nil
}
func (e *practiceTestEngine) Destroy(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.destroys++
	if e.destroyError != nil {
		return e.destroyError
	}
	delete(e.instances, id)
	return nil
}
func (e *practiceTestEngine) WithSSHMaterial(context.Context, string, string, func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks++
	return e.guestError
}
func practiceEngineFor(t *testing.T, s *Service) *practiceTestEngine {
	t.Helper()
	e := &practiceTestEngine{Engine: s.Engine, instances: map[string]lc.Instance{}}
	s.Engine = e
	return e
}
func startReadyPractice(t *testing.T, s *Service, v Session) *PracticeRun {
	t.Helper()
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/linux/start", `{}`, s.Config.Origin, v.CSRF), 202)
	if err := s.runPractice(context.Background(), Job{Kind: "practice-prepare", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Store.activePractice(context.Background(), v.User.ID)
	if err != nil || out.Status != "ready" {
		t.Fatal(out, err)
	}
	return out
}

func TestPracticeQuotaReservationConcurrencyAndUTCWeekSplit(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			p, e := st.reservePractice(ctx, "user", "lab", 180, now)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- p.ID
		})
	}
	wg.Wait()
	close(ids)
	id := ""
	for candidate := range ids {
		if id == "" {
			id = candidate
		}
		if candidate != id {
			t.Fatal("duplicate personal allocations")
		}
	}
	jobs, _ := st.Jobs(ctx)
	if len(jobs) != 1 {
		t.Fatal("duplicate provision jobs", jobs)
	}
	if _, err := st.reservePractice(ctx, "user", "other", 180, now); err == nil {
		t.Fatal("second active lab")
	}
	q, err := st.quota(ctx, "user", 180, now.Add(30*time.Minute))
	if err != nil || q.UsedSeconds != 1800 || q.RemainingSeconds != 9000 {
		t.Fatal(q, err)
	}
	start, end := weekBounds(now)
	if start.Weekday() != time.Monday || end.Sub(start) != 7*24*time.Hour {
		t.Fatal("UTC week", start, end)
	}
	// A run crossing Monday bills only this week's overlap, and prior-week
	// history cannot exhaust a fresh week. Unconfirmed cleanup keeps billing.
	_, err = st.DB.Exec(`INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires,ended) VALUES('old','student','lab','old-instance','ended',?,?,?)`, start.Add(-time.Hour).Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), start.Add(3*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	q, err = st.quota(ctx, "student", 180, now)
	if err != nil || q.RemainingSeconds != 0 || q.UsedSeconds != 10800 {
		t.Fatal(q, err)
	}
	if _, err = st.reservePractice(ctx, "student", "lab", 180, now); err == nil {
		t.Fatal("exhausted quota admitted")
	}
	run, err := st.reservePractice(ctx, "student", "lab", 180, end.Add(time.Hour))
	if err != nil || run.Expires.Sub(run.Created) != 3*time.Hour {
		t.Fatal("reset did not replenish", run, err)
	}
}
func TestPracticeLostResponsesRetainIdentityUntilConfirmedCleanup(t *testing.T) {
	s, v, _ := learningFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/linux/start", `{}`, s.Config.Origin, v.CSRF), 202)
	e.deployError = errors.New("native response lost")
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err == nil {
		t.Fatal("lost response treated as failed create")
	}
	before, err := s.Store.practice(ctx, run.ID)
	if err != nil || before.Status != "failed" {
		t.Fatal(before, err)
	}
	e.deployError = nil
	if err = s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Store.practice(ctx, run.ID)
	if before.InstanceID != after.InstanceID || e.deploys != 1 || after.Status != "ready" {
		t.Fatal("recovery replaced allocation", after, e.deploys)
	}
	// Foreign ownership cannot stop this allocation, including a guessed run ID.
	if _, err = s.Store.endPractice(ctx, run.ID, "foreign"); err == nil {
		t.Fatal("foreign cleanup accepted")
	}
	decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/linux/stop", `{}`, s.Config.Origin, v.CSRF), 202)
	e.destroyError = errors.New("termination response lost")
	if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err == nil {
		t.Fatal("unknown deletion reported complete")
	}
	after, _ = s.Store.practice(ctx, run.ID)
	if after.Status != "ending" || after.EndedAt != nil || after.InstanceID != before.InstanceID {
		t.Fatal("cleanup intent lost", after)
	}
	q, err := s.Store.quota(ctx, v.User.ID, 180, run.Created.Add(time.Hour))
	if err != nil || q.UsedSeconds != 3600 {
		t.Fatal("unconfirmed cleanup stopped billing", q, err)
	}
	root := s.Store.Root
	_ = s.Store.Close()
	s.Store, err = OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Store.Close()
	e.destroyError = nil
	if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err != nil || e.destroys != 2 {
		t.Fatal("confirmed deletion repeated", e.destroys, err)
	}
	after, _ = s.Store.practice(ctx, run.ID)
	if after.Status != "ended" || after.EndedAt == nil {
		t.Fatal(after)
	}
}
func TestLearningActionObservationAdvancesOnlyConfirmedCurrentStage(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	_ = s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), LearningProgress{LabID: p.ID, CompletedSteps: 1})
	s.scheduleLearningChecks(ctx)
	s.scheduleLearningChecks(ctx)
	jobs, _ := s.Store.Jobs(ctx)
	var job Job
	count := 0
	for _, j := range jobs {
		if j.Kind == "learning-check" {
			job = j
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate automated observations", count)
	}
	e.guestError = errPlaybookFailed
	if err := s.runLearningCheck(ctx, job); err != nil {
		t.Fatal(err)
	}
	progress, _ := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if progress.CompletedSteps != 1 {
		t.Fatal("failed check advanced stage")
	}
	_, _ = s.Store.DB.Exec(`UPDATE jobs SET status='complete' WHERE id=?`, job.ID)
	check, err := s.queueLearningCheck(ctx, v.User.ID, p.ID, "restore", run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.guestError = nil
	if err = s.runLearningCheck(ctx, Job{Kind: "learning-check", Target: check.ID}); err != nil {
		t.Fatal(err)
	}
	if err = s.runLearningCheck(ctx, Job{Kind: "learning-check", Target: check.ID}); err != nil || e.checks != 2 {
		t.Fatal("completed check replayed", e.checks, err)
	}
	progress, _ = s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if progress.CompletedSteps != 2 {
		t.Fatal("passing guest observation did not advance")
	}
	// A pass arriving after cleanup begins must not promote the stage.
	_ = s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), LearningProgress{LabID: p.ID, CompletedSteps: 1})
	_, _ = s.Store.DB.Exec(`UPDATE jobs SET status='complete' WHERE kind='learning-check'`)
	check, err = s.queueLearningCheck(ctx, v.User.ID, p.ID, "restore", run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.endPractice(ctx, run.ID, v.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	var record learningCheckRecord
	_ = s.Store.Get(ctx, "learning-check", check.ID, &record)
	record.Status = "passed"
	if err = s.saveLearningCheck(ctx, record, true); err != nil {
		t.Fatal(err)
	}
	progress, _ = s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if progress.CompletedSteps != 1 {
		t.Fatal("late pass advanced ended run")
	}
	_ = s.Store.Get(ctx, "learning-check", check.ID, &record)
	if record.Status != "unavailable" {
		t.Fatal(record)
	}
}
func TestPracticeDeadlineClaimsAndPendingImageProtection(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now().Add(-4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.jobClaims(ctx, Job{Kind: "practice-prepare", Target: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !claimsConflict(claims, []jobClaim{{"image:shared-workstation", true}}) || jobLane("practice-end") != "cleanup" || jobLane("learning-check") != "guest" {
		t.Fatal("claims or reserved cleanup lane missing", claims)
	}
	if reason, err := s.practiceImageBlock(ctx, []string{"shared-workstation"}); err != nil || reason == "" {
		t.Fatal("pending allocation did not protect pinned images", reason, err)
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.activeJobs = map[string]runningJob{"prepare": {job: Job{Kind: "practice-prepare"}, claims: claims, cancel: cancel}}
	s.enforcePracticeDeadlines(ctx)
	if jobCtx.Err() == nil {
		t.Fatal("expired provisioning survived")
	}
	current, _ := s.Store.activePractice(ctx, v.User.ID)
	if current.Status != "ending" {
		t.Fatal(current)
	}
	next, err := s.Store.NextJob(ctx)
	if err != nil || next.Kind != "practice-end" {
		t.Fatal("cleanup not prioritized", next, err)
	}
	s.enforcePracticeDeadlines(ctx)
	var count int
	_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='practice-end'`).Scan(&count)
	if count != 1 {
		t.Fatal("deadline duplicated cleanup")
	}
}
func TestPracticeDesktopAuthorizationIndependentOfMeeting(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	record, _ := s.Store.practice(ctx, run.ID)
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &desktopSession{ctx: dc, cancel: cancel, record: accessRecord{ID: "practice-desktop", UserID: v.User.ID, SessionID: v.ID, TeamID: "practice:" + run.ID, InstanceID: record.InstanceID, Expires: run.Expires}}
	if !s.desktopAuthorized(ctx, d) {
		t.Fatal("independent practice desktop denied")
	}
	d.record.UserID = "foreign"
	if s.desktopAuthorized(ctx, d) {
		t.Fatal("foreign practice desktop allowed")
	}
	d.record.UserID = v.User.ID
	p.Published = false
	_ = s.Store.Put(ctx, "learning-publication", p.ID, p)
	if w := request(s, "GET", "/api/learning/linux/download", "", "", ""); w.Code != 404 {
		t.Fatal("unpublished download allowed")
	}
	if !s.desktopAuthorized(ctx, d) {
		t.Fatal("unpublishing interrupted retained allocation")
	}
	decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/linux/stop", `{}`, s.Config.Origin, v.CSRF), 202)
	if s.desktopAuthorized(ctx, d) {
		t.Fatal("ending practice desktop authorized")
	}
}

func TestDesktopLessonUsesOwnerCookieAndExactOriginSubmissions(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	record, _ := s.Store.practice(ctx, run.ID)
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &desktopSession{ctx: dc, cancel: cancel, cookie: "desktop-cookie", record: accessRecord{ID: "practice-desktop", UserID: v.User.ID, SessionID: v.ID, TeamID: "practice:" + run.ID, InstanceID: record.InstanceID, Expires: run.Expires}}
	s.desktops[d.record.ID] = d
	call := func(method, path, origin, marker, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, s.Config.DesktopOrigin+path, strings.NewReader(`{"stepID":"identify","answer":"PRIVATE-ANSWER-SENTINEL"}`))
		r.AddCookie(&http.Cookie{Name: "__Host-labcentral_desktop", Value: d.cookie})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-LabCentral-Lesson", marker)
		r.Header.Set("X-LabCentral-Practice-Run", run.ID)
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/lesson", "", "", "")
	view := decodeLearning[LearningView](t, w, 200)
	if view.Lab.ID != p.ID || strings.Contains(w.Body.String(), "PRIVATE-") {
		t.Fatal("desktop lesson leaked private definition")
	}
	for _, wrong := range []string{"", "another-allocation"} {
		r := httptest.NewRequest("POST", s.Config.DesktopOrigin+"/lesson/answer", strings.NewReader(`{"stepID":"identify","answer":"PRIVATE-ANSWER-SENTINEL"}`))
		r.AddCookie(&http.Cookie{Name: "__Host-labcentral_desktop", Value: d.cookie})
		r.Header.Set("Origin", s.Config.DesktopOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-LabCentral-Lesson", "1")
		r.Header.Set("X-LabCentral-Practice-Run", wrong)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, r)
		if response.Code != 403 {
			t.Fatal("unbound desktop window submission accepted", response.Code)
		}
	}
	for _, q := range []struct{ origin, marker, kind string }{{"https://foreign.invalid", "1", "application/json"}, {s.Config.DesktopOrigin, "", "application/json"}, {s.Config.DesktopOrigin, "1", "text/plain"}} {
		if w := call("POST", "/lesson/answer", q.origin, q.marker, q.kind); w.Code != 403 {
			t.Fatal("desktop lesson CSRF", w.Code)
		}
	}
	decodeLearning[LearningProgress](t, call("POST", "/lesson/answer", s.Config.DesktopOrigin, "1", "application/json"), 200)
	progress, _ := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if progress.CompletedSteps != 1 {
		t.Fatal("desktop lesson did not save owner progress")
	}
	_, _ = s.Store.endPractice(ctx, run.ID, v.User.ID)
	if w := call("GET", "/lesson", "", "", ""); w.Code != 403 {
		t.Fatal("ended desktop lesson available")
	}
}

func TestMeetingCleanupPreservesIndependentPracticeDesktop(t *testing.T) {
	s, v, _ := learningFixture(t)
	ctx := context.Background()
	practiceEngineFor(t, s)
	run := startReadyPractice(t, s, v)
	record, _ := s.Store.practice(ctx, run.ID)
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	a := accessRecord{ID: "practice-desktop", UserID: v.User.ID, SessionID: v.ID, TeamID: "practice:" + run.ID, InstanceID: record.InstanceID, Expires: run.Expires}
	s.desktops[a.ID] = &desktopSession{ctx: dc, cancel: cancel, record: a}
	if err := s.Store.Put(ctx, "desktop", a.ID, a); err != nil {
		t.Fatal(err)
	}
	setupMeeting(t, s.Store)
	if err := s.meetingOperation(ctx, Job{Kind: "end", Target: "meeting"}); err != nil {
		t.Fatal(err)
	}
	if dc.Err() != nil {
		t.Fatal("meeting cleanup cancelled personal desktop")
	}
	active, err := s.Store.activePractice(ctx, v.User.ID)
	if err != nil || active == nil || active.Status != "ready" {
		t.Fatal("meeting cleanup changed personal allocation", active, err)
	}
	if err = s.Store.Get(ctx, "desktop", a.ID, new(accessRecord)); err != nil {
		t.Fatal("personal desktop intent deleted", err)
	}
}
