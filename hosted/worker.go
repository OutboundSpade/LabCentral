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
	if err := s.Config.Worker.Validate(); err != nil {
		return err
	}
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
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	limits := s.Config.Worker.Effective()
	done := make(chan error, limits.MaxBulkJobs+limits.MaxGuestJobs+1)
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
		case <-s.Store.wake:
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
	_ = s.Store.atomic(ctx, func(ctx context.Context) error {
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return err
		}
		if m == nil || m.Status == "ended" || (m.Status != "ending" && m.Expires.After(time.Now())) {
			return nil
		}
		m.Status = "ending"
		if err = s.Store.Put(ctx, "meeting", "current", m); err != nil {
			return err
		}
		s.stopProvisioning()
		_, err = s.Store.Enqueue(ctx, "end", m.ID)
		return err
	})
}
func (s *Service) policyLoop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var desktopChecked time.Time
	for {
		s.enforceDeadline(ctx)
		s.enforcePracticeDeadlines(ctx)
		s.scheduleLearningChecks(ctx)
		s.scheduleChecks(ctx)
		if time.Since(desktopChecked) >= 20*time.Second {
			s.revalidateDesktops(ctx)
			desktopChecked = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) recoverAccess(ctx context.Context) error {
	records, err := s.Store.List(ctx, "desktop")
	if err != nil {
		return err
	}
	return parallelResources(len(records), 8, func(n int) error {
		var record accessRecord
		if err := json.Unmarshal(records[n], &record); err != nil {
			return err
		}
		err := s.cleanupDesktop(ctx, record.ID, true)
		if err == errDesktopBusy {
			// The record and closing intent remain durable. The policy loop
			// checks inactive desktops every 20 seconds and queues another sweep;
			// do not immediately enqueue here and spin on an in-flight operation.
			return nil
		}
		return err
	})
}
func (s *Service) runJob(ctx context.Context, j Job) error {
	ctx, closeGuest := guestJobContext(ctx)
	defer closeGuest()
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
	case "prune-images":
		return s.runPruneImages(ctx, j)
	case "build", "remove-images":
		return s.runTemplateJob(ctx, j)
	case "prepare", "end":
		return s.meetingOperation(ctx, j)
	default:
		return fmt.Errorf("unknown job kind")
	}
}
func (s *Service) meetingOperation(ctx context.Context, j Job) error {
	if err := s.Config.Worker.Validate(); err != nil {
		return err
	}
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
		// Intent precedes each team's native effects and survives uncertain outcomes.
		_, err := s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ending' WHERE meeting_id=? AND status!='ended'`, m.ID)
		if err != nil {
			return err
		}
		records, err := s.Store.List(ctx, "desktop")
		if err != nil {
			return err
		}
		accessErr := parallelResources(len(records), 8, func(n int) error {
			var record accessRecord
			if err := json.Unmarshal(records[n], &record); err != nil {
				return err
			}
			if strings.HasPrefix(record.TeamID, "practice:") {
				return nil
			}
			// Unreachable guest access does not block confirmed native destruction.
			_ = s.cleanupDesktop(ctx, record.ID, false)
			return nil
		})
		destroyErr := parallelResources(len(teams), 8, func(n int) error {
			t := teams[n]
			if t.Status == "ended" {
				return nil
			}
			_, err := s.Engine.Inspect(ctx, t.InstanceID)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else if err == nil {
				err = s.Engine.Destroy(ctx, t.InstanceID)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			if err = s.forgetDestroyedDesktops(ctx, t.InstanceID); err != nil {
				return err
			}
			if err = os.Remove(filepath.Join(s.Config.Root, "exercise-keys", digest(t.InstanceID)+".pem")); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("%s exercise credential cleanup: %w", t.Name, err)
			}
			_, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ended' WHERE id=?`, t.ID)
			return err
		})
		if err = errors.Join(accessErr, destroyErr); err != nil {
			return err
		}
		_, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.status','ended') WHERE kind='meeting' AND id='current' AND json_extract(body,'$.id')=?`, m.ID)
		return err
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
	if e = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{}) }); e != nil {
		return e
	}
	// Independent failures do not cancel peers. Only meeting expiry, end or
	// worker shutdown cancels the shared context. Bound native API concurrency.
	parallelTeams := s.Config.Worker.Effective().MaxProvisioningConcurrency
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
	status := "ready"
	if failure != nil {
		status = "failed"
		states, err := s.Store.Teams(ctx, m.ID)
		if err != nil {
			return errors.Join(failure, err)
		}
		for _, team := range states {
			if team.Status == "ready" {
				status = "partial"
				break
			}
		}
	}
	_, e = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.status',?) WHERE kind='meeting' AND id='current' AND json_extract(body,'$.id')=? AND json_extract(body,'$.status') NOT IN ('ending','ended') AND json_extract(body,'$.expires')>?`, status, m.ID, time.Now().UTC().Format(time.RFC3339Nano))
	return errors.Join(failure, e)
}

// Ready teams remain accessible while failed teams reconcile their original IDs.
func (s *Service) prepareTeam(ctx context.Context, meeting string, t Team, plan lc.Plan) error {
	ctx, cancel := context.WithTimeout(ctx, s.prepareTimeout(plan))
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Readiness is already durable. Retries own only unfinished teams and must
	// not inspect, recover or rewrite ready teams while their checks run.
	if t.Status == "ready" {
		return nil
	}
	release, err := s.acquireProvisioning(ctx)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.Store.Meeting(ctx)
	if err != nil {
		return err
	}
	if current == nil || current.ID != meeting || current.Status == "ending" || current.Status == "ended" || !current.Expires.After(time.Now()) {
		return fmt.Errorf("meeting is ending")
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

// A fixed-size pool visits every resource, preserves successes and joins failures.
func parallelResources(count, limit int, fn func(int) error) error {
	failures := make([]error, count)
	work := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < min(count, limit); n++ {
		wg.Go(func() {
			for index := range work {
				failures[index] = fn(index)
			}
		})
	}
	for n := range count {
		work <- n
	}
	close(work)
	wg.Wait()
	return errors.Join(failures...)
}
