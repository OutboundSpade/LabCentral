package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
)

type consoleTestEngine struct {
	Engine
	mu                  sync.Mutex
	active              string
	events              []string
	closeErr, errorOpen error
	relay               string
	src                 lc.Source
	destroyErr          error
}

func (e *consoleTestEngine) OpenDesktopWithOptions(ctx context.Context, instance, machine string, o lc.AccessOptions) (lc.AccessInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := digest(instance + "\x00" + machine + "\x00" + o.Principal)[:24]
	e.events = append(e.events, "open:"+o.Principal)
	if e.active != "" && e.active != id {
		return lc.AccessInfo{}, fmt.Errorf("overlapping grant")
	}
	if !o.RetainWorkspace {
		return lc.AccessInfo{}, fmt.Errorf("profile retention omitted")
	}
	e.active = id
	if e.errorOpen != nil {
		return lc.AccessInfo{}, e.errorOpen
	}
	return lc.AccessInfo{ID: id, URL: e.relay + "/launch", Username: "lab", Password: "synthetic", Expires: time.Now().Add(time.Hour)}, nil
}
func (e *consoleTestEngine) CloseAccess(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, "close:"+id)
	if e.closeErr != nil {
		return e.closeErr
	}
	if e.active == id {
		e.active = ""
	}
	return nil
}
func (e *consoleTestEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	p := lc.Plan{Source: e.src}
	for name, spec := range e.src.Lab.Machines {
		p.Machines = append(p.Machines, lc.PlannedMachine{Name: name, Spec: spec, OS: lc.OSFamily(e.src.Lab.Images[spec.Image].OS)})
	}
	return lc.Instance{ID: id, Status: "ready", Plan: p}, nil
}
func windowsFixture(t *testing.T) (*Service, Session, []Team, *consoleTestEngine) {
	return consoleFixture(t, "aws")
}
func consoleFixture(t *testing.T, platform string) (*Service, Session, []Team, *consoleTestEngine) {
	t.Helper()
	s, v, _ := learningFixture(t)
	ctx := context.Background()
	src, b, err := instructor.LoadTemplate("../examples/windows-iis", true)
	if err != nil {
		t.Fatal(err)
	}
	if platform == "proxmox" {
		s.env.Platform = "proxmox"
		for name, image := range src.Lab.Images {
			image.OS = "linux"
			src.Lab.Images[name] = image
		}
	}
	s.env.Bases["windows-2022"] = lc.Base{OS: "windows", ID: "ami-0123456789abcdef0", Identity: "ami-0123456789abcdef0", User: "Administrator", DiskGiB: 30}
	s.env.Build.InstanceType = "t3.medium"
	s.env.Build.WindowsDCVURL = "https://example.org/dcv-pinned.msi"
	s.env.Build.WindowsDCVSHA256 = strings.Repeat("b", 64)
	if err = s.Store.Put(ctx, "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	teams := setupMeeting(t, s.Store)
	m, _ := s.Store.Meeting(ctx)
	m.Template = "pinned"
	if err = s.Store.Put(ctx, "meeting", "current", m); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready',instance_id='instance-' || id`); err != nil {
		t.Fatal(err)
	}
	teams, err = s.Store.Teams(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	seat(t, s, v, teams[0])
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "relay", Value: "synthetic"})
		w.WriteHeader(303)
	}))
	t.Cleanup(relay.Close)
	e := &consoleTestEngine{Engine: s.Engine, relay: relay.URL, src: src}
	s.Engine = e
	t.Cleanup(func() {
		s.desktopMu.Lock()
		defer s.desktopMu.Unlock()
		for _, d := range s.desktops {
			d.cancel()
		}
	})
	return s, v, teams, e
}
func seat(t *testing.T, s *Service, v Session, team Team) {
	t.Helper()
	ctx := context.Background()
	if err := s.Store.BeginSwitch(ctx, v.User.ID, team.ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
}
func consoleUser(t *testing.T, s *Service, name string, admin bool) Session {
	t.Helper()
	v := Session{ID: digest(name), User: User{ID: name, Name: name, Admin: admin}, CSRF: "csrf", Expires: time.Now().Add(time.Hour), Checked: time.Now()}
	for _, o := range []struct {
		k string
		v any
	}{{"session", v}, {"user", v.User}} {
		id := v.ID
		if o.k == "user" {
			id = v.User.ID
		}
		if err := s.Store.Put(context.Background(), o.k, id, o.v); err != nil {
			t.Fatal(err)
		}
	}
	return v
}
func consoleRequest(s *Service, v Session, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", s.Config.Origin+path, strings.NewReader(`{}`))
	r.Header.Set("Origin", s.Config.Origin)
	r.Header.Set("X-CSRF-Token", v.CSRF)
	cookie := v.User.ID
	if v.User.ID == "student" {
		cookie = "cookie"
	}
	r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func consoleRecord(v Session, team Team) accessRecord {
	return accessRecord{ID: digest(team.InstanceID + "\x00server\x00" + v.User.ID)[:24], UserID: v.User.ID, SessionID: v.ID, TeamID: team.ID, InstanceID: team.InstanceID, Machine: "server", OS: "windows", Expires: v.Expires}
}
func TestWindowsConflictTakeoverAndDisplacedCookie(t *testing.T) {
	testConsoleConflictTakeoverAndDisplacedCookie(t, "aws")
}
func testConsoleConflictTakeoverAndDisplacedCookie(t *testing.T, platform string) {
	s, a, teams, e := consoleFixture(t, platform)
	b := consoleUser(t, s, "Teammate", false)
	seat(t, s, b, teams[0])
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	old := s.desktops[consoleRecord(a, teams[0]).ID]
	conflict := decodeLearning[DesktopConflict](t, consoleRequest(s, b, "/api/access"), 409)
	if conflict.Code != "desktop_held" || conflict.HolderName != a.User.Name || conflict.TakenOverBy != "" || len(e.events) != 1 {
		t.Fatal(conflict, e.events)
	}
	decodeLearning[Launch](t, consoleRequest(s, b, "/api/access/takeover"), 200)
	if len(e.events) != 3 || !strings.HasPrefix(e.events[1], "close:") || e.events[2] != "open:Teammate" || old.ctx.Err() == nil {
		t.Fatal("revoke-before-grant", e.events)
	}
	state, err := s.desktopState(context.Background(), consoleRecord(a, teams[0]))
	if err != nil || state.Code != "desktop_taken_over" || state.TakenOverBy != "Teammate" || !state.CanTakeOver {
		t.Fatal(state, err)
	}
	r := httptest.NewRequest("GET", s.Config.DesktopOrigin+"/status", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-labcentral_desktop", Value: old.cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	state = decodeLearning[DesktopState](t, w, 200)
	if state.TakenOverBy != "Teammate" {
		t.Fatal(state)
	}
	r = httptest.NewRequest("GET", s.Config.DesktopOrigin+"/credentials", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-labcentral_desktop", Value: old.cookie})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if got := decodeLearning[DesktopConflict](t, w, 409); got.Code != "desktop_taken_over" {
		t.Fatal(got)
	}
	// Returning to the same team's desktop requires an explicit take back.
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access/takeover"), 200)
	state, err = s.desktopState(context.Background(), consoleRecord(a, teams[0]))
	if err != nil || state.Code != "owned" || state.TakenOverBy != "" {
		t.Fatal(state, err)
	}
}
func TestWindowsTakeoverAuthorization(t *testing.T) { testConsoleTakeoverAuthorization(t, "aws") }
func testConsoleTakeoverAuthorization(t *testing.T, platform string) {
	for _, tc := range []struct {
		name                        string
		admin, member, otherMeeting bool
		path                        string
		want                        int
	}{
		{name: "teammate", member: true, path: "/api/access/takeover", want: 200},
		{name: "coach", admin: true, path: "target", want: 200},
		{name: "admin", admin: true, path: "target", want: 200},
		{name: "other-team", path: "target", want: 403},
		{name: "outside", path: "target", want: 403},
		{name: "other-meeting", admin: true, otherMeeting: true, path: "target", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, teams, e := consoleFixture(t, platform)
			decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
			v := consoleUser(t, s, tc.name, tc.admin)
			if tc.member {
				seat(t, s, v, teams[0])
			} else if tc.name == "other-team" {
				seat(t, s, v, teams[1])
			}
			target := teams[0].ID
			if tc.otherMeeting {
				target = "other-meeting-team"
				if _, err := s.Store.DB.Exec(`INSERT INTO teams(id,meeting_id,name,capacity,instance_id,status) VALUES(?,'other','Other',2,'other-instance','ready')`, target); err != nil {
					t.Fatal(err)
				}
			}
			path := tc.path
			if path == "target" {
				path = "/api/admin/teams/" + target + "/desktop/takeover"
			}
			w := consoleRequest(s, v, path)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.want != 200 && len(e.events) != 1 {
				t.Fatal("unauthorized native operation", e.events)
			}
			if tc.want != 200 && strings.Contains(w.Body.String(), a.User.Name) {
				t.Fatal("holder name leaked")
			}
		})
	}
}
func TestWindowsUnknownRevokePersistsAcrossRestart(t *testing.T) {
	testConsoleUnknownRevokePersistsAcrossRestart(t, "aws")
}
func testConsoleUnknownRevokePersistsAcrossRestart(t *testing.T, platform string) {
	s, a, teams, e := consoleFixture(t, platform)
	b := consoleUser(t, s, "Next", false)
	seat(t, s, b, teams[0])
	ctx := context.Background()
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	old := consoleRecord(a, teams[0])
	e.closeErr = errors.New("response lost")
	w := consoleRequest(s, b, "/api/access/takeover")
	if got := decodeLearning[DesktopConflict](t, w, 503); got.Code != "desktop_outcome_unknown" || w.Header().Get("Retry-After") == "" {
		t.Fatal(got)
	}
	var holder desktopHolder
	if err := s.Store.Get(ctx, "desktop-holder", old.consoleKey(), &holder); err != nil || holder.Access.ID != old.ID {
		t.Fatal(holder, err)
	}
	if len(e.events) != 2 {
		t.Fatal(e.events)
	}
	// Open the same persisted hosted store with fresh relay/mutex state.
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next := &Service{Config: s.Config, Store: reopened, Engine: e, Identity: s.Identity, env: s.env, desktops: map[string]*desktopSession{}, tickets: map[string]launchTicket{}}
	if err = next.recoverAccess(ctx); err == nil {
		t.Fatal("unconfirmed revoke disappeared after restart")
	}
	if err = reopened.Get(ctx, "desktop-holder", old.consoleKey(), &holder); err != nil || holder.Access.ID != old.ID {
		t.Fatal(holder, err)
	}
	e.closeErr = nil
	if err = next.recoverAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Get(ctx, "desktop-holder", old.consoleKey(), &holder); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("holder not cleared after confirmation", err)
	}
	decodeLearning[Launch](t, consoleRequest(next, b, "/api/access/takeover"), 200)
	for _, d := range next.desktops {
		defer d.cancel()
	}
	state, err := next.desktopState(ctx, old)
	if err != nil || state.TakenOverBy != "Next" {
		t.Fatal(state, err)
	}
}
func TestWindowsConcurrentTakeoversNeverOverlap(t *testing.T) {
	testConsoleConcurrentTakeoversNeverOverlap(t, "aws")
}
func testConsoleConcurrentTakeoversNeverOverlap(t *testing.T, platform string) {
	s, a, teams, e := consoleFixture(t, platform)
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	var users []Session
	for i := 0; i < 3; i++ {
		v := consoleUser(t, s, fmt.Sprintf("member-%d", i), false)
		seat(t, s, v, teams[0])
		users = append(users, v)
	}
	var wg sync.WaitGroup
	codes := make(chan int, len(users))
	for _, v := range users {
		wg.Go(func() { codes <- consoleRequest(s, v, "/api/access/takeover").Code })
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("concurrent takeover failed", code)
		}
	}
	if len(e.events) != 7 {
		t.Fatal(e.events)
	}
	for i := 1; i < len(e.events); i += 2 {
		if !strings.HasPrefix(e.events[i], "close:") || !strings.HasPrefix(e.events[i+1], "open:") {
			t.Fatal(e.events)
		}
	}
}
func TestWindowsUnknownOpenRequiresConfirmedCleanup(t *testing.T) {
	testConsoleUnknownOpenRequiresConfirmedCleanup(t, "aws")
}
func testConsoleUnknownOpenRequiresConfirmedCleanup(t *testing.T, platform string) {
	s, a, teams, e := consoleFixture(t, platform)
	b := consoleUser(t, s, "Next", false)
	seat(t, s, b, teams[0])
	e.errorOpen = errors.New("open response lost")
	if w := consoleRequest(s, a, "/api/access"); w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code)
	}
	e.closeErr = errors.New("revoke response lost")
	if w := consoleRequest(s, b, "/api/access/takeover"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if len(e.events) != 2 {
		t.Fatal("unknown status created replacement", e.events)
	}
	e.errorOpen = nil
	e.closeErr = nil
	decodeLearning[Launch](t, consoleRequest(s, b, "/api/access/takeover"), 200)
}
func TestDesktopWindowsSessionIDAndBudgets(t *testing.T) {
	s, _, _ := serviceFor(t)
	if s.desktopOpenTimeout("linux") != 90*time.Second || s.desktopOpenTimeout("windows") != 10*time.Minute {
		t.Fatal("OS budgets")
	}
	if s.prepareTimeout(lc.Plan{}) != 30*time.Minute || s.prepareTimeout(lc.Plan{Machines: []lc.PlannedMachine{{OS: "windows"}}}) != 45*time.Minute {
		t.Fatal("prepare budgets")
	}
	s.Config.Desktop.WindowsOpenSeconds = 700
	s.Config.Worker.WindowsPrepareTimeoutMinutes = 60
	if s.desktopOpenTimeout("windows") != 700*time.Second || s.prepareTimeout(lc.Plan{Machines: []lc.PlannedMachine{{OS: "windows"}}}) != time.Hour {
		t.Fatal("configuration ignored")
	}
	if (DesktopOptions{WindowsOpenSeconds: -1}).Validate() == nil || (WorkerOptions{WindowsPrepareTimeoutMinutes: -1}).Validate() == nil {
		t.Fatal("invalid budget accepted")
	}
	if desktopSessionID(&desktopSession{record: accessRecord{OS: "windows"}, info: lc.AccessInfo{Username: "lab"}}) != "console" {
		t.Fatal("Windows session confused with account")
	}
	if desktopSessionID(&desktopSession{info: lc.AccessInfo{Username: "linux-user"}}) != "linux-user" {
		t.Fatal("Linux session changed")
	}
}
func TestWindowsCookieCredentialsUseConsole(t *testing.T) {
	s, a, teams, _ := windowsFixture(t)
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	d := s.desktops[consoleRecord(a, teams[0]).ID]
	r := httptest.NewRequest("GET", s.Config.DesktopOrigin+"/credentials", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-labcentral_desktop", Value: d.cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var credentials map[string]string
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &credentials) != nil || credentials["sessionID"] != "console" || credentials["username"] != "lab" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestWindowsSameUserReconnectAndMeetingOS(t *testing.T) {
	s, a, teams, e := windowsFixture(t)
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	original := s.desktops[consoleRecord(a, teams[0]).ID]
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	if len(e.events) != 2 || !strings.HasPrefix(e.events[1], "open:") || s.desktops[original.record.ID].cookie != original.cookie {
		t.Fatal("same user reconnect revoked its own grant", e.events)
	}
	out := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if out.Lab.DesktopState == nil || out.Lab.DesktopState.Code != "owned" || !out.Lab.DesktopActive {
		t.Fatal(out.Lab)
	}
	found := false
	for _, m := range out.Lab.Machines {
		found = found || m.Name == "server" && m.OS == "windows"
	}
	if !found {
		t.Fatal("meeting view lost OS", out.Lab.Machines)
	}
	// Holder and displacement names are inaccessible after a seat change starts.
	if err := s.Store.BeginSwitch(context.Background(), a.User.ID, teams[1].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", "/api/access/status", "", "", ""); w.Code != 403 || strings.Contains(w.Body.String(), a.User.Name) {
		t.Fatal("switching seat retained holder visibility", w.Code, w.Body.String())
	}
}

func (e *consoleTestEngine) Destroy(ctx context.Context, id string) error { return e.destroyErr }
func TestWindowsEndClearsHolderOnlyAfterConfirmedDestruction(t *testing.T) {
	testConsoleEndClearsHolderOnlyAfterConfirmedDestruction(t, "aws")
}
func testConsoleEndClearsHolderOnlyAfterConfirmedDestruction(t *testing.T, platform string) {
	s, a, teams, e := consoleFixture(t, platform)
	ctx := context.Background()
	decodeLearning[Launch](t, consoleRequest(s, a, "/api/access"), 200)
	record := consoleRecord(a, teams[0])
	e.closeErr = errors.New("guest unreachable")
	e.destroyErr = errors.New("native outcome unknown")
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.Status = "ending"
	if err = s.Store.Put(ctx, "meeting", "current", m); err != nil {
		t.Fatal(err)
	}
	j := Job{Kind: "end", Target: m.ID}
	if err = s.meetingOperation(ctx, j); err == nil {
		t.Fatal("unknown destruction reported complete")
	}
	if err = s.Store.Get(ctx, "desktop-holder", record.consoleKey(), new(desktopHolder)); err != nil {
		t.Fatal("holder lost before native confirmation", err)
	}
	e.destroyErr = nil
	if err = s.meetingOperation(ctx, j); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"desktop", "desktop-operation", "desktop-handoff", "desktop-cookie", "desktop-holder"} {
		rows, err := s.Store.List(ctx, kind)
		if err != nil || len(rows) != 0 {
			t.Fatal("destroyed desktop state retained", kind, len(rows), err)
		}
	}
}
