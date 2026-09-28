package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func setupMeeting(t *testing.T, s *Store) []Team {
	t.Helper()
	m := Meeting{ID: "meeting", Name: "Practice", Status: "ready", Expires: time.Now().Add(time.Hour)}
	if e := s.CreateMeeting(context.Background(), m, 5, 4); e != nil {
		t.Fatal(e)
	}
	teams, e := s.Teams(context.Background(), m.ID)
	if e != nil {
		t.Fatal(e)
	}
	return teams
}
func TestConcurrentSeatsAndPendingSwitch(t *testing.T) {
	s := testStore(t)
	teams := setupMeeting(t, s)
	ctx := context.Background()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.BeginSwitch(ctx, fmt.Sprint(i), teams[0].ID, "meeting") == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 4 {
		t.Fatalf("reserved %d seats", wins.Load())
	}
	now, _ := s.Teams(ctx, "meeting")
	if now[0].Members != 4 {
		t.Fatalf("pending reservations were not counted: %d", now[0].Members)
	}
	if e := s.BeginSwitch(ctx, "new", teams[1].ID, "meeting"); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishSwitch(ctx, "new"); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginSwitch(ctx, "new", teams[2].ID, "meeting"); e != nil {
		t.Fatal(e)
	}
	m, _ := s.Membership(ctx, "new")
	if !m.Switching || m.TeamID != teams[1].ID {
		t.Fatal("old membership lost before revocation")
	}
	if e := s.BeginSwitch(ctx, "new", teams[3].ID, "meeting"); e == nil {
		t.Fatal("overwrote pending switch")
	}
	if e := s.FinishSwitch(ctx, "new"); e != nil {
		t.Fatal(e)
	}
	m, _ = s.Membership(ctx, "new")
	if m.TeamID != teams[2].ID || m.Switching {
		t.Fatal("switch not committed")
	}
}

type testIdentity struct {
	user User
	fail bool
}

func (i *testIdentity) LoginURL(string, string, string) string { return "https://id.invalid/authorize" }
func (i *testIdentity) Exchange(context.Context, string, string, string) (User, *oauth2.Token, error) {
	return i.user, &oauth2.Token{}, nil
}
func (i *testIdentity) Refresh(context.Context, *oauth2.Token) (User, *oauth2.Token, error) {
	if i.fail {
		return User{}, nil, errors.New("revoked")
	}
	return i.user, &oauth2.Token{}, nil
}

type testEngine struct {
	Engine
	closeError error
	closed     int
}

