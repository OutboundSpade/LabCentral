package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"time"
)

type BreakDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Walkthrough string `json:"walkthrough"`
}
type BreakRequest struct {
	Action  string   `json:"action"`
	TeamIDs []string `json:"teamIDs"`
}
type BreakRun struct {
	ID          string    `json:"id"`
	MeetingID   string    `json:"meetingID"`
	TeamID      string    `json:"teamID"`
	Action      string    `json:"action"`
	Status      string    `json:"status"`
	Report      string    `json:"report"`
	RequestedBy string    `json:"requestedBy"`
	Started     time.Time `json:"started"`
	Updated     time.Time `json:"updated"`
}

func (s *Service) exerciseRuns(ctx context.Context, teams []Team) ([]BreakRun, error) {
	out := []BreakRun{}
	for _, t := range teams {
		var id string
		err := s.Store.Get(ctx, "exercise-current", t.ID, &id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var run BreakRun
		if err = s.Store.Get(ctx, "exercise", id, &run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}
func (s *Service) startExercises(w http.ResponseWriter, r *http.Request, v Session) {
	var req BreakRequest
	if !decode(w, r, &req) {
		return
	}
	runs, err := s.queueExercises(r.Context(), req, v.User.ID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, runs)
}

// Selection, per-team exclusion, intent, and job creation commit together.
func (s *Service) queueExercises(ctx context.Context, req BreakRequest, actor string) ([]BreakRun, error) {
	if len(req.TeamIDs) < 1 || len(req.TeamIDs) > 5 {
		return nil, fmt.Errorf("Choose an exercise and one to five ready teams.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
		return nil, fmt.Errorf("The meeting is not active.")
	}
	var src lc.Source
	if err = s.Store.Get(ctx, "source", m.Template, &src); err != nil {
		return nil, fmt.Errorf("The pinned source is unavailable.")
	}
	if err = s.validateExercise(ctx, m.Template, req.Action, src); err != nil {
		return nil, err
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	allowed := map[string]Team{}
	for _, team := range teams {
		if team.Status == "ready" {
			allowed[team.ID] = team
		}
	}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	runs := []BreakRun{}
	seen := map[string]bool{}
	for _, id := range req.TeamIDs {
		if _, ok := allowed[id]; !ok || seen[id] {
			return nil, fmt.Errorf("Select each ready team once.")
		}
		seen[id] = true
		if req.Action != "reset" {
			var uncertain int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM objects current JOIN objects run ON run.kind='exercise' AND run.id=json_extract(current.body,'$') WHERE current.kind='exercise-current' AND current.id=? AND json_extract(run.body,'$.status')='unknown'`, id).Scan(&uncertain); err != nil {
				return nil, err
			}
			if uncertain > 0 {
				return nil, fmt.Errorf("The previous guest mutation has an unknown outcome; run an explicit reset first.")
			}
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE kind='exercise' AND json_extract(body,'$.teamID')=? AND json_extract(body,'$.status') IN ('queued','resetting','applying','checking')`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, fmt.Errorf("%s already has an exercise operation in progress.", allowed[id].Name)
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j JOIN objects o ON o.kind='exercise' AND o.id=j.target WHERE j.kind='exercise' AND j.status IN ('queued','running') AND json_extract(o.body,'$.teamID')=?`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, fmt.Errorf("%s has an exercise recovery queued.", allowed[id].Name)
		}
		now := time.Now().UTC()
		run := BreakRun{ID: randomID(12), MeetingID: m.ID, TeamID: id, Action: req.Action, Status: "queued", RequestedBy: actor, Started: now, Updated: now}
		b, _ := json.Marshal(run)
		if _, err = tx.ExecContext(ctx, `INSERT INTO objects VALUES('exercise',?,?)`, run.ID, b); err != nil {
			return nil, err
		}
		b, _ = json.Marshal(run.ID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO objects VALUES('exercise-current',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, id, b); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) VALUES(?,'exercise',?,'queued',?)`, randomID(12), run.ID, now.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, tx.Commit()
}
func (s *Service) runExercise(ctx context.Context, j Job) (err error) {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	var run BreakRun
	if err = s.Store.Get(ctx, "exercise", j.Target, &run); err != nil {
		return err
	}
	if run.Status == "complete" {
		return nil
	}
	if run.Status == "unknown" {
		return fmt.Errorf("Guest mutation outcome is unknown; confirm recovery through an explicit reset.")
	}
	// Every replay validates the current assignment. Old failures cannot re-break a reset lab.
	var current string
	if err = s.Store.Get(ctx, "exercise-current", run.TeamID, &current); err != nil {
		return err
	}
	if current != run.ID {
		return fmt.Errorf("Exercise superseded by a newer operation.")
	}
	defer func() {
		if err != nil {
			run.Status = "failed"
			if errors.Is(err, errGuestUnknown) {
				run.Status = "unknown"
			}
			run.Report = err.Error()
			run.Updated = time.Now().UTC()
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = errors.Join(err, s.Store.Put(saveCtx, "exercise", run.ID, run))
		}
	}()
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		return err
	}
	if m == nil || m.ID != run.MeetingID || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
		return fmt.Errorf("Meeting has ended; no exercise was applied.")
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		return err
	}
	var team Team
	for _, t := range teams {
		if t.ID == run.TeamID {
			team = t
		}
	}
	if team.Status != "ready" {
		return fmt.Errorf("Team lab is not ready.")
	}
	var src lc.Source
	if err = s.Store.Get(ctx, "source", m.Template, &src); err != nil {
		return err
	}
	if err = s.validateExercise(ctx, m.Template, run.Action, src); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, m.Expires)
	defer cancel()

	run.Status = "resetting"
	if run.Action == "check" {
		run.Status = "checking"
	}
	run.Updated = time.Now().UTC()
	if err = s.Store.Put(ctx, "exercise", run.ID, run); err != nil {
		return err
	}
	s.mu.Unlock()
	locked = false
	runner := s.exerciseRunner

	var bundle InstructorBundle
	var report string
	if runner != nil {
		report, err = runner(ctx, team, run, src)
	} else if lookup := s.Store.Get(ctx, "template-instructor", m.Template, &bundle); lookup == nil {
		steps := bundle.Reset
		if run.Action == "check" {
			return fmt.Errorf("Use the template scoreboard checks for this revision.")
		}
		if err = s.runGuestPlaybooks(ctx, team, src, bundle, steps); err == nil && run.Action != "reset" {
			for _, x := range bundle.Breaks {
				if x.ID == run.Action {
					err = s.runGuestPlaybooks(ctx, team, src, bundle, x.Steps, x.Extensions)
					break
				}
			}
		}
		report = "Template exercise complete; pinned guest playbooks passed."
	} else {
		err = lookup
	}
	if err != nil {
		return err
	}
	run.Status = "complete"
	run.Report = report
	run.Updated = time.Now().UTC()
	return s.Store.Put(ctx, "exercise", run.ID, run)
}

func (s *Service) validateExercise(ctx context.Context, template, action string, src lc.Source) error {
	if err := s.actionableRevision(ctx, template); err != nil {
		return err
	}
	var bundle InstructorBundle
	err := s.Store.Get(ctx, "template-instructor", template, &bundle)

	if err != nil {
		return err
	}
	if action == "reset" && len(bundle.Reset) > 0 {
		return nil
	}
	for _, x := range bundle.Breaks {
		if x.ID == action {
			return nil
		}
	}
	return fmt.Errorf("Choose an exercise from the pinned template.")
}
