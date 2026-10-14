package hosted

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The view and mutation endpoint share eligibility. Historical failures remain
// auditable; obsolete guest actions cannot replay into a new meeting. Cleanup
// failures retain attention unless another attempt actually replaces them.
func (s *Service) retryDisposition(ctx context.Context, j Job) (blocked string, attention bool, err error) {
	attention = true
	var latestID, latestStatus string
	err = s.Store.DB.QueryRowContext(ctx, `SELECT id,status FROM jobs WHERE kind=? AND target=? ORDER BY created DESC,id DESC LIMIT 1`, j.Kind, j.Target).Scan(&latestID, &latestStatus)
	if err != nil {
		return "", true, err
	}
	if latestID != j.ID {
		if latestStatus == "complete" {
			return "Recovery completed in a later attempt.", false, nil
		}
		if latestStatus == "queued" || latestStatus == "running" {
			return "Recovery is already queued or running.", false, nil
		}
		return "Use the latest failed attempt.", false, nil
	}
	switch j.Kind {
	case "practice-prepare", "practice-end":
		run, err := s.Store.practice(ctx, j.Target)
		if err != nil {
			return "", true, err
		}
		if run.Status == "ended" {
			return "This cloud run has been cleaned up.", false, nil
		}
		if j.Kind == "practice-prepare" && (run.Status == "ending" || !run.Expires.After(time.Now())) {
			return "Cloud time ended. Stop this run to finish cleanup.", false, nil
		}
		if j.Kind == "practice-prepare" && run.Status == "ready" {
			return "This cloud run is already ready.", false, nil
		}
	case "learning-check":
		return "Action checks are retried from the current lab stage.", false, nil
	case "build", "remove-images":
		var latest string
		err = s.Store.Get(ctx, "template-operation", j.Target, &latest)
		if err == nil && latest != j.ID {
			return "This operation was superseded. Use the revision's current action.", false, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", true, err
		}
	case "check", "exercise":
		var run struct {
			ID        string `json:"id"`
			MeetingID string `json:"meetingID"`
			TeamID    string `json:"teamID"`
			Status    string `json:"status"`
		}
		if err = s.Store.Get(ctx, j.Kind, j.Target, &run); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "Recovery record unavailable. Contact the operator.", true, nil
			}
			return "", true, err
		}
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return "", true, err
		}
		if m == nil || m.ID != run.MeetingID || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
			return "This meeting ended or expired. Run checks or exercises in the current meeting.", false, nil
		}
		var current string
		if err = s.Store.Get(ctx, j.Kind+"-current", run.TeamID, &current); err != nil {
			return "", true, err
		}
		if current != run.ID {
			return "A newer team action superseded this run. Use the current checks or exercise.", false, nil
		}
		if run.Status == "complete" {
			return "This run already completed.", false, nil
		}
		if j.Kind == "exercise" && (run.Status == "unknown" || run.Status == "resetting" || run.Status == "applying") {
			return "Guest mutation outcome is unknown. Run an explicit reset.", true, nil
		}
		var state string
		if err = s.Store.DB.QueryRowContext(ctx, `SELECT status FROM teams WHERE id=? AND meeting_id=?`, run.TeamID, run.MeetingID).Scan(&state); err != nil {
			return "", true, err
		}
		if state != "ready" {
			return "The team lab must be ready before retrying.", true, nil
		}
	case "prepare":
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return "", true, err
		}
		if m == nil || m.ID != j.Target || m.Status == "ending" || m.Status == "ended" || !m.Expires.After(time.Now()) {
			return "This meeting ended or expired. Prepare the current meeting instead.", false, nil
		}
	}
	return "", true, nil
}
