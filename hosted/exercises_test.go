package hosted

import (
	"context"
	"encoding/json"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func exerciseFixture(t *testing.T) (*Service, []Team) {
	s, v, _ := serviceFor(t)
	v.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	teams := setupMeeting(t, s.Store)
	if _, err := s.Store.DB.Exec(`UPDATE teams SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	src := lc.Source{Lab: lc.Lab{Name: "branch-services"}, Digest: "pinned", Files: []lc.File{{Path: "web/apache-site.conf", Content: []byte("<VirtualHost *:80>\n</VirtualHost>")}, {Path: "web/public/index.php", Content: []byte("bank")}, {Path: "web.yml", Content: []byte("- tasks:\n  - ansible.builtin.user:\n      name: svc_reports\n      password: $6$fixture\n")}}}
	if err := s.Store.Put(context.Background(), "source", "", src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(context.Background(), "template", "", Template{Name: "branch-services"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(context.Background(), "template-images", "", []string{}); err != nil {
		t.Fatal(err)
	}
	b := InstructorBundle{Version: 3, Visibility: instructor.Visibility{Audience: "students"}, Practice: &instructor.Practice{DesktopMachine: "workstation"}, Files: []lc.File{{Path: "reset.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")}}, Reset: []GuestPlaybook{{Machine: "web", Playbook: "reset.yml"}}}
	for _, x := range []struct{ id, name string }{{"stop-web", "Web service stopped"}, {"change-port", "Web port changed"}, {"ssh-intrusion", "Simulated SSH intrusion"}} {
		b.Breaks = append(b.Breaks, TemplateBreak{ID: x.id, Name: x.name, Description: "A fixture service break.", Walkthrough: "Inspect and restore the fixture service.", Steps: b.Reset})
	}
	_ = s.Store.Put(context.Background(), "template-instructor", "", b)
	var template Template
	_ = s.Store.Get(context.Background(), "template", "", &template)
	view := instructorView(b)
	template.Instructor = &view
	_ = s.Store.Put(context.Background(), "template", "", template)

	s.exerciseRunner = func(context.Context, Team, BreakRun, lc.Source) (string, error) { return "verified", nil }
	return s, teams
}
func queueExercise(t *testing.T, s *Service, team, action string) BreakRun {
	t.Helper()
	runs, e := s.queueExercises(context.Background(), BreakRequest{Action: action, TeamIDs: []string{team}}, "admin")
	if e != nil {
		t.Fatal(e)
	}
	return runs[0]
}
func finishExerciseJob(t *testing.T, s *Service, run BreakRun, err error) {
	t.Helper()
	state := "complete"
	if err != nil {
		state = "failed"
	}
	if _, e := s.Store.DB.Exec(`UPDATE jobs SET status=? WHERE kind='exercise' AND target=?`, state, run.ID); e != nil {
		t.Fatal(e)
	}
}
func TestExercisesRequireAdminAndCSRF(t *testing.T) {
	s, teams := exerciseFixture(t)
	body := `{"action":"stop-web","teamIDs":["` + teams[0].ID + `"]}`
	for _, q := range []struct{ origin, csrf string }{{"https://foreign.invalid", "csrf"}, {s.Config.Origin, "wrong"}} {
		if w := request(s, "POST", "/api/admin/exercises", body, q.origin, q.csrf); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	var v Session
	s.Store.Get(context.Background(), "session", digest("cookie"), &v)
	v.User.Admin = false
	s.Store.Put(context.Background(), "session", v.ID, v)
	if w := request(s, "POST", "/api/admin/exercises", body, s.Config.Origin, "csrf"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(s, "GET", "/api/admin", "", "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	// A student not in a team never needs an engine inspection.
	w := request(s, "GET", "/api/me", "", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "exercise") || strings.Contains(w.Body.String(), "svc_reports") {
		t.Fatal("student data leaks exercises", w.Body.String())
	}
}
func TestExerciseSelectionAtomicAndConcurrent(t *testing.T) {
	s, teams := exerciseFixture(t)
	ctx := context.Background()
	for _, req := range []BreakRequest{{"arbitrary", []string{teams[0].ID}}, {"reset", []string{teams[0].ID, "foreign"}}, {"reset", []string{teams[0].ID, teams[0].ID}}} {
		if _, e := s.queueExercises(ctx, req, "admin"); e == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	jobs, _ := s.Store.Jobs(ctx)
	if len(jobs) != 0 {
		t.Fatal("partial invalid batch committed")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.queueExercises(ctx, BreakRequest{"stop-web", []string{teams[0].ID}}, "admin"); e == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
}
func TestExerciseReplayFailureAndSupersession(t *testing.T) {
	s, teams := exerciseFixture(t)
	ctx := context.Background()
	calls := 0
	s.exerciseRunner = func(context.Context, Team, BreakRun, lc.Source) (string, error) {
		calls++
		return "", errors.New("response lost")
	}
	run := queueExercise(t, s, teams[0].ID, "change-port")
	j := Job{Kind: "exercise", Target: run.ID}
	err := s.runExercise(ctx, j)
	if err == nil {
		t.Fatal("lost response passed")
	}
	finishExerciseJob(t, s, run, err)
	var saved BreakRun
	s.Store.Get(ctx, "exercise", run.ID, &saved)
	if saved.Status != "failed" {
		t.Fatal(saved)
	}
	reset := queueExercise(t, s, teams[0].ID, "reset")
	if err = s.runExercise(ctx, j); err == nil || calls != 1 {
		t.Fatal("superseded break ran again")
	}
	s.exerciseRunner = func(context.Context, Team, BreakRun, lc.Source) (string, error) {
		calls++
		return "reset verified", nil
	}
	resetJob := Job{Kind: "exercise", Target: reset.ID}
	if err = s.runExercise(ctx, resetJob); err != nil {
		t.Fatal(err)
	}
	if err = s.runExercise(ctx, resetJob); err != nil || calls != 2 {
		t.Fatal("completed result repeated guest mutation", err, calls)
	}
	jobs, _ := s.Store.Jobs(ctx)
	for _, old := range jobs {
		if old.Target == run.ID {
			b, _ := json.Marshal(TargetRequest{ID: old.ID})
			if w := request(s, "POST", "/api/admin/jobs/retry", string(b), s.Config.Origin, "csrf"); w.Code != 409 {
				t.Fatal("old retry accepted")
			}
		}
	}
}
func TestExerciseInterruptedRunResumesAndOtherTeamsProgress(t *testing.T) {
	s, teams := exerciseFixture(t)
	ctx := context.Background()
	first := queueExercise(t, s, teams[0].ID, "stop-web")
	second := queueExercise(t, s, teams[1].ID, "reset")
	first.Status = "applying"
	if e := s.Store.Put(ctx, "exercise", first.ID, first); e != nil {
		t.Fatal(e)
	}
	s.exerciseRunner = func(_ context.Context, team Team, _ BreakRun, _ lc.Source) (string, error) {
		if team.ID == teams[0].ID {
			return "", errors.New("web unavailable")
		}
		return "reset verified", nil
	}
	if s.runExercise(ctx, Job{Target: first.ID}) == nil {
		t.Fatal("failure hidden")
	}
	if err := s.runExercise(ctx, Job{Target: second.ID}); err != nil {
		t.Fatal(err)
	}
}
func TestExerciseExpiryAndConcurrentReset(t *testing.T) {
	s, teams := exerciseFixture(t)
	ctx := context.Background()
	run := queueExercise(t, s, teams[0].ID, "ssh-intrusion")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	s.exerciseRunner = func(context.Context, Team, BreakRun, lc.Source) (string, error) {
		close(entered)
		<-release
		return "done", nil
	}
	go func() { done <- s.runExercise(ctx, Job{Target: run.ID}) }()
	<-entered
	if _, e := s.queueExercises(ctx, BreakRequest{"reset", []string{teams[0].ID}}, "admin"); e == nil {
		t.Fatal("overlapping reset accepted")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	next := queueExercise(t, s, teams[1].ID, "reset")
	m, _ := s.Store.Meeting(ctx)
	m.Expires = time.Now().Add(-time.Second)
	s.Store.Put(ctx, "meeting", "current", m)
	s.exerciseRunner = func(context.Context, Team, BreakRun, lc.Source) (string, error) {
		t.Fatal("expired exercise mutated guest")
		return "", nil
	}
	if e := s.runExercise(ctx, Job{Target: next.ID}); e == nil {
		t.Fatal("expired meeting accepted")
	}
}
