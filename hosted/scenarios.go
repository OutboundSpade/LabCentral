package hosted

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"time"
)

type TemplateScenarioSummary struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Difficulty       string `json:"difficulty"`
	EstimatedMinutes int    `json:"estimatedMinutes"`
	VariantCount     int    `json:"variantCount"`
	MinBreaks        int    `json:"minBreaks"`
	MaxBreaks        int    `json:"maxBreaks"`
}

func scenarioSummary(s TemplateScenario) TemplateScenarioSummary {
	v := TemplateScenarioSummary{ID: s.ID, Title: s.Title, Description: s.Description, Difficulty: s.Difficulty, EstimatedMinutes: s.EstimatedMinutes, VariantCount: len(s.Variants)}
	for i, variant := range s.Variants {
		if i == 0 || len(variant.Breaks) < v.MinBreaks {
			v.MinBreaks = len(variant.Breaks)
		}
		v.MaxBreaks = max(v.MaxBreaks, len(variant.Breaks))
	}
	return v
}

type CreateTemplateAttemptRequest struct {
	ScenarioID string `json:"scenarioID,omitempty"`
	RequestID  string `json:"requestID"`
}

// Private attempt intent includes its answer key and exact programs before any
// resource is reserved. Subsequent imports cannot change an existing attempt.
type templateAttempt struct {
	ID         string                   `json:"id"`
	UserID     string                   `json:"userID"`
	Template   string                   `json:"template"`
	RequestID  string                   `json:"requestID"`
	ScenarioID string                   `json:"scenarioID"`
	VariantID  string                   `json:"variantID"`
	Breaks     []string                 `json:"breaks"`
	Scenario   *TemplateScenarioSummary `json:"scenario,omitempty"`
	Bundle     InstructorBundle         `json:"bundle"`
}

var attemptRequestPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

