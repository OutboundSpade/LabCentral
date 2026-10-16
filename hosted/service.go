package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Service struct {
	Config         Config
	Store          *Store
	Engine         Engine
	Identity       Identity
	env            lc.Environment
	desktopMu      sync.Mutex // protects only desktop handles and launch tickets
	desktops       map[string]*desktopSession
	tickets        map[string]launchTicket
	accessFlights  map[string]string
	authMu         sync.Mutex
	authLocks      map[string]*sessionLock
	loginMu        sync.Mutex
	userMu         sync.Mutex
	userLocks      map[string]*sessionLock
	sshOnce        sync.Once
	sshSlots       chan struct{}
	jobMu          sync.Mutex
	activeJobs     map[string]runningJob
	queuedClaims   map[string][]jobClaim
	provisionOnce  sync.Once
	provisionSlots chan struct{}
	exerciseRunner func(context.Context, Team, BreakRun, lc.Source) (string, error)
}

// New shares SSH admission with the optional provider wrapper. The server
// creates that wrapper before opening its engine and supplies it here.
func New(ctx context.Context, c Config, e Engine, budgets ...*BudgetedProvider) (*Service, error) {
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}
	if len(budgets) > 1 || (len(budgets) == 1 && budgets[0] == nil) {
		return nil, fmt.Errorf("provide at most one non-nil aggregate provider budget")
	}
	if len(budgets) == 1 {
		limits := c.Aggregate.Effective()
		if cap(budgets[0].slots) != limits.MaxNativeOperations || cap(budgets[0].sshSlots) != limits.MaxSSHSessions {
			return nil, fmt.Errorf("aggregate provider budget must match hosted configuration")
		}
	}
	identity, err := NewOIDC(ctx, c)
	if err != nil {
		return nil, err
	}
	env, err := lc.LoadEnvironment(c.Environment)
	if err != nil {
		return nil, err
	}
	if err = ValidateHostedEnvironment(env); err != nil {
		return nil, err
	}
	st, err := OpenStore(filepath.Join(c.Root, "hosted"))
	if err != nil {
		return nil, err
	}
	service := &Service{Config: c, Store: st, Engine: e, Identity: identity, env: env, desktops: map[string]*desktopSession{}, tickets: map[string]launchTicket{}}
	if len(budgets) > 0 {
		service.sshSlots = budgets[0].sshSlots
	}
	return service, nil
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, struct {
			Status string `json:"status"`
		}{"ok"})
	})
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.authorized(false, s.logout))
	mux.HandleFunc("GET /api/me", s.authorized(false, s.overview))
	mux.HandleFunc("GET /api/templates", s.authorized(false, s.templateCatalog))
	mux.HandleFunc("GET /api/templates/{id}", s.authorized(false, s.templateExplorer))
	mux.HandleFunc("POST /api/templates/{id}/attempt", s.authorized(false, s.createTemplateAttempt))
	mux.HandleFunc("GET /api/learning", s.authorized(false, s.learningCatalog))
	mux.HandleFunc("GET /api/learning/{id}", s.authorized(false, s.learningView))
	mux.HandleFunc("GET /api/learning/{id}/download", s.authorized(false, s.downloadLearning))
	mux.HandleFunc("POST /api/learning/{id}/answer", s.authorized(false, s.answerLearning))
	mux.HandleFunc("POST /api/learning/{id}/solution", s.authorized(false, s.solutionLearning))
	mux.HandleFunc("POST /api/learning/{id}/check", s.authorized(false, s.checkLearning))
	mux.HandleFunc("POST /api/learning/{id}/start", s.authorized(false, s.startPractice))
	mux.HandleFunc("POST /api/learning/{id}/stop", s.authorized(false, s.stopPractice))
	mux.HandleFunc("POST /api/learning/{id}/access", s.authorized(false, s.launchPractice))
	mux.HandleFunc("GET /api/admin/learning", s.authorized(true, s.learningAdmin))
	mux.HandleFunc("POST /api/team", s.authorized(false, s.join))
	mux.HandleFunc("POST /api/access", s.authorized(false, s.launch))
	mux.HandleFunc("POST /api/access/takeover", s.authorized(false, s.takeoverDesktop))
	mux.HandleFunc("GET /api/access/status", s.authorized(false, s.desktopStatus))
	mux.HandleFunc("POST /api/admin/teams/{id}/desktop", s.authorized(true, s.coachDesktop))
	mux.HandleFunc("POST /api/admin/teams/{id}/desktop/takeover", s.authorized(true, s.takeoverDesktop))
	mux.HandleFunc("GET /api/admin/teams/{id}/desktop/status", s.authorized(true, s.desktopStatus))
	mux.HandleFunc("POST /api/access/close", s.authorized(false, s.closeDesktop))
	mux.HandleFunc("GET /api/admin/teams/{id}/machines", s.authorized(true, s.coachMachines))
	mux.HandleFunc("GET /api/admin/teams/{id}/machines/{machine}/terminal", s.authorized(true, s.coachTerminal))
	mux.HandleFunc("GET /api/admin", s.authorized(true, s.admin))
	mux.HandleFunc("POST /api/admin/meeting", s.authorized(true, s.createMeeting))
	mux.HandleFunc("POST /api/admin/prepare", s.authorized(true, s.prepareMeeting))
	mux.HandleFunc("POST /api/admin/end", s.authorized(true, s.endMeeting))
	mux.HandleFunc("POST /api/admin/extend", s.authorized(true, s.extendMeeting))
	mux.HandleFunc("POST /api/admin/templates/refresh", s.authorized(true, s.refreshTemplates))
	mux.HandleFunc("POST /api/admin/templates/build", s.authorized(true, s.buildTemplate))
	mux.HandleFunc("POST /api/admin/templates/remove-images", s.authorized(true, s.removeTemplateImages))
	mux.HandleFunc("POST /api/admin/images/prune", s.authorized(true, s.pruneImages))
	mux.HandleFunc("POST /api/admin/jobs/retry", s.authorized(true, s.retryJob))
	mux.HandleFunc("POST /api/admin/checks/schedule", s.authorized(true, s.setCheckSchedule))
	mux.HandleFunc("POST /api/admin/checks/run", s.authorized(true, s.startChecks))
	mux.HandleFunc("POST /api/admin/exercises", s.authorized(true, s.startExercises))
	mux.Handle("/", http.FileServer(http.Dir(s.Config.WebRoot)))
	origin, _ := url.Parse(s.Config.Origin)
	desktop, _ := url.Parse(s.Config.DesktopOrigin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Cross-origin desktop POSTs need a non-null Origin. Send only the origin,
		// never application paths, login parameters or launch tickets as a referrer.
		w.Header().Set("Referrer-Policy", "strict-origin")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host == desktop.Host {
			s.desktopHandler(w, r)
			return
		}
		if r.Host != origin.Host {
			http.Error(w, "invalid host", 403)
			return
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'none'; form-action 'self' "+s.Config.DesktopOrigin)
		mux.ServeHTTP(w, r)
	})
}

