package hosted

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLiveScoringRetainsEverySavedTeamAtRefreshDeadline(t *testing.T) {
	s, _, _, _, _ := liveScoreFixture(t)
	ctx := context.Background()
	m, initialTeams, _, src, config, err := s.scoreAccess(ctx, Session{User: User{Admin: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	for n := range 19 - len(initialTeams) {
		if _, err = s.Store.DB.ExecContext(ctx, `INSERT INTO teams VALUES(?,?,?,?,?,?)`, fmt.Sprint("extra-", n), m.ID, fmt.Sprint("Extra ", n), 6, fmt.Sprint("instance-", n), "ready"); err != nil {
			t.Fatal(err)
		}
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil || len(teams) != 19 {
		t.Fatal(len(teams), err)
	}
	uncached := teams[len(teams)-1].ID
	for _, team := range teams[:len(teams)-1] {
		cache := scoreCache{Attempted: time.Now().Add(-time.Minute), Snapshot: &ScoreSnapshot{Version: 1, Round: 7, GeneratedAt: time.Now().UTC(), Services: []ScoredService{{ID: "web", Name: "Web", State: "up", Earned: 120, Possible: 200}}}}
		if err := s.Store.Put(ctx, "live-score-cache", guestScoreKey(team, m, src, config), cache); err != nil {
			t.Fatal(err)
		}
	}
	engine := s.Engine.(*scoreEngine)
	engine.entered, engine.release = make(chan struct{}, 20), make(chan struct{})
	start := time.Now()
	board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	if time.Since(start) > 23*time.Second || len(board.Teams) != 19 {
		t.Fatal("refresh exceeded response bound", time.Since(start), len(board.Teams))
	}
	for _, team := range board.Teams {
		if team.TeamID == uncached {
			if team.Status != "unavailable" || team.Earned != 0 || team.Updated != nil {
				t.Fatal("uncached team fabricated a score", team)
			}
		} else if team.Status != "stale" || team.Earned != 120 || team.Possible != 200 || team.Round != 7 {
			t.Fatal("saved total lost at deadline", team)
		}
	}
}

func TestLiveScoringCoalescedTimeoutKeepsSavedProjection(t *testing.T) {
	s, _, _, _, _ := liveScoreFixture(t)
	m, teams, _, src, config, err := s.scoreAccess(context.Background(), Session{User: User{Admin: true}})
	if err != nil {
		t.Fatal(err)
	}
	team := teams[0]
	key := guestScoreKey(team, m, src, config)
	s.scoreFlights = map[string]*scoreFlight{key: {done: make(chan struct{})}}
	saved := scoreCache{Snapshot: &ScoreSnapshot{Version: 1, Round: 1, GeneratedAt: time.Now().UTC(), Services: []ScoredService{{ID: "web", Name: "Web", State: "up", Earned: 10, Possible: 20}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got := s.cachedGuestScore(ctx, team, m, src, config, saved)
	if !got.Unavailable || got.Snapshot == nil || got.Snapshot.Services[0].Earned != 10 {
		t.Fatal("coalesced timeout discarded saved points", got)
	}
}

func TestLiveScoringReadsUnderTerminalSaturation(t *testing.T) {
	s, _, _, calls, _ := liveScoreFixture(t)
	s.Config.Aggregate = AggregateOptions{MaxSSHSessions: 2}
	release, err := s.acquireTerminal(context.Background(), "participant", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if extra, err := s.acquireTerminal(context.Background(), "other", "other-team"); err == nil {
		extra()
		t.Fatal("terminal reserve exceeded")
	}
	board := decodeLearning[LiveScoreboard](t, request(s, "GET", "/api/live-scoreboard", "", "", ""), 200)
	if calls.Load() != 1 || board.Teams[0].Earned != 120 || board.Teams[0].Status != "live" {
		t.Fatal("terminals starved live score read", board)
	}
}

func TestLiveScoringRefreshRechecksPersistedGeneration(t *testing.T) {
	s, _, _, _, _ := liveScoreFixture(t)
	ctx := context.Background()
	m, teams, _, src, config, err := s.scoreAccess(ctx, Session{User: User{Admin: true}})
	if err != nil {
		t.Fatal(err)
	}
	team := teams[0]
	latest := scoreCache{Attempted: time.Now().Add(-time.Minute), Snapshot: &ScoreSnapshot{Version: 1, Round: 8, GeneratedAt: time.Now().UTC(), Services: []ScoredService{{ID: "web", Name: "Web", State: "up", Earned: 80, Possible: 100}}}}
	if err := s.Store.Put(ctx, "live-score-cache", guestScoreKey(team, m, src, config), latest); err != nil {
		t.Fatal(err)
	}
	older := scoreCache{Snapshot: &ScoreSnapshot{Version: 1, Round: 1, GeneratedAt: time.Now().Add(-time.Minute), Services: []ScoredService{{ID: "web", Name: "Web", State: "up", Earned: 10, Possible: 100}}}}
	got := s.cachedGuestScore(ctx, team, m, src, config, older)
	if !got.Unavailable || got.Snapshot == nil || got.Snapshot.Round != 8 {
		t.Fatal("a pre-admission projection overwrote a newer saved round", got)
	}
}