func (s *Service) ownedPublication(ctx context.Context, id, user string) (LearningPublication, error) {
	var p LearningPublication
	err := s.Store.Get(ctx, "learning-publication", id, &p)
	if err == nil && (!p.Published || s.templateAudience(ctx, p.Template, p.OwnerID == user && s.coachForUser(ctx, user)) != nil) {
		err = fmt.Errorf("This lab is unavailable.")
	}
	if err == nil && p.OwnerID != "" && p.OwnerID != user {
		err = fmt.Errorf("This lab is unavailable.")
	}
	return p, err
}
func (s *Service) learningBundle(ctx context.Context, p LearningPublication) (InstructorBundle, error) {
	if p.OwnerID != "" {
		var a templateAttempt
		err := s.Store.Get(ctx, "template-attempt", p.ID, &a)
		if err == nil && (a.ID != p.ID || a.UserID != p.OwnerID || a.Template != p.Template || a.ScenarioID != p.ScenarioID) {
			err = fmt.Errorf("attempt identity does not match")
		}
		return a.Bundle, err
	}
	var b InstructorBundle
	err := s.Store.Get(ctx, "template-instructor", p.Template, &b)
	return b, err
}
func (s *Service) createTemplateAttemptDecision(w http.ResponseWriter, r *http.Request, v Session) {
	var req CreateTemplateAttemptRequest
	if !decode(w, r, &req) {
		return
	}
	if !attemptRequestPattern.MatchString(req.RequestID) {
		writeError(w, 400, "Supply a 24-character random request ID and reuse it when retrying.")
		return
	}
	templateID := r.PathValue("id")
	if s.templateAudience(r.Context(), templateID, v.User.Admin) != nil {
		writeError(w, 404, "This template revision is unavailable.")
		return
	}
	id := digest("attempt\x00" + v.User.ID + "\x00" + req.RequestID)[:24]
	var prior templateAttempt
	err := s.Store.Get(r.Context(), "template-attempt", id, &prior)
	if err == nil {
		if prior.Template != templateID || prior.ScenarioID != req.ScenarioID || prior.UserID != v.User.ID {
			writeError(w, 409, "This request ID belongs to another attempt.")
			return
		}
		p, e := s.ownedPublication(r.Context(), id, v.User.ID)
		if e != nil {
			writeError(w, 409, "This attempt is unavailable.")
			return
		}
		progress, e := s.Store.learningProgress(r.Context(), v.User.ID, id)
		if e != nil {
			writeError(w, 503, "Your attempt is unavailable. Retry with the same request ID.")
			return
		}
		writeJSON(w, 200, p.summary(progress))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 503, "Attempts are unavailable. Retry with the same request ID.")
		return
	}
	if err = s.actionableRevision(r.Context(), templateID); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if run, e := s.Store.activePractice(r.Context(), v.User.ID); e != nil {
		writeError(w, 503, "Cloud allocation information is unavailable.")
		return
	} else if run != nil {
		writeError(w, 409, "Stop and finish cleanup of your active cloud lab before creating another attempt.")
		return
	}
	var t Template
	var src lc.Source
	var b InstructorBundle
	if s.Store.Get(r.Context(), "template", templateID, &t) != nil || s.Store.Get(r.Context(), "source", templateID, &src) != nil || s.Store.Get(r.Context(), "template-instructor", templateID, &b) != nil {
		writeError(w, 409, "Choose a pinned template with authored practice stages.")
		return
	}
	if _, err = lc.PlanLab(src, s.env, nil); err != nil {
		writeError(w, 409, "This pinned source cannot be planned in the configured environment.")
		return
	}
	a := templateAttempt{ID: id, UserID: v.User.ID, Template: templateID, RequestID: req.RequestID, ScenarioID: req.ScenarioID, Bundle: b}
	p := LearningPublication{ID: id, Template: templateID, OwnerID: v.User.ID, ScenarioID: req.ScenarioID, Title: t.Name + " · Baseline practice", Description: src.Lab.Description, Mode: "self-paced", EstimatedMinutes: 30, Published: true, PublishedAt: time.Now().UTC()}
	if b.Library != nil {
		p.EstimatedMinutes = b.Library.EstimatedMinutes
	}
	if req.ScenarioID == "" {
		ids := []string{}
		for _, st := range b.Stages {
			if st.Kind() != "instructor" && st.Break == "" {
				ids = append(ids, st.ID)
			}
		}
		p.Steps, err = resolvePracticeStages(ids, b)
	} else {
		var scenario *TemplateScenario
		for i := range b.Scenarios {
			if b.Scenarios[i].ID == req.ScenarioID {
				scenario = &b.Scenarios[i]
			}
		}
		if scenario == nil {
			writeError(w, 400, "Choose a scenario from this pinned template.")
			return
		}
		if err = s.practiceTemplateReady(r.Context(), templateID); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		// Avoid an immediate repeat of the last saved combination, independently of
		// whether the previous allocation completed. Request replay never draws again.
		var last string
		e := s.Store.DB.QueryRowContext(r.Context(), `SELECT json_extract(a.body,'$.variantID') FROM objects a JOIN objects p ON p.kind='learning-publication' AND p.id=a.id WHERE a.kind='template-attempt' AND json_extract(a.body,'$.userID')=? AND json_extract(a.body,'$.template')=? AND json_extract(a.body,'$.scenarioID')=? ORDER BY json_extract(p.body,'$.publishedAt') DESC,p.id DESC LIMIT 1`, v.User.ID, templateID, req.ScenarioID).Scan(&last)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			writeError(w, 503, "Attempt history is unavailable. Retry with the same request ID.")
			return
		}
		choices := []ScenarioVariant{}
		for _, variant := range scenario.Variants {
			if variant.ID != last {
				choices = append(choices, variant)
			}
		}
		n, e := rand.Int(rand.Reader, big.NewInt(int64(len(choices))))
		if e != nil {
			writeError(w, 503, "Scenario selection is unavailable. Retry with the same request ID.")
			return
		}
		variant := choices[n.Int64()]
		a.VariantID = variant.ID
		a.Breaks = slices.Clone(variant.Breaks)
		summary := scenarioSummary(*scenario)
		a.Scenario = &summary
		p.Title = scenario.Title
		p.Description = scenario.Description
		p.EstimatedMinutes = scenario.EstimatedMinutes
		for _, id := range variant.Stages {
			for _, st := range b.Stages {
				if st.ID == id {
					p.Steps = append(p.Steps, learningStage(st))
				}
			}
		}
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	p.ReleasedSteps = len(p.Steps)
	tx, err := s.Store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 503, "Attempt could not be saved. Retry with the same request ID.")
		return
	}
	defer tx.Rollback()
	for _, o := range []struct {
		kind  string
		value any
	}{{"template-attempt", a}, {"learning-publication", p}} {
		raw, e := json.Marshal(o.value)
		if e == nil {
			_, e = tx.ExecContext(r.Context(), `INSERT INTO objects(kind,id,body) VALUES(?,?,?)`, o.kind, id, raw)
		}
		if e != nil {
			writeError(w, 503, "Attempt could not be saved. Retry with the same request ID.")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 503, "Attempt outcome is unknown. Retry with the same request ID.")
		return
	}
	writeJSON(w, 201, p.summary(LearningProgress{}))
}

