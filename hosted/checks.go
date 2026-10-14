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
	raw, err := s.Store.currentRuns(ctx, "check", teams)
	if err != nil {
		return nil, nil, err
	}
	for _, body := range raw {
		var run CheckRun
		if err = json.Unmarshal(body, &run); err != nil {
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
func (s *Service) setCheckScheduleDecision(w http.ResponseWriter, r *http.Request, v Session) {
	var req CheckScheduleRequest
	if !decode(w, r, &req) {
		return
	}
	if req.IntervalSeconds != 0 && (req.IntervalSeconds < 60 || req.IntervalSeconds > 3600) {
		writeError(w, 400, "Choose 60 to 3600 seconds, or zero to pause.")
		return
	}
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
func (s *Service) startChecksDecision(w http.ResponseWriter, r *http.Request, v Session) {
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

// Check intents, current pointers, schedule and jobs share one short immediate
// transaction. Batched statements avoid one query/three writes per team.
func (s *Service) queueChecks(ctx context.Context, m Meeting, schedule *CheckSchedule) error {
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, `WITH candidates AS MATERIALIZED (SELECT t.*,lower(hex(randomblob(12))) AS run_id FROM teams t WHERE meeting_id=? AND status='ready'
 AND NOT EXISTS(SELECT 1 FROM jobs j JOIN objects o ON o.kind IN ('check','exercise') AND o.id=j.target WHERE j.status IN ('queued','running') AND json_extract(o.body,'$.teamID')=t.id))
 INSERT INTO objects(kind,id,body)
 SELECT 'check',run_id,json_object('id',run_id,'meetingID',meeting_id,'teamID',id,'status','queued','started',?,'updated',?,'results',json('[]')) FROM candidates RETURNING id`, m.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(ids)
	_, err = tx.ExecContext(ctx, `INSERT INTO objects(kind,id,body) SELECT 'check-current',json_extract(body,'$.teamID'),json_quote(id) FROM objects WHERE kind='check' AND id IN (SELECT value FROM json_each(?)) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, raw)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) SELECT lower(hex(randomblob(12))),'check',value,'queued',? FROM json_each(?)`, now.Format(time.RFC3339Nano), raw)
	if err != nil {
		return err
	}
	if schedule != nil {
		schedule.Next = now.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
		raw, _ = json.Marshal(schedule)
		if _, err = tx.ExecContext(ctx, `UPDATE objects SET body=? WHERE kind='check-schedule' AND id=?`, raw, m.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.Store.signal()
	return nil
}
func (s *Service) scheduleChecks(ctx context.Context) {
	_ = s.Store.atomic(ctx, func(ctx context.Context) error {
		m, err := s.checkMeeting(ctx)
		if err != nil {
			return nil
		}
		var schedule CheckSchedule
		if s.Store.Get(ctx, "check-schedule", m.ID, &schedule) != nil || schedule.IntervalSeconds == 0 || schedule.Next.After(time.Now()) {
			return nil
		}
		return s.queueChecks(ctx, *m, &schedule)
	})
}
func (s *Service) runCheck(ctx context.Context, j Job) (err error) {
	ctx, closeGuest := guestJobContext(ctx)
	defer closeGuest()
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

func (s *Service) setCheckSchedule(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.setCheckScheduleDecision)(w, r, v)
}

func (s *Service) startChecks(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.startChecksDecision)(w, r, v)
}
