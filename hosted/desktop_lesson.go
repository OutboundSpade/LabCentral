package hosted

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The panel uses the same built daisyUI/Tailwind stylesheet as the static UI.
// Assets stay on the authenticated desktop origin; no third-party CDN is needed.
func (s *Service) desktopLessonStyles(w http.ResponseWriter, r *http.Request) {
	paths, err := filepath.Glob(filepath.Join(s.Config.WebRoot, "_app", "immutable", "assets", "*.css"))
	if err != nil || len(paths) == 0 {
		http.Error(w, "Lesson styles are unavailable.", 503)
		return
	}
	if len(paths) == 1 {
		// Preserve Vite's relative font URLs by keeping the stylesheet at its
		// original asset URL on the desktop origin.
		http.Redirect(w, r, "/_app/immutable/assets/"+filepath.Base(paths[0]), http.StatusTemporaryRedirect)
		return
	}
	var data []byte
	for _, path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil || len(data)+len(raw) > 4<<20 {
			http.Error(w, "Lesson styles are unavailable.", 503)
			return
		}
		rebased := strings.NewReplacer("url(./", "url(/_app/immutable/assets/", `url("./`, `url("/_app/immutable/assets/`, "url('./", "url('/_app/immutable/assets/").Replace(string(raw))
		data = append(data, rebased...)
		data = append(data, '\n')
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(data)
}

// The authenticated desktop origin can display the owner's current lesson beside
// the DCV viewport. Its writes have their own exact-origin and custom-header CSRF
// checks; browser cross-origin forms cannot send the required JSON header.
func (s *Service) desktopLesson(w http.ResponseWriter, r *http.Request, d *desktopSession) {
	if !strings.HasPrefix(d.record.TeamID, "practice:") {
		http.NotFound(w, r)
		return
	}
	// Cookies are shared across tabs on this origin. Bind the lesson request to
	// the allocation rendered into this window so another launch cannot silently
	// redirect a draft or a late submission to a different lab.
	if r.Header.Get("X-LabCentral-Practice-Run") != strings.TrimPrefix(d.record.TeamID, "practice:") {
		writeError(w, 403, "This desktop window no longer matches your cloud lab. Reopen it from LabCentral.")
		return
	}
	run, err := s.Store.practice(r.Context(), strings.TrimPrefix(d.record.TeamID, "practice:"))
	if err != nil || run.UserID != d.record.UserID || run.InstanceID != d.record.InstanceID || run.Status != "ready" {
		writeError(w, 403, "Your cloud lab is no longer available.")
		return
	}
	v, err := s.checkedSession(r.Context(), d.record.SessionID)
	if err != nil || v.User.ID != run.UserID {
		writeError(w, 403, "Your sign-in could not be verified.")
		return
	}
	r.SetPathValue("id", run.LabID)
	if r.URL.Path == "/lesson" && r.Method == "GET" {
		if err = s.touchLearning(r.Context(), v.User.ID, run.LabID); err != nil {
			writeError(w, 503, "Lesson activity could not be saved.")
			return
		}
		s.learningView(w, r, v)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "Use POST for lesson submissions.")
		return
	}
	if r.Header.Get("Origin") != s.Config.DesktopOrigin || r.Header.Get("X-LabCentral-Lesson") != "1" || r.Header.Get("Content-Type") != "application/json" {
		writeError(w, 403, "Lesson request verification failed.")
		return
	}
	switch r.URL.Path {
	case "/lesson/answer":
		s.answerLearning(w, r, v)
	case "/lesson/check":
		s.checkLearning(w, r, v)
	default:
		http.NotFound(w, r)
	}
}
