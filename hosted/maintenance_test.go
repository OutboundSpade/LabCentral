package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMaintenanceRefreshDeduplicatesPersistsAndPreservesOtherWork(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	st, err := OpenStore(filepath.Join(root, "hosted"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := Meeting{ID: "class", Template: "pinned", Status: "ready", Expires: time.Now().Add(time.Hour)}
	if err = st.Put(ctx, "meeting", "current", m); err != nil {
		t.Fatal(err)
	}
	other, err := st.Enqueue(ctx, "build", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(`UPDATE jobs SET status='failed' WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	type response struct {
		result any
		err    error
	}
	results := make(chan response, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			r, err := Maintain(ctx, root, "refresh")
			results <- response{r, err}
		})
	}
	workers.Wait()
	close(results)
	var id string
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		job, ok := r.result.(Job)
		if !ok || job.Kind != "refresh" || job.Target != "repository" || job.Status != "queued" || job.ID == "" {
			t.Fatal("unexpected operation", r.result)
		}
		if id != "" && id != job.ID {
			t.Fatal("concurrent requests queued different refreshes", id, job.ID)
		}
		id = job.ID
	}
	// A new connection observes the committed queue, independent of caller lifetime.
	reopened, err := OpenStore(filepath.Join(root, "hosted"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	jobs, err := reopened.Jobs(ctx)
	if err != nil || len(jobs) != 2 {
		t.Fatal("queue did not persist", jobs, err)
	}
	for _, job := range jobs {
		if job.ID == other.ID && job.Status != "failed" {
			t.Fatal("refresh retried unrelated work", job)
		}
	}
	actual, err := reopened.Meeting(ctx)
	if err != nil || actual == nil || actual.ID != m.ID || actual.Template != m.Template || actual.Status != m.Status {
		t.Fatal("refresh changed the meeting", actual, err)
	}
	if _, err = reopened.DB.Exec(`UPDATE jobs SET status='complete' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	next, err := Maintain(ctx, root, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if next.(Job).ID == id {
		t.Fatal("completed refresh prevented a new request", next)
	}
	if _, err = Maintain(ctx, root, "refresh-unknown"); err == nil {
		t.Fatal("unsupported operation accepted")
	}
	jobs, err = reopened.Jobs(ctx)
	if err != nil || len(jobs) != 3 {
		t.Fatal("unsupported operation changed queue", jobs, err)
	}
}

func TestMaintenanceReportsWorkOutsideRecentHistory(t *testing.T) {
	root := t.TempDir()
	st, err := OpenStore(filepath.Join(root, "hosted"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	old, err := st.Enqueue(ctx, "build", "old")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		j, err := st.Enqueue(ctx, "refresh", fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = st.DB.Exec(`UPDATE jobs SET status='complete' WHERE id=?`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Maintain(ctx, root, "status")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		ActiveJobs int
		Jobs       []Job
	}
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if status.ActiveJobs != 1 || len(status.Jobs) != 51 {
		t.Fatal(status)
	}
	if status.Jobs[0].ID != old.ID {
		t.Fatal("active work hidden by recent history")
	}
}

func TestMaintenanceRecoverySkipsObsoleteMeetings(t *testing.T) {
	for _, state := range []string{"waiting", "ending", "ended", "expired"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			st, err := OpenStore(filepath.Join(root, "hosted"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			m := Meeting{ID: "current-meeting", Status: state, Expires: time.Now().Add(time.Hour)}
			if state == "expired" {
				m.Status = "waiting"
				m.Expires = time.Now().Add(-time.Hour)
			}
			if err = st.Put(ctx, "meeting", "current", m); err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{"old-meeting", m.ID} {
				for _, kind := range []string{"prepare", "end"} {
					j, err := st.Enqueue(ctx, kind, target)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = st.DB.Exec(`UPDATE jobs SET status='failed' WHERE id=?`, j.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := 0; i < 2; i++ {
				if _, err = Maintain(ctx, root, "recover"); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err := st.Jobs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range jobs {
				want := "failed"
				if j.Target == m.ID && state != "ended" && (j.Kind == "end" || state == "waiting") {
					want = "queued"
				}
				if j.Status != want {
					t.Fatalf("%s %s: %s, want %s", j.Kind, j.Target, j.Status, want)
				}
			}
		})
	}
}