type endpoint func(http.ResponseWriter, *http.Request, Session)

func (s *Service) authorized(admin bool, next endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.session(r.Context(), r)
		if err != nil {
			writeError(w, 401, "Please sign in.")
			return
		}
		if admin && !session.User.Admin {
			writeError(w, 403, "Administrator access is required.")
			return
		}
		if r.Method != "GET" && (r.Header.Get("Origin") != s.Config.Origin || r.Header.Get("X-CSRF-Token") != session.CSRF) {
			writeError(w, 403, "Request verification failed. Reload the page and try again.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), verifiedUserKey{}, session.User)), session)
	}
}
func (s *Service) session(ctx context.Context, r *http.Request) (Session, error) {
	c, e := r.Cookie("__Host-labcentral")
	if e != nil {
		return Session{}, e
	}
	return s.checkedSession(ctx, digest(c.Value))
}
func (s *Service) checkedSession(ctx context.Context, id string) (Session, error) {
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	var v Session
	if e := s.Store.Get(ctx, "session", id, &v); e != nil {
		return v, e
	}
	if !time.Now().Before(v.Expires) {
		_ = s.Store.Delete(ctx, "session", id)
		return v, fmt.Errorf("session expired")
	}
	if time.Since(v.Checked) >= 4*time.Minute {
		// Refresh token rotation and its durable write must finish even when a
		// browser abandons a request. Session locking also serializes logout.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
		defer cancel()
		ctx = refreshCtx
		u, t, e := s.Identity.Refresh(ctx, v.Token)
		if e != nil || u.ID != v.User.ID {
			diagnostic := identityError{Stage: "authorization", Category: "rejected"}
			var typed *identityError
			if errors.As(e, &typed) {
				diagnostic = *typed
			}
			s.recordIdentityFailure(ctx, diagnostic)
			_ = s.Store.Delete(ctx, "session", id)
			return v, fmt.Errorf("identity could not be verified")
		}
		if e = s.Store.Put(ctx, "user", u.ID, u); e != nil {
			return v, e
		}
		v.User = u
		v.Token = t
		v.Checked = time.Now().UTC()
		if e = s.Store.Put(ctx, "session", id, v); e != nil {
			return v, e
		}
	}
	return v, nil
}

type loginAttempt struct {
	Verifier, Nonce string
	Expires         time.Time
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	s.loginMu.Lock()
	_, err := s.Store.DB.ExecContext(r.Context(), `DELETE FROM objects WHERE kind='login' AND json_extract(body,'$.Expires') < ?`, time.Now().UTC().Format(time.RFC3339Nano))
	var count int
	if err == nil {
		err = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM objects WHERE kind='login'`).Scan(&count)
	}
	s.loginMu.Unlock()
	if err != nil || count >= 1000 {
		writeError(w, 503, "Sign-in is busy. Please retry shortly.")
		return
	}
	state := randomID(24)
	a := loginAttempt{randomID(32), randomID(24), time.Now().Add(5 * time.Minute)}
	if e := s.Store.Put(r.Context(), "login", digest(state), a); e != nil {
		writeError(w, 503, "Sign-in is temporarily unavailable.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-labcentral_login", Value: state, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, s.Identity.LoginURL(state, a.Nonce, a.Verifier), 302)
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	c, e := r.Cookie("__Host-labcentral_login")
	state := r.URL.Query().Get("state")
	if e != nil || state == "" || c.Value != state {
		writeError(w, 400, "Sign-in expired. Start again.")
		return
	}
	var a loginAttempt
	s.loginMu.Lock()
	e = s.Store.Get(r.Context(), "login", digest(state), &a)
	if e == nil {
		e = s.Store.Delete(r.Context(), "login", digest(state))
	}
	s.loginMu.Unlock()
	if e != nil || !a.Expires.After(time.Now()) {
		writeError(w, 400, "Sign-in expired. Start again.")
		return
	}
	u, t, e := s.Identity.Exchange(r.Context(), r.URL.Query().Get("code"), a.Verifier, a.Nonce)
	if e != nil {
		writeError(w, 403, "Sign-in could not be completed. Check your access and try again.")
		return
	}
	if e = s.Store.Put(r.Context(), "user", u.ID, u); e != nil {
		writeError(w, 503, "Could not save your profile.")
		return
	}
	token := randomID(32)
	v := Session{ID: digest(token), User: u, CSRF: randomID(24), Token: t, Expires: time.Now().Add(8 * time.Hour), Checked: time.Now().UTC()}
	if e = s.Store.Put(r.Context(), "session", v.ID, v); e != nil {
		writeError(w, 503, "Could not save sign-in. Please try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-labcentral", Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 8 * 3600})
	http.SetCookie(w, &http.Cookie{Name: "__Host-labcentral_login", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/teams/", 303)
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request, v Session) {
	release, err := s.lockSession(r.Context(), v.ID)
	if err != nil {
		writeError(w, 503, "Sign-out could not be saved. Please retry.")
		return
	}
	err = s.Store.Delete(r.Context(), "session", v.ID)
	release()
	if err != nil {
		writeError(w, 503, "Sign-out could not be saved. Please retry.")
		return
	}
	err = s.revokeUser(r.Context(), v.User.ID)
	http.SetCookie(w, &http.Cookie{Name: "__Host-labcentral", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	if err != nil {
		writeError(w, 503, "Signed out; guest cleanup is pending recovery.")
		return
	}
	writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeLimit(w, r, dst, 16<<10)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		writeError(w, 400, "Invalid request.")
		return false
	}
	if e := d.Decode(new(any)); e != io.EOF {
		writeError(w, 400, "Expected one request document.")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{msg})
}
func cleanName(v string) bool {
	return len(strings.TrimSpace(v)) > 0 && len(v) <= 100 && !strings.ContainsAny(v, "\r\n\x00")
}
