package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func scenarioFixture(t *testing.T) (*Service, Session, InstructorBundle) {
	t.Helper()
	s, v, b := stageFixture(t)
	b.Library = &TemplateLibraryMetadata{Category: "CCDC preparation", Difficulty: "intermediate", EstimatedMinutes: 45, Tags: []string{"Linux", "recovery"}}
	b.Breaks = append(b.Breaks, TemplateBreak{ID: "second-break", Name: "Second service", Steps: []GuestPlaybook{{Machine: "workstation", Playbook: "second.yml"}}})
	b.Files = append(b.Files, lc.File{Path: "second.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")})
	b.Stages = append(b.Stages, TemplateStage{Completion: instructor.Completion{Check: "service"}, ID: "second", Title: "Restore another service", Instructions: "Inspect and restore the second service.", Break: "second-break", Solution: "PRIVATE-SECOND-SOLUTION"})
	b.Scenarios = []TemplateScenario{{ID: "ccdc", Title: "CCDC recovery", Description: "Investigate and restore branch services.", Difficulty: "intermediate", EstimatedMinutes: 45, Variants: []ScenarioVariant{
		{ID: "first", Compatible: true, Breaks: []string{"service-break"}, Stages: []string{"break"}},
		{ID: "second", Compatible: true, Breaks: []string{"second-break"}, Stages: []string{"second"}},
		{ID: "both", Compatible: true, Breaks: []string{"service-break", "second-break"}, Stages: []string{"break", "second"}},
	}}}
	var src lc.Source
	if err := s.Store.Get(context.Background(), "source", "pinned", &src); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(context.Background(), "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	return s, v, b
}
func attemptBody(key, scenario string) string {
	return fmt.Sprintf(`{"requestID":"%s","scenarioID":"%s"}`, key, scenario)
}
func createAttemptFor(t *testing.T, s *Service, v Session, key, scenario string) LearningLab {
	t.Helper()
	return decodeLearning[LearningLab](t, request(s, "POST", "/api/templates/pinned/attempt", attemptBody(key, scenario), s.Config.Origin, v.CSRF), 201)
}
func TestScenarioManifestRejectsIncompatibleAndUnverifiableCombinations(t *testing.T) {
	fixtureService, _, valid := scenarioFixture(t)
	for name, change := range map[string]func(*InstructorBundle){
		"not certified": func(b *InstructorBundle) { b.Scenarios[0].Variants[0].Compatible = false },
		"unknown break": func(b *InstructorBundle) { b.Scenarios[0].Variants[0].Breaks = []string{"missing"} },
		"duplicate break": func(b *InstructorBundle) {
			b.Scenarios[0].Variants[0].Breaks = []string{"service-break", "service-break"}
		},
		"missing check":         func(b *InstructorBundle) { b.Scenarios[0].Variants[0].Stages = []string{"inspect"} },
		"unselected break":      func(b *InstructorBundle) { b.Scenarios[0].Variants[0].Stages = []string{"second"} },
		"duplicate combination": func(b *InstructorBundle) { b.Scenarios[0].Variants[1].Breaks = []string{"service-break"} },
		"single variant":        func(b *InstructorBundle) { b.Scenarios[0].Variants = b.Scenarios[0].Variants[:1] },
		"version one":           func(b *InstructorBundle) { b.Version = 1 },
		"no reset":              func(b *InstructorBundle) { b.Reset = nil },
		"invalid difficulty":    func(b *InstructorBundle) { b.Library.Difficulty = "impossible" },
		"duplicate tags":        func(b *InstructorBundle) { b.Library.Tags = []string{"Linux", "Linux"} },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var b InstructorBundle
			_ = json.Unmarshal(raw, &b)
			change(&b)
			if b.Validate(mustSource(t, fixtureService)) == nil {
				t.Fatal("invalid scenario accepted")
			}
		})
	}
}
func TestTemplateAttemptsAuthorizationPrivacyAndIndependentProgress(t *testing.T) {
	s, v, _ := scenarioFixture(t)
	ctx := context.Background()
	body := attemptBody(strings.Repeat("a", 24), "ccdc")
	for _, headers := range []struct{ origin, csrf string }{{"https://foreign.invalid", v.CSRF}, {s.Config.Origin, "wrong"}} {
		if w := request(s, "POST", "/api/templates/pinned/attempt", body, headers.origin, headers.csrf); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := request(s, "POST", "/api/templates/pinned/attempt", attemptBody("invalid", "ccdc"), s.Config.Origin, v.CSRF); w.Code != 400 {
		t.Fatal(w.Code)
	}
	baseline := createAttemptFor(t, s, v, strings.Repeat("b", 24), "")
	scenario := createAttemptFor(t, s, v, strings.Repeat("a", 24), "ccdc")
	if !baseline.Attempt || baseline.StepCount != 2 || !scenario.Attempt || scenario.ScenarioID != "ccdc" {
		t.Fatal(baseline, scenario)
	}
	view := decodeLearning[LearningView](t, request(s, "GET", "/api/learning/"+scenario.ID, "", "", ""), 200)
	if view.Scenario == nil || view.LocalAvailable || view.Progress.CompletedSteps != 0 {
		t.Fatal(view)
	}
	for _, path := range []string{"/api/templates", "/api/templates/pinned", "/api/learning", "/api/learning/" + scenario.ID} {
		w := request(s, "GET", path, "", "", "")
		for _, private := range []string{"PRIVATE-", "exerciseIDs", "variantID", "checkID", "ownerID", "check.yml", "second.yml"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("%s exposed %q: %s", path, private, w.Body.String())
			}
		}
	}
	if request(s, "GET", "/api/learning/"+scenario.ID+"/download", "", "", "").Code != 409 {
		t.Fatal("scenario download implies local breaks")
	}
	decodeLearning[LearningProgress](t, request(s, "POST", "/api/learning/"+baseline.ID+"/answer", `{"stepID":"inspect","answer":"PRIVATE-STAGE-ANSWER"}`, s.Config.Origin, v.CSRF), 200)
	next := createAttemptFor(t, s, v, strings.Repeat("c", 24), "")
	progress, _ := s.Store.learningProgress(ctx, v.User.ID, next.ID)
	old, _ := s.Store.learningProgress(ctx, v.User.ID, baseline.ID)
	if progress.CompletedSteps != 0 || old.CompletedSteps != 1 {
		t.Fatal("new attempt erased or inherited progress", progress, old)
	}
	var a templateAttempt
	_ = s.Store.Get(ctx, "template-attempt", scenario.ID, &a)
	// Simulate a second signed-in student using the same cookie fixture.
	v.User.ID = "other"
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/download", "/solution", "/answer", "/check", "/start", "/access"} {
		method := "POST"
		if path == "" || path == "/download" {
			method = "GET"
		}
		payload := `{}`
		if path == "/answer" {
			payload = `{"stepID":"break","answer":"x"}`
		}
		if path == "/solution" || path == "/check" {
			payload = `{"stepID":"break"}`
		}
		if w := request(s, method, "/api/learning/"+scenario.ID+path, payload, s.Config.Origin, v.CSRF); w.Code != 404 && !(path == "/access" && w.Code == 409) {
			t.Fatalf("%s accessible to another student: %d %s", path, w.Code, w.Body.String())
		}
	}
	catalog := decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	for _, lab := range catalog.Labs {
		if lab.Attempt {
			t.Fatal("another student's attempt in catalog")
		}
	}
}
func TestScenarioSelectionReplayConcurrencyAndReopenedStore(t *testing.T) {
	s, v, _ := scenarioFixture(t)
	ctx := context.Background()
	key := strings.Repeat("a", 24)
	body := attemptBody(key, "ccdc")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := []string{}
	created := 0
	for range 12 {
		wg.Go(func() {
			w := request(s, "POST", "/api/templates/pinned/attempt", body, s.Config.Origin, v.CSRF)
			if w.Code != 200 && w.Code != 201 {
				t.Error(w.Code, w.Body.String())
				return
			}
			var lab LearningLab
			_ = json.Unmarshal(w.Body.Bytes(), &lab)
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, lab.ID)
			if w.Code == 201 {
				created++
			}
		})
	}
	wg.Wait()
	if len(ids) != 12 || created != 1 {
		t.Fatal(ids, created)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("request replay rerandomized")
		}
	}
	var a templateAttempt
	_ = s.Store.Get(ctx, "template-attempt", ids[0], &a)
	next := createAttemptFor(t, s, v, strings.Repeat("b", 24), "ccdc")
	var b templateAttempt
	_ = s.Store.Get(ctx, "template-attempt", next.ID, &b)
	if a.VariantID == b.VariantID {
		t.Fatal("immediate repeat of saved combination")
	}
	if w := request(s, "POST", "/api/templates/pinned/attempt", attemptBody(key, ""), s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal("request ID changed meaning")
	}
	jobs, _ := s.Store.Jobs(ctx)
	if len(jobs) != 0 {
		t.Fatal("attempt creation provisioned", jobs)
	}
	// Reopen a copied persistent journal, rather than claiming in-memory state is recovery.
	var file string
	rows, err := s.Store.DB.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int
		var name string
		if err = rows.Scan(&seq, &name, &file); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			break
		}
	}
	rows.Close()
	reopened, err := OpenStore(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Store = reopened
	replay := decodeLearning[LearningLab](t, request(s, "POST", "/api/templates/pinned/attempt", body, s.Config.Origin, v.CSRF), 200)
	var after templateAttempt
	_ = reopened.Get(ctx, "template-attempt", replay.ID, &after)
	if !reflect.DeepEqual(a, after) {
		t.Fatal("reopened attempt changed")
	}
}
func TestScenarioCombinesBreaksAfterOneResetAndStopsOnFailure(t *testing.T) {
	s, _, b := scenarioFixture(t)
	ctx := context.Background()
	var src lc.Source
	_ = s.Store.Get(ctx, "source", "pinned", &src)
	calls := []string{}
	runner := func(_ context.Context, _ Team, _ lc.Source, _ InstructorBundle, steps []GuestPlaybook, _ ...lc.Extensions) error {
		calls = append(calls, steps[0].Playbook)
		return nil
	}
	if err := s.applyScenario(ctx, Team{}, src, b, []string{"service-break", "second-break"}, runner); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"check.yml", "check.yml", "second.yml"}) {
		t.Fatal("a later break reset earlier effects", calls)
	}
	calls = nil
	failure := errors.New("guest outcome unknown")
	runner = func(_ context.Context, _ Team, _ lc.Source, _ InstructorBundle, steps []GuestPlaybook, _ ...lc.Extensions) error {
		calls = append(calls, steps[0].Playbook)
		if len(calls) == 2 {
			return failure
		}
		return nil
	}
	if err := s.applyScenario(ctx, Team{}, src, b, []string{"service-break", "second-break"}, runner); !errors.Is(err, failure) || len(calls) != 2 {
		t.Fatal("failure did not stop guest effects", calls, err)
	}
}
func TestScenarioUnknownGuestOutcomeRetainsSelectionAllocationAndCleanup(t *testing.T) {
	s, v, _ := scenarioFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	lab := createAttemptFor(t, s, v, strings.Repeat("a", 24), "ccdc")
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF), 202)
	e.guestError = errors.New("response lost")
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err == nil {
		t.Fatal("unknown guest result became ready")
	}
	p, _ := s.Store.practice(ctx, run.ID)
	if p.Status != "failed" || p.InstanceID == "" {
		t.Fatal(p)
	}
	var state scenarioPreparation
	_ = s.Store.Get(ctx, "scenario-preparation", run.ID, &state)
	if state.Status != "applying" {
		t.Fatal("lost durable uncertainty")
	}
	checks := e.checks
	e.guestError = nil
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err == nil || e.checks != checks || e.deploys != 1 {
		t.Fatal("uncertain mutation replayed", err, e.checks, e.deploys)
	}
	if w := request(s, "POST", "/api/templates/pinned/attempt", attemptBody(strings.Repeat("b", 24), "ccdc"), s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal("reroll before cleanup")
	}
	decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/stop", `{}`, s.Config.Origin, v.CSRF), 202)
	e.destroyError = errors.New("termination response lost")
	if err := s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err == nil {
		t.Fatal("unknown destruction treated as absent")
	}
	p, _ = s.Store.practice(ctx, run.ID)
	if p.Status != "ending" || p.EndedAt != nil {
		t.Fatal(p)
	}
	e.destroyError = nil
	if err := s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF); w.Code != 409 {
		t.Fatal("old scenario replayed")
	}
	createAttemptFor(t, s, v, strings.Repeat("b", 24), "ccdc")
}
func TestScenarioInvalidSavedIntentCannotProvision(t *testing.T) {
	s, v, _ := scenarioFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	lab := createAttemptFor(t, s, v, strings.Repeat("a", 24), "ccdc")
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF), 202)
	var a templateAttempt
	if err := s.Store.Get(ctx, "template-attempt", lab.ID, &a); err != nil {
		t.Fatal(err)
	}
	a.Breaks = []string{"unknown"}
	if err := s.Store.Put(ctx, "template-attempt", lab.ID, a); err != nil {
		t.Fatal(err)
	}
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err == nil || e.deploys != 0 || e.checks != 0 {
		t.Fatal("invalid saved intent reached provisioning", err, e.deploys, e.checks)
	}
}

