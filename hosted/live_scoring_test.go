package hosted

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

type scoreEngine struct {
	Engine
	src           lc.Source
	address, host string
	key           []byte
	badOwner      bool
	entered       chan struct{}
	release       chan struct{}
}

func (e *scoreEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	m := lc.PlannedMachine{Name: "scorer", Address: e.address, HostPublicKey: e.host, Spec: e.src.Lab.Machines["scorer"]}
	client := lc.PlannedMachine{Name: "workstation", Spec: e.src.Lab.Machines["workstation"]}
	return lc.Instance{ID: id, Status: "ready", Plan: lc.Plan{Source: e.src, Machines: []lc.PlannedMachine{m, client}}}, nil
}
func (e *scoreEngine) WithSSHSession(ctx context.Context, id, name string, fn func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		default:
		}
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	i, _ := e.Inspect(ctx, id)
	m := i.Plan.Machines[0]
	owner := id
	if e.badOwner {
		owner = "foreign"
	}
	return fn(i, m, lc.Resource{Kind: "machine", Name: name, Owner: owner, Complete: true, Address: e.address, PrivateAddress: m.Address}, e.key)
}

func liveScoreFixture(t *testing.T) (*Service, Session, []Team, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	s, v, teams, _ := rangeAccessFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte("version: 3\nname: live-range\nimages:\n  linux: {base: ubuntu-24.04, build: {playbook: init.yml, files: [init.yml]}}\nmachines:\n  scorer: {image: linux, cpu: 2, memory: 2Gi, disk: 20Gi, access: [ssh], hostnamePeers: []}\n  workstation: {image: linux, cpu: 2, memory: 2Gi, disk: 20Gi, access: [ssh], hostnamePeers: []}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.yml"), []byte("- hosts: all\n  tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := InstructorBundle{Version: 3, Visibility: instructor.Visibility{Audience: "coaches", HiddenMachines: []string{"scorer"}}, LiveScoring: &instructor.LiveScoring{Machine: "scorer", Port: 18080, Path: "/scoreboard.json", StaleAfterSeconds: 30}}
	if err := b.Validate(src); err != nil {
		t.Fatal(err)
	}
	var template Template
	if err := s.Store.Get(ctx, "template", "pinned", &template); err != nil {
		t.Fatal(err)
	}
	view := instructorView(b)
	template.Instructor = &view
	if err := s.Store.Put(ctx, "template", "pinned", template); err != nil {
		t.Fatal(err)
	}
	for kind, value := range map[string]any{"source": src, "template-instructor": b} {
		if err := s.Store.Put(ctx, kind, "pinned", value); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := s.Store.Meeting(ctx)
	m.Created = time.Now().Add(-time.Hour)
	if err := s.Store.Put(ctx, "meeting", "current", m); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "live-red-blue", m.ID, true); err != nil {
		t.Fatal(err)
	}
	// Use one active team to isolate coalescing from the number of ranges.
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='waiting' WHERE id!=?`, teams[0].ID); err != nil {
		t.Fatal(err)
	}
	calls, mode := &atomic.Int32{}, &atomic.Int32{}
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/scoreboard.json" || r.Method != "GET" {
			t.Error("unexpected feed request", r.Method, r.URL)
		}
		switch mode.Load() {
		case 1:
			http.Error(w, "PRIVATE-SCORER-ERROR", 500)
			return
		case 2:
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
			w.WriteHeader(302)
			return
		case 3:
			io.WriteString(w, strings.Repeat("x", (128<<10)+1))
			return
		case 4:
			io.WriteString(w, `{"version":1,"password":"PRIVATE-CREDENTIAL"}`)
			return
		}
		timestamp := time.Now().UTC()
		round := 5
		if mode.Load() == 7 {
			round = 6
		}
		if mode.Load() == 5 {
			timestamp = timestamp.Add(-time.Minute)
		}
		json.NewEncoder(w).Encode(ScoreSnapshot{Version: 1, Round: round, GeneratedAt: timestamp, Services: []ScoredService{{ID: "web", Name: "Web service", State: "up", Earned: 80, Possible: 100}, {ID: "mail", Name: "Mail service", State: "down", Earned: 40, Possible: 100}}})
	}))
	t.Cleanup(feed.Close)
	ssh := sshtest.New(t, func(address string) string {
		if address != "127.0.0.1:18080" {
			t.Error("SSH attempted arbitrary destination", address)
			return ""
		}
		return strings.TrimPrefix(feed.URL, "http://")
	}, nil)
	key, _ := sshtest.Key(t)
	s.Engine = &scoreEngine{Engine: s.Engine, src: src, address: ssh.Address, host: ssh.HostKey, key: key}
	return s, v, teams, calls, mode
}

func expireScoreCache(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.Store.List(ctx, "live-score-cache")
	if err != nil {
		t.Fatal(err)
	}
	// Derive the exact active cache key rather than rewriting arbitrary objects.
	m, teams, _, src, config, err := s.scoreAccess(ctx, Session{User: User{Admin: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		key := digest(m.ID + "\x00" + team.ID + "\x00" + team.InstanceID + "\x00" + m.Template + "\x00" + src.Digest + "\x00" + jsonValue(config))
		var c scoreCache
		if s.Store.Get(ctx, "live-score-cache", key, &c) == nil {
			c.Attempted = time.Now().Add(-time.Minute)
			if err := s.Store.Put(ctx, "live-score-cache", key, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(rows) == 0 {
		t.Fatal("no persisted score cache")
	}
}

func TestLiveScoringGuestFeedAndOptInIsolation(t *testing.T) {
	s, _, teams, calls, _ := liveScoreFixture(t)
	board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	if board.Teams[0].Earned != 120 || board.Teams[0].Possible != 200 || board.Teams[0].Status != "live" || len(board.Teams[0].Services) != 2 {
		t.Fatal(board)
	}
	if board.OwnTeamID != teams[0].ID {
		t.Fatal("student team was not derived from membership", board)
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			w := request(s, "GET", "/api/live-scoreboard", "", "", "")
			if w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("students caused repeated guest polling", calls.Load())
	}
	m, _ := s.Store.Meeting(context.Background())
	if err := s.Store.Delete(context.Background(), "live-red-blue", m.ID); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/live-scoreboard", "", "", "")
	if w.Code != 403 || calls.Load() != 1 {
		t.Fatal("ordinary lab contacted scorer", w.Code, calls.Load())
	}
	view := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if view.Lab.LiveScoring {
		t.Fatal("ordinary lab exposed live scoreboard")
	}
}

func TestLiveScoringStaleCacheRedactionAndPersistence(t *testing.T) {
	s, _, _, calls, mode := liveScoreFixture(t)
	decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	for _, failure := range []int32{1, 2, 3, 4} {
		expireScoreCache(t, s)
		mode.Store(failure)
		w := request(s, "GET", "/api/live-scoreboard", "", "", "")
		board := decodeLearning[LiveScoreboard](t, w, 200)
		if board.Teams[0].Status != "stale" || board.Teams[0].Earned != 120 || strings.Contains(w.Body.String(), "PRIVATE-") {
			t.Fatal("scorer failure lost last good score or disclosed errors", w.Body.String())
		}
	}
	if calls.Load() != 5 {
		t.Fatal("redirect followed or failed polling unbounded", calls.Load())
	}
	// A new service process uses persisted last-good data without another poll.
	s2 := &Service{Config: s.Config, Store: s.Store, Engine: s.Engine, Identity: s.Identity}
	board := decodeLearning[LiveScoreboard](t, request(s2, "GET", "/api/live-scoreboard", "", "", ""), 200)
	if board.Teams[0].Status != "stale" || calls.Load() != 5 {
		t.Fatal("last score did not survive service restart", board, calls.Load())
	}
}

func TestLiveScoringAuthorizationAndAllocationPinning(t *testing.T) {
	for _, change := range []string{"membership", "ended", "expired", "source", "owner", "no-feed"} {
		t.Run(change, func(t *testing.T) {
			s, v, _, calls, _ := liveScoreFixture(t)
			ctx := context.Background()
			m, _ := s.Store.Meeting(ctx)
			switch change {
			case "membership":
				s.Store.DB.ExecContext(ctx, `DELETE FROM memberships WHERE user_id=?`, v.User.ID)
			case "ended":
				m.Status = "ended"
				s.Store.Put(ctx, "meeting", "current", m)
			case "expired":
				m.Expires = time.Now().Add(-time.Second)
				s.Store.Put(ctx, "meeting", "current", m)
			case "source":
				e := s.Engine.(*scoreEngine)
				e.src.Digest = "changed"
			case "owner":
				s.Engine.(*scoreEngine).badOwner = true
			case "no-feed":
				s.Store.Delete(ctx, "template-instructor", m.Template)
			}
			w := request(s, "GET", "/api/live-scoreboard", "", "", "")
			if change == "source" || change == "owner" {
				b := decodeLearning[LiveScoreboard](t, w, 200)
				if b.Teams[0].Status != "unavailable" {
					t.Fatal("untrusted allocation supplied score", b)
				}
			} else if w.Code != 403 {
				t.Fatal(w.Code, w.Body.String())
			}
			if calls.Load() != 0 {
				t.Fatal("unauthorized request reached scorer", calls.Load())
			}
		})
	}
}

func TestLiveScoringFeedValidation(t *testing.T) {
	now := time.Now().UTC()
	meeting := Meeting{Created: now.Add(-time.Hour)}
	valid := ScoreSnapshot{Version: 1, Round: 1, GeneratedAt: now, Services: []ScoredService{{ID: "web", Name: "Web", State: "up", Earned: 1, Possible: 2}}}
	for _, change := range []string{"future", "negative", "overrun", "duplicate", "state", "old-meeting", "empty"} {
		snapshot := valid
		snapshot.Services = append([]ScoredService(nil), valid.Services...)
		switch change {
		case "future":
			snapshot.GeneratedAt = now.Add(time.Hour)
		case "negative":
			snapshot.Services[0].Earned = -1
		case "overrun":
			snapshot.Services[0].Earned = 3
		case "duplicate":
			snapshot.Services = append(snapshot.Services, snapshot.Services[0])
		case "state":
			snapshot.Services[0].State = "<script>"
		case "old-meeting":
			snapshot.GeneratedAt = meeting.Created.Add(-time.Hour)
		case "empty":
			snapshot.Services = nil
		}
		if validateScore(snapshot, meeting, now) == nil {
			t.Fatal("accepted invalid score", change)
		}
	}
}

func TestLiveScoringStandingsHideOtherTeamsServices(t *testing.T) {
	s, v, teams, _, _ := liveScoreFixture(t)
	ctx := context.Background()
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready' WHERE id=?`, teams[1].ID); err != nil {
		t.Fatal(err)
	}
	board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	for _, team := range board.Teams {
		if team.TeamID != teams[0].ID && len(team.Services) != 0 {
			t.Fatal("student received another team's service details", team)
		}
	}
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	board = decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	for _, team := range board.Teams {
		if team.TeamID == teams[1].ID && len(team.Services) != 2 {
			t.Fatal("coach service breakdown absent", team)
		}
	}
}

