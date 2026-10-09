package hosted

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func stageFixture(t *testing.T) (*Service, Session, InstructorBundle) {
	t.Helper()
	s, v, _ := learningFixture(t)
	ctx := context.Background()
	var b InstructorBundle
	if err := s.Store.Get(ctx, "template-instructor", "pinned", &b); err != nil {
		t.Fatal(err)
	}
	b.Version = 2
	b.Exercises = []TemplateExercise{{ID: "service-break", Name: "Private service break", Description: "PRIVATE-BREAK", Walkthrough: "PRIVATE-WALKTHROUGH", Steps: []GuestPlaybook{{Machine: "workstation", Playbook: "check.yml"}}}}
	b.Reset = b.Exercises[0].Steps
	b.Stages = []TemplateStage{
		{ID: "inspect", Title: "Inspect service", Instructions: "Inspect the baseline service configuration.", Kind: "answer", Prompt: "Name the service.", Answer: "PRIVATE-STAGE-ANSWER", Solution: "PRIVATE-STAGE-SOLUTION"},
		{ID: "verify", Title: "Verify baseline", Instructions: "Verify the baseline service.", Kind: "action", CheckID: "service", Solution: "PRIVATE-VERIFY-SOLUTION"},
		{ID: "break", Title: "Recover service", Instructions: "Recover the meeting service.", Kind: "action", CheckID: "service", ExerciseID: "service-break", Solution: "PRIVATE-BREAK-SOLUTION"},
		{ID: "discussion", Title: "Discuss recovery", Instructions: "Discuss your approach with the instructor.", Kind: "instructor"},
	}
	var src lc.Source
	_ = s.Store.Get(ctx, "source", "pinned", &src)
	if err := b.validate(src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	var template Template
	_ = s.Store.Get(ctx, "template", "pinned", &template)
	template.Name, template.Commit, template.Handover = src.Lab.Name, strings.Repeat("a", 40), "Student handover"
	view := b.view()
	template.Instructor = &view
	if err := s.Store.Put(ctx, "template", "pinned", template); err != nil {
		t.Fatal(err)
	}
	return s, v, b
}

func TestTemplateStageImportStrictValidationAndPortablePrivacy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "template")
	if err := os.CopyFS(dir, os.DirFS("../examples/workstation")); err != nil {
		t.Fatal(err)
	}
	raw := "version: 2\nnotes: PRIVATE-NOTE\nchecks:\n  - id: service\n    name: Service\n    points: 10\n    steps:\n      - {machine: workstation, playbook: check.yml}\nstages:\n  - id: inspect\n    title: Inspect\n    instructions: Read configuration.\n    kind: answer\n    prompt: Name the service.\n    answer: PRIVATE-ANSWER\n    solution: PRIVATE-SOLUTION\n  - id: verify\n    title: Verify\n    instructions: Verify the service.\n    kind: action\n    checkID: service\n"
	if err := os.Mkdir(filepath.Join(dir, "instructor"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instructor", "check.yml"), []byte("- hosts: localhost\n  tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instructor.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := extractInstructor(dir)
	if err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.validate(src); err != nil {
		t.Fatal(err)
	}
	if b.Stages[1].CheckID != "service" || len(b.view().Stages) != 2 {
		t.Fatal("private stage definition lost")
	}
	for _, f := range src.Files {
		if strings.Contains(f.Path, "instructor") || strings.Contains(string(f.Content), "PRIVATE-") {
			t.Fatal("author stages entered portable source")
		}
	}
	for name, raw := range map[string]string{
		"version one stages":   strings.Replace(raw, "version: 2", "version: 1", 1),
		"unknown stage field":  raw + "    randomVariant: true\n",
		"snake case reference": strings.Replace(raw, "checkID:", "check_id:", 1),
		"duplicate stage key":  raw + "    id: another\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "instructor.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := extractInstructor(root); err == nil {
				t.Fatal("ambiguous stage metadata accepted")
			}
		})
	}
}

