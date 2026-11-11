package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
)

// ScoreSnapshot is the complete public projection exported by a guest scorer.
// It carries no target addresses, credentials, probe reports or executable data.
type ScoreSnapshot struct {
	Version     int             `json:"version"`
	Round       int             `json:"round"`
	GeneratedAt time.Time       `json:"generatedAt"`
	Services    []ScoredService `json:"services"`
}
type ScoredService struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state" enum:"up,down,unknown"`
	Earned   int    `json:"earned"`
	Possible int    `json:"possible"`
}
type LiveTeamScore struct {
	TeamID   string          `json:"teamID"`
	Name     string          `json:"name"`
	Status   string          `json:"status" enum:"live,stale,unavailable,waiting"`
	Round    int             `json:"round"`
	Updated  *time.Time      `json:"updated,omitempty"`
	Earned   int             `json:"earned"`
	Possible int             `json:"possible"`
	Services []ScoredService `json:"services,omitempty"`
}
type LiveScoreboard struct {
	MeetingID      string          `json:"meetingID"`
	OwnTeamID      string          `json:"ownTeamID"`
	Teams          []LiveTeamScore `json:"teams"`
	RefreshSeconds int             `json:"refreshSeconds"`
}
type scoreCache struct {
	Attempted   time.Time      `json:"attempted"`
	Unavailable bool           `json:"unavailable"`
	Snapshot    *ScoreSnapshot `json:"snapshot,omitempty"`
}
type scoreFlight struct {
	done   chan struct{}
	result scoreCache
}

func (s *Service) liveRedBlue(ctx context.Context, meeting string) bool {
	var enabled bool
	return s.Store.Get(ctx, "live-red-blue", meeting, &enabled) == nil && enabled
}

