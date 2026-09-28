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
	"sync"
	"time"
)

// Run has one infrastructure worker. Authorization/expiry checks run independently.
func (s *Service) Run(ctx context.Context) error {
	unlock, e := s.Store.LockKey("hosted-worker")
	if e != nil {
		return e
	}
	defer unlock()
	if _, e = s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='queued' WHERE status='running'`); e != nil {
		return e
	}
	// Recovery remains a visible, retryable job if the provider is unavailable.
	if _, e = s.Store.Enqueue(ctx, "access-cleanup", "startup"); e != nil {
		return e
	}
	policyCtx, cancel := context.WithCancel(ctx)
	policyDone := make(chan struct{})
	go func() { defer close(policyDone); s.policyLoop(policyCtx) }()
	defer func() { cancel(); <-policyDone }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			chosen, e := s.Store.NextJob(ctx)
			if e != nil {
				return e
			}
			if chosen == nil {
				continue
			}
			if _, e = s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='running',error='' WHERE id=?`, chosen.ID); e != nil {
				return e
			}
			taskCtx, stop := context.WithTimeout(ctx, 2*time.Hour)
			s.jobMu.Lock()
			s.jobCancel, s.jobKind = stop, chosen.Kind
			s.jobMu.Unlock()
			e = s.runJob(taskCtx, *chosen)
			s.jobMu.Lock()
			s.jobCancel, s.jobKind = nil, ""
			s.jobMu.Unlock()
			stop()
			status, msg := "complete", ""
			if e != nil {
				status = "failed"
				if ctx.Err() != nil {
					status = "queued"
				}
				msg = e.Error()
			}
			if len(msg) > 2000 {
				msg = msg[:2000]
			}
			finalCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			_, save := s.Store.DB.ExecContext(finalCtx, `UPDATE jobs SET status=?,error=? WHERE id=?`, status, msg, chosen.ID)
			done()
			if save != nil {
				return save
			}
		}
	}
}

// stopProvisioning lets deadline cleanup preempt a long build or deployment.
func (s *Service) stopProvisioning() {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.jobCancel != nil && s.jobKind != "end" && s.jobKind != "access-cleanup" {
		s.jobCancel()
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
	for _, b := range rows {
		var record accessRecord
		if e = json.Unmarshal(b, &record); e != nil {
			return e
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
			return e
		}
	}
	return nil
}
func (s *Service) runJob(ctx context.Context, j Job) error {
	switch j.Kind {
	case "access-cleanup":
		return s.recoverAccess(ctx)
	case "exercise":
		return s.runExercise(ctx, j)
	case "refresh":
		return s.syncSource(ctx)
	case "build":
		plan, e := s.plan(ctx, j.Target)
		if e != nil {
			return e
		}
		if _, e = s.Engine.Build(ctx, plan); e != nil {
			return e
		}
		var t Template
		if e = s.Store.Get(ctx, "template", j.Target, &t); e != nil {
			return e
		}
		t.Built = true
		return s.Store.Put(ctx, "template", j.Target, t)
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
			unlock := s.lockUser(a.UserID)
			e = s.revokeUser(ctx, a.UserID)
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
