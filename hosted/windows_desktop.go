package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

// These records are hosted policy, independent of provider workspace identities.
// The holder is removed only in the same commit as confirmed access cleanup.
type desktopHolder struct {
	Access accessRecord
	Name   string
}
type desktopHandoff struct {
	ByName  string
	Expires time.Time
}
type DesktopState struct {
	Code        string `json:"code" enum:"available,held,owned,desktop_taken_over,desktop_outcome_unknown"`
	HolderName  string `json:"holderName,omitempty"`
	TakenOverBy string `json:"takenOverBy,omitempty"`
	CanTakeOver bool   `json:"canTakeOver"`
}
type DesktopConflict struct {
	Error       string `json:"error"`
	Code        string `json:"code" enum:"desktop_held,desktop_taken_over,desktop_outcome_unknown"`
	HolderName  string `json:"holderName,omitempty"`
	TakenOverBy string `json:"takenOverBy,omitempty"`
}

func (a accessRecord) consoleKey() string {
	if (a.OS != "windows" && a.Platform != "proxmox") || strings.HasPrefix(a.TeamID, "practice:") {
		return ""
	}
	return digest(a.InstanceID + "\x00" + a.Machine)
}
func (s *Service) lockConsole(ctx context.Context, key string) (func(), error) {
	// Separate namespace from identity subjects; user locks never acquire this lock.
	return s.lockUser(ctx, "\x00console:"+key)
}
func (s *Service) desktopOS(ctx context.Context, team Team, machine string) (string, error) {
	var template string
	if strings.HasPrefix(team.ID, "practice:") {
		run, err := s.Store.practice(ctx, strings.TrimPrefix(team.ID, "practice:"))
		if err != nil {
			return "", err
		}
		var p LearningPublication
		if err = s.Store.Get(ctx, "learning-publication", run.LabID, &p); err != nil {
			return "", err
		}
		template = p.Template
	} else {
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return "", err
		}
		if m == nil {
			return "", fmt.Errorf("meeting unavailable")
		}
		template = m.Template
	}
	var src lc.Source
	if err := s.Store.Get(ctx, "source", template, &src); err != nil {
		return "", err
	}
	spec, ok := src.Lab.Machines[machine]
	if !ok {
		return "", fmt.Errorf("pinned machine unavailable")
	}
	return lc.OSFamily(src.Lab.Images[spec.Image].OS), nil
}