func validateScore(snapshot ScoreSnapshot, meeting Meeting, now time.Time) error {
	if snapshot.Version != 1 || snapshot.Round < 1 || snapshot.Round > 1_000_000_000 || snapshot.GeneratedAt.IsZero() || snapshot.GeneratedAt.After(now.Add(30*time.Second)) || snapshot.GeneratedAt.Before(meeting.Created.Add(-30*time.Second)) || len(snapshot.Services) == 0 || len(snapshot.Services) > 256 {
		return fmt.Errorf("invalid scoreboard snapshot")
	}
	ids := map[string]bool{}
	total := 0
	for _, service := range snapshot.Services {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`).MatchString(service.ID) || ids[service.ID] || !cleanName(service.Name) || strings.ContainsAny(service.Name, "\x1b\t") || !lc.Contains([]string{"up", "down", "unknown"}, service.State) || service.Earned < 0 || service.Possible < 0 || service.Earned > service.Possible || service.Possible > 1_000_000_000 {
			return fmt.Errorf("invalid scoreboard service")
		}
		ids[service.ID] = true
		total += service.Possible
		if total > 1_000_000_000 {
			return fmt.Errorf("scoreboard points exceed bound")
		}
	}
	return nil
}

// The sole destination is loopback inside a pinned, hidden scorer VM, reached by
// verified SSH. Neither clients nor feeds can supply a URL, host or redirect.
func (s *Service) readGuestScore(ctx context.Context, team Team, meeting Meeting, src lc.Source, config instructor.LiveScoring) (ScoreSnapshot, error) {
	var snapshot ScoreSnapshot
	engine, ok := s.Engine.(terminalSSH)
	if !ok {
		return snapshot, fmt.Errorf("verified guest transport unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, release, err := s.sshContext(ctx)
	if err != nil {
		return snapshot, err
	}
	defer release()
	err = engine.WithSSHSession(ctx, team.InstanceID, config.Machine, func(inst lc.Instance, m lc.PlannedMachine, res lc.Resource, key []byte) error {
		if inst.ID != team.InstanceID || inst.Status != "ready" || inst.Plan.Source.Digest != src.Digest || !coachGuestMatches(team.InstanceID, config.Machine, m, res) || !res.Complete || lc.OSFamily(m.OS) != "linux" || m.Appliance != "" {
			return fmt.Errorf("scorer allocation differs from pinned source")
		}
		dialer, err := lc.MachineDialer(lc.InstanceGateway(inst), key)
		if err != nil {
			return err
		}
		defer dialer.Close()
		client, err := dialer.Dial(ctx, res.Address, m.SSHUser(), key, m.HostPublicKey)
		if err != nil {
			return err
		}
		defer client.Close()
		stop := context.AfterFunc(ctx, func() { client.Close() })
		defer stop()
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port))
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != address {
				return nil, fmt.Errorf("unexpected scorer destination")
			}
			return client.DialContext(ctx, "tcp", address)
		}}
		defer transport.CloseIdleConnections()
		httpClient := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("scoreboard redirects are disabled") }}
		u := url.URL{Scheme: "http", Host: address, Path: config.Path}
		request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("scoreboard feed unavailable")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
		if err != nil || len(raw) > 128<<10 {
			return fmt.Errorf("scoreboard exceeds response bound")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil {
			return fmt.Errorf("invalid scoreboard JSON")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("invalid scoreboard JSON suffix")
		}
		return validateScore(snapshot, meeting, time.Now())
	})
	return snapshot, err
}

// Coalesce students' polls and persist the last good snapshot across restarts.
// The cache key changes with the meeting, allocation, source and feed contract.
func (s *Service) cachedGuestScore(ctx context.Context, team Team, meeting Meeting, src lc.Source, config instructor.LiveScoring) scoreCache {
	key := digest(meeting.ID + "\x00" + team.ID + "\x00" + team.InstanceID + "\x00" + meeting.Template + "\x00" + src.Digest + "\x00" + jsonValue(config))
	s.scoreMu.Lock()
	if s.scoreFlights == nil {
		s.scoreFlights = map[string]*scoreFlight{}
	}
	if flight := s.scoreFlights[key]; flight != nil {
		s.scoreMu.Unlock()
		select {
		case <-flight.done:
			return flight.result
		case <-ctx.Done():
			return scoreCache{Unavailable: true}
		}
	}
	flight := &scoreFlight{done: make(chan struct{})}
	s.scoreFlights[key] = flight
	s.scoreMu.Unlock()
	defer func() { s.scoreMu.Lock(); delete(s.scoreFlights, key); close(flight.done); s.scoreMu.Unlock() }()
	var cache scoreCache
	err := s.Store.Get(ctx, "live-score-cache", key, &cache)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		flight.result = scoreCache{Unavailable: true}
		return flight.result
	}
	if cache.Snapshot != nil && validateScore(*cache.Snapshot, meeting, time.Now()) != nil {
		cache.Snapshot = nil
		cache.Unavailable = true
	}
	if time.Since(cache.Attempted) >= 10*time.Second {
		snapshot, err := s.readGuestScore(ctx, team, meeting, src, config)
		cache.Attempted = time.Now().UTC()
		cache.Unavailable = err != nil
		if err == nil {
			if cache.Snapshot != nil && (snapshot.GeneratedAt.Before(cache.Snapshot.GeneratedAt) || snapshot.Round < cache.Snapshot.Round || (snapshot.Round == cache.Snapshot.Round && (!snapshot.GeneratedAt.Equal(cache.Snapshot.GeneratedAt) || !slices.Equal(snapshot.Services, cache.Snapshot.Services)))) {
				cache.Unavailable = true
			} else {
				cache.Snapshot = &snapshot
			}
		}
		if err := s.Store.Put(ctx, "live-score-cache", key, cache); err != nil {
			cache.Unavailable = true
		}
	}
	flight.result = cache
	return cache
}

func (s *Service) scoreAccess(ctx context.Context, v Session) (Meeting, []Team, string, lc.Source, instructor.LiveScoring, error) {
	var src lc.Source
	var config instructor.LiveScoring
	m, err := s.Store.Meeting(ctx)
	if err != nil || m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) || !s.liveRedBlue(ctx, m.ID) {
		return Meeting{}, nil, "", src, config, fmt.Errorf("live scoreboard is not enabled")
	}
	teams, err := s.Store.Teams(ctx, m.ID)
	if err != nil {
		return *m, nil, "", src, config, err
	}
	own := ""
	if !v.User.Admin {
		membership, err := s.Store.Membership(ctx, v.User.ID)
		if err != nil || membership.Switching {
			return *m, nil, "", src, config, fmt.Errorf("team access unavailable")
		}
		for _, team := range teams {
			if team.ID == membership.TeamID {
				own = team.ID
			}
		}
		if own == "" {
			return *m, nil, "", src, config, fmt.Errorf("join a meeting team to view scores")
		}
	}
	var b InstructorBundle
	if s.Store.Get(ctx, "source", m.Template, &src) != nil || src.Lab.Version != 3 || s.Store.Get(ctx, "template-instructor", m.Template, &b) != nil || b.LiveScoring == nil || b.LiveScoring.Validate(src, b.Visibility.HiddenMachines) != nil {
		return *m, nil, "", src, config, fmt.Errorf("pinned scoreboard feed unavailable")
	}
	return *m, teams, own, src, *b.LiveScoring, nil
}

func (s *Service) liveScoreboard(w http.ResponseWriter, r *http.Request, v Session) {
	m, teams, own, src, config, err := s.scoreAccess(r.Context(), v)
	if err != nil {
		writeError(w, 403, "Live scoring is available only to members of an active Red vs. Blue meeting.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := LiveScoreboard{MeetingID: m.ID, OwnTeamID: own, RefreshSeconds: 15, Teams: make([]LiveTeamScore, len(teams))}
	slots := make(chan struct{}, 4)
	var group sync.WaitGroup
	for i, team := range teams {
		group.Go(func() {
			view := LiveTeamScore{TeamID: team.ID, Name: team.Name, Status: "waiting"}
			if team.Status == "ready" && team.InstanceID != "" {
				view.Status = "unavailable"
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					out.Teams[i] = view
					return
				}
				cache := s.cachedGuestScore(ctx, team, m, src, config)
				if snapshot := cache.Snapshot; snapshot != nil {
					view.Status = "live"
					view.Round = snapshot.Round
					view.Updated = &snapshot.GeneratedAt
					if cache.Unavailable || time.Since(snapshot.GeneratedAt) > time.Duration(config.StaleAfter())*time.Second {
						view.Status = "stale"
					}
					for _, service := range snapshot.Services {
						view.Earned += service.Earned
						view.Possible += service.Possible
					}
					if team.ID == own || v.User.Admin {
						view.Services = snapshot.Services
					}
				}
			}
			out.Teams[i] = view
		})
	}
	group.Wait()
	// A membership/meeting change during the guest read revokes the response.
	currentSession, sessionErr := s.checkedSession(r.Context(), v.ID)
	if sessionErr != nil || currentSession.User.ID != v.User.ID || currentSession.User.Admin != v.User.Admin {
		writeError(w, 403, "Live scoreboard authorization changed.")
		return
	}
	current, currentTeams, nowOwn, nowSource, nowConfig, err := s.scoreAccess(r.Context(), currentSession)
	if err != nil || current.ID != m.ID || current.Template != m.Template || own != nowOwn || nowSource.Digest != src.Digest || nowConfig != config {
		writeError(w, 403, "Live scoreboard access changed. Reload your meeting.")
		return
	}
	if len(currentTeams) != len(teams) {
		writeError(w, 403, "Live scoreboard allocations changed.")
		return
	}
	for i, team := range teams {
		if currentTeams[i].ID != team.ID || currentTeams[i].InstanceID != team.InstanceID || currentTeams[i].Status != team.Status {
			writeError(w, 403, "Live scoreboard allocations changed.")
			return
		}
	}
	sort.SliceStable(out.Teams, func(i, j int) bool { return out.Teams[i].Earned > out.Teams[j].Earned })
	writeJSON(w, 200, out)
}
