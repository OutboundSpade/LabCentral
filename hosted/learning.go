package hosted

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Learning policy belongs to hosted. Publications pin source and verification
// definitions; the portable source and library carry no identity or HTTP policy.
type LearningStep struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Kind         string `json:"kind"`
	Prompt       string `json:"prompt"`
	HasSolution  bool   `json:"hasSolution,omitempty"`
}
type LearningStepInput struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Instructions string             `json:"instructions"`
	Kind         string             `json:"kind"`
	Prompt       string             `json:"prompt"`
	Answer       string             `json:"answer"`
	CheckID      string             `json:"checkID"`
	Solution     string             `json:"solution,omitempty"`
	AnswerRule   *instructor.Answer `json:"answerRule,omitempty"`
}
type LearningPublication struct {
	ID               string              `json:"id"`
	Template         string              `json:"template"`
	Title            string              `json:"title"`
	Description      string              `json:"description"`
	Mode             string              `json:"mode"`
	EstimatedMinutes int                 `json:"estimatedMinutes"`
	Steps            []LearningStepInput `json:"steps"`
	Published        bool                `json:"published"`
	PublishedAt      time.Time           `json:"publishedAt"`
	ReleasedSteps    int                 `json:"releasedSteps"`
	OwnerID          string              `json:"ownerID,omitempty"`
	ScenarioID       string              `json:"scenarioID,omitempty"`
}
type LearningLab struct {
	ID               string    `json:"id"`
	Template         string    `json:"template"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Mode             string    `json:"mode"`
	EstimatedMinutes int       `json:"estimatedMinutes"`
	StepCount        int       `json:"stepCount"`
	PublishedAt      time.Time `json:"publishedAt"`
	CompletedSteps   int       `json:"completedSteps"`
	Attempt          bool      `json:"attempt,omitempty"`
	ScenarioID       string    `json:"scenarioID,omitempty"`
}
type LearningProgress struct {
	LabID          string    `json:"labID"`
	CompletedSteps int       `json:"completedSteps"`
	Updated        time.Time `json:"updated"`
	LastAnswer     string    `json:"lastAnswer,omitempty"`
	LastFeedback   string    `json:"lastFeedback,omitempty"`
}
type CloudQuota struct {
	LimitMinutes     int       `json:"limitMinutes"`
	UsedSeconds      int       `json:"usedSeconds"`
	RemainingSeconds int       `json:"remainingSeconds"`
	ResetsAt         time.Time `json:"resetsAt"`
}
type PracticeRun struct {
	ID      string     `json:"id"`
	LabID   string     `json:"labID"`
	UserID  string     `json:"userID"`
	Status  string     `json:"status"`
	Created time.Time  `json:"created"`
	Expires time.Time  `json:"expires"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
	Error   string     `json:"error,omitempty"`
}
type LearningCheck struct {
	ID      string    `json:"id"`
	LabID   string    `json:"labID"`
	StepID  string    `json:"stepID"`
	Status  string    `json:"status"`
	Report  string    `json:"report"`
	Updated time.Time `json:"updated"`
}
type LearningCatalog struct {
	Labs  []LearningLab `json:"labs"`
	Quota CloudQuota    `json:"quota"`
	Run   *PracticeRun  `json:"run"`
}
type LearningView struct {
	Lab            LearningLab              `json:"lab"`
	Steps          []LearningStep           `json:"steps"`
	Progress       LearningProgress         `json:"progress"`
	Quota          CloudQuota               `json:"quota"`
	Run            *PracticeRun             `json:"run"`
	ReleasedSteps  int                      `json:"releasedSteps"`
	CloudReady     bool                     `json:"cloudReady"`
	LocalAvailable bool                     `json:"localAvailable"`
	Check          *LearningCheck           `json:"check"`
	Scenario       *TemplateScenarioSummary `json:"scenario,omitempty"`
}
type LearningAdminView struct {
	Labs         []LearningPublication `json:"labs"`
	QuotaMinutes int                   `json:"quotaMinutes"`
}
type LearningAnswerRequest struct {
	StepID string `json:"stepID"`
	Answer string `json:"answer"`
}
type LearningCheckRequest struct {
	StepID string `json:"stepID"`
}
type LearningSolutionRequest struct {
	StepID string `json:"stepID"`
}
type LearningSolution struct {
	StepID   string `json:"stepID"`
	Solution string `json:"solution"`
}
type learningCheckRecord struct {
	LearningCheck
	UserID         string `json:"userID"`
	RunID          string `json:"runID"`
	CompletedSteps int    `json:"completedSteps"`
}

