package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

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
	if status.ActiveJobs != 1 || len(status.Jobs) != 50 {
		t.Fatal(status)
	}
	for _, j := range status.Jobs {
		if j.ID == old.ID {
			t.Fatal("fixture did not exclude old work")
		}
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