func TestTemplateStageValidationRejectsBoundsAndForeignReferences(t *testing.T) {
	_, _, base := stageFixture(t)
	for name, modify := range map[string]func(*InstructorBundle){
		"duplicate stage":    func(b *InstructorBundle) { b.Stages = append(b.Stages, b.Stages[0]) },
		"excessive stages":   func(b *InstructorBundle) { b.Stages = make([]TemplateStage, 21) },
		"unsafe id":          func(b *InstructorBundle) { b.Stages[0].ID = "../inspect" },
		"empty title":        func(b *InstructorBundle) { b.Stages[0].Title = " " },
		"empty instructions": func(b *InstructorBundle) { b.Stages[0].Instructions = " " },
		"long instructions":  func(b *InstructorBundle) { b.Stages[0].Instructions = strings.Repeat("x", 6001) },
		"long prompt":        func(b *InstructorBundle) { b.Stages[0].Prompt = strings.Repeat("x", 1001) },
		"long solution":      func(b *InstructorBundle) { b.Stages[0].Solution = strings.Repeat("x", 6001) },
		"empty answer":       func(b *InstructorBundle) { b.Stages[0].Answer = " " },
		"long answer":        func(b *InstructorBundle) { b.Stages[0].Answer = strings.Repeat("x", 1001) },
		"answer check":       func(b *InstructorBundle) { b.Stages[0].CheckID = "service" },
		"foreign check":      func(b *InstructorBundle) { b.Stages[1].CheckID = "foreign" },
		"action answer":      func(b *InstructorBundle) { b.Stages[1].Answer = "x" },
		"foreign break":      func(b *InstructorBundle) { b.Stages[2].ExerciseID = "foreign" },
		"instructor check":   func(b *InstructorBundle) { b.Stages[3].CheckID = "service" },
		"unknown kind":       func(b *InstructorBundle) { b.Stages[0].Kind = "random" },
		"legacy metadata":    func(b *InstructorBundle) { b.Version = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := base
			b.Stages = append([]TemplateStage{}, base.Stages...)
			modify(&b)
			if b.validateStages() == nil {
				t.Fatal("invalid stages accepted")
			}
		})
	}
}

func TestPublicTemplateCatalogRequiresIdentityAndExcludesPrivateState(t *testing.T) {
	s, _, b := stageFixture(t)
	ctx := context.Background()
	var src lc.Source
	_ = s.Store.Get(ctx, "source", "pinned", &src)
	src.Lab.Description = "Explore a service"
	src.Lab.Inputs = map[string]lc.Input{"private": {Type: "string", Default: "PRIVATE-INPUT"}}
	if err := s.Store.Put(ctx, "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/templates", "/api/templates/pinned"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "https://labs.invalid"+path, nil))
		if w.Code != 401 {
			t.Fatal("anonymous catalog access", w.Code)
		}
		w = request(s, "GET", path, "", "", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		for _, forbidden := range []string{"PRIVATE-", `"instructor":`, "checkID", "exerciseID", "walkthrough", "playbook", "inputs", "files", "operation", "source", `"answer":`, `"solution"`} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatal("private catalog field", forbidden, w.Body.String())
			}
		}
	}
	catalog := decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates", "", "", ""), 200)
	if len(catalog.Templates) != 1 || catalog.Templates[0].StageCount != len(b.Stages) || catalog.Templates[0].Description != src.Lab.Description || !reflect.DeepEqual(catalog.Templates[0].PracticeStageIDs, []string{"inspect", "verify"}) || len(catalog.Templates[0].Machines) != 1 {
		t.Fatal(catalog)
	}
	if !catalog.Templates[0].Stages[0].HasSolution {
		t.Fatal("missing deliberate reveal availability")
	}
	if request(s, "GET", "/api/templates/missing", "", "", "").Code != 404 {
		t.Fatal("missing revision not rejected")
	}
	// Imported legacy revisions remain explorable without invented author stages.
	legacy := Template{ID: "legacy", Name: src.Lab.Name}
	_ = s.Store.Put(ctx, "template", legacy.ID, legacy)
	_ = s.Store.Put(ctx, "source", legacy.ID, src)
	view := decodeLearning[PublicTemplate](t, request(s, "GET", "/api/templates/legacy", "", "", ""), 200)
	if len(view.Stages) != 0 || len(view.PracticeStageIDs) != 0 {
		t.Fatal("stages attached to old revision", view)
	}
	newer := time.Now().UTC()
	legacy.CommittedAt = &newer
	_ = s.Store.Put(ctx, "template", legacy.ID, legacy)
	catalog = decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates", "", "", ""), 200)
	if catalog.Templates[0].ID != "legacy" {
		t.Fatal("catalog not newest first")
	}
}

