package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type privacyEngine struct {
	Engine
	instance lc.Instance
}

func (e privacyEngine) Inspect(context.Context, string) (lc.Instance, error) { return e.instance, nil }

func privateFixture(t *testing.T) (*Service, Session, []Team, lc.Source) {
	t.Helper()
	s, v, b := stageFixture(t)
	ctx := context.Background()
	var src lc.Source
	if err := s.Store.Get(ctx, "source", "pinned", &src); err != nil {
		t.Fatal(err)
	}
	src.Lab.Machines["support"] = lc.Machine{Image: src.Lab.Machines["workstation"].Image, CPU: 1, Memory: "1Gi", Disk: "20Gi", Access: []string{"ssh"}}
	for name, image := range src.Lab.Images {
		for _, f := range src.Files {
			if f.Path != "lab.yaml" {
				image.Files = append(image.Files, f.Path)
			}
		}
		src.Lab.Images[name] = image
	}
	studentMachine := src.Lab.Machines["workstation"]
	studentMachine.HostnamePeers = []string{"workstation"}
	src.Lab.Machines["workstation"] = studentMachine
	for i := range b.Reset {
		b.Reset[i].Files = []string{b.Reset[i].Playbook}
	}
	for i := range b.Exercises {
		for j := range b.Exercises[i].Steps {
			b.Exercises[i].Steps[j].Files = []string{b.Exercises[i].Steps[j].Playbook}
		}
	}
	for i := range b.Checks {
		for j := range b.Checks[i].Steps {
			b.Checks[i].Steps[j].Files = []string{b.Checks[i].Steps[j].Playbook}
		}
	}
	snapshot := filepath.Join(t.TempDir(), "source")
	if err := os.CopyFS(snapshot, os.DirFS("../examples/workstation")); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(src.Lab)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshot, "lab.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	src, err = lc.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	b.Audience = "coaches"
	b.HiddenMachines = []string{"support"}
	if err := b.validate(src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	var template Template
	_ = s.Store.Get(ctx, "template", "pinned", &template)
	view := b.view()
	template.Instructor = &view
	if err := s.Store.Put(ctx, "template", "pinned", template); err != nil {
		t.Fatal(err)
	}
	meeting := Meeting{ID: "incident", Name: "Incident response practice", Template: "pinned", Status: "ready", Expires: time.Now().Add(time.Hour)}
	if err := s.Store.CreateMeeting(ctx, meeting, 2, 6); err != nil {
		t.Fatal(err)
	}
	teams, err := s.Store.Teams(ctx, meeting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE teams SET status='ready',instance_id='private-instance'`); err != nil {
		t.Fatal(err)
	}
	teams, _ = s.Store.Teams(ctx, meeting.ID)
	if err = s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, meeting.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
	s.Engine = privacyEngine{Engine: s.Engine, instance: lc.Instance{ID: "private-instance", Status: "ready", Plan: lc.Plan{Source: src, Machines: []lc.PlannedMachine{{Name: "workstation", Spec: src.Lab.Machines["workstation"]}, {Name: "support", Spec: src.Lab.Machines["support"]}}}}}
	return s, v, teams, src
}
func TestCoachOnlyRevisionExcludesAllStudentDiscoveryAndSpoilerRoutes(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/templates", "", 200}, {"GET", "/api/templates/pinned", "", 404},
		{"GET", "/api/learning", "", 200}, {"GET", "/api/learning/linux", "", 404}, {"GET", "/api/learning/linux/download", "", 404},
		{"POST", "/api/learning/linux/solution", `{"stepID":"identify"}`, 404}, {"POST", "/api/learning/linux/answer", `{"stepID":"identify","answer":"x"}`, 404},
		{"POST", "/api/learning/linux/check", `{"stepID":"identify"}`, 404}, {"POST", "/api/learning/linux/start", `{}`, 404},
		{"GET", "/api/admin/teams/" + teams[0].ID + "/machines", "", 403}, {"GET", "/api/admin/teams/" + teams[0].ID + "/machines/support/terminal", "", 403},
	} {
		w := request(s, test.method, test.path, test.body, s.Config.Origin, v.CSRF)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
		for _, secret := range []string{"support", "PRIVATE-", "Inspect service", "Pinned template revision:"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("%s leaked %q", test.path, secret)
			}
		}
	}
	overview := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if len(overview.Lab.Machines) != 1 || overview.Lab.Machines[0].Name != "workstation" || overview.Lab.Handover != "Student handover" {
		t.Fatal(overview.Lab)
	}
	// This deny persists even for an accidentally retained published snapshot.
	publication, err := s.publication(context.Background(), "linux")
	if err == nil {
		t.Fatal("private publication accessible", publication)
	}
	// Losing the private policy must never fall back to exposing everything.
	if err = s.Store.Delete(context.Background(), "template-instructor", "pinned"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/templates", "/api/me", "/api/learning"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 503 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}
func TestCoachAccessAndPublicationAuthorization(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	v.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	preview := decodeLearning[PublicTemplate](t, request(s, "GET", "/api/templates/pinned", "", "", ""), 200)
	if !preview.CoachOnly || len(preview.Machines) != 2 || len(preview.Stages) != 4 {
		t.Fatal(preview)
	}
	machines := decodeLearning[CoachMachines](t, request(s, "GET", "/api/admin/teams/"+teams[0].ID+"/machines", "", "", ""), 200)
	if len(machines.Machines) != 2 || machines.Machines[1].Name != "support" {
		t.Fatal(machines)
	}
	if machines.TeamID != teams[0].ID || machines.TeamName != teams[0].Name || machines.MeetingID != "incident" || machines.Template != "pinned" || machines.InstanceID != teams[0].InstanceID {
		t.Fatal("terminal allocation metadata missing", machines)
	}
	overview := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if len(overview.Lab.Machines) != 2 {
		t.Fatal(overview)
	}
	admin := decodeLearning[AdminView](t, request(s, "GET", "/api/admin", "", "", ""), 200)
	if len(admin.Templates) != 1 || admin.Templates[0].Instructor.Audience != "coaches" || len(admin.Templates[0].Instructor.Stages) != 4 {
		t.Fatal(admin.Templates)
	}
	for _, id := range []string{"other-team", "old-meeting-team"} {
		if w := request(s, "GET", "/api/admin/teams/"+id+"/machines", "", "", ""); w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
	body := `{"template":"pinned","title":"Do not disclose","mode":"self-paced","stageIDs":["inspect"]}`
	if w := request(s, "POST", "/api/admin/learning/publish", body, s.Config.Origin, "wrong"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(s, "POST", "/api/admin/learning/publish", body, s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if rows, err := s.Store.List(context.Background(), "learning-publication"); err != nil || len(rows) != 1 {
		t.Fatal("publication was written", err, len(rows))
	}
	for _, path := range []string{"/api/admin/teams/" + teams[0].ID + "/machines/support/terminal", "/api/admin/teams/" + teams[0].ID + "/machines/workstation/terminal"} {
		if w := request(s, "GET", path, "", "https://foreign.invalid", ""); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
func TestVisibilityAndGuestDependencyValidation(t *testing.T) {
	_, _, b := stageFixture(t)
	src, err := lc.Load("../examples/workstation")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []InstructorBundle{{Audience: "staff"}, {Audience: "coaches", HiddenMachines: []string{"missing"}}, {Audience: "coaches", HiddenMachines: []string{"workstation"}}, {Audience: "students", HiddenMachines: []string{"missing"}}} {
		if test.validateVisibility(src.Lab) == nil {
			t.Fatal(test)
		}
	}
	for _, step := range []GuestPlaybook{{Machine: "workstation", Playbook: "check.yml", Files: []string{"missing"}}, {Machine: "workstation", Playbook: "check.yml", Files: []string{"check.yml", "check.yml"}}, {Machine: "workstation", Playbook: "check.yml", GuestHosts: []string{"missing"}}, {Machine: "workstation", Playbook: "check.yml", GuestHosts: []string{"workstation", "workstation"}}} {
		if b.validateGuestInputs(src, step) == nil {
			t.Fatal(step)
		}
	}
	if err = b.validateGuestInputs(src, GuestPlaybook{Machine: "workstation", Playbook: "check.yml", Files: []string{"check.yml"}, GuestHosts: []string{"workstation"}}); err != nil {
		t.Fatal(err)
	}
}
func TestCoachAuthorizationExpiresAndRevokesWithoutMembership(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	v.User.Admin = true
	ctx := context.Background()
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	team, meeting, _, err := s.coachTeam(ctx, teams[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !s.coachAuthorized(ctx, v, team, meeting) {
		t.Fatal("coach required student membership")
	}
	changed := meeting
	changed.Template = "replacement"
	if err = s.Store.Put(ctx, "meeting", "current", changed); err != nil {
		t.Fatal(err)
	}
	if s.coachAuthorized(ctx, v, team, meeting) {
		t.Fatal("old terminal followed changed revision")
	}
	if err = s.Store.Put(ctx, "meeting", "current", meeting); err != nil {
		t.Fatal(err)
	}
	v.User.Admin = false
	if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if s.coachAuthorized(ctx, v, team, meeting) {
		t.Fatal("revoked coach remained authorized")
	}
	v.User.Admin = true
	if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	meeting.Expires = time.Now().Add(-time.Second)
	if err = s.Store.Put(ctx, "meeting", "current", meeting); err != nil {
		t.Fatal(err)
	}
	if s.coachAuthorized(ctx, v, team, meeting) {
		t.Fatal("expired meeting remained authorized")
	}
}

func TestCoachConsoleAnonymousAndWrongOriginCannotUpgrade(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	path := fmt.Sprintf("/api/admin/teams/%s/machines/support/terminal", teams[0].ID)
	r := httptest.NewRequest("GET", "https://labs.invalid"+path, nil)
	r.Header.Set("Origin", s.Config.Origin)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	v.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if w = request(s, "GET", path, "", "https://foreign.invalid", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	// The private metadata remains round-trippable and detached from portable source.
	var b InstructorBundle
	if err := s.Store.Get(context.Background(), "template-instructor", "pinned", &b); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b.view())
	if !strings.Contains(string(raw), `"hiddenMachines":["support"]`) {
		t.Fatal(string(raw))
	}
}

func TestGuestFilesKeepActorProgramsOffBankMachines(t *testing.T) {
	src, err := lc.Load("../examples/workstation")
	if err != nil {
		t.Fatal(err)
	}
	bundle := InstructorBundle{Audience: "coaches", HiddenMachines: []string{"support"}, Files: []lc.File{{Path: "check.yml", Content: []byte("ordinary service observation")}, {Path: "actor.py", Content: []byte("PRIVATE-ACTOR-SCRIPT")}}}
	instance := lc.Instance{Plan: lc.Plan{Machines: []lc.PlannedMachine{{Name: "workstation", Address: "10.77.0.10", HostPublicKey: "public-key"}}}}
	files, err := bundle.guestFiles(src, instance, GuestPlaybook{Machine: "workstation", Playbook: "check.yml", Files: []string{"check.yml"}})
	if err != nil || len(files) != 1 || strings.Contains(string(files[0].Content), "PRIVATE-") {
		t.Fatal(files, err)
	}
	if _, err = bundle.guestFiles(src, instance, GuestPlaybook{Machine: "workstation", Playbook: "check.yml"}); err == nil {
		t.Fatal("private bundle sent wholesale to bank guest")
	}
	files, err = bundle.guestFiles(src, instance, GuestPlaybook{Machine: "support", Playbook: "actor.py", Files: []string{"actor.py"}, GuestHosts: []string{"workstation"}})
	if err != nil || len(files) != 2 || files[1].Path != "guest-hosts.json" || strings.Contains(string(files[1].Content), "PRIVATE-") {
		t.Fatal(files, err)
	}
	if _, err = bundle.guestFiles(src, lc.Instance{}, GuestPlaybook{Machine: "support", Playbook: "actor.py", Files: []string{"actor.py"}, GuestHosts: []string{"workstation"}}); err == nil {
		t.Fatal("unknown host identity accepted")
	}
}

func TestPrivateDisclosureSurvivesStoreReopen(t *testing.T) {
	s, _, teams, _ := privateFixture(t)
	root := s.Store.Root
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	s.Store = reopened
	if s.studentTemplate(context.Background(), "pinned") == nil {
		t.Fatal("private policy lost after restart")
	}
	view := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if len(view.Lab.Machines) != 1 || view.Lab.Team.ID != teams[0].ID {
		t.Fatal(view)
	}
	if w := request(s, "GET", "/api/templates/pinned", "", "", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
