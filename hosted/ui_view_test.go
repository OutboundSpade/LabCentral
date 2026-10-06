package hosted

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

type uiViewEngine struct{ Engine }

func (e uiViewEngine) Inspect(context.Context, string) (lc.Instance, error) {
	return lc.Instance{}, nil
}

func TestOverviewDesktopAvailabilityIsScoped(t *testing.T) {
	s, v, _ := serviceFor(t)
	s.Engine = uiViewEngine{Engine: s.Engine}
	ctx := context.Background()
	teams := setupMeeting(t, s.Store)
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
	teams, _ = s.Store.Teams(ctx, "meeting")
	base := accessRecord{ID: "own", UserID: v.User.ID, SessionID: v.ID, TeamID: teams[0].ID, InstanceID: teams[0].InstanceID, Expires: v.Expires}
	for _, test := range []struct {
		name   string
		change func(*desktopSession)
		active bool
	}{
		{"own session", func(*desktopSession) {}, true},
		{"other user", func(d *desktopSession) { d.record.UserID = "someone-else" }, false},
		{"other login", func(d *desktopSession) { d.record.SessionID = "other-session" }, false},
		{"other team", func(d *desktopSession) { d.record.TeamID = teams[1].ID }, false},
		{"other instance", func(d *desktopSession) { d.record.InstanceID = "other-instance" }, false},
		{"expired intent", func(d *desktopSession) { d.record.Expires = time.Now().Add(-time.Second) }, false},
		{"expired relay", func(d *desktopSession) { d.info.Expires = time.Now().Add(-time.Second) }, false},
		{"cancelled relay", func(d *desktopSession) { d.cancel() }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, cancel := context.WithCancel(ctx)
			defer cancel()
			d := &desktopSession{record: base, ctx: connection, cancel: cancel, info: lc.AccessInfo{Expires: v.Expires, Password: "relay-secret-sentinel", URL: "http://127.0.0.1/?token=relay-token-sentinel"}}
			test.change(d)
			s.desktops = map[string]*desktopSession{"own": d}
			w := request(s, "GET", "/api/me", "", "", "")
			var out Overview
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Lab.DesktopActive != test.active {
				t.Fatalf("scoped availability: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "relay-secret-sentinel") || strings.Contains(w.Body.String(), "relay-token-sentinel") || strings.Contains(w.Body.String(), "joinedStudents") || strings.Contains(w.Body.String(), "repositoryURL") || strings.Contains(w.Body.String(), "teamDesktops") {
				t.Fatal("private data entered student view")
			}
		})
	}
	// A retained cleanup intent without an authenticated relay must not be called active.
	if err := s.Store.Put(ctx, "desktop", base.ID, base); err != nil {
		t.Fatal(err)
	}
	s.desktops = map[string]*desktopSession{}
	var out Overview
	w := request(s, "GET", "/api/me", "", "", "")
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Lab.DesktopActive {
		t.Fatal("retained intent reported active")
	}
	if err := s.Store.Get(ctx, "desktop", base.ID, new(accessRecord)); err != nil {
		t.Fatal("read lost cleanup intent", err)
	}
	connection, cancel := context.WithCancel(ctx)
	defer cancel()
	s.desktops[base.ID] = &desktopSession{record: base, ctx: connection, cancel: cancel, info: lc.AccessInfo{Expires: v.Expires}}
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[1].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	w = request(s, "GET", "/api/me", "", "", "")
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Lab.DesktopActive || out.Lab.Team != nil {
		t.Fatal("pending switch retained desktop availability")
	}
}