func (s *Service) weeklyMinutes() int {
	if s.Config.WeeklyCloudMinutes == 0 {
		return 180
	}
	return s.Config.WeeklyCloudMinutes
}
func progressKey(user, lab string) string { return digest(user + "\x00" + lab) }
func (p LearningPublication) summary(progress LearningProgress) LearningLab {
	return LearningLab{ID: p.ID, Template: p.Template, Title: p.Title, Description: p.Description, Mode: p.Mode, EstimatedMinutes: p.EstimatedMinutes, StepCount: len(p.Steps), PublishedAt: p.PublishedAt, CompletedSteps: progress.CompletedSteps, Attempt: p.OwnerID != "", ScenarioID: p.ScenarioID}
}
func (s *Store) learningProgress(ctx context.Context, user, lab string) (LearningProgress, error) {
	p := LearningProgress{LabID: lab}
	err := s.Get(ctx, "learning-progress", progressKey(user, lab), &p)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (s *Store) publications(ctx context.Context, publishedOnly bool) ([]LearningPublication, error) {
	rows, err := s.List(ctx, "learning-publication")
	if err != nil {
		return nil, err
	}
	out := []LearningPublication{}
	for _, raw := range rows {
		var p LearningPublication
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if !publishedOnly || p.Published {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PublishedAt.After(out[j].PublishedAt) })
	return out, nil
}
func (s *Service) publication(ctx context.Context, id string) (LearningPublication, error) {
	var p LearningPublication
	err := s.Store.Get(ctx, "learning-publication", id, &p)
	if err != nil || !p.Published || s.studentTemplate(ctx, p.Template) != nil {
		return p, fmt.Errorf("This lab is not published.")
	}
	return p, nil
}
func (s *Service) learningCatalog(w http.ResponseWriter, r *http.Request, v Session) {
	labs, err := s.Store.learningCatalogLabs(r.Context(), v.User.ID)
	out := LearningCatalog{Labs: labs}
	if err == nil {
		out.Quota, err = s.Store.quota(r.Context(), v.User.ID, s.weeklyMinutes(), time.Now())
	}
	if err == nil {
		out.Run, err = s.Store.activePractice(r.Context(), v.User.ID)
	}
	if err != nil {
		writeError(w, 503, "Published labs are unavailable. Please retry.")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Service) learningView(w http.ResponseWriter, r *http.Request, v Session) {
	p, err := s.ownedPublication(r.Context(), r.PathValue("id"), v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	out := LearningView{Steps: []LearningStep{}, ReleasedSteps: p.ReleasedSteps, LocalAvailable: true}
	if p.ScenarioID != "" {
		var attempt templateAttempt
		if s.Store.Get(r.Context(), "template-attempt", p.ID, &attempt) != nil || attempt.UserID != v.User.ID || attempt.ID != p.ID || attempt.Template != p.Template || attempt.ScenarioID != p.ScenarioID || attempt.Scenario == nil {
			writeError(w, 503, "Your scenario intent is unavailable.")
			return
		}
		out.Scenario = attempt.Scenario
		out.LocalAvailable = false
	}
	out.Progress, err = s.Store.learningProgress(r.Context(), v.User.ID, p.ID)
	out.Lab = p.summary(out.Progress)
	for _, step := range p.Steps {
		out.Steps = append(out.Steps, LearningStep{step.ID, step.Title, step.Instructions, step.Kind, step.Prompt, strings.TrimSpace(step.Solution) != ""})
	}
	if err == nil {
		out.Quota, err = s.Store.quota(r.Context(), v.User.ID, s.weeklyMinutes(), time.Now())
	}
	if err == nil {
		out.Run, err = s.Store.activePractice(r.Context(), v.User.ID)
	}
	if err == nil {
		out.CloudReady = s.practiceTemplateReady(r.Context(), p.Template) == nil
	}
	var check learningCheckRecord
	e := s.Store.Get(r.Context(), "learning-check-current", progressKey(v.User.ID, p.ID), &check)
	if e == nil {
		out.Check = &check.LearningCheck
	} else if !errors.Is(e, sql.ErrNoRows) {
		err = e
	}
	if err != nil {
		writeError(w, 503, "Your lab progress is unavailable.")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Service) learningAdmin(w http.ResponseWriter, r *http.Request, v Session) {
	pubs, err := s.Store.publications(r.Context(), false)
	if err != nil {
		writeError(w, 503, "Publications are unavailable.")
		return
	}
	writeJSON(w, 200, LearningAdminView{pubs, s.weeklyMinutes()})
}

var stepIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

// Practice starts from built baseline images. Meeting-only breaks and instructor
// checkpoints cannot be represented as an independently runnable publication.
func resolvePracticeStages(ids []string, bundle InstructorBundle) ([]LearningStepInput, error) {
	if len(ids) < 1 || len(ids) > 20 {
		return nil, fmt.Errorf("Choose 1–20 stages from the pinned template revision.")
	}
	byID := map[string]TemplateStage{}
	for _, stage := range bundle.Stages {
		byID[stage.ID] = stage
	}
	out, seen := []LearningStepInput{}, map[string]bool{}
	for _, id := range ids {
		stage, ok := byID[id]
		if !ok || seen[id] {
			return nil, fmt.Errorf("Choose unique stages from this pinned template revision.")
		}
		if stage.Break != "" || stage.Kind() == "instructor" {
			return nil, fmt.Errorf("Stage %q requires a meeting exercise or instructor; choose stages that work on the personal lab baseline.", id)
		}
		seen[id] = true
		out = append(out, learningStage(stage))
	}
	return out, nil
}
func currentLearningStep(p LearningPublication, progress LearningProgress, id string) (LearningStepInput, error) {
	n := progress.CompletedSteps
	if n < 0 || n >= len(p.Steps) || p.Steps[n].ID != id {
		return LearningStepInput{}, fmt.Errorf("This is not your current stage. Reload your progress.")
	}
	if n >= p.ReleasedSteps {
		return LearningStepInput{}, fmt.Errorf("Your instructor has not released this stage yet.")
	}
	return p.Steps[n], nil
}

// An intentional reveal is scoped to this user's current or completed released
// stage. It never accepts an answer or records successful practice.
func (s *Service) solutionLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req LearningSolutionRequest
	if !decode(w, r, &req) {
		return
	}
	unlock := s.lockUser(v.User.ID)
	defer unlock()
	p, err := s.ownedPublication(r.Context(), r.PathValue("id"), v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	progress, err := s.Store.learningProgress(r.Context(), v.User.ID, p.ID)
	if err != nil {
		writeError(w, 503, "Progress is unavailable.")
		return
	}
	for i, stage := range p.Steps {
		if stage.ID == req.StepID && i <= progress.CompletedSteps && i < p.ReleasedSteps && strings.TrimSpace(stage.Solution) != "" {
			writeJSON(w, 200, LearningSolution{stage.ID, stage.Solution})
			return
		}
	}
	writeError(w, 409, "A solution is available only for your current or completed released stage.")
}
func (s *Service) answerLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req LearningAnswerRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Answer) > 1000 {
		writeError(w, 400, "Answers must be at most 1000 bytes.")
		return
	}
	unlock := s.lockUser(v.User.ID)
	defer unlock()
	p, err := s.ownedPublication(r.Context(), r.PathValue("id"), v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	progress, err := s.Store.learningProgress(r.Context(), v.User.ID, p.ID)
	if err != nil {
		writeError(w, 503, "Progress is unavailable.")
		return
	}
	step, err := currentLearningStep(p, progress, req.StepID)
	if err != nil || step.Kind != "answer" {
		writeError(w, 409, "Submit an answer for your current released answer stage.")
		return
	}
	progress.LastAnswer = req.Answer
	progress.LastFeedback = "That answer did not match. Review the lab and try again."
	rule := step.AnswerRule
	if rule == nil {
		rule = &instructor.Answer{Accepted: []string{step.Answer}, CaseSensitive: true}
	}
	if rule.Matches(req.Answer) {
		progress.CompletedSteps++
		progress.LastFeedback = "Answer accepted. Your progress is saved."
	}
	progress.Updated = time.Now().UTC()
	if err = s.Store.Put(r.Context(), "learning-progress", progressKey(v.User.ID, p.ID), progress); err != nil {
		writeError(w, 503, "Your answer could not be saved. Please retry.")
		return
	}
	writeJSON(w, 200, progress)
}

// Only the retained portable snapshot is downloadable. Instructor answers,
// playbooks, provider configuration and infrastructure credentials are excluded.
func (s *Service) downloadLearning(w http.ResponseWriter, r *http.Request, v Session) {
	p, err := s.ownedPublication(r.Context(), r.PathValue("id"), v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	if p.ScenarioID != "" {
		writeError(w, 409, "Randomized scenarios require hosted preparation. Choose baseline practice for a portable download.")
		return
	}
	var src lc.Source
	if s.Store.Get(r.Context(), "source", p.Template, &src) != nil {
		writeError(w, 503, "The pinned source is unavailable.")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="labcentral-`+p.ID+`.tar.gz"`)
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, f := range src.Files {
		if err = tw.WriteHeader(&tar.Header{Name: "lab/" + f.Path, Mode: 0600, Size: int64(len(f.Content)), Typeflag: tar.TypeReg}); err != nil {
			return
		}
		if _, err = tw.Write(f.Content); err != nil {
			return
		}
	}
	var instructions strings.Builder
	fmt.Fprintf(&instructions, "# %s\n\n%s\n\nPinned template revision: %s\n\n", p.Title, p.Description, p.Template)
	instructions.WriteString("Run these VMs through the standalone LabCentral CLI on a configured AWS or Proxmox environment. This is not a Docker or laptop hypervisor bundle. Keep your environment file and credentials outside this directory.\n\nValidate before provisioning:\n\n```sh\nlabcentral validate lab\nlabcentral plan --environment /path/to/your/environment.yaml lab\n```\n\nCloud quota applies only to this hosted service. Local resources are operator-owned and must be cleaned up through the CLI. Answer progress is submitted through LabCentral; action verification requires a hosted cloud run. Instructor-paced stages still require release in LabCentral.\n")
	for i, step := range p.Steps {
		fmt.Fprintf(&instructions, "\n## %d. %s\n\n%s\n\n%s\n", i+1, step.Title, step.Instructions, step.Prompt)
	}
	content := []byte(instructions.String())
	if tw.WriteHeader(&tar.Header{Name: "LAB-INSTRUCTIONS.md", Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}) == nil {
		_, _ = tw.Write(content)
	}
}

func learningStage(st TemplateStage) LearningStepInput {
	answer := ""
	if st.Completion.Answer != nil && len(st.Completion.Answer.Accepted) > 0 {
		answer = st.Completion.Answer.Accepted[0]
	}
	return LearningStepInput{ID: st.ID, Title: st.Title, Instructions: st.Instructions, Kind: st.Kind(), Prompt: st.Prompt, Answer: answer, CheckID: st.Completion.Check, Solution: st.Solution, AnswerRule: st.Completion.Answer}
}
