package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
	"testing"
	"time"
)

func adminPracticeFixture(t *testing.T) (*Service, Session, LearningPublication) {
	t.Helper()
	s, v, p := learningFixture(t)
	v.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	return s, v, p
}
func practiceSetting(t *testing.T, s *Service) PracticeSettings {
	t.Helper()
	return decodeLearning[PracticeSettings](t, request(s, "GET", "/api/admin/practice", "", "", ""), 200)
}
func changePractice(t *testing.T, s *Service, v Session, enabled bool, preview PracticeSettings, want int) PracticeSettings {
	t.Helper()
	body, _ := json.Marshal(PracticeSettingsRequest{enabled, preview.Confirmation})
	w := request(s, "POST", "/api/admin/practice", string(body), s.Config.Origin, v.CSRF)
	if want != 200 {
		if w.Code != want {
			t.Fatalf("change status %d: %s", w.Code, w.Body.String())
		}
		return PracticeSettings{}
	}
	return decodeLearning[PracticeSettings](t, w, 200)
}
func TestPracticeSettingsAuthorizationAndAdmission(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	for _, method := range []string{"GET", "POST"} {
		if w := request(s, method, "/api/admin/practice", `{"enabled":false}`, s.Config.Origin, v.CSRF); w.Code != 403 {
			t.Fatal("student setting access", w.Code)
		}
	}
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/admin/practice", `{"enabled":false}`, "https://foreign.invalid", v.CSRF); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	initial := practiceSetting(t, s)
	if !initial.Enabled || len(initial.ActiveLabs) != 0 {
		t.Fatal(initial)
	}
	changePractice(t, s, v, false, PracticeSettings{}, 409)
	changePractice(t, s, v, false, initial, 200)
	for _, path := range []string{"/api/learning", "/api/learning/linux", "/api/learning/linux/download"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/templates/pinned/attempt", "/api/learning/linux/answer", "/api/learning/linux/solution", "/api/learning/linux/check", "/api/learning/linux/start", "/api/learning/linux/access"} {
		if w := request(s, "POST", path, `{}`, s.Config.Origin, v.CSRF); w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// Internal callers and admission transactions enforce the same persisted policy.
	if _, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now()); err == nil {
		t.Fatal("reservation bypassed disable")
	}
	if s.practiceDisclosure(ctx, p.ID) == nil {
		t.Fatal("desktop authorization bypassed disable")
	}
	if _, err := s.ownedPublication(ctx, p.ID, v.User.ID); err == nil {
		t.Fatal("publication bypassed disable")
	}
	view := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if view.PracticeEnabled {
		t.Fatal("overview enabled")
	}
	if w := request(s, "GET", "/api/admin", "", "", ""); w.Code != 200 {
		t.Fatal("admin management unavailable", w.Code, w.Body.String())
	}
	v.User.Admin = false
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/templates", "/api/templates/pinned"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 403 {
			t.Fatal("student catalog", path, w.Code)
		}
	}
	if w := request(s, "POST", "/api/team", `{"teamID":""}`, s.Config.Origin, v.CSRF); w.Code == 403 {
		t.Fatal("meeting policy blocked")
	}
}
func TestPracticeDisableStalePreviewAndAtomicCleanup(t *testing.T) {
	s, v, p := adminPracticeFixture(t)
	ctx := context.Background()
	preview := practiceSetting(t, s)
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	changePractice(t, s, v, false, preview, 409)
	preview = practiceSetting(t, s)
	if len(preview.ActiveLabs) != 1 || preview.ActiveLabs[0].ID != run.ID || preview.ActiveLabs[0].Lab != p.Title {
		t.Fatal(preview)
	}
	// A failed cleanup enqueue must roll back both setting and run state.
	if _, err = s.Store.DB.Exec(`CREATE TRIGGER reject_cleanup BEFORE INSERT ON jobs WHEN NEW.kind='practice-end' BEGIN SELECT RAISE(ABORT,'test cleanup persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	changePractice(t, s, v, false, preview, 503)
	if !practiceSetting(t, s).Enabled {
		t.Fatal("failed cleanup disabled policy")
	}
	current, err := s.Store.practice(ctx, run.ID)
	if err != nil || current.Status != "queued" {
		t.Fatal("failed cleanup mutated run", current, err)
	}
	if _, err = s.Store.DB.Exec(`DROP TRIGGER reject_cleanup`); err != nil {
		t.Fatal(err)
	}
	disabled := changePractice(t, s, v, false, preview, 200)
	if disabled.Enabled {
		t.Fatal(disabled)
	}
	current, err = s.Store.practice(ctx, run.ID)
	if err != nil || current.Status != "ending" {
		t.Fatal(current, err)
	}
	var jobs int
	if err = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='practice-end' AND target=? AND status='queued'`, run.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("cleanup intent", jobs, err)
	}
	if current.InstanceID == "" {
		t.Fatal("lost exact native identity")
	}
	enabled := changePractice(t, s, v, true, disabled, 200)
	if !enabled.Enabled {
		t.Fatal(enabled)
	}
	current, _ = s.Store.practice(ctx, run.ID)
	if current.Status != "ending" {
		t.Fatal("reenabling restarted run")
	}
	// Stale enable confirmation cannot undo a later disable.
	changePractice(t, s, v, false, enabled, 200)
	changePractice(t, s, v, true, disabled, 409)
}
func TestPracticeDisableSurvivesRestartAndCleansAllStates(t *testing.T) {
	s, v, p := adminPracticeFixture(t)
	ctx := context.Background()
	for _, status := range []string{"queued", "preparing", "ready", "failed", "ending"} {
		run, err := s.Store.reservePractice(ctx, status, p.ID, 180, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.DB.Exec(`UPDATE practice_runs SET status=? WHERE id=?`, status, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	changePractice(t, s, v, false, practiceSetting(t, s), 200)
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.requirePractice(ctx) == nil {
		t.Fatal("restart enabled practice")
	}
	view, err := reopened.practiceSettings(ctx)
	if err != nil || len(view.ActiveLabs) != 5 {
		t.Fatal(view, err)
	}
	for _, a := range view.ActiveLabs {
		if a.Status != "ending" {
			t.Fatal(a)
		}
	}
	var jobs int
	if err = reopened.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='practice-end' AND status='queued'`).Scan(&jobs); err != nil || jobs != 5 {
		t.Fatal(jobs, err)
	}
}
func TestPracticeReservationRacesDisable(t *testing.T) {
	s, v, p := adminPracticeFixture(t)
	ctx := context.Background()
	preview := practiceSetting(t, s)
	var wg sync.WaitGroup
	wg.Add(1)
	var reservationErr error
	go func() {
		defer wg.Done()
		_, reservationErr = s.Store.reservePractice(ctx, "racing", p.ID, 180, time.Now())
	}()
	body, _ := json.Marshal(PracticeSettingsRequest{false, preview.Confirmation})
	w := request(s, "POST", "/api/admin/practice", string(body), s.Config.Origin, v.CSRF)
	wg.Wait()
	switch w.Code {
	case 200:
		if reservationErr == nil {
			t.Fatal("reservation admitted after reviewed empty disable")
		}
	case 409:
		if reservationErr != nil {
			t.Fatal(reservationErr)
		}
		changePractice(t, s, v, false, practiceSetting(t, s), 200)
	default:
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.Store.reservePractice(ctx, "late", p.ID, 180, time.Now()); err == nil {
		t.Fatal("late reservation")
	}
}

func TestPracticeDisableCancelsInFlightPreparationAndRetainsUnknownCleanup(t *testing.T) {
	s, v, p := adminPracticeFixture(t)
	ctx := context.Background()
	run, err := s.Store.reservePractice(ctx, v.User.ID, p.ID, 180, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	engine := &blockedPracticeEngine{started: make(chan string, 1), release: make(chan struct{})}
	s.Engine = engine
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.activeJobs = map[string]runningJob{"prepare": {job: Job{Kind: "practice-prepare"}, cancel: cancel, claims: []jobClaim{{key: "practice:" + run.ID}}}}
	done := make(chan error, 1)
	go func() { done <- s.runPractice(jobCtx, Job{Kind: "practice-prepare", Target: run.ID}) }()
	select {
	case <-engine.started:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation never reached engine")
	}
	changePractice(t, s, v, false, practiceSetting(t, s), 200)
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preparation not cancelled")
	}
	original, err := s.Store.practice(ctx, run.ID)
	if err != nil || original.Status != "ending" {
		t.Fatal(original, err)
	}
	cleanup := &policyCleanupEngine{instance: original.InstanceID, fail: true}
	s.Engine = cleanup
	if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err == nil {
		t.Fatal("unknown destruction accepted")
	}
	unknown, err := s.Store.practice(ctx, run.ID)
	if err != nil || unknown.Status != "ending" || unknown.InstanceID != original.InstanceID || unknown.EndedAt != nil {
		t.Fatal("lost cleanup intent", unknown, err)
	}
	cleanup.fail = false
	if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err != nil {
		t.Fatal(err)
	}
	ended, err := s.Store.practice(ctx, run.ID)
	if err != nil || ended.Status != "ended" || ended.EndedAt == nil {
		t.Fatal(ended, err)
	}
}

type policyCleanupEngine struct {
	Engine
	instance string
	fail     bool
}

func (e *policyCleanupEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	if id != e.instance {
		return lc.Instance{}, fmt.Errorf("wrong allocation identity")
	}
	return lc.Instance{ID: id, Status: "ready"}, nil
}
func (e *policyCleanupEngine) Destroy(_ context.Context, id string) error {
	if id != e.instance {
		return fmt.Errorf("wrong allocation identity")
	}
	if e.fail {
		return context.DeadlineExceeded
	}
	return nil
}
