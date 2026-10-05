package hosted

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"strings"
	"sync"
	"testing"
)

func instructorRevisionFixture(t *testing.T) (*Service, []Team) {
	t.Helper()
	s, teams := prepareFixture(t)
	ctx := context.Background()
	var session Session
	if err := s.Store.Get(ctx, "session", digest("cookie"), &session); err != nil {
		t.Fatal(err)
	}
	session.User.Admin = true
	if err := s.Store.Put(ctx, "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	var source lc.Source
	if err := s.Store.Get(ctx, "source", "", &source); err != nil {
		t.Fatal(err)
	}
	b := InstructorBundle{Version: 1, Checks: []TemplateCheck{{ID: "website", Name: "Website", Points: 100, Steps: []GuestPlaybook{{Machine: "server", Playbook: "check.yml"}}}}, Files: []lc.File{{Path: "check.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")}}}
	view := b.view()
	for _, id := range []string{"", "new"} {
		for _, obj := range []struct {
			kind  string
			value any
		}{{"source", source}, {"template", Template{ID: id, Name: source.Lab.Name, Commit: strings.Repeat("a", 40), Built: true, Instructor: &view}}, {"template-instructor", b}} {
			if err := s.Store.Put(ctx, obj.kind, id, obj.value); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, teams
}

func TestInstructorRevisionAuthorizationAndDurableIdempotence(t *testing.T) {
	s, teams := instructorRevisionFixture(t)
	ctx := context.Background()
	body := `{"id":"new"}`
	for _, credentials := range []struct{ origin, csrf string }{{"https://untrusted.invalid", "csrf"}, {s.Config.Origin, "wrong"}} {
		if w := request(s, "POST", "/api/admin/meeting/instructor", body, credentials.origin, credentials.csrf); w.Code != 403 {
			t.Fatal("instructor adoption bypassed origin/CSRF", w.Code)
		}
	}
	var session Session
	s.Store.Get(ctx, "session", digest("cookie"), &session)
	session.User.Admin = false
	s.Store.Put(ctx, "session", session.ID, session)
	if w := request(s, "POST", "/api/admin/meeting/instructor", body, s.Config.Origin, "csrf"); w.Code != 403 {
		t.Fatal("student changed instructor revision", w.Code)
	}
	session.User.Admin = true
	s.Store.Put(ctx, "session", session.ID, session)
	before, _ := s.Store.Meeting(ctx)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if w := request(s, "POST", "/api/admin/meeting/instructor", body, s.Config.Origin, "csrf"); w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Meeting(ctx)
	if err != nil || after.Template != "new" {
		t.Fatal("revision not durable", after, err)
	}
	after.Template = before.Template
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(before)
	if string(a) != string(b) {
		t.Fatal("adoption changed meeting identity or deadline")
	}
	current, err := reopened.Teams(ctx, before.ID)
	a, _ = json.Marshal(current)
	b, _ = json.Marshal(teams)
	if err != nil || string(a) != string(b) {
		t.Fatal("adoption changed native team identities or capacity", err)
	}
	history, err := reopened.List(ctx, "instructor-revision")
	if err != nil || len(history) != 1 {
		t.Fatal("concurrent retry duplicated revision audit", len(history), err)
	}
}

func TestInstructorRevisionRejectsChangedSourceAndUnfinishedWork(t *testing.T) {
	for _, kind := range []string{"source", "unbuilt", "removing", "invalid-bundle", "prepare", "end", "check", "exercise", "active"} {
		t.Run(kind, func(t *testing.T) {
			s, teams := instructorRevisionFixture(t)
			ctx := context.Background()
			switch kind {
			case "source":
				src, err := lc.Load("../examples/single")
				if err != nil {
					t.Fatal(err)
				}
				s.Store.Put(ctx, "source", "new", src)
			case "unbuilt", "removing":
				var target Template
				s.Store.Get(ctx, "template", "new", &target)
				target.Built = kind != "unbuilt"
				target.Removing = kind == "removing"
				s.Store.Put(ctx, "template", "new", target)
			case "invalid-bundle":
				s.Store.Put(ctx, "template-instructor", "new", InstructorBundle{Version: 1, Checks: []TemplateCheck{{ID: "bad", Points: 1}}})
			case "active":
				s.activeJobs = map[string]runningJob{"active": {job: Job{Kind: "check"}, claims: []jobClaim{{"meeting:meeting", false}}}}
			default:
				target := "meeting"
				if kind == "check" {
					target = "check"
					s.Store.Put(ctx, "check", target, CheckRun{ID: target, MeetingID: "meeting", TeamID: teams[0].ID})
				}
				if kind == "exercise" {
					target = "exercise"
					s.Store.Put(ctx, "exercise", target, ExerciseRun{ID: target, MeetingID: "meeting", TeamID: teams[0].ID})
				}
				if _, err := s.Store.Enqueue(ctx, kind, target); err != nil {
					t.Fatal(err)
				}
			}
			if w := request(s, "POST", "/api/admin/meeting/instructor", `{"id":"new"}`, s.Config.Origin, "csrf"); w.Code != 409 {
				t.Fatal("unsafe adoption accepted", w.Code, w.Body.String())
			}
			m, _ := s.Store.Meeting(ctx)
			if m.Template != "" {
				t.Fatal("rejected revision changed the pin")
			}
		})
	}
}

func TestInstructorRevisionAuditFailureRollsBackMeetingPin(t *testing.T) {
	s, _ := instructorRevisionFixture(t)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER reject_revision BEFORE INSERT ON objects WHEN NEW.kind='instructor-revision' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END;`); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/admin/meeting/instructor", `{"id":"new"}`, s.Config.Origin, "csrf"); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	m, err := s.Store.Meeting(context.Background())
	if err != nil || m.Template != "" {
		t.Fatal("failed audit partially updated meeting", m, err)
	}
}