// A lost guest response is not replay permission. "applying" survives a crash
// and forces cleanup; "applied" is the only preparation checkpoint that resumes.
type scenarioPreparation struct {
	RunID     string `json:"runID"`
	AttemptID string `json:"attemptID"`
	VariantID string `json:"variantID"`
	Status    string `json:"status"`
}

func (s *Service) scenarioIntent(ctx context.Context, run practiceRecord, p LearningPublication, src lc.Source) (templateAttempt, error) {
	var a templateAttempt
	if err := s.Store.Get(ctx, "template-attempt", p.ID, &a); err != nil {
		return a, err
	}
	if a.UserID != run.UserID || a.ID != run.LabID || a.Template != p.Template || a.ScenarioID != p.ScenarioID || a.Scenario == nil || len(a.Breaks) == 0 {
		return a, fmt.Errorf("scenario intent does not match allocation")
	}
	matched := false
	for _, scenario := range a.Bundle.Scenarios {
		if scenario.ID != a.ScenarioID {
			continue
		}
		for _, variant := range scenario.Variants {
			if variant.ID == a.VariantID && slices.Equal(variant.Breaks, a.Breaks) {
				matched = true
			}
		}
	}
	if !matched {
		return a, fmt.Errorf("saved scenario combination is unavailable")
	}
	return a, nil
}

func (s *Service) prepareScenario(ctx context.Context, run practiceRecord, p LearningPublication, src lc.Source) error {
	ctx, closeGuest := guestJobContext(ctx)
	defer closeGuest()
	if p.ScenarioID == "" {
		return nil
	}
	a, err := s.scenarioIntent(ctx, run, p, src)
	if err != nil {
		return err
	}

	var state scenarioPreparation
	err = s.Store.Get(ctx, "scenario-preparation", run.ID, &state)
	if err == nil {
		if state.RunID != run.ID || state.AttemptID != p.ID || state.VariantID != a.VariantID {
			return fmt.Errorf("scenario preparation identity differs")
		}
		if state.Status == "applied" {
			return nil
		}
		return fmt.Errorf("scenario preparation outcome is unknown; stop this run and create a new attempt")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	state = scenarioPreparation{RunID: run.ID, AttemptID: p.ID, VariantID: a.VariantID, Status: "applying"}
	if err = s.Store.Put(ctx, "scenario-preparation", run.ID, state); err != nil {
		return err
	}
	if err = s.applyScenario(ctx, Team{InstanceID: run.InstanceID}, src, a.Bundle, a.Breaks, s.runGuestPlaybooks); err != nil {
		return err
	}
	state.Status = "applied"
	return s.Store.Put(ctx, "scenario-preparation", run.ID, state)
}

// Restore the shared baseline once; never run a reset between compatible breaks.
func (s *Service) applyScenario(ctx context.Context, team Team, src lc.Source, b InstructorBundle, ids []string, runner func(context.Context, Team, lc.Source, InstructorBundle, []GuestPlaybook, ...lc.Extensions) error) error {
	if err := runner(ctx, team, src, b, b.Reset); err != nil {
		return err
	}
	for _, id := range ids {
		found := false
		for _, x := range b.Breaks {
			if x.ID == id {
				found = true
				if err := runner(ctx, team, src, b, x.Steps, x.Extensions); err != nil {
					return err
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("saved scenario break is unavailable")
		}
	}
	return nil
}

func (s *Service) createTemplateAttempt(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.createTemplateAttemptDecision)(w, r, v)
}
