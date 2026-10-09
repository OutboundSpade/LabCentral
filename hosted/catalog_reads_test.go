package hosted

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLearningCatalogBatchesKeepPublicationAndUserBoundaries(t *testing.T) {
	s, v, base := learningFixture(t)
	ctx := context.Background()
	want := []LearningLab{}
	for i := range 260 {
		p := base
		p.ID = fmt.Sprintf("practice-%03d", i)
		p.PublishedAt = time.Date(2026, 10, 8, 12, 0, i, 0, time.UTC)
		p.Published = i%7 != 0
		progress := LearningProgress{LabID: p.ID, CompletedSteps: i % 3}
		if err := s.Store.Put(ctx, "learning-publication", p.ID, p); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), progress); err != nil {
				t.Fatal(err)
			}
		} else {
			progress.CompletedSteps = 0
		}
		if err := s.Store.Put(ctx, "learning-progress", progressKey("another-student", p.ID), LearningProgress{LabID: p.ID, CompletedSteps: 999, LastAnswer: "PRIVATE-OTHER-STUDENT"}); err != nil {
			t.Fatal(err)
		}
		if p.Published {
			want = append([]LearningLab{p.summary(progress)}, want...)
		}
	}
	// Unpublish the fixture so the expected sequence consists of these records.
	base.Published = false
	if err := s.Store.Put(ctx, "learning-publication", base.ID, base); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/learning", "", "", "")
	catalog := decodeLearning[LearningCatalog](t, w, 200)
	if !reflect.DeepEqual(catalog.Labs, want) {
		t.Fatal("catalog summaries, ordering or user progress changed")
	}
	for _, forbidden := range []string{"PRIVATE-", `"answer"`, `"solution"`, `"checkID"`, `"steps"`} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("catalog disclosed private data", forbidden)
		}
	}
	// Read the durable publication afresh after unpublication, with no view cache.
	var p LearningPublication
	if err := s.Store.Get(ctx, "learning-publication", want[0].ID, &p); err != nil {
		t.Fatal(err)
	}
	p.Published = false
	if err := s.Store.Put(ctx, "learning-publication", p.ID, p); err != nil {
		t.Fatal(err)
	}
	catalog = decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	if !reflect.DeepEqual(catalog.Labs, want[1:]) {
		t.Fatal("unpublished practice remained visible")
	}
}

func TestProjectedCatalogReadsFailOnMissingOrCorruptMetadata(t *testing.T) {
	for _, test := range []struct{ name, kind, id, raw, path string }{
		{"missing source", "source", "pinned", "", "/api/templates"},
		{"mismatched source identity", "template", "pinned", `{"id":"other"}`, "/api/templates"},
		{"corrupt source", "source", "pinned", `{`, "/api/templates/pinned"},
		{"corrupt stages", "template-instructor", "pinned", `{"stages":"broken"}`, "/api/templates"},
		{"corrupt publication", "learning-publication", "linux", `{`, "/api/learning"},
		{"corrupt published steps", "learning-publication", "linux", `{"id":"linux","published":true,"steps":"broken"}`, "/api/learning"},
		{"corrupt own progress", "learning-progress", "", `{`, "/api/learning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, v, p := learningFixture(t)
			id := test.id
			if test.kind == "learning-progress" {
				id = progressKey(v.User.ID, p.ID)
			}
			var err error
			if test.raw == "" {
				err = s.Store.Delete(context.Background(), test.kind, id)
			} else {
				_, err = s.Store.DB.Exec(`INSERT INTO objects VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, test.kind, id, []byte(test.raw))
			}
			if err != nil {
				t.Fatal(err)
			}
			if w := request(s, "GET", test.path, "", "", ""); w.Code != 503 || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("invalid metadata exposed a view", w.Code, w.Body.String())
			}
		})
	}
}

func TestLearningCatalogProgressComesOnlyFromCurrentUser(t *testing.T) {
	s, v, p := learningFixture(t)
	ctx := context.Background()
	// Extra publication metadata cannot supply a student's completion count.
	if err := s.Store.Put(ctx, "learning-publication", p.ID, struct {
		LearningPublication
		CompletedSteps int `json:"completedSteps"`
	}{p, 999}); err != nil {
		t.Fatal(err)
	}
	catalog := decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	if catalog.Labs[0].CompletedSteps != 0 {
		t.Fatal("publication supplied student progress")
	}
	if err := s.Store.Put(ctx, "learning-progress", progressKey(v.User.ID, p.ID), LearningProgress{LabID: p.ID, CompletedSteps: 1}); err != nil {
		t.Fatal(err)
	}
	catalog = decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	if catalog.Labs[0].CompletedSteps != 1 {
		t.Fatal("current user progress not applied")
	}
}

func TestPracticeQuotaIndexedHistoryPreservesExactOverlaps(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	start, _ := weekBounds(now)
	for _, test := range []struct {
		name           string
		created, ended time.Time
		pending        bool
		want           int
	}{
		{"fractional Monday", start.Add(-time.Hour), start.Add(time.Millisecond), false, 1},
		{"negative offset", start.Add(-time.Hour), start.Add(30 * time.Minute).In(time.FixedZone("west", -3600)), false, 1800},
		{"positive offset", start.Add(-time.Hour), start.Add(30 * time.Minute).In(time.FixedZone("east", 3600)), false, 1800},
		{"maximum parsed offset", start.Add(-time.Hour), start.Add(time.Millisecond).In(time.FixedZone("west", -24*3600-59*60)), false, 1},
		{"old pending cleanup", start.AddDate(0, 0, -14), time.Time{}, true, int(now.Sub(start) / time.Second)},
		{"future end", now.Add(-time.Minute), now.Add(48 * time.Hour), false, 60},
		{"ended last week", start.Add(-2 * time.Hour), start.Add(-time.Hour), false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ended, status := test.ended.Format(time.RFC3339Nano), "ended"
			if test.pending {
				ended, status = "", "ending"
			}
			if _, err := st.DB.Exec(`INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires,ended) VALUES(?,?,'lab','instance',?,?,?,?)`, test.name, test.name, status, test.created.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), ended); err != nil {
				t.Fatal(err)
			}
			q, err := st.quota(ctx, test.name, 180, now)
			if err != nil || q.UsedSeconds != test.want || q.RemainingSeconds != max(0, 180*60-test.want) {
				t.Fatal(q, err)
			}
		})
	}
	for _, test := range []struct{ user, created, ended string }{
		{"bad-created", "invalid", now.Format(time.RFC3339Nano)},
		{"bad-ended", now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano) + "invalid"},
	} {
		if _, err := st.DB.Exec(`INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires,ended) VALUES(?,?,'lab','instance','ended',?,?,?)`, test.user, test.user, test.created, now.Format(time.RFC3339Nano), test.ended); err != nil {
			t.Fatal(err)
		}
		if _, err := st.quota(ctx, test.user, 180, now); err == nil {
			t.Fatal("corrupt overlapping timestamp silently removed", test.user)
		}
	}
	rows, err := st.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+practiceQuotaSQL, "student", "student", start.AddDate(0, 0, -2).Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	searches := 0
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SEARCH practice_runs USING COVERING INDEX practice_quota_history") {
			searches++
		}
		if strings.Contains(detail, "SCAN practice_runs") {
			t.Fatal("quota scanned allocation history", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if searches != 2 {
		t.Fatal("pending and ended history do not both use the covering index", searches)
	}
}
