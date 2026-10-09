package hosted

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
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
	ID           string `json:"id"`
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Kind         string `json:"kind"`
	Prompt       string `json:"prompt"`
	Answer       string `json:"answer"`
	CheckID      string `json:"checkID"`
	Solution     string `json:"solution,omitempty"`
}
type PublishLearningRequest struct {
	Template         string              `json:"template"`
	Title            string              `json:"title"`
	Description      string              `json:"description"`
	Mode             string              `json:"mode"`
	EstimatedMinutes int                 `json:"estimatedMinutes"`
	Steps            []LearningStepInput `json:"steps"`
	StageIDs         []string            `json:"stageIDs,omitempty"`
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
	Lab            LearningLab      `json:"lab"`
	Steps          []LearningStep   `json:"steps"`
	Progress       LearningProgress `json:"progress"`
	Quota          CloudQuota       `json:"quota"`
	Run            *PracticeRun     `json:"run"`
	ReleasedSteps  int              `json:"releasedSteps"`
	CloudReady     bool             `json:"cloudReady"`
	LocalAvailable bool             `json:"localAvailable"`
	Check          *LearningCheck   `json:"check"`
}
type LearningAdminView struct {
	Labs         []LearningPublication `json:"labs"`
	QuotaMinutes int                   `json:"quotaMinutes"`
}
type ReleaseLearningRequest struct {
	ID            string `json:"id"`
	ReleasedSteps int    `json:"releasedSteps"`
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
	return LearningLab{p.ID, p.Template, p.Title, p.Description, p.Mode, p.EstimatedMinutes, len(p.Steps), p.PublishedAt, progress.CompletedSteps}
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
	if err != nil || !p.Published {
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
	p, err := s.publication(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	out := LearningView{Steps: []LearningStep{}, ReleasedSteps: p.ReleasedSteps, LocalAvailable: true}
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

func validateLearning(req PublishLearningRequest, b InstructorBundle) error {
	if !cleanName(req.Title) || len(req.Description) > 2000 || req.EstimatedMinutes < 1 || req.EstimatedMinutes > 1440 || len(req.Steps) < 1 || len(req.Steps) > 20 || (req.Mode != "self-paced" && req.Mode != "instructor") {
		return fmt.Errorf("Use a title, 1–1440 estimated minutes, a pacing mode and 1–20 stages.")
	}
	checks := map[string]bool{}
	for _, c := range b.Checks {
		checks[c.ID] = true
	}
	seen := map[string]bool{}
	for _, step := range req.Steps {
		if !stepIDPattern.MatchString(step.ID) || seen[step.ID] || !cleanName(step.Title) || len(step.Instructions) == 0 || len(step.Instructions) > 6000 || len(step.Prompt) > 1000 || len(step.Solution) > 6000 {
			return fmt.Errorf("Each stage needs a unique ID, title and bounded instructions.")
		}
		seen[step.ID] = true
		switch step.Kind {
		case "answer":
			if strings.TrimSpace(step.Answer) == "" || len(step.Answer) > 1000 || step.CheckID != "" {
				return fmt.Errorf("Answer stages require an accepted answer and no action check.")
			}
		case "action":
			if !checks[step.CheckID] || step.Answer != "" {
				return fmt.Errorf("Action stages require a check from the pinned revision and no accepted answer.")
			}
		case "instructor":
			if req.Mode != "instructor" || step.Answer != "" || step.CheckID != "" {
				return fmt.Errorf("Instructor checkpoints require instructor pacing and no answer or check.")
			}
		default:
			return fmt.Errorf("Stage kind must be answer, action or instructor.")
		}
	}
	return nil
}
func (s *Service) publishLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req PublishLearningRequest
	if !decodeLimit(w, r, &req, 256<<10) {
		return
	}
	if req.Mode == "" {
		req.Mode = "self-paced"
	}
	if req.EstimatedMinutes == 0 {
		req.EstimatedMinutes = 30
	}
	var template Template
	var src lc.Source
	var bundle InstructorBundle
	if s.Store.Get(r.Context(), "template", req.Template, &template) != nil || s.Store.Get(r.Context(), "source", req.Template, &src) != nil {
		writeError(w, 409, "Choose an imported template revision.")
		return
	}
	if _, err := lc.PlanLab(src, s.env, nil); err != nil {
		writeError(w, 409, "The pinned source cannot be planned in this environment.")
		return
	}
	err := s.Store.Get(r.Context(), "template-instructor", req.Template, &bundle)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 503, "Template checks are unavailable.")
		return
	}
	if len(req.StageIDs) > 0 {
		if len(req.Steps) > 0 || req.Mode != "self-paced" {
			writeError(w, 400, "Choose template stages for a self-paced publication without manual stages.")
			return
		}
		req.Steps, err = resolvePracticeStages(req.StageIDs, bundle)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	if err = validateLearning(req, bundle); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	released := len(req.Steps)
	if req.Mode == "instructor" {
		released = 1
	}
	p := LearningPublication{randomID(12), req.Template, req.Title, req.Description, req.Mode, req.EstimatedMinutes, req.Steps, true, time.Now().UTC(), released}
	if err = s.Store.Put(r.Context(), "learning-publication", p.ID, p); err != nil {
		writeError(w, 503, "Publication could not be saved.")
		return
	}
	writeJSON(w, 201, p)
}

// Practice starts from built baseline images. Meeting-only breaks and instructor
// checkpoints cannot be represented as an independently runnable publication.
func resolvePracticeStages(ids []string, bundle InstructorBundle) ([]LearningStepInput, error) {
	if len(ids) < 1 || len(ids) > 20 {
		return nil, fmt.Errorf("Choose 1–20 stages from the pinned template revision.")
	}
	if err := bundle.validateStages(); err != nil {
		return nil, err
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
		if stage.ExerciseID != "" || stage.Kind == "instructor" {
			return nil, fmt.Errorf("Stage %q requires a meeting exercise or instructor; choose stages that work on the personal lab baseline.", id)
		}
		seen[id] = true
		out = append(out, LearningStepInput{stage.ID, stage.Title, stage.Instructions, stage.Kind, stage.Prompt, stage.Answer, stage.CheckID, stage.Solution})
	}
	return out, nil
}
func (s *Service) releaseLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req ReleaseLearningRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.publication(r.Context(), req.ID)
	if err != nil || p.Mode != "instructor" || req.ReleasedSteps < p.ReleasedSteps || req.ReleasedSteps > len(p.Steps) {
		writeError(w, 409, "Release stages in order for a published instructor-paced lab.")
		return
	}
	p.ReleasedSteps = req.ReleasedSteps
	if err = s.Store.Put(r.Context(), "learning-publication", p.ID, p); err != nil {
		writeError(w, 503, "Stage release could not be saved.")
		return
	}
	writeJSON(w, 200, p)
}
func (s *Service) unpublishLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req TargetRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var p LearningPublication
	if s.Store.Get(r.Context(), "learning-publication", req.ID, &p) != nil {
		writeError(w, 404, "Publication is unavailable.")
		return
	}
	p.Published = false
	if err := s.Store.Put(r.Context(), "learning-publication", p.ID, p); err != nil {
		writeError(w, 503, "Publication could not be updated.")
		return
	}
	writeJSON(w, 200, p)
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
	p, err := s.publication(r.Context(), r.PathValue("id"))
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
	p, err := s.publication(r.Context(), r.PathValue("id"))
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
	got, want := digest(strings.TrimSpace(req.Answer)), digest(strings.TrimSpace(step.Answer))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
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
	p, err := s.publication(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, err.Error())
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
