package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
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
	case "practice-prepare", "practice-end":
		run, err := s.Store.practice(ctx, j.Target)
		if err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"practice:" + run.ID, true})
		if j.Kind == "practice-prepare" {
			var p LearningPublication
			if err = s.Store.Get(ctx, "learning-publication", run.LabID, &p); err != nil {
				return nil, err
			}
			claims = append(claims, jobClaim{"template:" + p.Template, false})
			keys, err := s.templateKeys(ctx, p.Template)
			if err != nil {
				return nil, err
			}
			for _, key := range keys {
				claims = append(claims, jobClaim{"image:" + key, false})
			}
		}
	case "learning-check":
		var check learningCheckRecord
		if err := s.Store.Get(ctx, "learning-check", j.Target, &check); err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"practice:" + check.RunID, false})
		run, err := s.Store.practice(ctx, check.RunID)
		if err != nil {
			return nil, err
		}
		p, err := s.ownedPublication(ctx, check.LabID, check.UserID)
		if err != nil {
			return nil, err
		}
		b, err := s.learningBundle(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, st := range p.Steps {
			if st.ID == check.StepID {
				for _, c := range b.Checks {
					if c.ID == st.CheckID {
						claims = appendMachineClaims(claims, run.InstanceID, c.Steps)
					}
				}
			}
		}
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
			claims = append(claims, jobClaim{"meeting:" + j.Target, false})
			teams, err := s.Store.Teams(ctx, m.ID)
			if err != nil {
				return nil, err
			}
			for _, team := range teams {
				if team.Status != "ready" {
					claims = append(claims, jobClaim{"team:" + team.ID, true})
				}
			}
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
		var r BreakRun
		if err := s.Store.Get(ctx, "exercise", j.Target, &r); err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"meeting:" + r.MeetingID, false}, jobClaim{"team:" + r.TeamID, true})
	case "check":
		var r CheckRun
		if err := s.Store.Get(ctx, "check", j.Target, &r); err != nil {
			return nil, err
		}
		claims = append(claims, jobClaim{"meeting:" + r.MeetingID, false}, jobClaim{"team:" + r.TeamID, false})
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return nil, err
		}
		if m != nil && m.ID == r.MeetingID {
			teams, err := s.Store.Teams(ctx, m.ID)
			if err != nil {
				return nil, err
			}
			var b InstructorBundle
			if err = s.Store.Get(ctx, "template-instructor", m.Template, &b); err != nil {
				return nil, err
			}
			for _, t := range teams {
				if t.ID == r.TeamID {
					for _, c := range b.Checks {
						claims = appendMachineClaims(claims, t.InstanceID, c.Steps)
					}
				}
			}
		}
	}
	return claims, nil
}

// A team's native operation and durable readiness finish before its claim is
// released. Other meeting teams may still be provisioning.
func (s *Service) releaseTeamClaim(jobID, teamID string) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	active, ok := s.activeJobs[jobID]
	if !ok || active.job.Kind != "prepare" {
		return
	}
	claims := active.claims[:0]
	for _, claim := range active.claims {
		if claim.key != "team:"+teamID {
			claims = append(claims, claim)
		}
	}
	active.claims = claims
	s.activeJobs[jobID] = active
}