func TestAdminDesktopCountsAndRepositoryURL(t *testing.T) {
	s, v, _ := serviceFor(t)
	ctx := context.Background()
	teams := setupMeeting(t, s.Store)
	s.Config.Repository = "git@github.com:training/example.git"
	for n, team := range teams[:2] {
		connection, cancel := context.WithCancel(ctx)
		defer cancel()
		d := &desktopSession{record: accessRecord{ID: team.ID, UserID: "private-user-sentinel", TeamID: team.ID, Expires: v.Expires}, ctx: connection, cancel: cancel, info: lc.AccessInfo{Expires: v.Expires, Password: "relay-secret-sentinel"}}
		s.desktops[team.ID] = d
		if n == 1 {
			d.cancel()
		}
	}
	if w := request(s, "GET", "/api/admin", "", "", ""); w.Code != 403 {
		t.Fatal("student could read admin activity")
	}
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	read := func() AdminView {
		w := request(s, "GET", "/api/admin", "", "", "")
		var out AdminView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("admin response %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-user-sentinel") || strings.Contains(w.Body.String(), "relay-secret-sentinel") || strings.Contains(w.Body.String(), "git@github.com") {
			t.Fatal("admin counts exposed native connection data")
		}
		return out
	}
	out := read()
	if out.RepositoryURL != "https://github.com/training/example" || out.ActiveDesktops != 1 || len(out.TeamDesktops) != len(teams) || out.TeamDesktops[0].Count != 1 || out.TeamDesktops[1].Count != 0 {
		t.Fatalf("wrong activity: %+v", out)
	}
	// Concurrent relay replacement/cancellation and admin reads use the same mutex.
	var changes sync.WaitGroup
	changes.Go(func() {
		for range 40 {
			connection, cancel := context.WithCancel(ctx)
			d := &desktopSession{record: accessRecord{TeamID: teams[0].ID, Expires: v.Expires}, ctx: connection, cancel: cancel, info: lc.AccessInfo{Expires: v.Expires}}
			s.mu.Lock()
			s.desktops["changing"] = d
			s.mu.Unlock()
			cancel()
			s.mu.Lock()
			delete(s.desktops, "changing")
			s.mu.Unlock()
		}
	})
	for range 40 {
		read()
	}
	changes.Wait()
}

func TestRepositoryLinkOmitsUnsafeConfiguration(t *testing.T) {
	for _, value := range []string{"https://token@github.com/org/repo.git", "git@github.com:org/repo.git?token=secret", "git@github.com:../repo.git", "git@untrusted.invalid:org/repo.git", "file:///private/keys"} {
		if repositoryURL(value) != "" {
			t.Fatalf("unsafe repository link for %q", value)
		}
	}
}

func TestJoinedStudentsDoesNotDoubleCountPendingReservations(t *testing.T) {
	s, v, _ := serviceFor(t)
	ctx := context.Background()
	teams := setupMeeting(t, s.Store)
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	count, err := s.Store.JoinedStudents(ctx, "meeting")
	if err != nil || count != 0 {
		t.Fatal("uncommitted first join counted", count, err)
	}
	if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[1].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	// The switch intent is durable while both teams reserve capacity.
	all, err := s.Store.Teams(ctx, "meeting")
	if err != nil || all[0].Members != 1 || all[1].Members != 1 {
		t.Fatal("switch reservations missing", all, err)
	}
	count, err = s.Store.JoinedStudents(ctx, "meeting")
	if err != nil || count != 1 {
		t.Fatal("pending switch double counted", count, err)
	}
	if count, err := s.Store.JoinedStudents(ctx, "another-meeting"); err != nil || count != 0 {
		t.Fatal("foreign meeting counted", count, err)
	}
	if w := request(s, "GET", "/api/admin", "", "", ""); w.Code != 403 {
		t.Fatal("student read private counts")
	}
	v.User.Admin = true
	if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/admin", "", "", "")
	var out AdminView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.JoinedStudents != 1 {
		t.Fatal("wrong admin headcount", w.Code, w.Body.String())
	}
	// Concurrent committed switches keep one student rather than two reserved seats.
	var changes sync.WaitGroup
	changes.Go(func() {
		for i := range 40 {
			if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
				t.Error(err)
				return
			}
			if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[i%2].ID, "meeting"); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 40 {
		if count, err := s.Store.JoinedStudents(ctx, "meeting"); err != nil || count != 1 {
			t.Error("concurrent switch miscounted", count, err)
		}
	}
	changes.Wait()
}