func TestScenarioGuestVerificationUsesAttemptSnapshot(t *testing.T) {
	s, v, b := scenarioFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	lab := createAttemptFor(t, s, v, strings.Repeat("a", 24), "ccdc")
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF), 202)
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	var p LearningPublication
	if err := s.Store.Get(ctx, "learning-publication", lab.ID, &p); err != nil {
		t.Fatal(err)
	}
	// A subsequent metadata update cannot remove the attempt's pinned check.
	b.Checks = nil
	if err := s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	check := decodeLearning[LearningCheck](t, request(s, "POST", "/api/learning/"+lab.ID+"/check", fmt.Sprintf(`{"stepID":"%s"}`, p.Steps[0].ID), s.Config.Origin, v.CSRF), 202)
	before := e.checks
	if err := s.runLearningCheck(ctx, Job{Kind: "learning-check", Target: check.ID}); err != nil {
		t.Fatal(err)
	}
	progress, err := s.Store.learningProgress(ctx, v.User.ID, lab.ID)
	if err != nil || progress.CompletedSteps != 1 || e.checks != before+1 {
		t.Fatal("attempt check was replaced", progress, err, e.checks)
	}
}

func TestScenarioAppliedCheckpointDoesNotRepeatGuestEffects(t *testing.T) {
	s, v, _ := scenarioFixture(t)
	ctx := context.Background()
	e := practiceEngineFor(t, s)
	lab := createAttemptFor(t, s, v, strings.Repeat("a", 24), "ccdc")
	run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF), 202)
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	checks := e.checks
	// Model a crash after the applied checkpoint but before durable readiness.
	if _, err := s.Store.DB.ExecContext(ctx, "UPDATE practice_runs SET status='preparing' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err != nil || e.checks != checks {
		t.Fatal("confirmed effects repeated", err)
	}
}
func TestStandardLibraryTemplateStrictImport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "template")
	if err := os.CopyFS(dir, os.DirFS("../templates/ccdc-service-recovery")); err != nil {
		t.Fatal(err)
	}
	bundle, err := extractInstructor(dir)
	if err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.Validate(src); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Scenarios) != 1 || scenarioSummary(bundle.Scenarios[0]).MaxBreaks != 2 {
		t.Fatal(bundle.Scenarios)
	}
	for _, f := range src.Files {
		if strings.Contains(f.Path, "instructor") || strings.Contains(string(f.Content), "exerciseIDs") {
			t.Fatal("private definitions in portable source")
		}
	}
	for _, raw := range []string{"version: 2\nscenarios: wrong\n", "version: 2\nscenarios:\n  - id: ccdc\n    invented: true\n", "version: 1\nscenarios: []\nrandomPool: true\n"} {
		root := t.TempDir()
		_ = os.WriteFile(filepath.Join(root, "instructor.yaml"), []byte(raw), 0600)
		if _, err = extractInstructor(root); err == nil {
			t.Fatal("unknown scenario fields accepted")
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "instructor.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private manifest retained")
	}
}
