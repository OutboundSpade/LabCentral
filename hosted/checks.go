package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"net/http"
	"time"
)

type CheckSchedule struct {
	MeetingID       string    `json:"meetingID"`
	IntervalSeconds int       `json:"intervalSeconds"`
	Next            time.Time `json:"next"`
}
type CheckScheduleRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}
type CheckResult struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Points int    `json:"points"`
	Passed bool   `json:"passed"`
	Report string `json:"report"`
}
type CheckRun struct {
	ID        string        `json:"id"`
	MeetingID string        `json:"meetingID"`
	TeamID    string        `json:"teamID"`
	Status    string        `json:"status"`
	Started   time.Time     `json:"started"`
	Updated   time.Time     `json:"updated"`
	Results   []CheckResult `json:"results"`
}

func (s *Service) checkView(ctx context.Context, m Meeting, teams []Team) (*CheckSchedule, []CheckRun, error) {
	var schedule CheckSchedule
	err := s.Store.Get(ctx, "check-schedule", m.ID, &schedule)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	var current *CheckSchedule
	if err == nil {
		current = &schedule
	}
	runs := []CheckRun{}
	for _, team := range teams {
		var id string
		err = s.Store.Get(ctx, "check-current", team.ID, &id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var run CheckRun
		if err = s.Store.Get(ctx, "check", id, &run); err != nil {
			return nil, nil, err
		}
		runs = append(runs, run)
	}
	return current, runs, nil
}
func (s *Service) checkMeeting(ctx context.Context) (*Meeting, error) {
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
		return nil, fmt.Errorf("No active meeting.")
	}
	if err = s.actionableRevision(ctx, m.Template); err != nil {
		return nil, err
	}
	var b InstructorBundle
	if err = s.Store.Get(ctx, "template-instructor", m.Template, &b); err != nil || len(b.Checks) == 0 {
		return nil, fmt.Errorf("Pinned template has no checks.")
	}
	return m, nil
}
func (s *Service) setCheckSchedule(w http.ResponseWriter, r *http.Request, v Session) {
	var req CheckScheduleRequest
	if !decode(w, r, &req) {
		return
	}
	if req.IntervalSeconds != 0 && (req.IntervalSeconds < 60 || req.IntervalSeconds > 3600) {
		writeError(w, 400, "Choose 60 to 3600 seconds, or zero to pause.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.checkMeeting(r.Context())
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	schedule := CheckSchedule{MeetingID: m.ID, IntervalSeconds: req.IntervalSeconds, Next: time.Now().UTC()}
	if err = s.Store.Put(r.Context(), "check-schedule", m.ID, schedule); err != nil {
		writeError(w, 503, "Could not persist check schedule.")
		return
	}
	writeJSON(w, 200, schedule)
}
func (s *Service) startChecks(w http.ResponseWriter, r *http.Request, v Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.checkMeeting(r.Context())
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err = s.queueChecks(r.Context(), *m, nil); err != nil {
		writeError(w, 503, "Could not queue checks.")
		return
	}
	writeJSON(w, 202, struct {
		OK bool `json:"ok"`
	}{true})
}

// Called with s.mu held; check intent, current result and next due time commit
// with jobs. Restart never catches up missed ticks or duplicates active checks.
func (s *Service) queueChecks(ctx context.Context, m Meeting, schedule *CheckSchedule) error {
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		return err
	}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, team := range teams {
		if team.Status != "ready" {
			continue
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j JOIN objects o ON o.kind IN ('check','exercise') AND o.id=j.target WHERE j.status IN ('queued','running') AND json_extract(o.body,'$.teamID')=?`, team.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		run := CheckRun{ID: randomID(12), MeetingID: m.ID, TeamID: team.ID, Status: "queued", Started: now, Updated: now, Results: []CheckResult{}}
		for _, obj := range []struct {
			kind, id string
			value    any
		}{{"check", run.ID, run}, {"check-current", team.ID, run.ID}} {
			b, _ := json.Marshal(obj.value)
			if _, err = tx.ExecContext(ctx, `INSERT INTO objects VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, obj.kind, obj.id, b); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) VALUES(?,'check',?,'queued',?)`, randomID(12), run.ID, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if schedule != nil {
		schedule.Next = now.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
		b, _ := json.Marshal(schedule)
		if _, err = tx.ExecContext(ctx, `UPDATE objects SET body=? WHERE kind='check-schedule' AND id=?`, b, m.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) scheduleChecks(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.checkMeeting(ctx)
	if err != nil {
		return
	}
	var schedule CheckSchedule
	if s.Store.Get(ctx, "check-schedule", m.ID, &schedule) != nil || schedule.IntervalSeconds == 0 || schedule.Next.After(time.Now()) {
		return
	}
	_ = s.queueChecks(ctx, *m, &schedule)
}
func (s *Service) runCheck(ctx context.Context, j Job) (err error) {
	var run CheckRun
	if err = s.Store.Get(ctx, "check", j.Target, &run); err != nil {
		return err
	}
	if run.Status == "complete" {
		return nil
	}
	defer func() {
		if err != nil {
			run.Status = "failed"
			run.Updated = time.Now().UTC()
			saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			err = errors.Join(err, s.Store.Put(saveCtx, "check", run.ID, run))
		}
	}()
	var current string
	if err = s.Store.Get(ctx, "check-current", run.TeamID, &current); err != nil {
		return err
	}
	if current != run.ID {
		return fmt.Errorf("Check was superseded.")
	}
	m, err := s.checkMeeting(ctx)
	if err != nil {
		return err
	}
	if m.ID != run.MeetingID {
		return fmt.Errorf("Meeting changed.")
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
		return fmt.Errorf("Team is not ready.")
	}
	var b InstructorBundle
	var src lc.Source
	if err = s.Store.Get(ctx, "template-instructor", m.Template, &b); err != nil {
		return err
	}
	if err = s.Store.Get(ctx, "source", m.Template, &src); err != nil {
		return err
	}
	ctx, stop := context.WithDeadline(ctx, m.Expires)
	defer stop()
	run.Status = "checking"
	run.Results = []CheckResult{}
	if err = s.Store.Put(ctx, "check", run.ID, run); err != nil {
		return err
	}
	for _, c := range b.Checks {
		checkCtx, cancel := context.WithTimeout(ctx, instructor.Deadline(c.Steps))
		failure := s.runGuestPlaybooks(checkCtx, team, src, b, c.Steps, c.Extensions)
		cancel()
		result := CheckResult{ID: c.ID, Name: c.Name, Points: c.Points, Passed: failure == nil, Status: "passed", Report: "Passed"}
		if failure != nil {
			result.Report = failure.Error()
			result.Status = "unavailable"
			if errors.Is(failure, errPlaybookFailed) {
				result.Status = "failed"
			}
		}
		run.Results = append(run.Results, result)
		run.Updated = time.Now().UTC()
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = s.Store.Put(ctx, "check", run.ID, run); err != nil {
			return err
		}
	}
	run.Status = "complete"
	run.Updated = time.Now().UTC()
	return s.Store.Put(ctx, "check", run.ID, run)
}
