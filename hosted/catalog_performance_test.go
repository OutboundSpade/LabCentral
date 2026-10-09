package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These benchmarks exercise local durable read paths, not native cloud capacity.
func benchmarkReadStore(b *testing.B) *Store {
	b.Helper()
	s, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

func BenchmarkTemplateCatalogPinnedBundles(b *testing.B) {
	st := benchmarkReadStore(b)
	s := &Service{Store: st}
	ctx := context.Background()
	src, err := lc.Load("../examples/workstation")
	if err != nil {
		b.Fatal(err)
	}
	src.Files = append(src.Files, lc.File{Path: "large.txt", Content: []byte(strings.Repeat("x", 256<<10))})
	bundle := InstructorBundle{Version: 2, Files: []lc.File{{Path: "large.yml", Content: []byte(strings.Repeat("x", 256<<10))}}, Stages: []TemplateStage{{ID: "inspect", Title: "Inspect", Kind: "answer", Instructions: "Read the service.", Answer: "private", Solution: "private"}}}
	for i := range 16 {
		id := fmt.Sprintf("revision-%02d", i)
		for _, o := range []struct {
			kind  string
			value any
		}{{"template", Template{ID: id, Name: "Workstation", Built: true}}, {"source", src}, {"template-instructor", bundle}} {
			if err := st.Put(ctx, o.kind, id, o.value); err != nil {
				b.Fatal(err)
			}
		}
	}
	r := httptest.NewRequest("GET", "/api/templates", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w := httptest.NewRecorder()
		s.templateCatalog(w, r, Session{})
		if w.Code != 200 {
			b.Fatal(w.Body.String())
		}
	}
}

func BenchmarkPracticeQuotaHistoricalRuns(b *testing.B) {
	st := benchmarkReadStore(b)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires,ended) VALUES(?,?,'lab','instance','ended',?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	old := now.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	for i := range 20000 {
		user := "other"
		if i%2 == 0 {
			user = "student"
		}
		if _, err := stmt.Exec(fmt.Sprint(i), user, old, old, old); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires) VALUES('active','student','lab','instance','ending',?,?)`, now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		q, err := st.quota(ctx, "student", 180, now)
		if err != nil || q.UsedSeconds != 60 {
			b.Fatal(q, err)
		}
	}
}

func BenchmarkLearningCatalogPublications(b *testing.B) {
	st := benchmarkReadStore(b)
	s := &Service{Store: st}
	ctx := context.Background()
	steps := make([]LearningStepInput, 20)
	for i := range steps {
		steps[i] = LearningStepInput{ID: fmt.Sprint(i), Instructions: strings.Repeat("x", 1024), Answer: strings.Repeat("private", 128), Solution: strings.Repeat("private", 128)}
	}
	for i := range 128 {
		id := fmt.Sprintf("lab-%03d", i)
		p := LearningPublication{ID: id, Template: "pinned", Title: "Published practice", Steps: steps, Published: true, PublishedAt: time.Now().UTC()}
		if err := st.Put(ctx, "learning-publication", id, p); err != nil {
			b.Fatal(err)
		}
		if err := st.Put(ctx, "learning-progress", progressKey("student", id), LearningProgress{LabID: id, CompletedSteps: 2}); err != nil {
			b.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/api/learning", nil)
	v := Session{User: User{ID: "student"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w := httptest.NewRecorder()
		s.learningCatalog(w, r, v)
		if w.Code != 200 {
			b.Fatal(w.Body.String())
		}
	}
}
