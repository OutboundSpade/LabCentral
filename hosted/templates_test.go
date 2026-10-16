package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type imageTestEngine struct {
	Engine
	mu        sync.Mutex
	images    []lc.ImageRecord
	instances []lc.Instance
	builds    []lc.Command
	fail      string
	removed   []string
	abandoned []string
}

func (e *imageTestEngine) Images(context.Context) ([]lc.ImageRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]lc.ImageRecord{}, e.images...), nil
}
func (e *imageTestEngine) List(context.Context) ([]lc.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]lc.Instance{}, e.instances...), nil
}
func (e *imageTestEngine) Builds(context.Context) ([]lc.Command, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]lc.Command{}, e.builds...), nil
}
func (e *imageTestEngine) RemoveImage(_ context.Context, key string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if key == e.fail {
		return fmt.Errorf("provider deletion unconfirmed")
	}
	for n, i := range e.images {
		if i.Key == key {
			e.images = append(e.images[:n], e.images[n+1:]...)
			e.removed = append(e.removed, key)
			return nil
		}
	}
	return errors.New("unknown image")
}
func (e *imageTestEngine) AbandonBuild(_ context.Context, key string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for n, c := range e.builds {
		if c.Image.CacheKey == key {
			e.builds = append(e.builds[:n], e.builds[n+1:]...)
			e.abandoned = append(e.abandoned, key)
			return nil
		}
	}
	return errors.New("unknown build")
}
func (e *imageTestEngine) BuildWithRecovery(_ context.Context, p lc.Plan) ([]lc.ImageRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, i := range p.Images {
		if i.BuildProgram() != "" {
			e.images = append(e.images, lc.ImageRecord{Key: i.CacheKey, Name: i.Name})
		}
	}
	return e.images, nil
}
func templateFixture(t *testing.T) (*Service, *imageTestEngine) {
	t.Helper()
	s, v, _ := serviceFor(t)
	v.User.Admin = true
	ctx := context.Background()
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	newer := old.Add(24 * time.Hour)
	for _, r := range []Template{{ID: "old", Name: "branch-services", Commit: strings.Repeat("a", 40), CommitTitle: "Initial Example Branch workstation", CommittedAt: &old, Built: true}, {ID: "new", Name: "branch-services", Commit: strings.Repeat("b", 40), CommitTitle: "Include netstat and verify workstation network tools", CommittedAt: &newer}} {
		if err := s.Store.Put(ctx, "template", r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	for id, keys := range map[string][]string{"old": {"web", "workstation"}, "new": {"new-web", "new-workstation"}} {
		if err := s.Store.Put(ctx, "template-images", id, keys); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Store.Put(ctx, "template-head", "branch-services", "new"); err != nil {
		t.Fatal(err)
	}
	e := &imageTestEngine{images: []lc.ImageRecord{{Key: "web", Name: "web"}, {Key: "workstation", Name: "workstation"}}}
	s.Engine = e
	return s, e
}
func finishImageJob(t *testing.T, s *Service, j Job, err error) {
	t.Helper()
	status := "complete"
	message := ""
	if err != nil {
		status = "failed"
		message = err.Error()
	}
	if _, e := s.Store.DB.Exec(`UPDATE jobs SET status=?,error=? WHERE id=?`, status, message, j.ID); e != nil {
		t.Fatal(e)
	}
}
func TestTemplateTimelineOrderingAndCounts(t *testing.T) {
	s, _ := templateFixture(t)
	r, err := s.templates(context.Background())
	if err != nil || len(r) != 2 {
		t.Fatal(r, err)
	}
	if r[0].ID != "new" || !r[0].Latest || r[1].ImagesReady != 2 || r[1].Latest {
		t.Fatal(r)
	}
}
func TestTemplateRemovalAuthorizationAndReferences(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		admin bool
		csrf  string
	}{{false, "csrf"}, {true, "wrong"}} {
		var v Session
		_ = s.Store.Get(ctx, "session", digest("cookie"), &v)
		v.User.Admin = tc.admin
		_ = s.Store.Put(ctx, "session", v.ID, v)
		w := request(s, http.MethodPost, "/api/admin/templates/remove-images", `{"id":"old"}`, s.Config.Origin, tc.csrf)
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := s.Store.Put(ctx, "meeting", "current", Meeting{ID: "m", Template: "old", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.queueTemplateOperation(ctx, "remove-images", "old"); err == nil {
		t.Fatal("active meeting images removable")
	}
	if err := s.Store.Delete(ctx, "meeting", "current"); err != nil {
		t.Fatal(err)
	}
	e.instances = []lc.Instance{{Status: "recovery_required", Plan: lc.Plan{Images: []lc.PlannedImage{{CacheKey: "workstation"}}}}}
	if _, err := s.queueTemplateOperation(ctx, "remove-images", "old"); err == nil {
		t.Fatal("retained lab images removable")
	}
	if len(e.removed) != 0 {
		t.Fatal(e.removed)
	}
}
func TestImageCleanupPartialFailureReplayAndRebuildFence(t *testing.T) {
	s, e := templateFixture(t)
	buildableRevisionFixture(t, s, "old")
	ctx := context.Background()
	e.fail = "workstation"
	j, err := s.queueTemplateOperation(ctx, "remove-images", "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.queueTemplateOperation(ctx, "build", "old"); err == nil {
		t.Fatal("build overlapped cleanup")
	}
	err = s.runJob(ctx, j)
	if err == nil {
		t.Fatal("uncertain deletion reported complete")
	}
	finishImageJob(t, s, j, err)
	var r Template
	_ = s.Store.Get(ctx, "template", "old", &r)
	if r.Built || !r.Removing || r.RemovedAt != nil {
		t.Fatal("cleanup intent lost", r)
	}
	if _, err = s.queueTemplateOperation(ctx, "build", "old"); err == nil {
		t.Fatal("unresolved cleanup allowed rebuild")
	}
	// A restarted worker must retain the removal fence and exact image keys.
	root := s.Store.Root
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = reopened
	t.Cleanup(func() { reopened.Close() })
	e.fail = ""
	retry, err := s.queueTemplateOperation(ctx, "remove-images", "old")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runJob(ctx, j); err == nil {
		t.Fatal("superseded cleanup replayed")
	}
	if err = s.runJob(ctx, retry); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, retry, nil)
	if err = s.runJob(ctx, retry); err != nil {
		t.Fatal(err)
	}
	_ = s.Store.Get(ctx, "template", "old", &r)
	if r.Removing || r.RemovedAt == nil || len(e.removed) != 2 {
		t.Fatal(r, e.removed)
	}
	if _, err = s.queueTemplateOperation(ctx, "build", "old"); err != nil {
		t.Fatal("cannot rebuild removed revision", err)
	}
}
func TestImageCleanupRetainsSharedImagesAndAbandonsUnfinishedBuild(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	if err := s.Store.Put(ctx, "template", "shared", Template{ID: "shared", Built: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "template-images", "shared", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	e.images = e.images[:1]
	e.builds = []lc.Command{{Image: lc.PlannedImage{CacheKey: "workstation", Name: "workstation"}}}
	j, err := s.queueTemplateOperation(ctx, "remove-images", "old")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if len(e.images) != 1 || len(e.removed) != 0 || len(e.abandoned) != 1 {
		t.Fatal("shared image removed or build leaked", e)
	}
}
func TestTemplateOperationConcurrentReservation(t *testing.T) {
	s, _ := templateFixture(t)
	var wg sync.WaitGroup
	var wins int
	var mu sync.Mutex
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.queueTemplateOperation(context.Background(), "remove-images", "old"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatal(wins)
	}
}
func TestCommitMetadata(t *testing.T) {
	at, title, err := parseCommitMetadata("2026-09-28T13:42:01+00:00\nInclude netstat\n")
	if err != nil || at.Hour() != 13 || title != "Include netstat" {
		t.Fatal(at, title, err)
	}
	if _, _, err = parseCommitMetadata("not a date\nsubject"); err == nil {
		t.Fatal("invalid date accepted")
	}
}
func TestTemplateImageKeysArePrivate(t *testing.T) {
	s, _ := templateFixture(t)
	w := request(s, "GET", "/api/admin", "", "", "")
	var v AdminView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "new-workstation") {
		t.Fatal("internal keys exposed")
	}
}

func TestTemplateBuildRecordsKeysAndReadiness(t *testing.T) {
	s, _ := prepareFixture(t)
	ctx := context.Background()
	src, err := lc.Load("../examples/single")
	if err != nil {
		t.Fatal(err)
	}
	s.env.Build = lc.BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	if err = s.Store.Put(ctx, "source", "revision", src); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Put(ctx, "template", "revision", Template{ID: "revision", Name: src.Lab.Name}); err != nil {
		t.Fatal(err)
	}
	e := &imageTestEngine{}
	s.Engine = e
	j, err := s.queueTemplateOperation(ctx, "build", "revision")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, j, nil)
	var revision Template
	if err = s.Store.Get(ctx, "template", "revision", &revision); err != nil {
		t.Fatal(err)
	}
	if !revision.Built || revision.BuiltAt == nil {
		t.Fatal(revision)
	}
	keys, err := s.templateKeys(ctx, "revision")
	if err != nil || len(keys) != 1 || len(e.images) != 1 || keys[0] != e.images[0].Key {
		t.Fatal(keys, e.images, err)
	}
	if err = s.runJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if len(e.images) != 1 {
		t.Fatal("completed build replayed")
	}
}
func TestImageRemovalBlocksMeetingCreation(t *testing.T) {
	s, _ := templateFixture(t)
	ctx := context.Background()
	if _, err := s.queueTemplateOperation(ctx, "remove-images", "old"); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/admin/meeting", `{"name":"Practice","template":"old"}`, s.Config.Origin, "csrf")
	if w.Code != 409 {
		t.Fatal("meeting accepted images pending removal", w.Code)
	}
}

func buildableRevisionFixture(t *testing.T, s *Service, id string) {
	t.Helper()
	other, _, _ := learningFixture(t)
	s.env = other.env
	var src lc.Source
	if err := other.Store.Get(context.Background(), "source", "pinned", &src); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(context.Background(), "source", id, src); err != nil {
		t.Fatal(err)
	}
}
