package hosted

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstructorFilesExcludedBeforePortableSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte("version: 2\nname: private-notes\nimages:\n  base:\n    base: ubuntu-24.04\nmachines:\n  server:\n    image: base\n    cpu: 1\n    memory: 1Gi\n    disk: 10Gi\n    access: [ssh]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(dir, "instructor"), 0700)
	_ = os.WriteFile(filepath.Join(dir, "instructor", "check.yml"), []byte("- hosts: localhost\n  tasks: []\n"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "instructor.yaml"), []byte("version: 1\nnotes: private instructor sentinel\nchecks:\n  - id: service\n    name: Service\n    points: 10\n    steps:\n      - {machine: server, playbook: check.yml}\n"), 0600)
	bundle, err := extractInstructor(dir)
	if err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.validate(src); err != nil {
		t.Fatal(err)
	}
	for _, f := range src.Files {
		if strings.Contains(f.Path, "instructor") || strings.Contains(string(f.Content), "sentinel") {
			t.Fatal("instructor material in student image source")
		}
	}
	bundle.Checks[0].Steps[0].Machine = "unknown"
	if bundle.validate(src) == nil {
		t.Fatal("unknown target accepted")
	}
}
func TestInstructorManifestRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{"version: 1\nversion: 1\n", "version: 1\nunknown: true\n", "version: 1\nnotes: &x private\nchecks: *x\n", "version: 3\n", "version: 1\n---\nversion: 1\n"} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "instructor.yaml"), []byte(raw), 0600)
		if _, err := extractInstructor(dir); err == nil {
			t.Fatal("ambiguous manifest accepted", raw)
		}
	}
}
func checkFixture(t *testing.T) (*Service, []Team) {
	s, teams := exerciseFixture(t)
	b := InstructorBundle{Version: 1, Notes: "private instructor sentinel", Checks: []TemplateCheck{{ID: "website", Name: "Website", Points: 50, Steps: []GuestPlaybook{{Machine: "web", Playbook: "check.yml"}}}}}
	ctx := context.Background()
	if err := s.Store.Put(ctx, "template-instructor", "", b); err != nil {
		t.Fatal(err)
	}
	var template Template
	if err := s.Store.Get(ctx, "template", "", &template); err != nil {
		t.Fatal(err)
	}
	view := b.view()
	template.Instructor = &view
	if err := s.Store.Put(ctx, "template", "", template); err != nil {
		t.Fatal(err)
	}
	return s, teams
}
func TestChecksRequireAdminCSRFAndRetainScheduleAcrossRestart(t *testing.T) {
	s, teams := checkFixture(t)
	ctx := context.Background()
	for _, path := range []string{"/api/admin/checks/schedule", "/api/admin/checks/run"} {
		for _, q := range []struct{ origin, csrf string }{{"https://foreign.invalid", "csrf"}, {s.Config.Origin, "wrong"}} {
			if request(s, "POST", path, `{"intervalSeconds":120}`, q.origin, q.csrf).Code != 403 {
				t.Fatal("CSRF accepted")
			}
		}
	}
	if r := request(s, "POST", "/api/admin/checks/schedule", `{"intervalSeconds":30}`, s.Config.Origin, "csrf"); r.Code != 400 {
		t.Fatal(r.Code)
	}
	if r := request(s, "POST", "/api/admin/checks/schedule", `{"intervalSeconds":120}`, s.Config.Origin, "csrf"); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	s.scheduleChecks(ctx)
	s.scheduleChecks(ctx)
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil || len(jobs) != len(teams) {
		t.Fatal("duplicate ticks or missing ready teams", jobs, err)
	}
	var schedule CheckSchedule
	if err = s.Store.Get(ctx, "check-schedule", "meeting", &schedule); err != nil || !schedule.Next.After(time.Now()) {
		t.Fatal(schedule, err)
	}
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var retained CheckSchedule
	if err = reopened.Get(ctx, "check-schedule", "meeting", &retained); err != nil || retained.IntervalSeconds != 120 {
		t.Fatal(retained, err)
	}
	// Even a due tick after restart does not duplicate active per-team checks.
	schedule.Next = time.Now().Add(-time.Hour)
	_ = s.Store.Put(ctx, "check-schedule", "meeting", schedule)
	s.scheduleChecks(ctx)
	jobs, _ = s.Store.QueuedJobs(ctx)
	if len(jobs) != len(teams) {
		t.Fatal("active checks duplicated")
	}
	r := request(s, "GET", "/api/me", "", "", "")
	if strings.Contains(r.Body.String(), "sentinel") || strings.Contains(r.Body.String(), "checks") {
		t.Fatal("private checks leaked to student overview")
	}
	var v Session
	_ = s.Store.Get(ctx, "session", digest("cookie"), &v)
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	if request(s, "GET", "/api/admin", "", "", "").Code != 403 {
		t.Fatal("student read notes")
	}
	for _, path := range []string{"/api/admin/checks/schedule", "/api/admin/checks/run"} {
		if request(s, "POST", path, `{"intervalSeconds":120}`, s.Config.Origin, "csrf").Code != 403 {
			t.Fatal("student controlled checks")
		}
	}
}
func TestFailedGuestChecksStayVisibleAndEndedMeetingCannotRun(t *testing.T) {
	s, teams := checkFixture(t)
	ctx := context.Background()
	m, _ := s.Store.Meeting(ctx)
	if err := s.queueChecks(ctx, *m, nil); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Store.QueuedJobs(ctx)
	for _, j := range jobs {
		if err := s.runCheck(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	_, runs, err := s.checkView(ctx, *m, teams)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Status != "complete" || len(r.Results) != 1 || r.Results[0].Passed || r.Results[0].Report != "guest execution unavailable" {
			t.Fatal("guest unavailable falsely scored as passed", r)
		}
	}
	before, _ := json.Marshal(runs)
	m.Status = "ended"
	_ = s.Store.Put(ctx, "meeting", "current", m)
	if request(s, "POST", "/api/admin/checks/run", "{}", s.Config.Origin, "csrf").Code != 409 {
		t.Fatal("ended meeting check accepted")
	}
	s.scheduleChecks(ctx)
	_, after, _ := s.checkView(ctx, *m, teams)
	b, _ := json.Marshal(after)
	if string(b) != string(before) {
		t.Fatal("expiry discarded results")
	}
}

func TestConfiguredInstructorTemplate(t *testing.T) {
	root := os.Getenv("LABCENTRAL_TEMPLATE_CHECK")
	if root == "" {
		t.Skip("set LABCENTRAL_TEMPLATE_CHECK to a local template checkout for import validation")
	}
	dir := filepath.Join(t.TempDir(), "template")
	if err := os.CopyFS(dir, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	b, err := extractInstructor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("no instructor manifest")
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.validate(src); err != nil {
		t.Fatal(err)
	}
	t.Logf("Imported pinned instructor metadata, %d exercises and %d checks; portable source digest %s", len(b.Exercises), len(b.Checks), src.Digest)
}
