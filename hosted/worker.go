package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Run fences one scheduler process, then admits jobs whose resource claims do
// not conflict. End/authorization cleanup has a reserved slot.
func (s *Service) Run(ctx context.Context) error {
	unlock, err := s.Store.LockKey("hosted-worker")
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='queued' WHERE status='running'`); err != nil {
		return err
	}
	if _, err = s.Store.Enqueue(ctx, "access-cleanup", "startup"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	workers.Go(func() { s.policyLoop(ctx) })
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	done := make(chan error, 6)
	for {
		if err := s.dispatchJobs(ctx, &workers, done); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if err != nil {
				return err
			}
		case <-tick.C:
		}
	}
}

func (s *Service) stopProvisioning() {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	for _, job := range s.activeJobs {
		if job.job.Kind == "prepare" || job.job.Kind == "exercise" || job.job.Kind == "check" {
			job.cancel()
		}
	}
}
func (s *Service) enforceDeadline(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.Store.Meeting(ctx)
	if e == nil && m != nil && m.Status != "ended" && (m.Status == "ending" || !m.Expires.After(time.Now())) {
		m.Status = "ending"
		if s.Store.Put(ctx, "meeting", "current", m) == nil {
			s.stopProvisioning()
			_, _ = s.Store.Enqueue(ctx, "end", m.ID)
		}
	}
}
func (s *Service) policyLoop(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		s.enforceDeadline(ctx)
		s.enforcePracticeDeadlines(ctx)
		s.scheduleLearningChecks(ctx)
		s.scheduleChecks(ctx)
		s.revalidateDesktops(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) recoverAccess(ctx context.Context) error {
	rows, e := s.Store.List(ctx, "desktop")
	if e != nil {
		return e
	}
	var failures []error
	for _, b := range rows {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		var record accessRecord
		if e = json.Unmarshal(b, &record); e != nil {
			failures = append(failures, e)
			continue
		}
		unlock := s.lockUser(record.UserID)
		s.mu.Lock()
		desktop := s.desktops[record.ID]
		active := desktop != nil && desktop.ctx.Err() == nil
		s.mu.Unlock()
		if active {
			unlock()
			continue
		}
		e = s.Engine.CloseAccess(ctx, record.ID)
		if e == nil {
			e = s.Store.Delete(ctx, "desktop", record.ID)
			if e == nil {
				s.mu.Lock()
				delete(s.desktops, record.ID)
				s.mu.Unlock()
			}
		}
		unlock()
		if e != nil {
			// Preserve uncertain ownership, but still recover other users. A
			// single unreachable guest must not strand the rest of this batch.
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) runJob(ctx context.Context, j Job) error {
	switch j.Kind {
	case "practice-prepare", "practice-end":
		return s.runPractice(ctx, j)
	case "learning-check":
		return s.runLearningCheck(ctx, j)
	case "access-cleanup":
		return s.recoverAccess(ctx)
	case "check":
		return s.runCheck(ctx, j)
	case "exercise":
		return s.runExercise(ctx, j)
	case "refresh":
		return s.syncSource(ctx)
	case "build", "remove-images":
		return s.runTemplateJob(ctx, j)
	case "prepare", "end":
		return s.meetingOperation(ctx, j)
	default:
		return fmt.Errorf("unknown job kind")
	}
}
func (s *Service) meetingOperation(ctx context.Context, j Job) error {
	m, e := s.Store.Meeting(ctx)
	if e != nil {
		return e
	}
	if m == nil || m.ID != j.Target {
		return fmt.Errorf("meeting identity mismatch")
	}
	teams, e := s.Store.Teams(ctx, m.ID)
	if e != nil {
		return e
	}
	if j.Kind == "end" {
		records, e := s.Store.List(ctx, "desktop")
		if e != nil {
			return e
		}
		var failures []error
		for _, b := range records {
			var a accessRecord
			if e = json.Unmarshal(b, &a); e != nil {
				failures = append(failures, e)
				continue
			}
			if strings.HasPrefix(a.TeamID, "practice:") {
				continue
			}
			unlock := s.lockUser(a.UserID)
			e = s.revokeMatchingDesktop(ctx, a.UserID, a.InstanceID)
			unlock()
			// Engine destruction retains access cleanup intent until VM absence.
		}
		// A failed team must not keep unrelated teams running and accruing charges.
		for _, t := range teams {
			_, e = s.Engine.Inspect(ctx, t.InstanceID)
			if errors.Is(e, sql.ErrNoRows) {
				e = nil
			} else if e == nil {
				e = s.Engine.Destroy(ctx, t.InstanceID)
			}
			if e != nil {
				failures = append(failures, fmt.Errorf("%s: %w", t.Name, e))
				continue
			}
			if e = os.Remove(filepath.Join(s.Config.Root, "exercise-keys", digest(t.InstanceID)+".pem")); e != nil && !os.IsNotExist(e) {
				failures = append(failures, fmt.Errorf("%s exercise credential cleanup: %w", t.Name, e))
				continue
			}
			if _, e = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ended' WHERE id=?`, t.ID); e != nil {
				failures = append(failures, e)
			}
		}
		if e = errors.Join(failures...); e != nil {
			return e
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		m.Status = "ended"
		return s.Store.Put(ctx, "meeting", "current", m)
	}
	if m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
		return fmt.Errorf("meeting has ended")
	}
	ctx, cancel := context.WithDeadline(ctx, m.Expires)
	defer cancel()
	plan, e := s.plan(ctx, m.Template)
	if e != nil {
		return e
	}
	// Independent failures do not cancel peers. Only meeting expiry, end or
	// worker shutdown cancels the shared context. Bound native API concurrency.
	const parallelTeams = 3
	failures := make([]error, len(teams))
	work := make(chan int)
	var workers sync.WaitGroup
	for n := 0; n < parallelTeams; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				if err := s.prepareTeam(ctx, m.ID, teams[index], plan); err != nil {
					failures[index] = fmt.Errorf("%s: %w", teams[index].Name, err)
				}
				s.releaseTeamClaim(j.ID, teams[index].ID)
			}
		}()
	}
	for index := range teams {
		work <- index
	}
	close(work)
	workers.Wait()
	failure := errors.Join(failures...)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, e := s.Store.Meeting(ctx)
	if e != nil {
		return errors.Join(failure, e)
	}
	if current != nil && current.ID == m.ID && current.Status != "ending" && current.Status != "ended" && current.Expires.After(time.Now()) {
		current.Status = "ready"
		if failure != nil {
			current.Status = "failed"
			states, err := s.Store.Teams(ctx, m.ID)
			if err != nil {
				return errors.Join(failure, err)
			}
			for _, team := range states {
				if team.Status == "ready" {
					current.Status = "partial"
					break
				}
			}
		}
		return errors.Join(failure, s.Store.Put(ctx, "meeting", "current", current))
	}
	return failure
}

