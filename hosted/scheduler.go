package hosted

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

type jobClaim struct {
	key   string
	write bool
}
type runningJob struct {
	job    Job
	claims []jobClaim
	cancel context.CancelFunc
}
type jobTiming struct {
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}

func (s *Service) jobClaims(ctx context.Context, j Job) ([]jobClaim, error) {
	claims := []jobClaim{}
	switch j.Kind {
	case "refresh":
		claims = append(claims, jobClaim{"repository", true})
	case "access-cleanup":
		claims = append(claims, jobClaim{"desktops", true})
	case "build", "remove-images", "prepare":
		template := j.Target
		if j.Kind == "prepare" {
			m, err := s.Store.Meeting(ctx)
			if err != nil {
				return nil, err
			}
			if m == nil || m.ID != j.Target {
				return []jobClaim{{"meeting:" + j.Target, true}}, nil
			}
			template = m.Template
			claims = append(claims, jobClaim{"meeting:" + j.Target, true})
		}
		write := j.Kind != "prepare"
		claims = append(claims, jobClaim{"template:" + template, write})
		keys, err := s.templateKeys(ctx, template)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			claims = append(claims, jobClaim{"image:" + key, write})
		}
	case "end":
		claims = append(claims, jobClaim{"meeting:" + j.Target, true}, jobClaim{"desktops", true})
	case "exercise":
		var r ExerciseRun
		if err := s.Store.Get(ctx, "exercise", j.Target, &r); err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"meeting:" + r.MeetingID, false}, jobClaim{"team:" + r.TeamID, true})
	case "check":
		var r CheckRun
		if err := s.Store.Get(ctx, "check", j.Target, &r); err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"meeting:" + r.MeetingID, false}, jobClaim{"team:" + r.TeamID, true})
	}
	return claims, nil
}
func claimsConflict(a, b []jobClaim) bool {
	for _, x := range a {
		for _, y := range b {
			if x.key == y.key && (x.write || y.write) {
				return true
			}
		}
	}
	return false
}

func (s *Service) dispatchJobs(ctx context.Context, workers *sync.WaitGroup, done chan<- error) error {
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		claims, err := s.jobClaims(ctx, j)
		if err != nil {
			// Invalid/stale work fails visibly without blocking the rest of the queue.
			if _, save := s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='failed',error=? WHERE id=? AND status='queued'`, err.Error(), j.ID); save != nil {
				return save
			}
			continue
		}
		s.jobMu.Lock()
		if s.activeJobs == nil {
			s.activeJobs = map[string]runningJob{}
		}
		ordinary, builds, blocked := 0, 0, false
		for _, active := range s.activeJobs {
			if active.job.Kind != "end" && active.job.Kind != "access-cleanup" {
				ordinary++
			}
			if active.job.Kind == "build" {
				builds++
			}
			if claimsConflict(claims, active.claims) {
				blocked = true
			}
			// End cancels its dependencies, then waits for their durable checkpoint.
			if j.Kind == "end" && claimsConflict(claims, active.claims) && active.job.Kind != "access-cleanup" && active.job.Kind != "end" {
				active.cancel()
			}
		}
		priority := j.Kind == "end" || j.Kind == "access-cleanup"
		if blocked || len(s.activeJobs) >= 4 || (!priority && ordinary >= 3) || (j.Kind == "build" && builds >= 2) {
			s.jobMu.Unlock()
			continue
		}
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		result, err := s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='running',error='' WHERE id=? AND status='queued'`, j.ID)
		if err != nil {
			cancel()
			s.jobMu.Unlock()
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			cancel()
			s.jobMu.Unlock()
			return err
		}
		if count == 0 {
			cancel()
			s.jobMu.Unlock()
			continue
		}
		timing := jobTiming{Started: time.Now().UTC()}
		if err = s.Store.Put(ctx, "job-timing", j.ID, timing); err != nil {
			cancel()
			s.jobMu.Unlock()
			return err
		}
		j.Status = "running"
		s.activeJobs[j.ID] = runningJob{j, claims, cancel}
		s.jobMu.Unlock()
		workers.Go(func() {
			err := s.runJob(taskCtx, j)
			cancel()
			status, msg := "complete", ""
			if err != nil {
				status, msg = "failed", err.Error()
				if ctx.Err() != nil {
					status = "queued"
				}
			}
			if len(msg) > 2000 {
				msg = msg[:2000]
			}
			finalCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_, save := s.Store.DB.ExecContext(finalCtx, `UPDATE jobs SET status=?,error=? WHERE id=?`, status, msg, j.ID)
			now := time.Now().UTC()
			timing.Finished = &now
			save = errors.Join(save, s.Store.Put(finalCtx, "job-timing", j.ID, timing))
			stop()
			s.jobMu.Lock()
			delete(s.activeJobs, j.ID)
			s.jobMu.Unlock()
			select {
			case done <- save:
			case <-ctx.Done():
			}
		})
	}
	return nil
}

func (s *Service) jobDetails(ctx context.Context, jobs []Job) error {
	for n := range jobs {
		j := &jobs[n]
		var timing jobTiming
		if err := s.Store.Get(ctx, "job-timing", j.ID, &timing); err == nil {
			j.Started = &timing.Started
			j.Finished = timing.Finished
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if j.Status != "queued" {
			continue
		}
		claims, err := s.jobClaims(ctx, *j)
		if err != nil {
			continue
		}
		s.jobMu.Lock()
		for _, active := range s.activeJobs {
			if claimsConflict(claims, active.claims) {
				j.WaitingOn = append(j.WaitingOn, active.job.ID)
			}
		}
		s.jobMu.Unlock()
	}
	return nil
}
