package hosted

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// This engine consumes the original library instance records. It never plans or
// provisions a replacement for a retained allocation.
type v2UpgradeEngine struct {
	Engine
	st          *Store
	mu          sync.Mutex
	destroyed   []string
	images      []lc.ImageRecord
	removed     []string
	failDestroy string
}

func (e *v2UpgradeEngine) Inspect(ctx context.Context, id string) (lc.Instance, error) {
	var i lc.Instance
	err := e.st.Get(ctx, "instance", id, &i)
	return i, err
}
func (e *v2UpgradeEngine) List(ctx context.Context) ([]lc.Instance, error) {
	rows, err := e.st.List(ctx, "instance")
	if err != nil {
		return nil, err
	}
	out := []lc.Instance{}
	for _, raw := range rows {
		var i lc.Instance
		if err = json.Unmarshal(raw, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}
func (e *v2UpgradeEngine) Destroy(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == e.failDestroy {
		return errors.New("provider deletion unconfirmed")
	}
	e.destroyed = append(e.destroyed, id)
	return e.st.Delete(ctx, "instance", id)
}
func (e *v2UpgradeEngine) Images(context.Context) ([]lc.ImageRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.images), nil
}
func (e *v2UpgradeEngine) Builds(context.Context) ([]lc.Command, error) { return nil, nil }
func (e *v2UpgradeEngine) RemoveImage(_ context.Context, key string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, im := range e.images {
		if im.Key == key {
			e.images = append(e.images[:i], e.images[i+1:]...)
			e.removed = append(e.removed, key)
			return nil
		}
	}
	return errors.New("unknown image identity")
}
func (e *v2UpgradeEngine) CloseAccess(context.Context, string) error { return nil }

func v2UpgradeFixture(t *testing.T) (*Service, Session, *v2UpgradeEngine) {
	t.Helper()
	root := t.TempDir()
	external := os.Getenv("LABCENTRAL_V2_STATE_FIXTURE")
	if external != "" {
		if err := os.CopyFS(root, os.DirFS(external)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := OpenStore(filepath.Join(root, "hosted"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if external == "" {
		seedV2UpgradeState(t, st)
	}
	// Move the fixture's meeting deadline forward without changing its revision,
	// membership, allocation identities or historical snapshots.
	meeting, err := st.Meeting(context.Background())
	if err != nil || meeting == nil {
		t.Fatal(meeting, err)
	}
	meeting.Expires = time.Now().Add(4 * time.Hour)
	if err = st.Put(context.Background(), "meeting", "current", meeting); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(`UPDATE practice_runs SET expires=? WHERE status='ready'`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var env lc.Environment
	var images []lc.ImageRecord
	for _, o := range []struct {
		kind string
		out  any
	}{{"upgrade-environment", &env}, {"upgrade-images", &images}} {
		if err = st.Get(context.Background(), o.kind, "fixture", o.out); err != nil {
			t.Fatal(err)
		}
	}
	e := &v2UpgradeEngine{st: st, images: images}
	v := Session{ID: digest("cookie"), User: User{ID: "student", Name: "Student", Admin: true}, CSRF: "csrf", Expires: time.Now().Add(time.Hour), Checked: time.Now()}
	if err = st.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	return &Service{Store: st, Engine: e, env: env, Config: Config{Root: root, Origin: "https://labs.invalid", DesktopOrigin: "https://desktop.invalid", WebRoot: t.TempDir()}, Identity: &testIdentity{user: v.User}, desktops: map[string]*desktopSession{}, tickets: map[string]launchTicket{}}, v, e
}

// The retained fixture derives its legacy stored shapes from the public
// service-recovery template plus synthetic history and resource identities.
// LABCENTRAL_V2_STATE_FIXTURE optionally reads an operator-owned state copy.
func seedV2UpgradeState(t *testing.T, st *Store) {
	t.Helper()
	f, err := os.Open("testdata/retained-v2.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Objects []struct {
			Kind, ID string
			Body     json.RawMessage
		}
	}
	// practice_runs keeps the main table's spelling in the fixture.
	var tables map[string]json.RawMessage
	if err = json.Unmarshal(raw, &tables); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(tables["objects"], &fixture.Objects); err != nil {
		t.Fatal(err)
	}
	for _, o := range fixture.Objects {
		if _, err = st.DB.Exec(`INSERT INTO objects(kind,id,body) VALUES(?,?,?)`, o.Kind, o.ID, []byte(o.Body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"teams", "memberships", "jobs", "practice_runs"} {
		var rows []map[string]any
		if err = json.Unmarshal(tables[table], &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			cols := make([]string, 0, len(row))
			for col := range row {
				cols = append(cols, col)
			}
			slices.Sort(cols)
			args := make([]any, 0, len(cols))
			for _, col := range cols {
				args = append(args, row[col])
			}
			query := `INSERT INTO ` + table + ` (` + strings.Join(cols, ",") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
			if _, err = st.DB.Exec(query, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestV2StateHistoryAndAuthorization(t *testing.T) {
	s, v, _ := v2UpgradeFixture(t)
	ctx := context.Background()
	admin := decodeLearning[AdminView](t, request(s, "GET", "/api/admin", "", "", ""), 200)
	if len(admin.Templates) != 4 || len(admin.Teams) != 2 || len(admin.CheckRuns) != 2 || len(admin.BreakRuns) != 1 {
		t.Fatal("lost admin history", admin)
	}
	// /api/admin is the templates admin data route and also owns the team
	// list and scoreboard; there is no separate GET /api/admin/templates route.
	for _, run := range admin.CheckRuns {
		if run.Status != "complete" || len(run.Results) == 0 || run.Results[0].Status != "passed" {
			t.Fatal("lost scoreboard", run)
		}
	}
	for _, tpl := range admin.Templates {
		if tpl.Instructor == nil || len(tpl.Instructor.Breaks) == 0 {
			t.Fatal("lost instructor breaks", tpl.Name)
		}
		if tpl.ID != "retained-instructor-v1" && tpl.Name != "branch-services-ir" && (len(tpl.Instructor.Stages) == 0 || tpl.Instructor.Stages[0].Kind() == "instructor") {
			t.Fatal("lost legacy stage kind", tpl.Name)
		}
	}
	v.User.Admin = false
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", "/api/admin", "", "", "").Code != 403 {
		t.Fatal("student administration")
	}
	catalog := decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates", "", "", ""), 200)
	if len(catalog.Templates) != 3 {
		t.Fatal("coach policy lost", catalog)
	}
	for _, tpl := range catalog.Templates {
		w := request(s, "GET", "/api/templates/"+tpl.ID, "", "", "")
		view := decodeLearning[PublicTemplate](t, w, 200)
		if tpl.ID != "retained-instructor-v1" && (view.StageCount == 0 || view.Stages[0].Kind != "answer") {
			t.Fatal("lost stage history", view)
		}
		for _, private := range []string{`"accepted"`, `"checkID"`, `"exerciseIDs"`, `"variantID"`, `"solution"`} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private author metadata leaked", private)
			}
		}
	}
	labs := decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	if len(labs.Labs) != 3 {
		t.Fatal("lost attempt/publication history", labs)
	}
	for _, lab := range labs.Labs {
		view := decodeLearning[LearningView](t, request(s, "GET", "/api/learning/"+lab.ID, "", "", ""), 200)
		if view.Progress.CompletedSteps != 1 || view.CloudReady {
			t.Fatal("legacy progress/readiness", view)
		}
		if lab.ScenarioID != "" && (view.Scenario == nil || view.Scenario.MaxBreaks != 2) {
			t.Fatal("lost scenario intent", view)
		}
		if lab.ID == "legacy-lesson" && (view.Check == nil || view.Check.ID != "v2-learning-check" || view.ReleasedSteps != 2) {
			t.Fatal("lost legacy verification or release gate", view)
		}
		if lab.Attempt {
			var intent templateAttempt
			if err := s.Store.Get(ctx, "template-attempt", lab.ID, &intent); err != nil || intent.Bundle.Version != 2 || len(intent.Bundle.Breaks) == 0 || intent.Bundle.Stages[0].Kind() != "answer" {
				t.Fatal("lost private attempt snapshot", intent, err)
			}
			if lab.ScenarioID != "" && len(intent.Breaks) == 0 {
				t.Fatal("lost selected scenario programs")
			}
			body, _ := json.Marshal(CreateTemplateAttemptRequest{RequestID: intent.RequestID, ScenarioID: intent.ScenarioID})
			if w := request(s, "POST", "/api/templates/"+intent.Template+"/attempt", string(body), s.Config.Origin, v.CSRF); w.Code != 200 {
				t.Fatal("historical request replay failed", w.Code, w.Body.String())
			}
		}
	}
	overview := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if len(overview.Teams) != 2 || overview.Lab.Team == nil {
		t.Fatal("lost membership", overview)
	}
	v.User.ID = "other"
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	for _, lab := range labs.Labs {
		if lab.Attempt && request(s, "GET", "/api/learning/"+lab.ID, "", "", "").Code != 404 {
			t.Fatal("foreign attempt disclosed")
		}
	}
	other := decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
	if len(other.Labs) != 1 || other.Labs[0].CompletedSteps != 0 {
		t.Fatal("foreign progress disclosed", other)
	}
}
func TestV2StateRejectsNewGuestActions(t *testing.T) {
	s, v, _ := v2UpgradeFixture(t)
	m, err := s.Store.Meeting(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	teams, err := s.Store.Teams(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ path, body string }{
		{"/api/templates/" + m.Template + "/attempt", `{"requestID":"` + strings.Repeat("e", 24) + `"}`},
		{"/api/admin/exercises", `{"action":"stop-web","teamIDs":["` + teams[0].ID + `"]}`},
		{"/api/admin/exercises", `{"action":"reset","teamIDs":["` + teams[0].ID + `"]}`},
		{"/api/admin/checks/run", `{}`}, {"/api/learning/legacy-lesson/start", `{}`}, {"/api/learning/legacy-lesson/check", `{"stepID":"restore"}`},
	} {
		w := request(s, "POST", q.path, q.body, s.Config.Origin, v.CSRF)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "historical") {
			t.Errorf("%s: %d %s", q.path, w.Code, w.Body.String())
		}
	}
}
func TestV2StateCleanupRetainsOriginalIdentities(t *testing.T) {
	s, v, e := v2UpgradeFixture(t)
	ctx := context.Background()
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Store.activePractice(ctx, "pending-student")
	if err != nil || run == nil {
		t.Fatal(run, err)
	}
	record, err := s.Store.practice(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.failDestroy = record.InstanceID
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pending Job
	for _, j := range jobs {
		if j.Kind == "practice-end" {
			pending = j
		}
	}
	if pending.ID == "" || s.runJob(ctx, pending) == nil {
		t.Fatal("unconfirmed cleanup lost")
	}
	after, err := s.Store.practice(ctx, run.ID)
	if err != nil || after.Status != "ending" || after.InstanceID != record.InstanceID {
		t.Fatal("cleanup intent changed", after, err)
	}
	e.failDestroy = ""
	if err = s.runJob(ctx, pending); err != nil {
		t.Fatal(err)
	}
	active, err := s.Store.activePractice(ctx, v.User.ID)
	if err != nil || active == nil {
		t.Fatal(active, err)
	}
	activeRecord, err := s.Store.practice(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request(s, "POST", "/api/learning/"+active.LabID+"/stop", `{}`, s.Config.Origin, v.CSRF).Code != 202 {
		t.Fatal("old active practice cannot stop")
	}
	if err = s.runJob(ctx, Job{Kind: "practice-end", Target: active.ID}); err != nil {
		t.Fatal(err)
	}
	end := decodeLearning[Job](t, request(s, "POST", "/api/admin/end", `{}`, s.Config.Origin, v.CSRF), 202)
	e.failDestroy = teams[0].InstanceID
	if s.runJob(ctx, end) == nil {
		t.Fatal("meeting uncertainty ignored")
	}
	retained, err := s.Store.Meeting(ctx)
	if err != nil || retained.Status != "ending" {
		t.Fatal(retained, err)
	}
	e.failDestroy = ""
	if err = s.runJob(ctx, end); err != nil {
		t.Fatal(err)
	}
	expected := []string{record.InstanceID, activeRecord.InstanceID, teams[0].InstanceID, teams[1].InstanceID}
	slices.Sort(expected)
	got := slices.Clone(e.destroyed)
	slices.Sort(got)
	if !slices.Equal(expected, got) {
		t.Fatal("replaced or skipped instance identities", expected, got)
	}
	for _, id := range expected {
		if _, err = e.Inspect(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("allocation remains", id, err)
		}
	}
	var keys []string
	if err = s.Store.Get(ctx, "template-images", m.Template, &keys); err != nil {
		t.Fatal(err)
	}
	// Missing saved keys must not trigger v3 replanning or deletion by name.
	if err = s.Store.Delete(ctx, "template-images", m.Template); err != nil {
		t.Fatal(err)
	}
	admin := decodeLearning[AdminView](t, request(s, "GET", "/api/admin", "", "", ""), 200)
	for _, tpl := range admin.Templates {
		if tpl.ID == m.Template && tpl.RemovalBlocked == "" {
			t.Fatal("missing historical image identities did not block removal")
		}
	}
	if _, err = s.queueTemplateOperation(ctx, "remove-images", m.Template); !errors.Is(err, errRetainedRevision) || len(e.removed) != 0 {
		t.Fatal("missing image identities permitted cleanup", err, e.removed)
	}
	if err = s.Store.Put(ctx, "template-images", m.Template, keys); err != nil {
		t.Fatal(err)
	}
	removal, err := s.queueTemplateOperation(ctx, "remove-images", m.Template)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runJob(ctx, removal); err != nil {
		t.Fatal(err)
	}
	slices.Sort(keys)
	slices.Sort(e.removed)
	if !slices.Equal(keys, e.removed) {
		t.Fatal("changed original image identities", keys, e.removed)
	}
}

func TestV2StateRefreshKeepsPinnedRevisions(t *testing.T) {
	s, _, _ := v2UpgradeFixture(t)
	ctx := context.Background()
	before, err := s.rawTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string][]byte{}
	for _, tpl := range before {
		for _, kind := range []string{"source", "template-instructor"} {
			var raw []byte
			if err = s.Store.DB.QueryRow(`SELECT body FROM objects WHERE kind=? AND id=?`, kind, tpl.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			snapshots[kind+":"+tpl.ID] = raw
		}
	}
	root := t.TempDir()
	templateRoot := filepath.Join(root, "templates")
	if err = os.Mkdir(templateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	names := []string{"branch-services", "branch-services-ir", "ccdc-service-recovery"}
	for _, name := range names {
		from := "../templates/ccdc-service-recovery"
		dest := filepath.Join(templateRoot, name)
		if err = os.CopyFS(dest, os.DirFS(from)); err != nil {
			t.Fatal(err)
		}
		if name != "ccdc-service-recovery" {
			path := filepath.Join(dest, "lab.yaml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "ccdc-service-recovery", name)), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	archivePath := filepath.Join(t.TempDir(), "templates.tar")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	err = filepath.WalkDir(templateRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err = tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	commit := strings.Repeat("f", 40)
	script := "#!/bin/sh\ncase \"$*\" in\n*clone*) exit 0;;\n*rev-parse*) echo " + commit + ";;\n*show*) printf '2026-10-14T12:00:00Z\\nV3 refresh fixture\\n';;\n*ls-tree*) printf '040000 tree 0000000000000000000000000000000000000000\\ttemplates\\0';;\n*archive*) cat " + quote(archivePath) + ";;\n*) exit 1;;\nesac\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	s.Config.Repository = "git@github.com:example/lab-templates.git"
	if err = s.runJob(ctx, Job{Kind: "refresh"}); err != nil {
		t.Fatal(err)
	}
	var report TemplateImportReport
	if err = s.Store.Get(ctx, "template-import-report", "current", &report); err != nil || len(report.Failures) != 0 {
		t.Fatal(report, err)
	}
	after, err := s.rawTemplates(ctx)
	if err != nil || len(after) != len(before)+3 {
		t.Fatal("revisions replaced", len(after), err)
	}
	for _, name := range names {
		id := digest(commit + ":" + name)[:24]
		var b InstructorBundle
		var head string
		var src lc.Source
		if err = s.Store.Get(ctx, "template-instructor", id, &b); err != nil || b.Version != 3 {
			t.Fatal(name, b.Version, err)
		}
		if err = s.Store.Get(ctx, "source", id, &src); err != nil || src.Lab.Version != 3 {
			t.Fatal(name, err)
		}
		if err = s.Store.Get(ctx, "template-head", name, &head); err != nil || head != id {
			t.Fatal("head did not advance", name, head, err)
		}
	}
	for key, before := range snapshots {
		parts := strings.SplitN(key, ":", 2)
		var after []byte
		if err = s.Store.DB.QueryRow(`SELECT body FROM objects WHERE kind=? AND id=?`, parts[0], parts[1]).Scan(&after); err != nil || !bytes.Equal(before, after) {
			t.Fatal("old snapshot changed", key, err)
		}
	}
	admin := decodeLearning[AdminView](t, request(s, "GET", "/api/admin", "", "", ""), 200)
	if len(admin.Templates) != 7 || admin.Meeting.Template == digest(commit + ":branch-services")[:24] {
		t.Fatal("refresh changed meeting pin", admin.Meeting)
	}
	// Retained v2 revisions are marked retired; freshly imported v3 revisions are not.
	retired := 0
	for _, tpl := range admin.Templates {
		if tpl.Retired {
			retired++
		}
		if tpl.Commit == commit && tpl.Retired {
			t.Fatal("freshly imported v3 revision marked retired", tpl.Name)
		}
		if tpl.Commit != commit && !tpl.Retired {
			t.Fatal("retained v2 revision not marked retired", tpl.Name)
		}
	}
	if retired != 4 {
		t.Fatal("retired revisions", retired)
	}
	decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
}

func TestV2StateInterruptedJobsDoNotExecuteGuests(t *testing.T) {
	s, _, _ := v2UpgradeFixture(t)
	ctx := context.Background()
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var countBefore int
	if err = s.Store.DB.QueryRow(`SELECT count(*) FROM jobs`).Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	s.scheduleChecks(ctx)
	s.scheduleLearningChecks(ctx)
	var countAfter int
	if err = s.Store.DB.QueryRow(`SELECT count(*) FROM jobs`).Scan(&countAfter); err != nil || countAfter != countBefore {
		t.Fatal("historical scheduler queued new guest work", countBefore, countAfter, err)
	}
	var exerciseID, checkID string
	if err = s.Store.Get(ctx, "exercise-current", teams[0].ID, &exerciseID); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Get(ctx, "check-current", teams[0].ID, &checkID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, id string }{{"exercise", exerciseID}, {"check", checkID}, {"learning-check", "v2-learning-check"}} {
		if _, err = s.Store.DB.Exec(`UPDATE objects SET body=json_set(body,'$.status','queued') WHERE kind=? AND id=?`, target.kind, target.id); err != nil {
			t.Fatal(err)
		}
		if target.kind == "learning-check" {
			if _, err = s.Store.DB.Exec(`UPDATE objects SET body=json_set(body,'$.status','queued') WHERE kind='learning-check-current' AND json_extract(body,'$.id')=?`, target.id); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.runJob(ctx, Job{Kind: target.kind, Target: target.id}); !errors.Is(err, errRetainedRevision) {
			t.Fatal("interrupted guest job replayed", target, err)
		}
	}
	// The fake has no guest execution or provisioning methods. Any accidental
	// invocation would panic instead of concealing an unexpected native effect.
}

func TestV2StateConcurrentHistoryAndCleanup(t *testing.T) {
	s, _, _ := v2UpgradeFixture(t)
	ctx := context.Background()
	jobs, err := s.Store.QueuedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pending Job
	for _, j := range jobs {
		if j.Kind == "practice-end" {
			pending = j
		}
	}
	if pending.ID == "" {
		t.Fatal("missing cleanup job")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 4 {
				for _, path := range []string{"/api/admin", "/api/templates", "/api/learning", "/api/me"} {
					w := request(s, "GET", path, "", "", "")
					if w.Code != 200 {
						t.Errorf("concurrent %s: %d %s", path, w.Code, w.Body.String())
					}
				}
			}
		})
	}
	wg.Go(func() {
		if err := s.runJob(ctx, pending); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
}
