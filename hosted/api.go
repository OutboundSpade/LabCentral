package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"time"
)

func (s *Service) overview(w http.ResponseWriter, r *http.Request, v Session) {
	ctx := r.Context()
	out := Overview{User: v.User, CSRF: v.CSRF, Teams: []Team{}, Lab: LabView{Machines: []Machine{}}}
	var err error
	out.Meeting, err = s.Store.Meeting(ctx)
	if err != nil {
		writeError(w, 503, "Meeting information is unavailable.")
		return
	}
	out.Membership, err = s.Store.Membership(ctx, v.User.ID)
	if err != nil {
		writeError(w, 503, "Membership information is unavailable.")
		return
	}
	if out.Meeting != nil {
		out.Teams, err = s.Store.Teams(ctx, out.Meeting.ID)
		if err != nil {
			writeError(w, 503, "Teams are unavailable.")
			return
		}
		for _, team := range out.Teams {
			if team.ID == out.Membership.TeamID && !out.Membership.Switching {
				out.Lab.Team = &team
				out.Lab.Teammates, err = s.Store.Teammates(ctx, team.ID)
				if err != nil {
					writeError(w, 503, "Team information is unavailable.")
					return
				}
				var t Template
				if err = s.Store.Get(ctx, "template", out.Meeting.Template, &t); err == nil {
					out.Lab.Handover = t.Handover
				}
				inst, e := s.Engine.Inspect(ctx, team.InstanceID)
				if e == nil {
					for _, m := range inst.Plan.Machines {
						out.Lab.Machines = append(out.Lab.Machines, Machine{m.Name, inst.Status, lc.Contains(m.Spec.Access, "desktop")})
					}
				}
			}
		}
	}
	writeJSON(w, 200, out)
}
func (s *Service) join(w http.ResponseWriter, r *http.Request, v Session) {
	var req JoinTeam
	if !decode(w, r, &req) {
		return
	}
	unlock := s.lockUser(v.User.ID)
	defer unlock()
	meeting, e := s.Store.Meeting(r.Context())
	if e != nil || meeting == nil || meeting.Status == "ending" || meeting.Status == "ended" || !meeting.Expires.After(time.Now()) {
		writeError(w, 409, "There is no open meeting to join.")
		return
	}
	if e = s.Store.BeginSwitch(r.Context(), v.User.ID, req.TeamID, meeting.ID); e != nil {
		writeError(w, 409, e.Error())
		return
	}
	membership, e := s.Store.Membership(r.Context(), v.User.ID)
	if e != nil {
		writeError(w, 503, "Your team change is pending. Please retry.")
		return
	}
	if membership.Switching {
		if e = s.revokeUser(r.Context(), v.User.ID); e != nil {
			writeError(w, 503, "Your previous desktop is closing. Retry your team change shortly.")
			return
		}
		if e = s.Store.FinishSwitch(r.Context(), v.User.ID); e != nil {
			writeError(w, 503, "Your team change is pending. Please retry.")
			return
		}
	}
	writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true})
}
func (s *Service) templates(ctx context.Context) ([]Template, error) {
	rows, e := s.Store.List(ctx, "template")
	if e != nil {
		return nil, e
	}
	out := []Template{}
	for _, b := range rows {
		var t Template
		if e = json.Unmarshal(b, &t); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, nil
}
func (s *Service) admin(w http.ResponseWriter, r *http.Request, v Session) {
	ctx := r.Context()
	out := AdminView{Teams: []Team{}, Jobs: []Job{}, Templates: []Template{}}
	var e error
	out.Meeting, e = s.Store.Meeting(ctx)
	if e == nil && out.Meeting != nil {
		out.Teams, e = s.Store.Teams(ctx, out.Meeting.ID)
	}
	if e == nil {
		out.Jobs, e = s.Store.Jobs(ctx)
	}
	if e == nil {
		out.Templates, e = s.templates(ctx)
	}
	if e != nil {
		writeError(w, 503, "Administrator data is unavailable.")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Service) createMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	var q CreateMeeting
	if !decode(w, r, &q) {
		return
	}
	if q.Hours == 0 {
		q.Hours = 4
	}
	if q.TeamCount == 0 {
		q.TeamCount = 5
	}
	if q.Capacity == 0 {
		q.Capacity = 4
	}
	if !cleanName(q.Name) || q.Hours < 1 || q.Hours > 8 || q.TeamCount < 1 || q.TeamCount > 5 || q.Capacity < 1 || q.Capacity > 4 {
		writeError(w, 400, "Use a name, one to eight hours, up to five teams, and up to four members per team.")
		return
	}
	var template Template
	if e := s.Store.Get(r.Context(), "template", q.Template, &template); e != nil || !template.Built {
		writeError(w, 409, "Build a template revision before creating the meeting.")
		return
	}
	m := Meeting{randomID(12), q.Name, q.Template, "waiting", time.Now().UTC(), time.Now().UTC().Add(time.Duration(q.Hours) * time.Hour)}
	s.mu.Lock()
	e := s.Store.CreateMeeting(r.Context(), m, q.TeamCount, q.Capacity)
	s.mu.Unlock()
	if e != nil {
		writeError(w, 409, e.Error())
		return
	}
	writeJSON(w, 201, m)
}
func (s *Service) meetingJob(w http.ResponseWriter, r *http.Request, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.Store.Meeting(r.Context())
	if e != nil || m == nil || m.Status == "ended" {
		writeError(w, 409, "No current meeting.")
		return
	}
	if kind == "prepare" && (m.Status == "ending" || !m.Expires.After(time.Now())) {
		writeError(w, 409, "The meeting has ended.")
		return
	}
	if kind == "end" {
		s.stopProvisioning()
		m.Status = "ending"
		m.Expires = time.Now().UTC()
		if e = s.Store.Put(r.Context(), "meeting", "current", m); e != nil {
			writeError(w, 503, "Could not persist the meeting deadline.")
			return
		}
	}
	j, e := s.Store.Enqueue(r.Context(), kind, m.ID)
	if e != nil {
		writeError(w, 503, "Could not queue the operation.")
		return
	}
	writeJSON(w, 202, j)
}
func (s *Service) prepareMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingJob(w, r, "prepare")
}
func (s *Service) endMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingJob(w, r, "end")
}
func (s *Service) extendMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	var q ExtendMeeting
	if !decode(w, r, &q) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.Store.Meeting(r.Context())
	if e != nil || m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) || q.Hours < 1 || q.Hours > 8 || m.Expires.Add(time.Duration(q.Hours)*time.Hour).After(m.Created.Add(8*time.Hour)) {
		writeError(w, 409, "Only an active meeting can be extended, up to eight hours total.")
		return
	}
	m.Expires = m.Expires.Add(time.Duration(q.Hours) * time.Hour)
	if e = s.Store.Put(r.Context(), "meeting", "current", m); e != nil {
		writeError(w, 503, "Could not save the new deadline.")
		return
	}
	writeJSON(w, 200, m)
}
func (s *Service) refreshTemplates(w http.ResponseWriter, r *http.Request, v Session) {
	j, e := s.Store.Enqueue(r.Context(), "refresh", "repository")
	if e != nil {
		writeError(w, 503, "Could not queue the refresh.")
		return
	}
	writeJSON(w, 202, j)
}
func (s *Service) buildTemplate(w http.ResponseWriter, r *http.Request, v Session) {
	var q TargetRequest
	if !decode(w, r, &q) {
		return
	}
	var t Template
	if e := s.Store.Get(r.Context(), "template", q.ID, &t); e != nil {
		writeError(w, 404, "Template revision not found.")
		return
	}
	j, e := s.Store.Enqueue(r.Context(), "build", q.ID)
	if e != nil {
		writeError(w, 503, "Could not queue the build.")
		return
	}
	writeJSON(w, 202, j)
}
func (s *Service) retryJob(w http.ResponseWriter, r *http.Request, v Session) {
	var q TargetRequest
	if !decode(w, r, &q) {
		return
	}
	var kind, target, status string
	e := s.Store.DB.QueryRowContext(r.Context(), `SELECT kind,target,status FROM jobs WHERE id=?`, q.ID).Scan(&kind, &target, &status)
	if e != nil || status != "failed" {
		writeError(w, 409, "Only a failed job can be retried.")
		return
	}
	j, e := s.Store.Enqueue(r.Context(), kind, target)
	if e != nil {
		writeError(w, 503, "Could not queue recovery.")
		return
	}
	writeJSON(w, 202, j)
}
func (s *Service) plan(ctx context.Context, id string) (lc.Plan, error) {
	var src lc.Source
	if e := s.Store.Get(ctx, "source", id, &src); e != nil {
		return lc.Plan{}, e
	}
	return lc.PlanLab(src, s.env, nil)
}
func (s *Service) readyTeam(ctx context.Context, user string) (Team, *Meeting, error) {
	m, e := s.Store.Meeting(ctx)
	if e != nil || m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
		return Team{}, m, fmt.Errorf("meeting is not active")
	}
	membership, e := s.Store.Membership(ctx, user)
	if e != nil || membership.Switching || membership.TeamID == "" {
		return Team{}, m, fmt.Errorf("join a team before opening your desktop")
	}
	teams, e := s.Store.Teams(ctx, m.ID)
	if e != nil {
		return Team{}, m, e
	}
	for _, t := range teams {
		if t.ID == membership.TeamID && t.Status == "ready" {
			return t, m, nil
		}
	}
	return Team{}, m, fmt.Errorf("your team lab is not ready")
}