func TestTemplateStagePublicationPinsSelectionAndRejectsUnsupportedPractice(t *testing.T) {
	s, v, b := stageFixture(t)
	ctx := context.Background()
	req := PublishLearningRequest{Template: "pinned", Title: "Baseline recovery", StageIDs: []string{"verify", "inspect"}}
	body, _ := json.Marshal(req)
	if request(s, "POST", "/api/admin/learning/publish", string(body), s.Config.Origin, v.CSRF).Code != 403 {
		t.Fatal("student selected private stages")
	}
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	for _, headers := range []struct{ origin, csrf string }{{"https://foreign.invalid", v.CSRF}, {s.Config.Origin, "bad"}} {
		if request(s, "POST", "/api/admin/learning/publish", string(body), headers.origin, headers.csrf).Code != 403 {
			t.Fatal("stage publication CSRF bypass")
		}
	}
	p := decodeLearning[LearningPublication](t, request(s, "POST", "/api/admin/learning/publish", string(body), s.Config.Origin, v.CSRF), 201)
	if p.Mode != "self-paced" || p.ReleasedSteps != 2 || p.Steps[0].ID != "verify" || p.Steps[1].Answer != b.Stages[0].Answer || p.Steps[1].Solution != b.Stages[0].Solution {
		t.Fatal("publication did not snapshot author selection", p)
	}
	for name, modify := range map[string]func(*PublishLearningRequest){
		"unknown":               func(r *PublishLearningRequest) { r.StageIDs = []string{"foreign"} },
		"duplicate":             func(r *PublishLearningRequest) { r.StageIDs = []string{"inspect", "inspect"} },
		"meeting break":         func(r *PublishLearningRequest) { r.StageIDs = []string{"break"} },
		"instructor checkpoint": func(r *PublishLearningRequest) { r.StageIDs = []string{"discussion"} },
		"legacy pacing":         func(r *PublishLearningRequest) { r.Mode = "instructor" },
		"mixed definitions":     func(r *PublishLearningRequest) { r.Steps = p.Steps },
		"old revision":          func(r *PublishLearningRequest) { r.Template = "legacy" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := req
			modify(&copy)
			body, _ := json.Marshal(copy)
			want := 400
			if name == "old revision" {
				want = 409
			}
			if w := request(s, "POST", "/api/admin/learning/publish", string(body), s.Config.Origin, v.CSRF); w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	// Author metadata refreshed elsewhere cannot rewrite an existing publication.
	b.Stages[0].Answer, b.Stages[0].Solution = "changed", "changed"
	_ = s.Store.Put(ctx, "template-instructor", "pinned", b)
	root := s.Store.Root
	_ = s.Store.Close()
	var err error
	s.Store, err = OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Store.Close()
	var retained LearningPublication
	if err = s.Store.Get(ctx, "learning-publication", p.ID, &retained); err != nil || !reflect.DeepEqual(retained, p) {
		t.Fatal("publication changed across reopen", retained, err)
	}
	for _, path := range []string{"/api/learning", "/api/learning/" + p.ID} {
		w := request(s, "GET", path, "", "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), "checkID") || strings.Contains(w.Body.String(), `"solution"`) {
			t.Fatal("publication private fields leaked", w.Body.String())
		}
	}
	download := request(s, "GET", "/api/learning/"+p.ID+"/download", "", "", "")
	gz, err := gzip.NewReader(bytes.NewReader(download.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	for {
		_, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(archive)
		if err != nil || strings.Contains(string(data), "PRIVATE-") {
			t.Fatal("private answer/solution in download", err)
		}
	}
}

func TestLearningSolutionIntentionalRevealEnforcesProgressWithoutMutation(t *testing.T) {
	s, v, _ := stageFixture(t)
	ctx := context.Background()
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	p := decodeLearning[LearningPublication](t, request(s, "POST", "/api/admin/learning/publish", `{"template":"pinned","title":"Explore","stageIDs":["inspect","verify"]}`, s.Config.Origin, v.CSRF), 201)
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	path := "/api/learning/" + p.ID + "/solution"
	body := `{"stepID":"inspect"}`
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "https://labs.invalid"+path, strings.NewReader(body)))
	if w.Code != 401 {
		t.Fatal("anonymous solution", w.Code)
	}
	for _, headers := range []struct{ origin, csrf string }{{"https://foreign.invalid", v.CSRF}, {s.Config.Origin, "bad"}} {
		if request(s, "POST", path, body, headers.origin, headers.csrf).Code != 403 {
			t.Fatal("reveal CSRF bypass")
		}
	}
	for _, id := range []string{"verify", "missing"} {
		if request(s, "POST", path, `{"stepID":"`+id+`"}`, s.Config.Origin, v.CSRF).Code != 409 {
			t.Fatal("future/unknown solution revealed", id)
		}
	}
	before, _ := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() { codes <- request(s, "POST", path, body, s.Config.Origin, v.CSRF).Code })
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("current solution rejected", code)
		}
	}
	after, _ := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("revealing solution mutated progress")
	}
	result := decodeLearning[LearningSolution](t, request(s, "POST", path, body, s.Config.Origin, v.CSRF), 200)
	if result.StepID != "inspect" || result.Solution != "PRIVATE-STAGE-SOLUTION" {
		t.Fatal(result)
	}
	progress := LearningProgress{LabID: p.ID, CompletedSteps: 1, Updated: time.Now().UTC(), LastAnswer: "kept", LastFeedback: "kept"}
	_ = s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), progress)
	decodeLearning[LearningSolution](t, request(s, "POST", path, body, s.Config.Origin, v.CSRF), 200)
	decodeLearning[LearningSolution](t, request(s, "POST", path, `{"stepID":"verify"}`, s.Config.Origin, v.CSRF), 200)
	p.ReleasedSteps = 1
	_ = s.Store.Put(ctx, "learning-publication", p.ID, p)
	if request(s, "POST", path, `{"stepID":"verify"}`, s.Config.Origin, v.CSRF).Code != 409 {
		t.Fatal("unreleased solution disclosed")
	}
	p.Steps[0].Solution = ""
	_ = s.Store.Put(ctx, "learning-publication", p.ID, p)
	if request(s, "POST", path, body, s.Config.Origin, v.CSRF).Code != 409 {
		t.Fatal("missing/legacy solution accepted")
	}
	after, _ = s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if !reflect.DeepEqual(progress, after) {
		t.Fatal("reveal overwrote saved answer/feedback")
	}
}

