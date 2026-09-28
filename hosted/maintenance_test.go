package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
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