// Ready teams remain accessible while failed teams reconcile their original IDs.
func (s *Service) prepareTeam(ctx context.Context, meeting string, t Team, plan lc.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.Store.Meeting(ctx)
	if err != nil {
		return err
	}
	if current == nil || current.ID != meeting || current.Status == "ending" || current.Status == "ended" || !current.Expires.After(time.Now()) {
		return fmt.Errorf("meeting is ending")
	}
	// Readiness is already durable. Retries own only unfinished teams and must
	// not inspect, recover or rewrite ready teams while their checks run.
	if t.Status == "ready" {
		return nil
	}
	inst, inspect := s.Engine.Inspect(ctx, t.InstanceID)
	if inspect == nil && inst.Status == "ready" {
		_, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready' WHERE id=?`, t.ID)
		return err
	}
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='preparing' WHERE id=?`, t.ID); err != nil {
		return err
	}
	if errors.Is(inspect, sql.ErrNoRows) {
		inst, err = s.Engine.DeployWithID(ctx, plan, t.InstanceID)
	} else if inspect != nil {
		err = inspect
	} else {
		inst, err = s.Engine.Recover(ctx, t.InstanceID)
	}
	if err == nil && inst.Status != "ready" {
		err = fmt.Errorf("team lab recovery returned status %q", inst.Status)
	}
	status := "ready"
	if err != nil {
		status = "failed"
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, save := s.Store.DB.ExecContext(saveCtx, `UPDATE teams SET status=? WHERE id=?`, status, t.ID)
	return errors.Join(err, save)
}
