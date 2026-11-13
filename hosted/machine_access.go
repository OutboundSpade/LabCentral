package hosted

import (
	"context"
	"fmt"
	"net/http"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

// Student targets come from current membership, never from a supplied team ID.
func (s *Service) machineTeam(ctx context.Context, v Session, teamID string, coach bool) (Team, Meeting, lc.Source, error) {
	if coach {
		if !v.User.Admin {
			return Team{}, Meeting{}, lc.Source{}, fmt.Errorf("administrator access required")
		}
		return s.coachTeam(ctx, teamID)
	}
	team, meeting, err := s.readyTeam(ctx, v.User.ID)
	if err != nil {
		return Team{}, Meeting{}, lc.Source{}, err
	}
	var src lc.Source
	err = s.Store.Get(ctx, "source", meeting.Template, &src)
	return team, *meeting, src, err
}

func (s *Service) machineAllowed(ctx context.Context, template string, src lc.Source, name, access string, coach bool) bool {
	spec, ok := src.Lab.Machines[name]
	if !ok || (access != "" && !lc.Contains(spec.Access, access)) {
		return false
	}
	if coach {
		return true
	}
	policy, err := s.Store.instructorPolicy(ctx, template)
	return err == nil && !lc.Contains(policy.Visibility.HiddenMachines, name) && (access != "ssh" || lc.Contains(policy.Visibility.ParticipantTerminals, name))
}

func (s *Service) machineRecordAllowed(ctx context.Context, a accessRecord) bool {
	if !a.MachineScoped {
		return true
	}
	meeting, err := s.Store.Meeting(ctx)
	if err != nil || meeting == nil {
		return false
	}
	var src lc.Source
	if s.Store.Get(ctx, "source", meeting.Template, &src) != nil {
		return false
	}
	return s.machineAllowed(ctx, meeting.Template, src, a.Machine, "desktop", a.Coach)
}

func (s *Service) machineAuthorized(ctx context.Context, v Session, team Team, meeting Meeting, src lc.Source, name string, coach bool) bool {
	current, err := s.checkedSession(ctx, v.ID)
	if err != nil || current.User.ID != v.User.ID {
		return false
	}
	nowTeam, nowMeeting, nowSource, err := s.machineTeam(ctx, current, team.ID, coach)
	return err == nil && nowTeam.ID == team.ID && nowTeam.InstanceID == team.InstanceID && nowMeeting.ID == meeting.ID && nowMeeting.Template == meeting.Template && nowSource.Digest == src.Digest && s.machineAllowed(ctx, meeting.Template, nowSource, name, "ssh", coach)
}

func (s *Service) teamMachines(w http.ResponseWriter, r *http.Request, v Session) {
	s.machineInventory(w, r, v, false)
}
func (s *Service) machineInventory(w http.ResponseWriter, r *http.Request, v Session, coach bool) {
	team, meeting, src, err := s.machineTeam(r.Context(), v, r.PathValue("id"), coach)
	if err != nil {
		status := 403
		if coach {
			status = 409
		}
		writeError(w, status, "Team machine access is unavailable.")
		return
	}
	inst, err := s.Engine.Inspect(r.Context(), team.InstanceID)
	if err != nil || inst.ID != team.InstanceID || inst.Status != "ready" || inst.Plan.Source.Digest != src.Digest {
		writeError(w, 503, "Pinned machines are unavailable.")
		return
	}
	out := CoachMachines{TeamID: team.ID, TeamName: team.Name, MeetingID: meeting.ID, MeetingName: meeting.Name, Template: meeting.Template, InstanceID: team.InstanceID, Machines: []Machine{}}
	for _, m := range inst.Plan.Machines {
		if !s.machineAllowed(r.Context(), meeting.Template, src, m.Name, "", coach) {
			continue
		}
		spec := src.Lab.Machines[m.Name]
		view := Machine{Name: m.Name, OS: lc.OSFamily(m.OS), Status: inst.Status, SSH: s.machineAllowed(r.Context(), meeting.Template, src, m.Name, "ssh", coach), Desktop: lc.Contains(spec.Access, "desktop"), Appliance: m.Appliance}
		if m.Appliance == "panos" {
			view.ManagementURL = "https://" + m.Address + "/"
		}
		out.Machines = append(out.Machines, view)
	}
	writeJSON(w, 200, out)
}

func (s *Service) selectedDesktopTarget(ctx context.Context, v Session, teamID, name string) (Team, *Meeting, string, error) {
	if name == "" {
		return s.meetingDesktopTarget(ctx, v, teamID)
	}
	coach := teamID != ""
	team, meeting, src, err := s.machineTeam(ctx, v, teamID, coach)
	if err == nil && !s.machineAllowed(ctx, meeting.Template, src, name, "desktop", coach) {
		err = fmt.Errorf("machine desktop is unavailable")
	}
	return team, &meeting, name, err
}

func (s *Service) closeMachineDesktop(w http.ResponseWriter, r *http.Request, v Session) {
	team, _, name, err := s.selectedDesktopTarget(r.Context(), v, r.PathValue("id"), r.PathValue("machine"))
	if err != nil {
		writeError(w, 403, "Machine access is unavailable.")
		return
	}
	id := digest(team.InstanceID + "\x00" + name + "\x00" + v.User.ID)[:24]
	if err = s.cleanupDesktop(r.Context(), id, false); err != nil {
		writeError(w, 503, "Desktop disconnected; guest cleanup is pending.")
		return
	}
	writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true})
}

func (s *Service) machineDesktopState(ctx context.Context, v Session, team Team, name, os string) (*DesktopState, bool, error) {
	a := accessRecord{ID: digest(team.InstanceID + "\x00" + name + "\x00" + v.User.ID)[:24], UserID: v.User.ID, TeamID: team.ID, InstanceID: team.InstanceID, Machine: name, OS: os, Platform: s.env.Platform}
	state, err := s.desktopState(ctx, a)
	s.desktopMu.Lock()
	d := s.desktops[a.ID]
	active := d != nil && d.record.SessionID == v.ID && desktopAvailable(d, time.Now())
	s.desktopMu.Unlock()
	return &state, active, err
}
