package hosted

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func failedCheck(t *testing.T, s *Service, team Team) Job {
	t.Helper()
	ctx := context.Background()
	run := CheckRun{ID: randomID(12), MeetingID: team.MeetingID, TeamID: team.ID, Status: "failed"}
	for _, obj := range []struct {
		kind, id string
		value    any
	}{{"check", run.ID, run}, {"check-current", team.ID, run.ID}} {
		if err := s.Store.Put(ctx, obj.kind, obj.id, obj.value); err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.Store.Enqueue(ctx, "check", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE jobs SET status='failed',error='context canceled' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	j.Status = "failed"
	return j
}

func TestRecoveryEligibilityMatchesAPIAndRetainsHistory(t *testing.T) {
	for _, reason := range []string{"ended", "superseded", "current"} {
		t.Run(reason, func(t *testing.T) {
			s, teams := checkFixture(t)
			ctx := context.Background()
			job := failedCheck(t, s, teams[0])
			switch reason {
			case "ended":
				m, _ := s.Store.Meeting(ctx)
				m.Status = "ended"
				if err := s.Store.Put(ctx, "meeting", "current", m); err != nil {
					t.Fatal(err)
				}
			case "superseded":
				if err := s.Store.Put(ctx, "check-current", teams[0].ID, "newer"); err != nil {
					t.Fatal(err)
				}
			}
			// Eligibility is derived from durable records after reopening, not memory.
			reopened, err := OpenStore(s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			s.Store = reopened
			jobs := []Job{job}
			if err = s.jobDetails(ctx, jobs); err != nil {
				t.Fatal(err)
			}
			eligible := reason == "current"
			if jobs[0].Retryable != eligible || jobs[0].NeedsAttention != eligible || jobs[0].TargetLabel != teams[0].Name {
				t.Fatal(jobs)
			}
			body, _ := json.Marshal(TargetRequest{ID: job.ID})
			want := 409
			if eligible {
				want = 202
			}
			w := request(s, "POST", "/api/admin/jobs/retry", string(body), s.Config.Origin, "csrf")
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if !eligible {
				var response struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &response)
				if response.Error != jobs[0].RetryBlocked {
					t.Fatal(response, jobs)
				}
			}
			var retained string
			if err = s.Store.DB.QueryRow(`SELECT error FROM jobs WHERE id=?`, job.ID).Scan(&retained); err != nil || retained != "context canceled" {
				t.Fatal("history lost", retained, err)
			}
		})
	}
}

func TestConcurrentRetryEnqueuesOnceAndResolvesOldFailure(t *testing.T) {
	s, teams := checkFixture(t)
	ctx := context.Background()
	job := failedCheck(t, s, teams[0])
	body, _ := json.Marshal(TargetRequest{ID: job.ID})
	var workers sync.WaitGroup
	for n := 0; n < 12; n++ {
		workers.Go(func() {
			w := request(s, "POST", "/api/admin/jobs/retry", string(body), s.Config.Origin, "csrf")
			if w.Code != 202 && w.Code != 409 {
				t.Errorf("retry: %d %s", w.Code, w.Body.String())
			}
		})
	}
	workers.Wait()
	var count int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='check' AND target=? AND status='queued'`, job.Target).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	jobs := []Job{job}
	if err := s.jobDetails(ctx, jobs); err != nil || jobs[0].Retryable || jobs[0].NeedsAttention {
		t.Fatal(jobs, err)
	}
	if _, err := s.Store.DB.Exec(`UPDATE jobs SET status='complete' WHERE kind='check' AND target=? AND status='queued'`, job.Target); err != nil {
		t.Fatal(err)
	}
	if err := s.jobDetails(ctx, jobs); err != nil || jobs[0].RetryBlocked != "Recovery completed in a later attempt." {
		t.Fatal(jobs, err)
	}
}

func TestRetryRequiresAdminAndCSRFAndKeepsCleanupAttention(t *testing.T) {
	s, _ := checkFixture(t)
	ctx := context.Background()
	j, err := s.Store.Enqueue(ctx, "end", "old-meeting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE jobs SET status='failed' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	j.Status = "failed"
	jobs := []Job{j}
	if err = s.jobDetails(ctx, jobs); err != nil || !jobs[0].NeedsAttention {
		t.Fatal("cleanup silently dismissed", jobs, err)
	}
	body, _ := json.Marshal(TargetRequest{ID: j.ID})
	for _, q := range []struct{ origin, csrf string }{{"https://foreign.invalid", "csrf"}, {s.Config.Origin, "wrong"}} {
		if w := request(s, "POST", "/api/admin/jobs/retry", string(body), q.origin, q.csrf); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	var v Session
	_ = s.Store.Get(ctx, "session", digest("cookie"), &v)
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	if w := request(s, "POST", "/api/admin/jobs/retry", string(body), s.Config.Origin, "csrf"); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestTemplateRetryReservesOnceWithoutReenteringMutex(t *testing.T) {
	s, _ := templateFixture(t)
	buildableRevisionFixture(t, s, "old")
	ctx := context.Background()
	j, err := s.queueTemplateOperation(ctx, "build", "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE jobs SET status='failed' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(TargetRequest{ID: j.ID})
	if w := request(s, "POST", "/api/admin/jobs/retry", string(body), s.Config.Origin, "csrf"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/admin/jobs/retry", string(body), s.Config.Origin, "csrf"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if err = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE target='old' AND status='queued'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
