package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"time"
)

func (s *Service) practiceTemplateReady(ctx context.Context, id string) error {
	if err := s.revisionPruneBlock(ctx, id); err != nil {
		return err
	}
	if err := s.actionableRevision(ctx, id); err != nil {
		return err
	}
	var t Template
	var src lc.Source
	if s.Store.Get(ctx, "template", id, &t) != nil || !t.Built || t.Removing {
		return fmt.Errorf("The pinned images must be built before cloud practice.")
	}
	if s.Store.Get(ctx, "source", id, &src) != nil {
		return fmt.Errorf("Pinned source unavailable.")
	}
	plan, err := lc.PlanLab(src, s.env, nil)
	if err != nil {
		return err
	}
	desktop, err := s.desktopMachine(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range plan.Machines {
		if m.Name == desktop && lc.Contains(m.Spec.Access, "desktop") {
			return nil
		}
	}
	return fmt.Errorf("Cloud practice requires the template desktop machine.")
}
func (s *Service) startPracticeDecision(w http.ResponseWriter, r *http.Request, v Session) {
	p, err := s.ownedPublication(r.Context(), r.PathValue("id"), v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	// Coordinate with image-removal eligibility and publication updates. No
	// infrastructure mutation occurs in this request.
	p, err = s.ownedPublication(r.Context(), p.ID, v.User.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	if err = s.practiceTemplateReady(r.Context(), p.Template); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if p.ScenarioID != "" {
		var ended int
		if err = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM practice_runs WHERE lab_id=? AND status='ended'`, p.ID).Scan(&ended); err != nil {
			writeError(w, 503, "Scenario history is unavailable.")
			return
		}
		if ended > 0 {
			writeError(w, 409, "Create a new scenario attempt to practice again. This attempt retains its progress.")
			return
		}
	}
	// A repeated start is idempotent and already owns its reservation.
	existing, err := s.Store.activePractice(r.Context(), v.User.ID)
	if err != nil {
		writeError(w, 503, "Capacity inventory is unavailable.")
		return
	}
	if existing == nil {
		size, e := s.revisionCapacity(r.Context(), p.Template, 1)
		if e == nil {
			e = s.checkCapacity(r.Context(), size)
		}
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
	}
	run, err := s.Store.reservePractice(r.Context(), v.User.ID, p.ID, s.weeklyMinutes(), time.Now().UTC())
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, run)
}
func (s *Service) stopPractice(w http.ResponseWriter, r *http.Request, v Session) {
	run, err := s.Store.activePractice(r.Context(), v.User.ID)
	if err != nil {
		writeError(w, 503, "Cloud allocation information is unavailable.")
		return
	}
	if run == nil || run.LabID != r.PathValue("id") {
		writeError(w, 409, "You have no active cloud run for this lab.")
		return
	}
	ended, err := s.Store.endPractice(r.Context(), run.ID, v.User.ID)
	if err != nil {
		writeError(w, 503, "Cleanup intent could not be saved. Please retry.")
		return
	}
	s.cancelPracticeJobs(run.ID)
	writeJSON(w, 202, ended)
}
func (s *Service) cancelPracticeJobs(id string) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	for _, job := range s.activeJobs {
		if job.job.Kind != "practice-end" {
			for _, c := range job.claims {
				if c.key == "practice:"+id {
					job.cancel()
					break
				}
			}
		}
	}
}
func (s *Service) enforcePracticeDeadlines(ctx context.Context) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT id,user_id,expires,status FROM practice_runs WHERE status!='ended'`)
	if err != nil {
		return
	}
	type dueRun struct{ id, user string }
	due := []dueRun{}
	for rows.Next() {
		var id, user, expiry, status string
		if rows.Scan(&id, &user, &expiry, &status) != nil {
			continue
		}
		t, e := time.Parse(time.RFC3339Nano, expiry)
		if e == nil && (status == "ending" || !t.After(time.Now())) {
			due = append(due, dueRun{id, user})
		}
	}
	rows.Close()
	for _, p := range due {
		if _, err = s.Store.endPractice(ctx, p.id, p.user); err == nil {
			s.cancelPracticeJobs(p.id)
		}
	}
}
func (s *Service) runPractice(ctx context.Context, j Job) (err error) {
	run, err := s.Store.practice(ctx, j.Target)
	if err != nil {
		return err
	}
	if run.Status == "ended" {
		return nil
	}
	if j.Kind == "practice-end" {
		return s.destroyPractice(ctx, run)
	}
	if run.Status == "ending" || !run.Expires.After(time.Now()) {
		_, err = s.Store.endPractice(context.WithoutCancel(ctx), run.ID, run.UserID)
		return err
	}
	if run.Status == "ready" {
		return nil
	}
	// Expiry must interrupt native preparation even when the policy loop is
	// waiting on identity checks. Cleanup retains this exact allocation identity.
	ctx, cancel := context.WithDeadline(ctx, run.Expires)
	defer cancel()
	defer func() {
		if err != nil {
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !run.Expires.After(time.Now()) {
				_, save := s.Store.endPractice(saveCtx, run.ID, run.UserID)
				err = errors.Join(err, save)
				return
			}
			_, save := s.Store.DB.ExecContext(saveCtx, `UPDATE practice_runs SET status='failed',error='Cloud preparation failed. Stop this run to clean up before trying again.' WHERE id=? AND status IN ('queued','preparing')`, run.ID)
			err = errors.Join(err, save)
		}
	}()
	var p LearningPublication
	var src lc.Source
	if err = s.Store.Get(ctx, "learning-publication", run.LabID, &p); err != nil {
		return err
	}
	if !p.Published {
		_, err = s.Store.endPractice(context.WithoutCancel(ctx), run.ID, run.UserID)
		return err
	}
	if p.OwnerID != "" && p.OwnerID != run.UserID {
		return fmt.Errorf("practice allocation does not own its attempt")
	}
	if err = s.practiceDisclosure(ctx, run.LabID); err != nil {
		return err
	}
	if err = s.practiceTemplateReady(ctx, p.Template); err != nil {
		return err
	}
	if err = s.Store.Get(ctx, "source", p.Template, &src); err != nil {
		return err
	}
	plan, err := lc.PlanLab(src, s.env, nil)
	if err != nil {
		return err
	}
	if p.OwnerID != "" {
		_, e := s.learningBundle(ctx, p)
		if e != nil {
			return e
		}
	}
	if p.ScenarioID != "" {
		if _, e := s.scenarioIntent(ctx, run, p, src); e != nil {
			return e
		}
	}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{}) }); err != nil {
		return err
	}
	ctx, prepareCancel := context.WithTimeout(ctx, s.prepareTimeout(plan))
	defer prepareCancel()
	release, err := s.acquireProvisioning(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := s.Store.DB.ExecContext(ctx, `UPDATE practice_runs SET status='preparing',error='' WHERE id=? AND status IN ('queued','failed','preparing')`, run.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	inst, e := s.Engine.Inspect(ctx, run.InstanceID)
	switch {
	case errors.Is(e, sql.ErrNoRows):
		inst, err = s.Engine.DeployWithID(ctx, plan, run.InstanceID)
	case e != nil:
		err = e
	case inst.Status != "ready":
		inst, err = s.Engine.Recover(ctx, run.InstanceID)
	}
	if err != nil {
		return err
	}
	if inst.Plan.Source.Digest != src.Digest || inst.ID != run.InstanceID || inst.Status != "ready" {
		return fmt.Errorf("practice allocation does not match its pinned source")
	}
	if err = s.prepareScenario(ctx, run, p, src); err != nil {
		return err
	}
	if !run.Expires.After(time.Now()) {
		_, err = s.Store.endPractice(context.WithoutCancel(ctx), run.ID, run.UserID)
		return err
	}
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE practice_runs SET status='ready',error='' WHERE id=? AND status='preparing'`, run.ID)
	return err
}
func (s *Service) destroyPractice(ctx context.Context, run practiceRecord) error {
	// Guest cleanup can be unreachable; native destruction must still proceed.
	// Transport cancellation occurs first and records survive until VM absence.
	_ = s.revokePractice(ctx, run)
	inst, err := s.Engine.Inspect(ctx, run.InstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err == nil && inst.Status != "destroyed" {
		err = s.Engine.Destroy(ctx, run.InstanceID)
	}
	if err != nil {
		return err
	}
	if err = s.forgetDestroyedDesktops(ctx, run.InstanceID); err != nil {
		return err
	}
	// Destroy's successful return is the library's native absence confirmation.
	// Unknown outcomes retain both identity and cleanup intent, and accrue quota.
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE practice_runs SET status='ended',ended=?,error='' WHERE id=? AND status='ending'`, time.Now().UTC().Format(time.RFC3339Nano), run.ID)
	return err
}
func (s *Service) readyPractice(ctx context.Context, user, lab string) (practiceRecord, error) {
	view, err := s.Store.activePractice(ctx, user)
	if err != nil {
		return practiceRecord{}, err
	}
	if view == nil || view.LabID != lab || view.Status != "ready" || !view.Expires.After(time.Now()) {
		return practiceRecord{}, fmt.Errorf("Your cloud lab is not ready or its time has ended.")
	}
	if err = s.practiceDisclosure(ctx, lab); err != nil {
		return practiceRecord{}, err
	}
	return s.Store.practice(ctx, view.ID)
}
func (s *Service) revokePractice(ctx context.Context, run practiceRecord) error {
	// Revoke only this allocation; ending a personal lab must not disconnect a
	// student's separate meeting desktop.
	return s.revokeMatchingDesktop(ctx, run.UserID, run.InstanceID)
}
func (s *Service) launchPractice(w http.ResponseWriter, r *http.Request, v Session) {
	run, err := s.readyPractice(r.Context(), v.User.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	p, err := s.ownedPublication(r.Context(), run.LabID, v.User.ID)
	if err != nil {
		writeError(w, 409, "Pinned template unavailable.")
		return
	}
	machine, err := s.desktopMachine(r.Context(), p.Template)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	s.launchDesktop(w, r, v, Team{ID: "practice:" + run.ID, InstanceID: run.InstanceID}, run.Expires, machine)
}

func (s *Service) startPractice(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.startPracticeDecision)(w, r, v)
}
