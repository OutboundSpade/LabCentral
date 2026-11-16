package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadMemoCoalescesCancellationAndRetriesErrors(t *testing.T) {
	var memo readMemo[int]
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		value, err := memo.get(context.Background(), "one", "key", func() (int, error) { calls.Add(1); close(entered); <-release; return 7, nil })
		if value != 7 || err != nil {
			t.Error(value, err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := memo.get(ctx, "one", "key", func() (int, error) { t.Error("duplicate computation"); return 0, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	<-done
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			value, err := memo.get(context.Background(), "one", "key", func() (int, error) { calls.Add(1); return 0, nil })
			if value != 7 || err != nil {
				t.Error(value, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if _, err := memo.get(context.Background(), "two", "key", func() (int, error) { return 0, errors.New("unavailable") }); err == nil {
		t.Fatal("error lost")
	}
	value, err := memo.get(context.Background(), "two", "key", func() (int, error) { return 9, nil })
	if value != 9 || err != nil {
		t.Fatal("error retained", value, err)
	}
}

func TestReadGenerationsRollbackAndPersist(t *testing.T) {
	root := t.TempDir()
	st, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial, err := st.readGeneration(ctx, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("rollback")
	err = st.atomic(ctx, func(ctx context.Context) error {
		if e := st.Put(ctx, "template", "revision", Template{ID: "revision"}); e != nil {
			return e
		}
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	after, err := st.readGeneration(ctx, "catalog")
	if err != nil || after != initial {
		t.Fatal(after, initial, err)
	}
	if err = st.Put(ctx, "template", "revision", Template{ID: "revision"}); err != nil {
		t.Fatal(err)
	}
	after, err = st.readGeneration(ctx, "catalog")
	if err != nil || after <= initial {
		t.Fatal(after, initial, err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	persisted, err := st.readGeneration(ctx, "catalog")
	if err != nil || persisted != after {
		t.Fatal(persisted, after, err)
	}
	_, err = st.instructorPolicy(ctx, "revision")
	if err != nil {
		t.Fatal(err)
	}
	err = st.atomic(ctx, func(ctx context.Context) error {
		b := InstructorBundle{}
		b.Visibility.Audience = "coaches"
		if err := st.Put(ctx, "template-instructor", "revision", b); err != nil {
			return err
		}
		policy, err := st.instructorPolicy(ctx, "revision")
		if err != nil || policy.Visibility.Audience != "coaches" {
			t.Fatal(policy, err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	policy, err := st.instructorPolicy(ctx, "revision")
	if err != nil || policy.Visibility.Audience == "coaches" {
		t.Fatal(policy, err)
	}
}

func TestAdminPollReadBudget(t *testing.T) {
	s, e, cookies := meetingReadFixture(t)
	fixedNow := time.Now()
	s.inventoryClock = func() time.Time { return fixedNow }
	e.cost = time.Millisecond
	h := s.Handler()
	poll := func() {
		r := httptest.NewRequest("GET", s.Config.Origin+"/api/admin", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: cookies[36]})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Error(w.Code, w.Body.String())
		}
	}
	poll()
	initial := e.calls.Load()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(poll)
	}
	wg.Wait()
	if delta := e.calls.Load() - initial; delta > 3 {
		t.Fatal("per-request engine reads returned", delta)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 6 {
		poll()
	}
	runtime.ReadMemStats(&after)
	// Baseline allocated ~369 MiB per admin poll. Leave headroom for encoding
	// instructor material while catching per-head decoding and planning.
	if bytes := (after.TotalAlloc - before.TotalAlloc) / 6; bytes > 32<<20 {
		t.Fatalf("admin poll allocates %d bytes; repeated planning/decoding returned", bytes)
	}
	if _, err := s.Store.Enqueue(context.Background(), "end", "read-meeting"); err != nil {
		t.Fatal(err)
	}
	initial = e.calls.Load()
	poll()
	if e.calls.Load()-initial != 3 {
		t.Fatal("lifecycle write failed to invalidate inventory")
	}
	templates, _, err := s.adminTemplates(context.Background())
	if err != nil || len(templates) != 70 {
		t.Fatal(len(templates), err)
	}
}

func TestDiagnosticsLoopbackAndSlowLogPrivacy(t *testing.T) {
	for _, address := range []string{"0.0.0.0:6060", "[::]:6060", "192.0.2.1:6060", "localhost:6060", "127.0.0.1", "example.org:6060"} {
		if _, listener, err := NewDiagnosticsServer(address); err == nil {
			listener.Close()
			t.Fatal("unsafe diagnostics address", address)
		}
	}
	server, listener, err := NewDiagnosticsServer("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/debug/pprof/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "goroutine") {
		t.Fatal(w.Code)
	}
	var line string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/templates/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	wrapped := slowAPI(mux, 0, func(format string, args ...any) { line = fmt.Sprintf(format, args...) })
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://labs.invalid/api/templates/PRIVATE-REVISION?user=PRIVATE-USER", nil))
	if !strings.Contains(line, "route=\"GET /api/templates/{id}\" status=503") || strings.Contains(line, "PRIVATE") {
		t.Fatal(line)
	}
	s, _, _ := serviceFor(t)
	if request(s, "GET", "/debug/pprof/", "", "", "").Code != 404 {
		t.Fatal("public profiling")
	}
}

func TestReadMemoEvictsOneEntryAtLimit(t *testing.T) {
	var m readMemo[int]
	var misses atomic.Int64
	get := func(n int) {
		v, err := m.get(context.Background(), "g", fmt.Sprint(n), func() (int, error) { misses.Add(1); return n, nil })
		if err != nil || v != n {
			t.Fatal(v, err)
		}
	}
	for n := range readMemoLimit + 10 {
		get(n)
	}
	if len(m.entries) != readMemoLimit {
		t.Fatal("memo size", len(m.entries))
	}
	// Installations hold hundreds of revisions; a full memo must not reset itself.
	before := misses.Load()
	for n := readMemoLimit - 100; n < readMemoLimit+10; n++ {
		get(n)
	}
	// Each miss evicts one arbitrary entry; clearing the memo would miss all 110.
	if delta := misses.Load() - before; delta > 10 {
		t.Fatal("full memo reset instead of evicting single entries", delta)
	}
}

// Snapshot rollover rebuilds the admin model; it must reuse cached plans and
// source versions instead of re-parsing every stored source.
func TestAdminRebuildReusesPlansAcrossSnapshots(t *testing.T) {
	s, _, _ := meetingReadFixture(t)
	now := time.Now()
	s.inventoryClock = func() time.Time { return now }
	ctx := context.Background()
	warm, _, err := s.adminTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Without triggers, rewriting sources leaves the source generation unchanged,
	// so any re-read of a source would expose the rewritten body.
	rows, err := s.Store.DB.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'hosted_%_objects_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	rows.Close()
	for _, name := range triggers {
		if _, err = s.Store.DB.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Store.DB.Exec(`UPDATE objects SET body='{"lab":{"version":3}}' WHERE kind='source'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	rebuilt, _, err := s.adminTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt) != len(warm) {
		t.Fatal(len(rebuilt), len(warm))
	}
	for n := range warm {
		if rebuilt[n].ImagesTotal != warm[n].ImagesTotal || rebuilt[n].Problem != warm[n].Problem {
			t.Fatalf("revision %s was re-planned after snapshot rollover", warm[n].ID)
		}
	}
}

func TestPlanErrorsAreCachedPerGeneration(t *testing.T) {
	s, _, _ := meetingReadFixture(t)
	templates, err := s.rawTemplates(context.Background())
	if err != nil || len(templates) == 0 {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), inventoryReadKey{}, &inventorySnapshot{})
	var failing string
	delete(s.env.Bases, "windows-2022")
	for _, tpl := range templates {
		if _, err = s.plan(ctx, tpl.ID); err != nil {
			failing = tpl.ID
			break
		}
	}
	if failing == "" {
		t.Fatal("fixture has no revision that fails without the Windows base")
	}
	if _, err = s.Store.DB.Exec(`DROP TRIGGER hosted_source_objects_UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE objects SET body='{"lab":{"version":3}}' WHERE kind='source' AND id=?`, failing); err != nil {
		t.Fatal(err)
	}
	if _, err = s.plan(ctx, failing); err == nil {
		t.Fatal("a deterministic planning failure was recomputed instead of cached")
	}
}

func TestAdminPollCarriesOnlyMeetingInstructorMaterial(t *testing.T) {
	s, _, cookies := meetingReadFixture(t)
	h := s.Handler()
	get := func(path, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", s.Config.Origin+path, nil)
		r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/api/admin", cookies[36])
	var view AdminView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Meeting == nil {
		t.Fatal(w.Code, w.Body.String())
	}
	var other string
	for _, tpl := range view.Templates {
		if (tpl.Instructor != nil) != (tpl.ID == view.Meeting.Template) {
			t.Fatal("instructor material on revision", tpl.ID)
		}
		if tpl.ID != view.Meeting.Template {
			other = tpl.ID
		}
	}
	if len(view.Breaks) == 0 {
		t.Fatal("meeting breaks were dropped")
	}
	// The cached model still holds every revision's material.
	cached, _, err := s.adminTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range cached {
		if tpl.ID == other && tpl.Instructor == nil {
			t.Fatal("stripping the response mutated the shared cache")
		}
	}
	if w = get("/api/admin/templates/"+other+"/instructor", cookies[36]); w.Code != 200 || !strings.Contains(w.Body.String(), "breaks") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = get("/api/admin/templates/"+other+"/instructor", cookies[0]); w.Code != 403 {
		t.Fatal("student read instructor material", w.Code)
	}
	if w = get("/api/admin/templates/missing/instructor", cookies[36]); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