func TestLiveScoringMeetingOptInPersistsAndDoesNotCarryForward(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	m := Meeting{ID: "red-blue", Status: "waiting", Created: time.Now(), Expires: time.Now().Add(time.Hour)}
	if err := store.CreateMeeting(ctx, m, 1, 2, true); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store}
	if !s.liveRedBlue(ctx, m.ID) {
		t.Fatal("live option was not persisted")
	}
	m.Status = "ended"
	if err := store.Put(ctx, "meeting", "current", m); err != nil {
		t.Fatal(err)
	}
	m.ID = "ordinary"
	m.Status = "waiting"
	if err := store.CreateMeeting(ctx, m, 1, 2); err != nil {
		t.Fatal(err)
	}
	if s.liveRedBlue(ctx, m.ID) {
		t.Fatal("live scoring leaked into an ordinary meeting")
	}
}

func TestLiveScoringRevalidatesAllocationAfterGuestRead(t *testing.T) {
	s, _, teams, _, _ := liveScoreFixture(t)
	engine := s.Engine.(*scoreEngine)
	engine.entered = make(chan struct{}, 1)
	engine.release = make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(s, "GET", "/api/live-scoreboard", "", "", "") }()
	select {
	case <-engine.entered:
	case <-time.After(3 * time.Second):
		close(engine.release)
		t.Fatal("guest read never started")
	}
	_, err := s.Store.DB.ExecContext(context.Background(), `UPDATE teams SET instance_id='replacement-instance' WHERE id=?`, teams[0].ID)
	close(engine.release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Code != 403 {
			t.Fatal("old allocation score escaped after replacement", result.Code, result.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest read did not finish")
	}
}

func TestLiveScoringColdPollsCoalesce(t *testing.T) {
	s, _, _, calls, _ := liveScoreFixture(t)
	engine := s.Engine.(*scoreEngine)
	engine.entered = make(chan struct{}, 1)
	engine.release = make(chan struct{})
	var group sync.WaitGroup
	for range 10 {
		group.Go(func() {
			result := request(s, "GET", "/api/live-scoreboard", "", "", "")
			if result.Code != 200 {
				t.Error(result.Code, result.Body.String())
			}
		})
	}
	select {
	case <-engine.entered:
	case <-time.After(3 * time.Second):
		close(engine.release)
		t.Fatal("guest read never started")
	}
	close(engine.release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("cold concurrent requests replayed guest reads", calls.Load())
	}
}

func TestLiveScoringMeetingCreationRequiresExplicitOptionAndFeed(t *testing.T) {
	for _, option := range []string{"ordinary", "live", "missing-feed"} {
		t.Run(option, func(t *testing.T) {
			s, v, _, _, _ := liveScoreFixture(t)
			ctx := context.Background()
			v.User.Admin = true
			if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
				t.Fatal(err)
			}
			old, _ := s.Store.Meeting(ctx)
			old.Status = "ended"
			if err := s.Store.Put(ctx, "meeting", "current", old); err != nil {
				t.Fatal(err)
			}
			if option == "missing-feed" {
				if err := s.Store.Delete(ctx, "template-instructor", "pinned"); err != nil {
					t.Fatal(err)
				}
			}
			body := jsonValue(CreateMeeting{Name: "Practice", Template: "pinned", Hours: 1, TeamCount: 1, Capacity: 2, LiveRedBlue: option != "ordinary"})
			response := request(s, "POST", "/api/admin/meeting", body, s.Config.Origin, v.CSRF)
			if option == "missing-feed" {
				if response.Code != 409 {
					t.Fatal("missing feed accepted", response.Code, response.Body.String())
				}
				return
			}
			meeting := decodeLearning[Meeting](t, response, 201)
			if s.liveRedBlue(ctx, meeting.ID) != (option == "live") {
				t.Fatal("meeting opt-in did not match request")
			}
		})
	}
}

