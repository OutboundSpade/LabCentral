package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"regexp"
	"strings"
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
				policy, policyErr := s.Store.instructorPolicy(ctx, out.Meeting.Template)
				if policyErr != nil {
					writeError(w, 503, "Lab information is unavailable.")
					return
				}
				inst, e := s.Engine.Inspect(ctx, team.InstanceID)
				if e == nil {
					for _, m := range inst.Plan.Machines {
						if !v.User.Admin && lc.Contains(policy.Visibility.HiddenMachines, m.Name) {
							continue
						}
						out.Lab.Machines = append(out.Lab.Machines, Machine{Name: m.Name, OS: lc.OSFamily(m.OS), Status: inst.Status, Desktop: lc.Contains(m.Spec.Access, "desktop")})
					}
				}
			}
		}
	}
	if out.Lab.Team != nil && out.Meeting != nil && !out.Membership.Switching && out.Meeting.Status != "ending" && out.Meeting.Status != "ended" && out.Meeting.Expires.After(time.Now()) {
		s.desktopMu.Lock()
		for _, d := range s.desktops {
			if desktopAvailable(d, time.Now()) && d.record.UserID == v.User.ID && d.record.SessionID == v.ID && d.record.TeamID == out.Lab.Team.ID && d.record.InstanceID == out.Lab.Team.InstanceID {
				out.Lab.DesktopActive = true
				break
			}
		}
		s.desktopMu.Unlock()
		machine, e := s.desktopMachine(ctx, out.Meeting.Template)
		if e == nil {
			os, e := s.desktopOS(ctx, *out.Lab.Team, machine)
			if e == nil && os == "windows" {
				a := accessRecord{ID: digest(out.Lab.Team.InstanceID + "\x00" + machine + "\x00" + v.User.ID)[:24], UserID: v.User.ID, TeamID: out.Lab.Team.ID, InstanceID: out.Lab.Team.InstanceID, Machine: machine, OS: os}
				state, e := s.desktopState(ctx, a)
				if e != nil {
					writeError(w, 503, "Desktop state is unavailable.")
					return
				}
				out.Lab.DesktopState = &state
			}
		}
	}
	writeJSON(w, 200, out)
}

// Report an available authenticated relay, not a browser connection or fresh guest probe.
func desktopAvailable(d *desktopSession, now time.Time) bool {
	return d.ctx.Err() == nil && d.record.Expires.After(now) && d.info.Expires.After(now)
}