func TestLearningSolutionUsesOwnProgressAndRejectsUnpublishedHistory(t *testing.T) {
	s, v, _ := stageFixture(t)
	ctx := context.Background()
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	p := decodeLearning[LearningPublication](t, request(s, "POST", "/api/admin/learning/publish", `{"template":"pinned","title":"Explore","stageIDs":["inspect","verify"]}`, s.Config.Origin, v.CSRF), 201)
	v.User.Admin = false
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	studentA := LearningProgress{LabID: p.ID, CompletedSteps: 1, Updated: time.Now().UTC(), LastAnswer: "Student A's saved answer", LastFeedback: "Student A's saved feedback"}
	if err := s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), studentA); err != nil {
		t.Fatal(err)
	}
	path := "/api/learning/" + p.ID + "/solution"
	decodeLearning[LearningSolution](t, request(s, "POST", path, `{"stepID":"verify"}`, s.Config.Origin, v.CSRF), 200)
	studentB := v
	studentB.User = User{ID: "student-b", Name: "Student B"}
	if err := s.Store.Put(ctx, "session", v.ID, studentB); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", path, `{"stepID":"verify"}`, s.Config.Origin, studentB.CSRF); w.Code != 409 {
		t.Fatal("another student's progress permitted a future reveal", w.Code, w.Body.String())
	}
	decodeLearning[LearningSolution](t, request(s, "POST", path, `{"stepID":"inspect"}`, s.Config.Origin, studentB.CSRF), 200)
	retainedA, err := s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if err != nil || !reflect.DeepEqual(retainedA, studentA) {
		t.Fatal("other student's reveal changed existing progress", retainedA, err)
	}
	progressB, err := s.Store.learningProgress(ctx, studentB.User.ID, p.ID)
	if err != nil || !reflect.DeepEqual(progressB, LearningProgress{LabID: p.ID}) {
		t.Fatal("reveal created progress for a fresh student", progressB, err)
	}
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	decodeLearning[LearningPublication](t, request(s, "POST", "/api/admin/learning/unpublish", `{"id":"`+p.ID+`"}`, s.Config.Origin, v.CSRF), 200)
	if err := s.Store.Put(ctx, "session", v.ID, studentB); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", path, `{"stepID":"inspect"}`, s.Config.Origin, studentB.CSRF); w.Code != 404 {
		t.Fatal("unpublished solution remained available", w.Code, w.Body.String())
	}
	var retained LearningPublication
	if err := s.Store.Get(ctx, "learning-publication", p.ID, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Published || !reflect.DeepEqual(retained.Steps, p.Steps) {
		t.Fatal("unpublishing removed private snapshot/history", retained)
	}
	retainedA, err = s.Store.learningProgress(ctx, v.User.ID, p.ID)
	if err != nil || !reflect.DeepEqual(retainedA, studentA) {
		t.Fatal("unpublishing discarded student history", retainedA, err)
	}
}