// Separate admission budgets prevent long builds/preparation from consuming
// every guest-operation slot. Resource dependencies apply across all lanes.
func jobLane(kind string) string {
	switch kind {
	case "end", "access-cleanup", "practice-end":
		return "cleanup"
	case "check", "exercise", "learning-check":
		return "guest"
	default:
		return "bulk"
	}
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
	if err := s.Config.Worker.Validate(); err != nil {
		return err
	}
	limits := s.Config.Worker.Effective()
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		return err
	}
	queued := map[string]bool{}
	for _, j := range jobs {
		queued[j.ID] = true
	}
	s.jobMu.Lock()
	for id := range s.queuedClaims {
		if !queued[id] {
			delete(s.queuedClaims, id)
		}
	}
	s.jobMu.Unlock()
	for _, j := range jobs {
		claims, err := s.cachedJobClaims(ctx, j)
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
		bulk, guest, builds, blocked := 0, 0, 0, false
		for _, active := range s.activeJobs {
			switch jobLane(active.job.Kind) {
			case "bulk":
				bulk++
			case "guest":
				guest++
			}
			if active.job.Kind == "build" {
				builds++
			}
			if claimsConflict(claims, active.claims) {
				blocked = true
			}
			// End cancels its dependencies, then waits for their durable checkpoint.
			if (j.Kind == "end" || j.Kind == "practice-end") && claimsConflict(claims, active.claims) && jobLane(active.job.Kind) != "cleanup" {
				active.cancel()
			}
		}
		lane := jobLane(j.Kind)
		if blocked || len(s.activeJobs) >= limits.MaxBulkJobs+limits.MaxGuestJobs+1 || (lane == "bulk" && bulk >= limits.MaxBulkJobs) || (lane == "guest" && guest >= limits.MaxGuestJobs) || (j.Kind == "build" && builds >= limits.MaxImageBuildJobs) {
			s.jobMu.Unlock()
			continue
		}
		s.jobMu.Unlock()
		taskCtx, cancel := context.WithTimeout(ctx, time.Duration(limits.JobTimeoutMinutes)*time.Minute)
		result, err := s.Store.DB.ExecContext(ctx, `UPDATE jobs SET status='running',error='' WHERE id=? AND status='queued'`, j.ID)
		if err != nil {
			cancel()
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			cancel()
			return err
		}
		if count == 0 {
			cancel()
			continue
		}
		timing := jobTiming{Started: time.Now().UTC()}
		if err = s.Store.Put(ctx, "job-timing", j.ID, timing); err != nil {
			cancel()
			return err
		}
		j.Status = "running"
		s.jobMu.Lock()
		delete(s.queuedClaims, j.ID)
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
	metadata, err := s.jobMetadata(ctx, jobs)
	if err != nil {
		return err
	}
	for n := range jobs {
		j := &jobs[n]
		if j.Status == "failed" {
			var err error
			j.RetryBlocked, j.NeedsAttention, err = s.retryDisposition(ctx, *j)
			if err != nil {
				return err
			}
			j.Retryable = j.RetryBlocked == ""
		}
		info := metadata[j.ID]
		j.TargetLabel = info.label
		if !info.timing.Started.IsZero() {
			j.Started = &info.timing.Started
			j.Finished = info.timing.Finished
		}
		if j.Status != "queued" {
			continue
		}
		claims, err := s.cachedJobClaims(ctx, *j)
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

func appendMachineClaims(claims []jobClaim, instance string, steps []GuestPlaybook) []jobClaim {
	for _, st := range steps {
		key := "machine:" + instance + ":" + st.Machine
		found := false
		for _, c := range claims {
			if c.key == key {
				found = true
				break
			}
		}
		if !found {
			claims = append(claims, jobClaim{key, true})
		}
	}
	return claims
}
func (s *Service) cachedJobClaims(ctx context.Context, j Job) ([]jobClaim, error) {
	s.jobMu.Lock()
	claims, ok := s.queuedClaims[j.ID]
	s.jobMu.Unlock()
	if ok {
		return claims, nil
	}
	claims, err := s.jobClaims(ctx, j)
	if err != nil {
		return nil, err
	}
	s.jobMu.Lock()
	if s.queuedClaims == nil {
		s.queuedClaims = map[string][]jobClaim{}
	}
	s.queuedClaims[j.ID] = claims
	s.jobMu.Unlock()
	return claims, nil
}

type jobReadMetadata struct {
	label  string
	timing jobTiming
}

func (s *Service) jobMetadata(ctx context.Context, jobs []Job) (map[string]jobReadMetadata, error) {
	ids := make([]string, len(jobs))
	for n, j := range jobs {
		ids[n] = j.ID
	}
	raw, _ := json.Marshal(ids)
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT j.id,t.name,timing.body FROM jobs j LEFT JOIN objects run ON run.kind=j.kind AND run.id=j.target AND j.kind IN ('check','exercise') LEFT JOIN teams t ON t.id=json_extract(run.body,'$.teamID') LEFT JOIN objects timing ON timing.kind='job-timing' AND timing.id=j.id WHERE j.id IN (SELECT value FROM json_each(?))`, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]jobReadMetadata{}
	for rows.Next() {
		var id string
		var label sql.NullString
		var raw []byte
		if err = rows.Scan(&id, &label, &raw); err != nil {
			return nil, err
		}
		entry := jobReadMetadata{label: label.String}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &entry.timing); err != nil {
				return nil, err
			}
		}
		out[id] = entry
	}
	return out, rows.Err()
}
