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

func (s *Service) checkLearning(w http.ResponseWriter, r *http.Request, v Session) {
	var req LearningCheckRequest
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
	if err = s.actionableRevision(r.Context(), p.Template); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	progress, err := s.Store.learningProgress(r.Context(), v.User.ID, p.ID)
	if err != nil {
		writeError(w, 503, "Progress is unavailable.")
		return
	}
	step, err := currentLearningStep(p, progress, req.StepID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if step.Kind == "instructor" {
		progress.CompletedSteps++
		progress.LastAnswer = ""
		progress.LastFeedback = "Instructor checkpoint completed. Your progress is saved."
		progress.Updated = time.Now().UTC()
		if err = s.Store.Put(r.Context(), "learning-progress", progressKey(v.User.ID, p.ID), progress); err != nil {
			writeError(w, 503, "Progress could not be saved.")
			return
		}
		writeJSON(w, 200, LearningCheck{ID: randomID(12), LabID: p.ID, StepID: step.ID, Status: "passed", Report: progress.LastFeedback, Updated: progress.Updated})
		return
	}
	if step.Kind != "action" {
		writeError(w, 409, "This stage requires an answer.")
		return
	}
	run, err := s.readyPractice(r.Context(), v.User.ID, p.ID)
	if err != nil {
		writeError(w, 409, "Action verification requires your ready cloud lab.")
		return
	}
	check, err := s.queueLearningCheck(r.Context(), v.User.ID, p.ID, step.ID, run.ID, progress.CompletedSteps)
	if err != nil {
		writeError(w, 503, "Action verification could not be queued.")
		return
	}
	writeJSON(w, 202, check)
}

// Active action stages are observed in guests without requiring a student to
// press a button. A failing observation preserves the stage; only a confirmed
// pass commits advancement. No missed observations are replayed after restart.
func (s *Service) scheduleLearningChecks(ctx context.Context) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT id,user_id,lab_id FROM practice_runs WHERE status='ready' AND expires>?`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return
	}
	type candidate struct{ run, user, lab string }
	list := []candidate{}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.run, &c.user, &c.lab) == nil {
			list = append(list, c)
		}
	}
	rows.Close()
	for _, c := range list {
		p, err := s.ownedPublication(ctx, c.lab, c.user)
		if err != nil {
			continue
		}
		if s.actionableRevision(ctx, p.Template) != nil {
			continue
		}
		progress, err := s.Store.learningProgress(ctx, c.user, c.lab)
		if err != nil {
			continue
		}
		if progress.CompletedSteps >= len(p.Steps) || progress.CompletedSteps >= p.ReleasedSteps {
			continue
		}
		step := p.Steps[progress.CompletedSteps]
		if step.Kind != "action" {
			continue
		}
		var previous learningCheckRecord
		if s.Store.Get(ctx, "learning-check-current", progressKey(c.user, c.lab), &previous) == nil && previous.StepID == step.ID && time.Since(previous.Updated) < 20*time.Second {
			continue
		}
		_, _ = s.queueLearningCheck(ctx, c.user, c.lab, step.ID, c.run, progress.CompletedSteps)
	}
}
func (s *Service) queueLearningCheck(ctx context.Context, user, lab, step, run string, completed int) (LearningCheck, error) {
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return LearningCheck{}, err
	}
	defer tx.Rollback()
	var body []byte
	err = tx.QueryRowContext(ctx, `SELECT o.body FROM jobs j JOIN objects o ON o.kind='learning-check' AND o.id=j.target WHERE j.kind='learning-check' AND j.status IN ('queued','running') AND json_extract(o.body,'$.userID')=? AND json_extract(o.body,'$.labID')=?`, user, lab).Scan(&body)
	if err == nil {
		var existing learningCheckRecord
		if err = json.Unmarshal(body, &existing); err != nil {
			return LearningCheck{}, err
		}
		return existing.LearningCheck, nil
	}
	if err != sql.ErrNoRows {
		return LearningCheck{}, err
	}
	// The stop request can race a check request; admission and ownership are
	// rechecked in this transaction and again by the worker before guest access.
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM practice_runs WHERE id=? AND user_id=? AND lab_id=? AND status='ready' AND expires>?`, run, user, lab, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&count); err != nil {
		return LearningCheck{}, err
	}
	if count != 1 {
		return LearningCheck{}, fmt.Errorf("cloud run has ended")
	}
	check := learningCheckRecord{LearningCheck: LearningCheck{ID: randomID(12), LabID: lab, StepID: step, Status: "queued", Report: "Verification queued.", Updated: time.Now().UTC()}, UserID: user, RunID: run, CompletedSteps: completed}
	body, _ = json.Marshal(check)
	for _, record := range []struct{ kind, id string }{{"learning-check", check.ID}, {"learning-check-current", progressKey(user, lab)}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO objects(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, record.kind, record.id, body); err != nil {
			return check.LearningCheck, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) VALUES(?,'learning-check',?,'queued',?)`, randomID(12), check.ID, check.Updated.Format(time.RFC3339Nano))
	if err != nil {
		return check.LearningCheck, err
	}
	return check.LearningCheck, tx.Commit()
}
func (s *Service) saveLearningCheck(ctx context.Context, check learningCheckRecord, advance bool) error {
	unlock := s.lockUser(check.UserID)
	defer unlock()
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var current learningCheckRecord
	if err = tx.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind='learning-check-current' AND id=?`, progressKey(check.UserID, check.LabID)).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		return err
	}
	if current.ID != check.ID {
		return fmt.Errorf("verification was superseded")
	}
	if advance {
		var status, expiry string
		if err = tx.QueryRowContext(ctx, `SELECT status,expires FROM practice_runs WHERE id=? AND user_id=?`, check.RunID, check.UserID).Scan(&status, &expiry); err != nil {
			return err
		}
		end, e := time.Parse(time.RFC3339Nano, expiry)
		if e != nil || status != "ready" || !end.After(time.Now()) {
			check.Status = "unavailable"
			check.Report = "Cloud time ended before verification was saved."
			advance = false
		}
	}
	if advance {
		progress := LearningProgress{LabID: check.LabID}
		err = tx.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind='learning-progress' AND id=?`, progressKey(check.UserID, check.LabID)).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if err = json.Unmarshal(raw, &progress); err != nil {
				return err
			}
		}
		if progress.CompletedSteps != check.CompletedSteps {
			return fmt.Errorf("stage changed during verification")
		}
		progress.CompletedSteps++
		progress.Updated = check.Updated
		progress.LastAnswer = ""
		progress.LastFeedback = "Action verified in your cloud lab. Your progress is saved."
		raw, _ = json.Marshal(progress)
		if _, err = tx.ExecContext(ctx, `INSERT INTO objects(kind,id,body) VALUES('learning-progress',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, progressKey(check.UserID, check.LabID), raw); err != nil {
			return err
		}
	}
	raw, _ = json.Marshal(check)
	for _, record := range []struct{ kind, id string }{{"learning-check", check.ID}, {"learning-check-current", progressKey(check.UserID, check.LabID)}} {
		if _, err = tx.ExecContext(ctx, `UPDATE objects SET body=? WHERE kind=? AND id=?`, raw, record.kind, record.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) runLearningCheck(ctx context.Context, j Job) (err error) {
	var check learningCheckRecord
	if err = s.Store.Get(ctx, "learning-check", j.Target, &check); err != nil {
		return err
	}
	if check.Status == "passed" || check.Status == "failed" || check.Status == "unavailable" {
		return nil
	}
	defer func() {
		if err != nil {
			check.Status = "unavailable"
			check.Report = "Guest verification is unavailable. Retry when your cloud lab is ready."
			check.Updated = time.Now().UTC()
			save, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = errors.Join(err, s.saveLearningCheck(save, check, false))
		}
	}()
	run, err := s.readyPractice(ctx, check.UserID, check.LabID)
	if err != nil || run.ID != check.RunID {
		return fmt.Errorf("verification allocation is no longer ready")
	}
	p, err := s.ownedPublication(ctx, check.LabID, check.UserID)
	if err != nil {
		return err
	}
	if err = s.actionableRevision(ctx, p.Template); err != nil {
		return err
	}
	progress, err := s.Store.learningProgress(ctx, check.UserID, check.LabID)
	if err != nil {
		return err
	}
	step, err := currentLearningStep(p, progress, check.StepID)
	if err != nil || step.Kind != "action" || progress.CompletedSteps != check.CompletedSteps {
		return fmt.Errorf("verification stage is no longer current")
	}
	var src lc.Source
	var bundle InstructorBundle
	if err = s.Store.Get(ctx, "source", p.Template, &src); err != nil {
		return err
	}
	if bundle, err = s.learningBundle(ctx, p); err != nil {
		return err
	}
	var steps []GuestPlaybook
	var extensions lc.Extensions
	for _, c := range bundle.Checks {
		if c.ID == step.CheckID {
			steps = c.Steps
			extensions = c.Extensions
			break
		}
	}
	if len(steps) == 0 {
		return fmt.Errorf("pinned action check is unavailable")
	}
	check.Status = "running"
	check.Report = "Verifying the action in your cloud lab."
	check.Updated = time.Now().UTC()
	if err = s.saveLearningCheck(ctx, check, false); err != nil {
		return err
	}
	err = s.runGuestPlaybooks(ctx, Team{InstanceID: run.InstanceID}, src, bundle, steps, extensions)
	check.Updated = time.Now().UTC()
	if errors.Is(err, errPlaybookFailed) {
		check.Status = "failed"
		check.Report = "The action check has not passed. Review the instructions and try again."
		return s.saveLearningCheck(ctx, check, false)
	}
	if err != nil {
		return err
	}
	check.Status = "passed"
	check.Report = "Action verified. Your progress is saved."
	return s.saveLearningCheck(ctx, check, true)
}
