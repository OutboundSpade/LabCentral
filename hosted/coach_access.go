package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

type CoachMachines struct {
	TeamID      string    `json:"teamID"`
	TeamName    string    `json:"teamName"`
	MeetingID   string    `json:"meetingID"`
	MeetingName string    `json:"meetingName"`
	Template    string    `json:"template"`
	InstanceID  string    `json:"instanceID"`
	Machines    []Machine `json:"machines"`
}

// Administrator access is bound to a current meeting team and pinned guest.
// Control frames never take a guest address, key, command job or provider resource.
func (s *Service) coachTeam(ctx context.Context, id string) (Team, Meeting, lc.Source, error) {
	meeting, err := s.Store.Meeting(ctx)
	if err != nil || meeting == nil || meeting.Status == "ending" || meeting.Status == "ended" || !meeting.Expires.After(time.Now()) {
		return Team{}, Meeting{}, lc.Source{}, fmt.Errorf("Meeting access is unavailable.")
	}
	teams, err := s.Store.Teams(ctx, meeting.ID)
	if err != nil {
		return Team{}, Meeting{}, lc.Source{}, fmt.Errorf("Team access is unavailable.")
	}
	for _, team := range teams {
		if team.ID == id && team.Status == "ready" && team.InstanceID != "" {
			var src lc.Source
			if s.Store.Get(ctx, "source", meeting.Template, &src) != nil {
				return Team{}, Meeting{}, src, fmt.Errorf("Pinned source is unavailable.")
			}
			return team, *meeting, src, nil
		}
	}
	return Team{}, Meeting{}, lc.Source{}, fmt.Errorf("Choose a ready team in the current meeting.")
}
func (s *Service) coachMachines(w http.ResponseWriter, r *http.Request, v Session) {
	team, meeting, src, err := s.coachTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	inst, err := s.Engine.Inspect(r.Context(), team.InstanceID)
	if err != nil || inst.Status != "ready" || inst.Plan.Source.Digest != src.Digest {
		writeError(w, 503, "Pinned machines are unavailable.")
		return
	}
	out := CoachMachines{TeamID: team.ID, TeamName: team.Name, MeetingID: meeting.ID, MeetingName: meeting.Name, Template: meeting.Template, InstanceID: team.InstanceID, Machines: []Machine{}}
	for _, m := range inst.Plan.Machines {
		if spec, declared := src.Lab.Machines[m.Name]; !declared || !lc.Contains(spec.Access, "ssh") {
			continue
		}
		out.Machines = append(out.Machines, Machine{Name: m.Name, OS: lc.OSFamily(m.OS), Status: inst.Status, Desktop: lc.Contains(m.Spec.Access, "desktop")})
	}
	writeJSON(w, 200, out)
}
func (s *Service) coachAuthorized(ctx context.Context, v Session, team Team, meeting Meeting) bool {
	current, err := s.checkedSession(ctx, v.ID)
	if err != nil || !current.User.Admin || current.User.ID != v.User.ID {
		return false
	}
	nowTeam, nowMeeting, _, err := s.coachTeam(ctx, team.ID)
	return err == nil && nowTeam.InstanceID == team.InstanceID && nowMeeting.ID == meeting.ID && nowMeeting.Template == meeting.Template
}

const (
	coachSessionRenewal websocket.StatusCode = 4000
	coachAccessRevoked  websocket.StatusCode = 4003
)

var errCoachRevoked = errors.New("console authorization ended")
var errCoachProtocol = errors.New("invalid terminal control message")