// Call with the machine lock held. Never transfer the native ownership marker
// until CloseAccess confirms the previous grant is absent. A crash leaves both
// the old access and handoff intent available to ordinary access recovery.
func (s *Service) claimConsole(ctx context.Context, a accessRecord, takeover bool) (*DesktopConflict, error) {
	if !s.recordAuthorized(ctx, a) {
		return nil, fmt.Errorf("desktop authorization ended")
	}
	var holder desktopHolder
	err := s.Store.Get(ctx, "desktop-holder", a.consoleKey(), &holder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if holder.Access.ID == a.ID {
		return nil, nil
	}
	if !takeover {
		conflict := &DesktopConflict{Error: "The desktop is held by " + holder.Name + ".", Code: "desktop_held", HolderName: holder.Name}
		var displaced desktopHandoff
		if err := s.Store.Get(ctx, "desktop-displaced", a.ID, &displaced); err == nil {
			conflict.Code = "desktop_taken_over"
			conflict.TakenOverBy = displaced.ByName
			conflict.Error = "The desktop was taken over by " + displaced.ByName + "."
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return conflict, nil
	}
	if !s.recordAuthorized(ctx, a) {
		return nil, fmt.Errorf("desktop authorization ended")
	}
	if err = s.Store.Put(ctx, "desktop-handoff", holder.Access.ID, desktopHandoff{ByName: a.UserName, Expires: holder.Access.Expires}); err != nil {
		return nil, err
	}
	if err = s.cleanupDesktopLocked(ctx, holder.Access.ID, false); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) meetingDesktopTarget(ctx context.Context, v Session, teamID string) (Team, *Meeting, string, error) {
	var team Team
	var meeting *Meeting
	var err error
	if teamID != "" {
		if !v.User.Admin {
			return team, nil, "", fmt.Errorf("meeting instructor access required")
		}
		var m Meeting
		team, m, _, err = s.coachTeam(ctx, teamID)
		meeting = &m
	} else {
		team, meeting, err = s.readyTeam(ctx, v.User.ID)
	}
	if err != nil {
		return team, meeting, "", err
	}
	machine, err := s.desktopMachine(ctx, meeting.Template)
	return team, meeting, machine, err
}
func (s *Service) takeoverDesktop(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingDesktop(w, r, v, true)
}
func (s *Service) coachDesktop(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingDesktop(w, r, v, false)
}
func (s *Service) meetingDesktop(w http.ResponseWriter, r *http.Request, v Session, takeover bool) {
	team, m, machine, err := s.meetingDesktopTarget(r.Context(), v, r.PathValue("id"))
	if err != nil {
		status := 409
		if r.PathValue("id") != "" {
			status = 403
		}
		writeError(w, status, "Meeting desktop access is unavailable.")
		return
	}
	s.launchDesktop(w, r, v, team, m.Expires, machine, takeover)
}
func (s *Service) desktopState(ctx context.Context, a accessRecord) (DesktopState, error) {
	state := DesktopState{Code: "available"}
	if a.consoleKey() == "" {
		return state, nil
	}
	var displaced desktopHandoff
	if err := s.Store.Get(ctx, "desktop-displaced", a.ID, &displaced); err == nil {
		state.Code = "desktop_taken_over"
		state.TakenOverBy = displaced.ByName
	} else if !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	var holder desktopHolder
	if err := s.Store.Get(ctx, "desktop-holder", a.consoleKey(), &holder); errors.Is(err, sql.ErrNoRows) {
		return state, nil
	} else if err != nil {
		return state, err
	}
	state.HolderName = holder.Name
	state.CanTakeOver = holder.Access.UserID != a.UserID
	if state.Code != "desktop_taken_over" {
		if holder.Access.ID == a.ID {
			state.Code = "owned"
		} else {
			state.Code = "held"
		}
	}
	var op desktopOperation
	if err := s.Store.Get(ctx, "desktop-operation", holder.Access.ID, &op); err != nil {
		return state, err
	}
	if op.Status == "closing" {
		state.Code = "desktop_outcome_unknown"
	}
	return state, nil
}
func (s *Service) desktopStatus(w http.ResponseWriter, r *http.Request, v Session) {
	team, _, machine, err := s.meetingDesktopTarget(r.Context(), v, r.PathValue("id"))
	if err != nil {
		writeError(w, 403, "Meeting desktop access is unavailable.")
		return
	}
	os, err := s.desktopOS(r.Context(), team, machine)
	if err != nil {
		writeError(w, 503, "Pinned machine unavailable.")
		return
	}
	a := accessRecord{ID: digest(team.InstanceID + "\x00" + machine + "\x00" + v.User.ID)[:24], UserID: v.User.ID, TeamID: team.ID, InstanceID: team.InstanceID, Machine: machine, OS: os, Platform: s.env.Platform}
	state, err := s.desktopState(r.Context(), a)
	if err != nil {
		writeError(w, 503, "Desktop state is unavailable.")
		return
	}
	writeJSON(w, 200, state)
}

// Cookie records contain no relay credentials. They allow an already displaced
// browser to poll once its active relay handle has been removed, including after
// restart. Current identity and the original meeting allocation are rechecked.
func (s *Service) desktopCookieStatus(w http.ResponseWriter, r *http.Request, cookie string) bool {
	var a accessRecord
	if s.Store.Get(r.Context(), "desktop-cookie", digest(cookie), &a) != nil || !s.recordAuthorized(r.Context(), a) {
		return false
	}
	state, err := s.desktopState(r.Context(), a)
	if err != nil {
		writeError(w, 503, "Desktop state is unavailable.")
		return true
	}
	if r.URL.Path == "/status" {
		writeJSON(w, 200, state)
		return true
	}
	if state.TakenOverBy != "" {
		writeJSON(w, 409, DesktopConflict{Error: "The desktop was taken over by " + state.TakenOverBy + ".", Code: "desktop_taken_over", TakenOverBy: state.TakenOverBy})
		return true
	}
	return false
}
