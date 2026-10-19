package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Defaults preserve existing installations. The setting belongs to hosted policy.
type practicePolicy struct {
	Enabled  bool   `json:"enabled"`
	Revision string `json:"revision"`
}
type PracticeAllocation struct {
	ID     string `json:"id"`
	User   string `json:"user"`
	Lab    string `json:"lab"`
	Status string `json:"status"`
}
type PracticeSettings struct {
	Enabled      bool                 `json:"enabled"`
	Confirmation string               `json:"confirmation"`
	ActiveLabs   []PracticeAllocation `json:"activeLabs"`
}
type PracticeSettingsRequest struct {
	Enabled      bool   `json:"enabled"`
	Confirmation string `json:"confirmation"`
}

func (s *Store) practicePolicy(ctx context.Context) (practicePolicy, error) {
	p := practicePolicy{Enabled: true}
	err := s.Get(ctx, "site-settings", "practice", &p)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (s *Store) requirePractice(ctx context.Context) error {
	p, err := s.practicePolicy(ctx)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return fmt.Errorf("Practice labs are disabled by an administrator. Team meetings remain available.")
	}
	return nil
}
func (s *Store) practiceSettings(ctx context.Context) (PracticeSettings, error) {
	p, err := s.practicePolicy(ctx)
	out := PracticeSettings{Enabled: p.Enabled, ActiveLabs: []PracticeAllocation{}}
	if err != nil {
		return out, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id,coalesce(json_extract(u.body,'$.name'),r.user_id),coalesce(json_extract(l.body,'$.title'),r.lab_id),r.status FROM practice_runs r LEFT JOIN objects u ON u.kind='user' AND u.id=r.user_id LEFT JOIN objects l ON l.kind='learning-publication' AND l.id=r.lab_id WHERE r.status!='ended' ORDER BY r.id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var a PracticeAllocation
		if err = rows.Scan(&a.ID, &a.User, &a.Lab, &a.Status); err != nil {
			return out, err
		}
		out.ActiveLabs = append(out.ActiveLabs, a)
		ids = append(ids, a.ID)
	}
	out.Confirmation = digest(jsonValue([]any{p, ids}))
	return out, rows.Err()
}
func (s *Service) practiceSettingsView(w http.ResponseWriter, r *http.Request, v Session) {
	var out PracticeSettings
	err := s.Store.atomic(r.Context(), func(ctx context.Context) error { var err error; out, err = s.Store.practiceSettings(ctx); return err })
	if err != nil {
		writeError(w, 503, "Practice settings are unavailable.")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Service) setPracticeSettings(w http.ResponseWriter, r *http.Request, v Session) {
	// Cancel in-flight preparation only after the disable and cleanup jobs commit.
	var cancelled []string
	s.coordinate(func(w http.ResponseWriter, r *http.Request, v Session) {
		var req PracticeSettingsRequest
		if !decode(w, r, &req) {
			return
		}
		current, err := s.Store.practiceSettings(r.Context())
		if err != nil {
			writeError(w, 503, "Practice settings are unavailable.")
			return
		}
		if req.Enabled == current.Enabled {
			writeJSON(w, 200, current)
			return
		}
		if req.Confirmation == "" || req.Confirmation != current.Confirmation {
			writeError(w, 409, "Practice allocations or settings changed. Review the current list and confirm again.")
			return
		}
		if err = s.Store.Put(r.Context(), "site-settings", "practice", practicePolicy{req.Enabled, randomID(12)}); err != nil {
			writeError(w, 503, "Could not save practice settings.")
			return
		}
		if !req.Enabled {
			for _, a := range current.ActiveLabs {
				run, e := s.Store.practice(r.Context(), a.ID)
				if e == nil {
					_, e = s.Store.endPractice(r.Context(), run.ID, run.UserID)
				}
				if e != nil {
					writeError(w, 503, "Could not save cleanup intent.")
					return
				}
				cancelled = append(cancelled, a.ID)
			}
		}
		out, err := s.Store.practiceSettings(r.Context())
		if err != nil {
			writeError(w, 503, "Could not read practice settings.")
			return
		}
		writeJSON(w, 200, out)
	})(w, r, v)
	// Re-enable never cancels cleanup. Read each durable ending intent after commit;
	// rolled-back decisions cannot cancel an unrelated preparation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	for _, id := range cancelled {
		run, err := s.Store.practice(ctx, id)
		if err == nil && run.Status == "ending" {
			s.cancelPracticeJobs(id)
		}
	}
}
func practiceRoute(r *http.Request, admin bool) bool {
	path := r.URL.Path
	return strings.HasPrefix(path, "/api/learning") || (strings.HasPrefix(path, "/api/templates") && (!admin || r.Method != "GET"))
}