var repositoryPath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func repositoryURL(repository string) string {
	const prefix = "git@github.com:"
	if !strings.HasPrefix(repository, prefix) {
		return ""
	}
	path := strings.TrimSuffix(strings.TrimPrefix(repository, prefix), ".git")
	if !repositoryPath.MatchString(path) {
		return ""
	}
	return "https://github.com/" + path
}
func (s *Service) join(w http.ResponseWriter, r *http.Request, v Session) {
	var req JoinTeam
	if !decode(w, r, &req) {
		return
	}
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
		if e = s.Store.FinishSwitchTo(r.Context(), v.User.ID, req.TeamID); e != nil {
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
	out := AdminView{RepositoryURL: repositoryURL(s.Config.Repository), TeamDesktops: []TeamDesktopCount{}, Teams: []Team{}, Jobs: []Job{}, Templates: []Template{}, CheckRuns: []CheckRun{}, BreakRuns: []BreakRun{}, Breaks: []BreakDefinition{}, ImportFailures: []TemplateImportFailure{}}
	var err error
	out.Meeting, err = s.Store.Meeting(ctx)
	if err == nil {
		var report TemplateImportReport
		e := s.Store.Get(ctx, "template-import-report", "current", &report)
		if e == nil {
			out.ImportFailures = report.Failures
		} else if !errors.Is(e, sql.ErrNoRows) {
			err = e
		}
	}
	if err == nil && out.Meeting != nil {
		out.Teams, err = s.Store.Teams(ctx, out.Meeting.ID)
		if err == nil {
			out.JoinedStudents, err = s.Store.JoinedStudents(ctx, out.Meeting.ID)
		}
	}
	if err == nil {
		out.Jobs, err = s.Store.Jobs(ctx)
	}
	if err == nil {
		err = s.jobDetails(ctx, out.Jobs)
	}
	if err == nil {
		out.Templates, err = s.templates(ctx)
		for _, t := range out.Templates {
			if t.Problem != "" {
				out.ImportFailures = append(out.ImportFailures, TemplateImportFailure{Directory: t.Name + " (" + t.ID + ")", Error: t.Problem})
			}
		}
	}
	if err == nil && s.Engine != nil {
		summary, _, inventoryErr := s.imageInventory(ctx)
		if inventoryErr == nil {
			out.ImageSummary = &summary
		}
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
	s.desktopMu.Lock()
	counts := map[string]int{}
	now := time.Now()
	for _, d := range s.desktops {
		if desktopAvailable(d, now) {
			out.ActiveDesktops++
			counts[d.record.TeamID]++
		}
	}
	s.desktopMu.Unlock()
	for _, team := range out.Teams {
		out.TeamDesktops = append(out.TeamDesktops, TeamDesktopCount{TeamID: team.ID, Count: counts[team.ID]})
	}
	if err == nil && out.Meeting != nil {
		for _, template := range out.Templates {
			if template.ID != out.Meeting.Template {
				continue
			}
			if template.Instructor != nil {
				out.Breaks = template.Instructor.Breaks

			}
			break
		}
		out.BreakRuns, err = s.exerciseRuns(ctx, out.Teams)
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
func (s *Service) createMeetingDecision(w http.ResponseWriter, r *http.Request, v Session) {
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
	if !cleanName(q.Name) || q.Hours < 1 || q.Hours > 8 || q.TeamCount < 1 || q.TeamCount > maxMeetingTeams || q.Capacity < 1 || q.Capacity > 100 {
		writeError(w, 400, "Use a name, one to eight hours, up to 100 teams, and up to 100 students per team.")
		return
	}
	var template Template
	if e := s.Store.Get(r.Context(), "template", q.Template, &template); e != nil || !template.Built || template.Removing {
		writeError(w, 409, "Build a template revision before creating the meeting.")
		return
	}
	if err := s.revisionPruneBlock(r.Context(), q.Template); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err := s.actionableRevision(r.Context(), q.Template); err != nil {
		writeError(w, 409, err.Error())
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
	m, e := s.Store.Meeting(r.Context())
	if e != nil || m == nil || m.Status == "ended" {
		writeError(w, 409, "No current meeting.")
		return
	}
	if kind == "prepare" && (m.Status == "ending" || !m.Expires.After(time.Now())) {
		writeError(w, 409, "The meeting has ended.")
		return
	}
	if kind == "prepare" {
		if err := s.actionableRevision(r.Context(), m.Template); err != nil {
			writeError(w, 409, err.Error())
			return
		}
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
func (s *Service) prepareMeetingDecision(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingJob(w, r, "prepare")
}
func (s *Service) endMeetingDecision(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingJob(w, r, "end")
}
func (s *Service) extendMeetingDecision(w http.ResponseWriter, r *http.Request, v Session) {
	var q ExtendMeeting
	if !decode(w, r, &q) {
		return
	}
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
func (s *Service) retryJobDecision(w http.ResponseWriter, r *http.Request, v Session) {
	var q TargetRequest
	if !decode(w, r, &q) {
		return
	}
	var job Job
	e := s.Store.DB.QueryRowContext(r.Context(), `SELECT id,kind,target,status FROM jobs WHERE id=?`, q.ID).Scan(&job.ID, &job.Kind, &job.Target, &job.Status)
	if e != nil || job.Status != "failed" {
		writeError(w, 409, "Only a failed job can be retried.")
		return
	}
	blocked, _, e := s.retryDisposition(r.Context(), job)
	if e != nil {
		writeError(w, 503, "Could not verify recovery eligibility.")
		return
	}
	if blocked != "" {
		writeError(w, 409, blocked)
		return
	}
	var next Job
	if job.Kind == "build" || job.Kind == "remove-images" {
		next, e = s.queueTemplateDecision(r.Context(), job.Kind, job.Target)
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
	} else {
		next, e = s.Store.Enqueue(r.Context(), job.Kind, job.Target)
	}
	if e != nil {
		writeError(w, 503, "Could not queue recovery.")
		return
	}
	writeJSON(w, 202, next)
}

func (s *Service) plan(ctx context.Context, id string) (lc.Plan, error) {
	if err := s.actionableRevision(ctx, id); err != nil {
		return lc.Plan{}, err
	}
	var src lc.Source
	if e := s.Store.Get(ctx, "source", id, &src); e != nil {
		return lc.Plan{}, e
	}
	return s.planSource(src)
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

func (s *Service) createMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.createMeetingDecision)(w, r, v)
}

func (s *Service) extendMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.extendMeetingDecision)(w, r, v)
}

func (s *Service) retryJob(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.retryJobDecision)(w, r, v)
}

func (s *Service) prepareMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.prepareMeetingDecision)(w, r, v)
}

func (s *Service) endMeeting(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(s.endMeetingDecision)(w, r, v)
}

// maxMeetingTeams bounds one meeting and any per-team request within it.
const maxMeetingTeams = 100
