package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
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
	bundle := InstructorBundle{Visibility: instructor.Visibility{Audience: "students"}, Practice: &instructor.Practice{DesktopMachine: "workstation"}, Version: 3, Files: []lc.File{{Path: "large.yml", Content: []byte(strings.Repeat("x", 256<<10))}}, Stages: []TemplateStage{{Completion: instructor.Completion{Answer: &instructor.Answer{Accepted: []string{"private"}, CaseSensitive: true}}, ID: "inspect", Title: "Inspect", Instructions: "Read the service.", Solution: "private"}}}
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

// meetingReadEngine models the local library journal's decoding cost and lets
// operators add a configurable delay. It never calls a cloud or guest.
type meetingReadEngine struct {
	Engine
	images     []lc.ImageRecord
	instances  []lc.Instance
	builds     []lc.Command
	cost       time.Duration
	calls      atomic.Int64
	guestCalls atomic.Int64
	guestCost  time.Duration
}

func (e *meetingReadEngine) read(ctx context.Context, src, dst any) error {
	e.calls.Add(1)
	if e.cost > 0 {
		if err := loadWait(ctx, e.cost); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
func (e *meetingReadEngine) Images(ctx context.Context) (out []lc.ImageRecord, err error) {
	err = e.read(ctx, e.images, &out)
	return
}
func (e *meetingReadEngine) List(ctx context.Context) (out []lc.Instance, err error) {
	err = e.read(ctx, e.instances, &out)
	return
}
func (e *meetingReadEngine) Builds(ctx context.Context) (out []lc.Command, err error) {
	err = e.read(ctx, e.builds, &out)
	return
}
func (e *meetingReadEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	for _, i := range e.instances {
		if i.ID == id {
			return i, nil
		}
	}
	return lc.Instance{}, sql.ErrNoRows
}
func (e *meetingReadEngine) WithSSHSession(ctx context.Context, _ string, _ string, _ func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.guestCalls.Add(1)
	if e.guestCost > 0 {
		if err := loadWait(ctx, e.guestCost); err != nil {
			return err
		}
	}
	return fmt.Errorf("fixture guest unavailable")
}

func (e *meetingReadEngine) WithSSHMaterial(ctx context.Context, id, machine string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	return e.WithSSHSession(ctx, id, machine, fn)
}

func meetingReadFixture(t *testing.T) (*Service, *meetingReadEngine, []string) {
	t.Helper()
	s, _, _ := learningFixture(t)
	ctx := context.Background()
	if _, err := s.Store.DB.Exec(`DELETE FROM objects WHERE kind IN ('template','source','template-instructor','template-images','learning-publication')`); err != nil {
		t.Fatal(err)
	}
	s.env.Bases["windows-2022"] = lc.Base{ID: "ami-0123456789abcdef0", Identity: "ami-0123456789abcdef0", User: "Administrator", OS: "windows", DiskGiB: 30}
	s.env.Build.InstanceType = "t3.medium"
	s.env.Build.WindowsDCVURL = "https://example.invalid/dcv.msi"
	s.env.Build.WindowsDCVSHA256 = strings.Repeat("a", 64)
	e := &meetingReadEngine{cost: loadDuration(t, "LABCENTRAL_READ_ENGINE_COST", 0), guestCost: loadDuration(t, "LABCENTRAL_READ_GUEST_COST", 0)}
	s.Engine = e
	var meetingSource lc.Source
	var meetingBundle InstructorBundle
	var meetingPlan lc.Plan
	var meetingID string
	for n := range 28 {
		for revision := range 1 + n%4 {
			root := t.TempDir()
			from := "../templates/ccdc-service-recovery"
			if n%3 == 1 {
				from = "../examples/windows-iis"
			}
			if err := os.CopyFS(root, os.DirFS(from)); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, "lab.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("template-%02d", n)
			raw = []byte(regexp.MustCompile(`(?m)^name: .*`).ReplaceAllString(string(raw), "name: "+name))
			if n == 0 {
				raw = []byte(strings.ReplaceAll(string(raw), "    access:", "    hostnamePeers: []\n    access:"))
				raw = append(raw, []byte("  scorer:\n    image: services\n    cpu: 1\n    memory: 1Gi\n    disk: 20Gi\n    hostnamePeers: []\n    access: [ssh]\n")...)
			}
			if err = os.WriteFile(filepath.Join(root, "lab.yaml"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			// Explicit build selections keep unused handover files out of image inputs.
			if err = os.WriteFile(filepath.Join(root, "handover.txt"), []byte(strings.Repeat("Investigation notes.\n", 8192)), 0600); err != nil {
				t.Fatal(err)
			}
			// Distinct build inputs keep the 30 journal images unique, while
			// superseded revisions still exercise the protection rules.
			if err := filepath.WalkDir(filepath.Join(root, "images"), func(p string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				if filepath.Ext(p) != ".yml" && filepath.Ext(p) != ".ps1" {
					return nil
				}
				content, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				content = append(content, []byte(fmt.Sprintf("\n# fixture template %d revision %d\n", n, revision))...)
				return os.WriteFile(p, content, 0600)
			}); err != nil {
				t.Fatal(err)
			}
			src, b, err := instructor.LoadTemplate(root, true)
			if err != nil {
				t.Fatal(err)
			}
			for j := len(b.Stages); j < 20; j++ {
				b.Stages = append(b.Stages, instructor.Stage{ID: fmt.Sprintf("stage-%02d", j), Title: "Investigation", Instructions: strings.Repeat("Inspect **service** configuration and retain evidence.\n", 60), Solution: strings.Repeat("PRIVATE solution. ", 80), Completion: instructor.Completion{Answer: &instructor.Answer{Accepted: []string{"private"}, CaseSensitive: true}}})
			}
			if n == 0 {
				b.Visibility.Audience = "coaches"
				b.Visibility.HiddenMachines = []string{"scorer"}
				b.LiveScoring = &instructor.LiveScoring{Machine: "scorer", Port: 18080, Path: "/scoreboard.json", StaleAfterSeconds: 180}
			}
			id := fmt.Sprintf("%024x", n*4+revision+1)
			at := time.Date(2026, 10, revision+1, 12, 0, 0, 0, time.UTC)
			if err = s.Store.importRevision(ctx, id, src, b, "Student handover", strings.Repeat(fmt.Sprint(revision), 40), at, "Revise investigation"); err != nil {
				t.Fatal(err)
			}
			p, err := s.plan(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			keys := []string{}
			for _, im := range p.Images {
				if im.BuildProgram() != "" {
					keys = append(keys, im.CacheKey)
				}
			}
			// Keep saved keys as real build jobs do; protectedRevisionKeys still plans heads.
			if err = s.Store.Put(ctx, "template-images", id, keys); err != nil {
				t.Fatal(err)
			}
			if revision == n%4 {
				for _, im := range p.Images {
					if len(e.images) < 30 {
						e.images = append(e.images, lc.ImageRecord{Key: im.CacheKey, Name: im.Name})
						e.builds = append(e.builds, lc.Command{Image: im, Source: src, Environment: s.env})
					}
				}
			}
			if n == 0 {
				meetingID = id
				meetingSource = src
				meetingBundle = *b
				meetingPlan = p
			}
		}
	}
	now := time.Now().UTC()
	m := Meeting{ID: "read-meeting", Template: meetingID, Status: "ready", Created: now.Add(-time.Minute), Expires: now.Add(time.Hour)}
	if err := s.Store.CreateMeeting(ctx, m, 6, 6, true); err != nil {
		t.Fatal(err)
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	cookies := []string{}
	for n := range 39 {
		cookie := fmt.Sprintf("read-user-%02d", n)
		cookies = append(cookies, cookie)
		v := Session{ID: digest(cookie), User: User{ID: cookie, Name: cookie, Admin: n >= 36}, Expires: now.Add(time.Hour), Checked: now, CSRF: "csrf"}
		for kind, value := range map[string]any{"session": v, "user": v.User} {
			id := v.ID
			if kind == "user" {
				id = cookie
			}
			if err = s.Store.Put(ctx, kind, id, value); err != nil {
				t.Fatal(err)
			}
		}
		if n < 36 {
			team := teams[n/6]
			if _, err = s.Store.DB.Exec(`INSERT INTO memberships(user_id,team_id) VALUES(?,?)`, cookie, team.ID); err != nil {
				t.Fatal(err)
			}
			dctx, dcancel := context.WithCancel(ctx)
			t.Cleanup(dcancel)
			accessID := digest(team.ID + "\x00workstation\x00" + cookie)[:24]
			s.desktops[accessID] = &desktopSession{ctx: dctx, cancel: dcancel, record: accessRecord{ID: accessID, UserID: cookie, SessionID: v.ID, TeamID: team.ID, InstanceID: team.ID, Machine: "workstation", Expires: now.Add(time.Hour)}, info: lc.AccessInfo{Expires: now.Add(time.Hour)}}
		}
	}
	for _, team := range teams {
		if _, err = s.Store.DB.Exec(`UPDATE teams SET instance_id=?,status='ready' WHERE id=?`, team.ID, team.ID); err != nil {
			t.Fatal(err)
		}
		e.instances = append(e.instances, lc.Instance{ID: team.ID, Status: "ready", Plan: meetingPlan})
		cache := scoreCache{Attempted: now, Snapshot: &ScoreSnapshot{Version: 1, Round: 1, GeneratedAt: now, Services: []ScoredService{{ID: "web", Name: "Website", State: "up", Earned: 10, Possible: 10}}}}
		team.InstanceID = team.ID
		team.Status = "ready"
		if err = s.Store.Put(ctx, "live-score-cache", guestScoreKey(team, m, meetingSource, *meetingBundle.LiveScoring), cache); err != nil {
			t.Fatal(err)
		}
	}
	for len(e.instances) < 40 {
		e.instances = append(e.instances, lc.Instance{ID: fmt.Sprint("historical-", len(e.instances)), Status: "destroyed", Plan: meetingPlan})
	}
	if err = s.Store.Put(ctx, "check-schedule", m.ID, CheckSchedule{MeetingID: m.ID, IntervalSeconds: 60, Next: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	publication := LearningPublication{ID: "read-lab", Template: meetingID, Title: "Investigation", Published: true, PublishedAt: now, Steps: []LearningStepInput{{ID: "inspect", Kind: "answer", Instructions: "Inspect services", Answer: "private"}}}
	// The ordinary learning cohort uses a visible revision, separately from the coach range.
	publication.Template = fmt.Sprintf("%024x", 9)
	if err = s.Store.Put(ctx, "learning-publication", publication.ID, publication); err != nil {
		t.Fatal(err)
	}
	if len(e.images) != 30 || len(e.instances) != 40 {
		t.Fatal("incomplete load inventory")
	}
	seen := map[string]bool{}
	for _, image := range e.images {
		if seen[image.Key] {
			t.Fatal("duplicate load image key")
		}
		seen[image.Key] = true
	}
	return s, e, cookies
}

// TestMeetingReadLoad compresses the UI's polling schedule into virtual minutes.
// Endpoint CPU labels support: go tool pprof -tags /path/cpu.pprof. No cloud,
// desktop video frames, real OIDC, SSH handshakes or guest programs are simulated.
func TestMeetingReadLoad(t *testing.T) {
	if os.Getenv("LABCENTRAL_READ_LOAD") != "1" {
		t.Skip("set LABCENTRAL_READ_LOAD=1")
	}
	s, e, cookies := meetingReadFixture(t)
	handler := s.Handler()
	virtualNow := time.Now()
	s.inventoryClock = func() time.Time { return virtualNow } // read-load-clock
	minutes := 2
	if raw := os.Getenv("LABCENTRAL_READ_MINUTES"); raw != "" {
		var err error
		minutes, err = strconv.Atoi(raw)
		if err != nil || minutes < 1 {
			t.Fatal("invalid minutes")
		}
	}
	type metric struct {
		n     int
		wall  time.Duration
		bytes uint64
	}
	stats := map[string]*metric{}
	for second := range minutes * 60 {
		virtualNow = virtualNow.Add(time.Second) // read-load-clock
		// Advance scheduled guest work at virtual intervals without waiting for a
		// real minute. Failures remain unavailable through the actual hosted paths.
		if second%60 == 0 {
			pprof.Do(context.Background(), pprof.Labels("endpoint", "background/checks"), func(ctx context.Context) {
				m, err := s.Store.Meeting(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.Store.Put(ctx, "check-schedule", m.ID, CheckSchedule{MeetingID: m.ID, IntervalSeconds: 60, Next: time.Now().Add(-time.Second)}); err != nil {
					t.Fatal(err)
				}
				s.scheduleChecks(ctx)
				jobs, err := s.Store.QueuedJobs(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, j := range jobs {
					if j.Kind != "check" {
						continue
					}
					_ = s.runCheck(ctx, j)
					if _, err = s.Store.DB.ExecContext(ctx, "UPDATE jobs SET status='complete' WHERE id=?", j.ID); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
		if second%15 == 0 {
			setLoadScoreAttempts(t, s, time.Now().Add(-15*time.Second))
		}

		for n, cookie := range cookies {
			paths := []string{}
			if second%10 == 0 {
				paths = append(paths, "/api/me")
			}
			if n >= 36 && second%2 == 0 {
				paths = append(paths, "/api/admin")
			}
			if second%15 == 0 {
				paths = append(paths, "/api/live-scoreboard")
			}
			if n < 36 && second%3 == 0 {
				paths = append(paths, "/api/machines/workstation/desktop/status")
			}
			if n < 12 && second%30 == 0 {
				paths = append(paths, "/api/templates")
			}
			if n >= 12 && n < 24 && second%30 == 0 {
				paths = append(paths, "/api/learning")
			}
			if n >= 24 && n < 36 && second%3 == 0 {
				paths = append(paths, "/api/learning/read-lab")
			}
			for _, path := range paths {
				r := httptest.NewRequest("GET", s.Config.Origin+path, nil)
				r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: cookie})
				var a, b runtime.MemStats
				runtime.ReadMemStats(&a)
				start := time.Now()
				w := httptest.NewRecorder()
				pprof.Do(r.Context(), pprof.Labels("endpoint", path), func(ctx context.Context) { handler.ServeHTTP(w, r.WithContext(ctx)) })
				elapsed := time.Since(start)
				runtime.ReadMemStats(&b)
				if w.Code != 200 {
					t.Fatalf("%s user=%d: %d %s", path, n, w.Code, w.Body.String())
				}
				v := stats[path]
				if v == nil {
					v = &metric{}
					stats[path] = v
				}
				v.n++
				v.wall += elapsed
				v.bytes += b.TotalAlloc - a.TotalAlloc
				if path == "/api/live-scoreboard" && n == 0 {
					// Freeze this virtual interval after its first refresh, even if CPU
					// profiling or race instrumentation takes more than ten real seconds.
					setLoadScoreAttempts(t, s, time.Now().Add(time.Hour))
				}
			}
		}
	}
	for path, m := range stats {
		t.Logf("READ_LOAD endpoint=%s requests/min=%d wall_ms/min=%.2f alloc_MiB/min=%.2f", path, m.n/minutes, float64(m.wall.Microseconds())/1000/float64(minutes), float64(m.bytes)/(1<<20)/float64(minutes))
	}
	t.Logf("READ_LOAD engine_calls=%d simulated_minutes=%d", e.calls.Load(), minutes)
	t.Logf("READ_LOAD guest_attempts=%d (fixture transport failures, no handshakes)", e.guestCalls.Load())
}

func setLoadScoreAttempts(t *testing.T, s *Service, attempted time.Time) {
	t.Helper()
	ctx := context.Background()
	m, teams, _, src, config, err := s.scoreAccess(ctx, Session{User: User{Admin: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		key := guestScoreKey(team, m, src, config)
		var cache scoreCache
		if err = s.Store.Get(ctx, "live-score-cache", key, &cache); err != nil {
			t.Fatal(err)
		}
		cache.Attempted = attempted
		if err = s.Store.Put(ctx, "live-score-cache", key, cache); err != nil {
			t.Fatal(err)
		}
	}
}