func TestLiveScoringCompletedRoundsCannotBeRepaintedAsFresh(t *testing.T) {
	s, _, _, _, mode := liveScoreFixture(t)
	first := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	for _, change := range []int32{5, 6} {
		expireScoreCache(t, s)
		mode.Store(change)
		board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
		if board.Teams[0].Status != "stale" || !board.Teams[0].Updated.Equal(*first.Teams[0].Updated) {
			t.Fatal("old round was repainted as current", board)
		}
	}
	expireScoreCache(t, s)
	mode.Store(7)
	board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	if board.Teams[0].Status != "live" || board.Teams[0].Round != 6 {
		t.Fatal("next completed round failed to restore live scoring", board)
	}
}

func TestLiveScoringMWCCDCExportContract(t *testing.T) {
	// Generated by the companion kit's actual worker with mocked service checks,
	// empty signed probes, a temporary DB and a fixed public completion timestamp.
	// This proves the JSON contract, not real service health or AWS forwarding.
	raw, err := os.ReadFile("testdata/mwccdc-live-score.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot ScoreSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if err := validateScore(snapshot, Meeting{Created: snapshot.GeneratedAt.Add(-time.Minute)}, snapshot.GeneratedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	passed, earned, possible := 0, 0, 0
	for _, service := range snapshot.Services {
		if service.State == "up" {
			passed++
		}
		earned += service.Earned
		possible += service.Possible
	}
	if len(snapshot.Services) != 25 || passed != 21 || earned != 2100 || possible != 2500 {
		t.Fatal("kit scoring projection differs from hosted contract", len(snapshot.Services), passed, earned, possible)
	}
}
