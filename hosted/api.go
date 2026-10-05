package hosted

import (
	"context"
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
func (s *Service) admin(w http.ResponseWriter, r *http.Request, v Session) {
	ctx := r.Context()
	out := AdminView{Teams: []Team{}, Jobs: []Job{}, Templates: []Template{}, CheckRuns: []CheckRun{}, ExerciseRuns: []ExerciseRun{}, Exercises: []ExerciseDefinition{}}
	var err error
	out.Meeting, err = s.Store.Meeting(ctx)
	if err == nil && out.Meeting != nil {
		out.Teams, err = s.Store.Teams(ctx, out.Meeting.ID)
	}
	if err == nil {
		out.Jobs, err = s.Store.Jobs(ctx)
	}
	if err == nil {
		err = s.jobDetails(ctx, out.Jobs)
	}
	if err == nil {
		out.Templates, err = s.templates(ctx)
	}
	if err == nil {
		for n := range out.Templates {
			if out.Templates[n].Operation != nil {
				jobs := []Job{*out.Templates[n].Operation}
				err = s.jobDetails(ctx, jobs)
				if err != nil {
					break
				}
				out.Templates[n].Operation = &jobs[0]
			}
		}
	}
	s.mu.Lock()
	for _, d := range s.desktops {
		if d.ctx.Err() == nil {
			out.ActiveDesktops++
		}
	}
	s.mu.Unlock()
	if err == nil && out.Meeting != nil {
		for _, template := range out.Templates {
			if template.ID != out.Meeting.Template {
				continue
			}
			if template.Instructor != nil {
				out.Exercises = template.Instructor.Exercises
			} else if template.Name == "branch-services" {
				out.Exercises = exerciseDefinitions()
			}
			break
		}
		out.ExerciseRuns, err = s.exerciseRuns(ctx, out.Teams)
		if err == nil {
			out.CheckSchedule, out.CheckRuns, err = s.checkView(ctx, *out.Meeting, out.Teams)
		}
	}
	if err != nil {
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
		q.Capacity = 6
	}
	if !cleanName(q.Name) || q.Hours < 1 || q.Hours > 8 || q.TeamCount < 1 || q.TeamCount > 100 || q.Capacity < 1 || q.Capacity > 100 {
		writeError(w, 400, "Use a name, one to eight hours, up to 100 teams, and up to 100 students per team.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var template Template
	if e := s.Store.Get(r.Context(), "template", q.Template, &template); e != nil || !template.Built || template.Removing {
		writeError(w, 409, "Build a template revision before creating the meeting.")
		return
	}
	now := time.Now().UTC()
	m := Meeting{randomID(12), q.Name, q.Template, "waiting", now, now.Add(time.Duration(q.Hours) * time.Hour)}
	e := s.Store.CreateMeeting(r.Context(), m, q.TeamCount, q.Capacity)
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
	if e != nil || m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) || q.Hours < 1 || q.Hours > 8 {
		writeError(w, 409, "Only an active meeting can be extended, up to eight hours total.")
		return
	}
	expires := m.Expires.Add(time.Duration(q.Hours) * time.Hour)
	limit := m.Created.Add(8 * time.Hour)
	if expires.After(limit) {
		// Earlier creations read the clock twice; preserve their intended whole-hour limit.
		if expires.Sub(limit) > time.Millisecond {
			writeError(w, 409, "Only an active meeting can be extended, up to eight hours total.")
			return
		}
		expires = limit
	}
	m.Expires = expires
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
	s.templateOperation(w, r, "build")
}
func (s *Service) removeTemplateImages(w http.ResponseWriter, r *http.Request, v Session) {
	s.templateOperation(w, r, "remove-images")
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
	if kind == "build" || kind == "remove-images" {
		var latest string
		if s.Store.Get(r.Context(), "template-operation", target, &latest) == nil && latest != q.ID {
			writeError(w, 409, "This operation was superseded. Use the revision's current action.")
			return
		}
		j, err := s.queueTemplateOperation(r.Context(), kind, target)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 202, j)
		return
	}
	if kind == "exercise" {
		s.mu.Lock()
		defer s.mu.Unlock()
		var run ExerciseRun
		var latest string
		m, err := s.Store.Meeting(r.Context())
		if err != nil || m == nil || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) ||
			s.Store.Get(r.Context(), "exercise", target, &run) != nil || run.MeetingID != m.ID ||
			s.Store.Get(r.Context(), "exercise-current", run.TeamID, &latest) != nil || latest != run.ID {
			writeError(w, 409, "This exercise is expired or superseded. Select a current team action.")
			return
		}
	}
	if kind == "check" {
		s.mu.Lock()
		defer s.mu.Unlock()
		var run CheckRun
		var latest string
		m, err := s.checkMeeting(r.Context())
		if err != nil || s.Store.Get(r.Context(), "check", target, &run) != nil || run.MeetingID != m.ID || s.Store.Get(r.Context(), "check-current", run.TeamID, &latest) != nil || latest != run.ID {
			writeError(w, 409, "This check is expired or superseded. Run current checks instead.")
			return
		}
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