type coachTerminalHello struct {
	CSRF       string `json:"csrf"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	MeetingID  string `json:"meetingID"`
	Template   string `json:"template"`
	InstanceID string `json:"instanceID"`
}
type coachResize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func coachDimensions(cols, rows int) bool {
	return cols >= 2 && cols <= 500 && rows >= 2 && rows <= 200
}
func (h coachTerminalHello) valid(v Session, meeting Meeting) bool {
	return h.CSRF == v.CSRF && (h.MeetingID == "" || h.MeetingID == meeting.ID) &&
		(h.Template == "" || h.Template == meeting.Template) &&
		((h.Cols == 0 && h.Rows == 0) || coachDimensions(h.Cols, h.Rows))
}

// The WebSocket holds no recoverable command queue: reconnecting reauthorizes
// the pinned allocation and starts a fresh guest shell. Cleanup frees its fence.
func (s *Service) coachTerminal(w http.ResponseWriter, r *http.Request, v Session) {
	if r.Header.Get("Origin") != s.Config.Origin {
		writeError(w, 403, "Request verification failed.")
		return
	}
	team, meeting, src, err := s.coachTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	name := r.PathValue("machine")
	spec, ok := src.Lab.Machines[name]
	engine, supported := s.Engine.(exerciseSSH)
	if !ok || !lc.Contains(spec.Access, "ssh") || !supported {
		writeError(w, 404, "Machine SSH access is unavailable.")
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(16 << 10)
	expires := minTime(meeting.Expires, time.Now().Add(10*time.Minute))
	deadlineCtx, stopDeadline := context.WithDeadline(r.Context(), expires)
	defer stopDeadline()
	ctx, cancel := context.WithCancelCause(deadlineCtx)
	defer cancel(nil)
	// Native cancellation must not abort the WebSocket read before the browser
	// receives a policy/renewal close frame. Only this handler owns wire shutdown.
	wireCtx, stopWire := context.WithCancel(r.Context())
	defer stopWire()
	helloCtx, helloCancel := context.WithTimeout(wireCtx, 5*time.Second)
	kind, data, err := connection.Read(helloCtx)
	helloCancel()
	var hello coachTerminalHello
	if err != nil || kind != websocket.MessageText || json.Unmarshal(data, &hello) != nil || !hello.valid(v, meeting) || (hello.InstanceID != "" && hello.InstanceID != team.InstanceID) || !s.coachAuthorized(ctx, v, team, meeting) {
		connection.Close(websocket.StatusPolicyViolation, "Request verification failed")
		return
	}
	if hello.Cols == 0 {
		hello.Cols, hello.Rows = 100, 30
	}
	type frame struct {
		kind websocket.MessageType
		data []byte
	}
	frames := make(chan frame, 8)
	go func() {
		for {
			kind, data, err := connection.Read(wireCtx)
			if err != nil {
				cancel(err)
				return
			}
			select {
			case frames <- frame{kind, data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(ctx, 5*time.Second)
				allowed := s.coachAuthorized(check, v, team, meeting)
				stop()
				if !allowed {
					cancel(errCoachRevoked)
					return
				}
			}
		}
	}()
	go func() {
		// Detect a half-open browser connection even while the guest is quiet.
		// The reader above handles control frames; pings contain no credentials.
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(wireCtx, 10*time.Second)
				err := connection.Ping(pingCtx)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	release, admit := s.acquireSSH(ctx)
	if admit != nil {
		return
	}
	defer release()
	err = engine.WithSSHMaterial(ctx, team.InstanceID, name, func(inst lc.Instance, m lc.PlannedMachine, res lc.Resource, key []byte) error {
		if !s.coachAuthorized(ctx, v, team, meeting) || inst.ID != team.InstanceID || inst.Status != "ready" || inst.Plan.Source.Digest != src.Digest || !coachGuestMatches(team.InstanceID, name, m, res) || lc.OSFamily(m.OS) != lc.OSFamily(src.Lab.Images[spec.Image].OS) {
			cancel(errCoachRevoked)
			return errCoachRevoked
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
		if err := ctx.Err(); err != nil {
			return err
		}
		clientDone := make(chan struct{})
		defer close(clientDone)
		go func() {
			select {
			case <-ctx.Done():
				client.Close()
			case <-clientDone:
			}
		}()
		terminal, err := client.NewSession()
		if err != nil {
			return err
		}
		defer terminal.Close()
		input, err := terminal.StdinPipe()
		if err != nil {
			return err
		}
		output, err := terminal.StdoutPipe()
		if err != nil {
			return err
		}
		terminal.Stderr = io.Discard // PTY combines output; provider diagnostics stay private.
		if err = terminal.RequestPty("xterm-256color", hello.Rows, hello.Cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return err
		}
		if err = terminal.Shell(); err != nil {
			return err
		}
		ready, _ := json.Marshal(struct {
			Type    string    `json:"type"`
			Expires time.Time `json:"expires"`
		}{"ready", expires})
		writeCtx, stopWrite := context.WithTimeout(wireCtx, 10*time.Second)
		err = connection.Write(writeCtx, websocket.MessageText, ready)
		stopWrite()
		if err != nil {
			return err
		}
		outputErrors := make(chan error, 1)
		outputDone := make(chan struct{})
		go func() {
			defer close(outputDone)
			buffer := make([]byte, 4096)
			for {
				n, err := output.Read(buffer)
				if ctx.Err() != nil {
					return
				}
				if n > 0 {
					writeCtx, stop := context.WithTimeout(wireCtx, 10*time.Second)
					e := connection.Write(writeCtx, websocket.MessageBinary, buffer[:n])
					stop()
					if e != nil {
						outputErrors <- e
						return
					}
				}
				if err != nil {
					if !errors.Is(err, io.EOF) {
						outputErrors <- err
					}
					return
				}
			}
		}()
		shellDone := make(chan error, 1)
		go func() { shellDone <- terminal.Wait() }()
		for {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case err := <-outputErrors:
				return err
			case err := <-shellDone:
				var exit *ssh.ExitError
				if err == nil || errors.As(err, &exit) {
					// Wait reports the shell exit before the browser writer may have
					// drained its last bytes. Preserve the final prompt/output.
					select {
					case <-ctx.Done():
						return context.Cause(ctx)
					case <-outputDone:
					}
					select {
					case err := <-outputErrors:
						return err
					default:
						return nil
					}
				}
				return err
			case frame := <-frames:
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				if frame.kind == websocket.MessageBinary {
					if _, err := input.Write(frame.data); err != nil {
						return err
					}
				} else {
					var resize coachResize
					if json.Unmarshal(frame.data, &resize) != nil || resize.Type != "resize" || !coachDimensions(resize.Cols, resize.Rows) {
						return errCoachProtocol
					}
					// Windows OpenSSH consoles keep the initial PTY size.
					if m.OS == "windows" {
						continue
					}
					if err := terminal.WindowChange(resize.Rows, resize.Cols); err != nil {
						return err
					}
				}
			}
		}
	})
	switch {
	case errors.Is(context.Cause(ctx), errCoachRevoked):
		connection.Close(coachAccessRevoked, "Meeting or administrator access ended")
	case errors.Is(deadlineCtx.Err(), context.DeadlineExceeded):
		if !meeting.Expires.After(time.Now()) {
			connection.Close(coachAccessRevoked, "Meeting access ended")
		} else {
			connection.Close(coachSessionRenewal, "Session limit reached; reconnect starts a fresh shell")
		}
	case errors.Is(err, errCoachProtocol):
		connection.Close(websocket.StatusPolicyViolation, "Invalid terminal control message")
	case err != nil && r.Context().Err() == nil:
		connection.Close(websocket.StatusInternalError, "Guest console unavailable. Reconnect starts a fresh shell.")
	default:
		connection.Close(websocket.StatusNormalClosure, "Console ended")
	}
}

func coachGuestMatches(instance, name string, machine lc.PlannedMachine, resource lc.Resource) bool {
	// AWS uses a public management endpoint while the plan pins its private lab
	// address. Both adapters retain that private address on the owned resource.
	return machine.Name == name && machine.Address != "" && resource.Kind == "machine" && resource.Owner == instance && resource.Name == name && resource.Address != "" && resource.PrivateAddress == machine.Address
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