func (e *testEngine) CloseAccess(context.Context, string) error { e.closed++; return e.closeError }
func serviceFor(t *testing.T) (*Service, Session, *testEngine) {
	st := testStore(t)
	engine := &testEngine{}
	v := Session{ID: digest("cookie"), User: User{ID: "student", Name: "Student"}, CSRF: "csrf", Expires: time.Now().Add(time.Hour), Checked: time.Now()}
	if e := st.Put(context.Background(), "session", v.ID, v); e != nil {
		t.Fatal(e)
	}
	return &Service{Config: Config{Origin: "https://labs.invalid", DesktopOrigin: "https://desktop.invalid", WebRoot: t.TempDir()}, Store: st, Engine: engine, Identity: &testIdentity{user: v.User}, desktops: map[string]*desktopSession{}, tickets: map[string]launchTicket{}}, v, engine
}
func request(s *Service, method, path, body, origin, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://labs.invalid"+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: "cookie"})
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestAuthorizationAndCSRF(t *testing.T) {
	s, _, _ := serviceFor(t)
	for _, c := range []struct {
		method, path, origin, csrf string
		want                       int
	}{{"GET", "/api/admin", "", "", 403}, {"POST", "/api/team", "https://foreign.invalid", "csrf", 403}, {"POST", "/api/team", "https://labs.invalid", "wrong", 403}, {"GET", "/api/me", "", "", 200}} {
		w := request(s, c.method, c.path, `{"teamID":""}`, c.origin, c.csrf)
		if w.Code != c.want {
			t.Fatalf("%s %s = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}
func TestRevocationFailureBlocksTeamSwitch(t *testing.T) {
	s, v, engine := serviceFor(t)
	teams := setupMeeting(t, s.Store)
	ctx := context.Background()
	_ = s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting")
	_ = s.Store.FinishSwitch(ctx, v.User.ID)
	_ = s.Store.Put(ctx, "desktop", "access", accessRecord{ID: "access", UserID: v.User.ID, TeamID: teams[0].ID})
	engine.closeError = errors.New("provider unavailable")
	w := request(s, "POST", "/api/team", fmt.Sprintf(`{"teamID":%q}`, teams[1].ID), s.Config.Origin, v.CSRF)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	m, _ := s.Store.Membership(ctx, v.User.ID)
	if !m.Switching || m.TeamID != teams[0].ID {
		t.Fatal(m)
	}
	if _, _, e := s.readyTeam(ctx, v.User.ID); e == nil {
		t.Fatal("switching user retained desktop authorization")
	}
	engine.closeError = nil
	w = request(s, "POST", "/api/team", fmt.Sprintf(`{"teamID":%q}`, teams[1].ID), s.Config.Origin, v.CSRF)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	m, _ = s.Store.Membership(ctx, v.User.ID)
	if m.Switching || m.TeamID != teams[1].ID {
		t.Fatal(m)
	}
}
func TestExpiredGroupMembershipFailsClosed(t *testing.T) {
	s, v, _ := serviceFor(t)
	v.Checked = time.Now().Add(-5 * time.Minute)
	_ = s.Store.Put(context.Background(), "session", v.ID, v)
	s.Identity = &testIdentity{fail: true}
	if w := request(s, "GET", "/api/me", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if e := s.Store.Get(context.Background(), "session", v.ID, new(Session)); e == nil {
		t.Fatal("invalid session retained")
	}
}
func TestOneUseLaunchAndCrossTeamDenial(t *testing.T) {
	s, v, _ := serviceFor(t)
	teams := setupMeeting(t, s.Store)
	ctx := context.Background()
	_ = s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting")
	_ = s.Store.FinishSwitch(ctx, v.User.ID)
	_, _ = s.Store.DB.Exec(`UPDATE teams SET status='ready'`)
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &desktopSession{record: accessRecord{ID: "access", UserID: v.User.ID, SessionID: v.ID, TeamID: teams[0].ID, InstanceID: teams[0].InstanceID, Expires: time.Now().Add(time.Hour)}, ctx: dc, cancel: cancel, cookie: "desktop-cookie"}
	s.desktops["access"] = d
	s.tickets[digest("ticket")] = launchTicket{"access", v.ID, time.Now().Add(time.Minute)}
	launch := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://desktop.invalid/launch", strings.NewReader("ticket=ticket"))
		r.Header.Set("Origin", s.Config.Origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := launch(); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := launch(); w.Code != 403 {
		t.Fatal("ticket replay accepted")
	}
	_ = s.Store.BeginSwitch(ctx, v.User.ID, teams[1].ID, "meeting")
	_ = s.Store.FinishSwitch(ctx, v.User.ID)
	if s.desktopAuthorized(ctx, d) {
		t.Fatal("cross-team access accepted")
	}
}
func TestPublicContractDoesNotExposeEngineState(t *testing.T) {
	b, e := json.Marshal(Team{InstanceID: "secret-infrastructure-id"})
	if e != nil || strings.Contains(string(b), "secret") {
		t.Fatal(string(b), e)
	}
	b, _ = json.Marshal(Template{Source: lc.Source{Digest: "private"}})
	if strings.Contains(string(b), "private") {
		t.Fatal(string(b))
	}
}

func TestAdministratorEmptyStateUsesCollections(t *testing.T) {
	s := &Service{Store: testStore(t)}
	w := httptest.NewRecorder()
	s.admin(w, httptest.NewRequest(http.MethodGet, "https://labs.invalid/api/admin", nil), Session{})
	var data struct {
		Teams     []Team     `json:"teams"`
		Jobs      []Job      `json:"jobs"`
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || data.Teams == nil || data.Jobs == nil || data.Templates == nil {
		t.Fatal("empty admin response contains null collections", w.Body.String())
	}
}

func TestRepeatedJoinPreservesCurrentDesktop(t *testing.T) {
	s, v, engine := serviceFor(t)
	teams := setupMeeting(t, s.Store)
	body := fmt.Sprintf(`{"teamID":%q}`, teams[0].ID)
	if w := request(s, "POST", "/api/team", body, s.Config.Origin, v.CSRF); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := s.Store.Put(context.Background(), "desktop", "access", accessRecord{ID: "access", UserID: v.User.ID, TeamID: teams[0].ID}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/team", body, s.Config.Origin, v.CSRF); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if engine.closed != 0 {
		t.Fatal("replayed join revoked the user's current desktop")
	}
}

func TestMeetingExtensionRejectsOverflowBeforeArithmetic(t *testing.T) {
	s, session, _ := serviceFor(t)
	setupMeeting(t, s.Store)
	session.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	meeting, _ := s.Store.Meeting(context.Background())
	meeting.Created = time.Now().UTC()
	if err := s.Store.Put(context.Background(), "meeting", "current", meeting); err != nil {
		t.Fatal(err)
	}
	response := request(s, "POST", "/api/admin/extend", `{"hours":9223372036854775807}`, s.Config.Origin, session.CSRF)
	if response.Code != 409 {
		t.Fatal("oversized extension accepted", response.Code)
	}
	after, _ := s.Store.Meeting(context.Background())
	if !after.Expires.Equal(meeting.Expires) {
		t.Fatal("invalid extension changed deadline")
	}
}
