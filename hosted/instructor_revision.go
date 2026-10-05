package hosted

import (
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"time"
)

// Explicitly move an idle meeting to immutable instructor metadata without
// changing portable source, native images, instances or student workspaces.
func (s *Service) updateInstructorRevision(w http.ResponseWriter, r *http.Request, v Session) {
	var req TargetRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	ctx := r.Context()
	m, err := s.Store.Meeting(ctx)
	if err != nil || m == nil || m.Status != "ready" || !m.Expires.After(time.Now()) {
		writeError(w, 409, "Only a ready, active meeting can adopt instructor updates.")
		return
	}
	var target, original Template
	if s.Store.Get(ctx, "template", req.ID, &target) != nil || s.Store.Get(ctx, "template", m.Template, &original) != nil || !target.Built || target.Removing || target.Instructor == nil || target.Commit == "" {
		writeError(w, 409, "Choose a built, retained instructor revision.")
		return
	}
	var oldSource, newSource lc.Source
	var bundle InstructorBundle
	if s.Store.Get(ctx, "source", original.ID, &oldSource) != nil || s.Store.Get(ctx, "source", target.ID, &newSource) != nil || oldSource.Digest != newSource.Digest || oldSource.Lab.Name != newSource.Lab.Name {
		writeError(w, 409, "This revision changes lab files; use it for a new meeting.")
		return
	}
	if err = lc.Validate(newSource); err != nil {
		writeError(w, 409, "The target source is invalid.")
		return
	}
	if err = lc.Validate(oldSource); err != nil {
		writeError(w, 409, "The current source is invalid.")
		return
	}
	if err = s.Store.Get(ctx, "template-instructor", target.ID, &bundle); err != nil || bundle.validate(newSource) != nil {
		writeError(w, 409, "The target instructor revision is invalid.")
		return
	}
	if m.Template == target.ID {
		writeJSON(w, 200, m)
		return
	}
	meetingClaim := []jobClaim{{"meeting:" + m.ID, true}}
	for _, active := range s.activeJobs {
		if claimsConflict(meetingClaim, active.claims) {
			writeError(w, 409, "Wait for active meeting work to finish.")
			return
		}
	}
	jobs, err := s.Store.Jobs(ctx)
	if err != nil {
		writeError(w, 503, "Could not inspect meeting work.")
		return
	}
	for _, job := range jobs {
		if job.Status != "queued" && job.Status != "running" {
			continue
		}
		claims, err := s.jobClaims(ctx, job)
		if err != nil || claimsConflict(meetingClaim, claims) {
			writeError(w, 409, "Wait for meeting preparation, exercises and service checks to finish.")
			return
		}
	}
	change := struct {
		Meeting, From, To, Actor string
		Changed                  time.Time
	}{m.ID, m.Template, target.ID, v.User.ID, time.Now().UTC()}
	m.Template = target.ID
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err == nil {
		defer tx.Rollback()
		for _, record := range []struct {
			kind, id string
			value    any
		}{{"meeting", "current", m}, {"instructor-revision", randomID(12), change}} {
			var data []byte
			data, err = json.Marshal(record.value)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO objects(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, record.kind, record.id, data)
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		writeError(w, 503, "Could not persist the instructor revision.")
		return
	}
	writeJSON(w, 200, m)
}
