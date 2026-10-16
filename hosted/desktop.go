package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"html"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type accessRecord struct {
	ID, UserID, SessionID, TeamID, InstanceID string
	Expires                                   time.Time
	Machine, OS, UserName                     string
	Coach                                     bool
}
type desktopSession struct {
	record  accessRecord
	info    lc.AccessInfo
	proxy   *httputil.ReverseProxy
	cookies []*http.Cookie
	cookie  string
	ctx     context.Context
	cancel  context.CancelFunc
}
type launchTicket struct {
	AccessID, SessionID string
	Expires             time.Time
}

func (s *Service) launch(w http.ResponseWriter, r *http.Request, v Session) {
	s.meetingDesktop(w, r, v, false)
}

// Persist a generation before opening a native session. Membership and expiry
// are revalidated after I/O; a concurrent revocation invalidates this generation.
func (s *Service) launchDesktop(w http.ResponseWriter, r *http.Request, v Session, team Team, expiry time.Time, machine string, takeover ...bool) {
	if v.Expires.Before(expiry) {
		expiry = v.Expires
	}
	id := digest(team.InstanceID + "\x00" + machine + "\x00" + v.User.ID)[:24]
	os, err := s.desktopOS(r.Context(), team, machine)
	if err != nil {
		writeError(w, 503, "Pinned machine unavailable.")
		return
	}
	record := accessRecord{ID: id, UserID: v.User.ID, SessionID: v.ID, TeamID: team.ID, InstanceID: team.InstanceID, Expires: expiry, Machine: machine, OS: os, UserName: v.User.Name, Coach: r.PathValue("id") != "" && v.User.Admin}
	if key := record.consoleKey(); key != "" {
		unlock, err := s.lockConsole(r.Context(), key)
		if err != nil {
			w.Header().Set("Retry-After", "3")
			writeError(w, 503, "Desktop operation pending.")
			return
		}
		defer unlock()
		conflict, err := s.claimConsole(r.Context(), record, len(takeover) > 0 && takeover[0])
		if err != nil {
			_, _ = s.Store.Enqueue(r.Context(), "access-cleanup", "pending")
			w.Header().Set("Retry-After", "3")
			writeJSON(w, 503, DesktopConflict{Error: "Desktop revocation is unconfirmed. Retry shortly; saved files are retained.", Code: "desktop_outcome_unknown"})
			return
		}
		if conflict != nil {
			writeJSON(w, 409, conflict)
			return
		}
	}
	generation, err := s.reserveDesktop(r.Context(), record)
	if err != nil {
		_, _ = s.Store.Enqueue(r.Context(), "access-cleanup", "pending")
		if errors.Is(err, errDesktopBusy) {
			// Another open or close of this desktop is in flight (a Windows cold desktop can
			// take several minutes); clients may retry the same request.
			w.Header().Set("Retry-After", "3")
		}
		writeError(w, 503, "Your desktop is opening or closing. Retry shortly.")
		return
	}
	// Release the per-user intent lock before provider and relay HTTP work.
	openCtx, cancel := context.WithTimeout(r.Context(), s.desktopOpenTimeout(os))
	defer cancel()
	budgetCtx, release, err := s.sshContext(openCtx)
	var info lc.AccessInfo
	if err == nil {
		info, err = s.Engine.OpenDesktopWithOptions(budgetCtx, team.InstanceID, machine, lc.AccessOptions{Principal: v.User.ID, Expires: expiry.Format(time.RFC3339), RetainWorkspace: true})
		release()
	}

	var next *desktopSession
	if err == nil && info.ID != id {
		err = fmt.Errorf("desktop identity mismatch")
	}
	if err == nil {
		s.desktopMu.Lock()
		prior := s.desktops[id]
		s.desktopMu.Unlock()
		if prior != nil && prior.ctx.Err() == nil && prior.info.URL == info.URL {
			ctx, stop := context.WithCancel(context.Background())
			next = &desktopSession{record: record, info: info, proxy: prior.proxy, cookies: prior.cookies, cookie: prior.cookie, ctx: ctx, cancel: stop}
		} else {
			next, err = newDesktop(record, info)
		}
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer stop()
	ticket, save := s.finishDesktop(saveCtx, record, generation, next, err)
	if save != nil || err != nil {
		if next != nil {
			next.cancel()
		}
		_, _ = s.Store.Enqueue(saveCtx, "access-cleanup", "pending")
		w.Header().Set("Retry-After", "3")
		writeError(w, 503, "Your desktop could not start. Retry shortly; your files are retained.")
		return
	}
	writeJSON(w, 200, Launch{s.Config.DesktopOrigin + "/launch", ticket})
}
func newDesktop(record accessRecord, info lc.AccessInfo) (*desktopSession, error) {
	target, e := url.Parse(info.URL)
	if e != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.User != nil {
		return nil, fmt.Errorf("invalid provider relay")
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Get(info.URL)
	if e != nil {
		return nil, e
	}
	res.Body.Close()
	if res.StatusCode != 303 {
		return nil, fmt.Errorf("relay ticket was not accepted")
	}
	cookies := res.Cookies()
	if len(cookies) == 0 {
		return nil, fmt.Errorf("relay did not authenticate")
	}
	target.Path = ""
	target.RawQuery = ""
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktopSession{record: record, info: info, cookies: cookies, cookie: randomID(32), ctx: ctx, cancel: cancel}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = target.Host
		r.Header.Set("Origin", target.String())
		r.Header.Del("Cookie")
		for _, c := range cookies {
			r.AddCookie(c)
		}
	}
	proxy.ModifyResponse = func(r *http.Response) error {
		r.Header.Del("Set-Cookie")
		r.Header.Del("Content-Security-Policy")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		http.Error(w, "Desktop disconnected. Return to your lab to reconnect.", 502)
	}
	d.proxy = proxy
	return d, nil
}
func (s *Service) desktopHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self' data:; img-src 'self' data: blob:; font-src 'self' data:; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'")
	if r.URL.Path == "/launch" {
		s.acceptLaunch(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.Config.DesktopOrigin {
		http.Error(w, "invalid origin", 403)
		return
	}
	cookie, e := r.Cookie("__Host-labcentral_desktop")
	if e != nil {
		http.Error(w, "Open your desktop from LabCentral.", 401)
		return
	}
	s.desktopMu.Lock()
	var d *desktopSession
	for _, candidate := range s.desktops {
		if candidate.cookie == cookie.Value {
			d = candidate
			break
		}
	}
	s.desktopMu.Unlock()
	if (r.URL.Path == "/status" || d == nil || d.ctx.Err() != nil) && s.desktopCookieStatus(w, r, cookie.Value) {
		return
	}
	if d == nil {
		http.Error(w, "Desktop session ended. Return to your lab to reconnect.", 401)
		return
	}
	if !s.desktopAuthorized(r.Context(), d) {
		d.cancel()
		http.Error(w, "Desktop access has ended.", 403)
		return
	}
	if r.URL.Path == "/lesson" || r.URL.Path == "/lesson/answer" || r.URL.Path == "/lesson/check" {
		s.desktopLesson(w, r, d)
		return
	}
	if r.URL.Path == "/lesson-client.js" {
		http.ServeFile(w, r, filepath.Join(s.Config.WebRoot, "lesson.js"))
		return
	}
	if r.URL.Path == "/lesson-ui.css" {
		s.desktopLessonStyles(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_app/") {
		http.FileServer(http.Dir(s.Config.WebRoot)).ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		practiceID := ""
		if strings.HasPrefix(d.record.TeamID, "practice:") {
			practiceID = strings.TrimPrefix(d.record.TeamID, "practice:")
		}
		page := strings.NewReplacer("{{origin}}", html.EscapeString(s.Config.Origin), "{{practiceRun}}", html.EscapeString(practiceID)).Replace(desktopHTML)
		_, _ = w.Write([]byte(page))
		return
	}
	if r.URL.Path == "/desktop.css" {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(desktopCSS))
		return
	}
	if r.URL.Path == "/client.js" {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(desktopJS))
		return
	}
	if r.URL.Path == "/credentials" {
		writeJSON(w, 200, struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			SessionID string `json:"sessionID"`
		}{d.info.Username, d.info.Password, desktopSessionID(d)})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dcvjs/") {
		http.StripPrefix("/dcvjs/", http.FileServer(http.Dir(s.Config.SDKRoot))).ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(d.ctx, cancel)
	defer stop()
	d.proxy.ServeHTTP(w, r.WithContext(ctx))
}
func (s *Service) acceptLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Origin") != s.Config.Origin {
		http.Error(w, "invalid launch", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if e := r.ParseForm(); e != nil {
		http.Error(w, "invalid launch", 400)
		return
	}
	s.desktopMu.Lock()
	key := digest(r.PostForm.Get("ticket"))
	t, ok := s.tickets[key]
	delete(s.tickets, key)
	d := s.desktops[t.AccessID]
	s.desktopMu.Unlock()
	if !ok || d == nil || !t.Expires.After(time.Now()) || d.record.SessionID != t.SessionID || !s.desktopAuthorized(r.Context(), d) {
		http.Error(w, "Launch link expired. Open the desktop again from your lab.", 403)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-labcentral_desktop", Value: d.cookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: d.record.Expires})
	http.Redirect(w, r, "/", 303)
}
func (s *Service) desktopAuthorized(ctx context.Context, d *desktopSession) bool {
	if d.ctx.Err() != nil || !d.record.Expires.After(time.Now()) {
		return false
	}
	v, e := s.checkedSession(ctx, d.record.SessionID)
	if e != nil || v.User.ID != d.record.UserID {
		return false
	}
	ctx = context.WithValue(ctx, verifiedUserKey{}, v.User)
	if strings.HasPrefix(d.record.TeamID, "practice:") {
		run, err := s.Store.practice(ctx, strings.TrimPrefix(d.record.TeamID, "practice:"))
		publicationErr := s.practiceDisclosure(ctx, run.LabID)
		return err == nil && publicationErr == nil && run.UserID == v.User.ID && run.InstanceID == d.record.InstanceID && run.Status == "ready" && run.Expires.After(time.Now())
	}
	if d.record.Coach {
		if !v.User.Admin {
			return false
		}
		team, _, _, err := s.coachTeam(ctx, d.record.TeamID)
		return err == nil && team.InstanceID == d.record.InstanceID
	}
	team, _, e := s.readyTeam(ctx, d.record.UserID)
	return e == nil && team.ID == d.record.TeamID && team.InstanceID == d.record.InstanceID
}

// Each resource retains its own closing intent until provider confirmation.
func (s *Service) revokeUser(ctx context.Context, user string) error {
	return s.revokeMatchingDesktop(ctx, user, "")
}
func (s *Service) revokeMatchingDesktop(ctx context.Context, user, instance string) error {
	records, err := s.Store.List(ctx, "desktop")
	if err != nil {
		return err
	}
	failures := make([]error, 0)
	for _, raw := range records {
		var record accessRecord
		if err = json.Unmarshal(raw, &record); err != nil {
			failures = append(failures, err)
			continue
		}
		if record.UserID == user && (instance == "" || record.InstanceID == instance) {
			if err = s.cleanupDesktop(ctx, record.ID, false); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
func (s *Service) closeDesktop(w http.ResponseWriter, r *http.Request, v Session) {
	e := s.revokeUser(r.Context(), v.User.ID)
	if e != nil {
		writeError(w, 503, "Desktop disconnected; guest cleanup is pending.")
		return
	}
	writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true})
}
func (s *Service) revalidateDesktops(ctx context.Context) {
	s.desktopMu.Lock()
	list := make([]*desktopSession, 0, len(s.desktops))
	for _, d := range s.desktops {
		list = append(list, d)
	}
	s.desktopMu.Unlock()
	// Authorize concurrently, independently of infrastructure and guest cleanup.
	// Cancellation closes transports immediately; the durable worker retries cleanup.
	var checks sync.WaitGroup
	for _, d := range list {
		checks.Go(func() {
			check, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if !s.desktopAuthorized(check, d) {
				d.cancel()
			}
		})
	}
	checks.Wait()
	// Database admission may be busy. Relay and launch-ticket access must
	// never retain their mutex while waiting for durable reads or enqueueing.
	_, _ = s.Store.DB.ExecContext(ctx, `DELETE FROM objects WHERE kind IN ('desktop-cookie','desktop-displaced') AND julianday(json_extract(body,'$.Expires'))<=julianday(?)`, time.Now().UTC().Format(time.RFC3339Nano))
	records, _ := s.Store.List(ctx, "desktop")
	cleanup := false
	s.desktopMu.Lock()
	for _, b := range records {
		var a accessRecord
		if json.Unmarshal(b, &a) == nil && (s.desktops[a.ID] == nil || s.desktops[a.ID].ctx.Err() != nil) {
			cleanup = true
			break
		}
	}
	for k, t := range s.tickets {
		if !t.Expires.After(time.Now()) {
			delete(s.tickets, k)
		}
	}
	s.desktopMu.Unlock()
	if cleanup {
		// Recovery rechecks active relays while holding each user's lock, so a
		// concurrent reconnect can safely make this cleanup signal obsolete.
		_, _ = s.Store.Enqueue(ctx, "access-cleanup", "pending")
	}
}

const desktopHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/desktop.css"><title>Your lab desktop · LabCentral</title></head><body data-practice-run="{{practiceRun}}"><header><a href="{{origin}}/lab/">← Your lab</a><p id="status" role="status">Connecting…</p><button id="clipboard" type="button" disabled title="Copy from the desktop. Paste with Ctrl+V or ⌘V.">Copy from desktop</button><button id="paste" type="button" disabled>Paste to desktop</button><button id="fullscreen" type="button">Full screen</button></header><main id="desktop-viewport" aria-label="Remote desktop"><div id="dcv-display"></div></main><dialog id="paste-dialog" aria-labelledby="paste-title"><h2 id="paste-title">Paste to desktop</h2><p><label for="paste-text">Paste text here with Ctrl+V or ⌘V.</label></p><textarea id="paste-text" rows="4" cols="28" spellcheck="false" autocomplete="off" aria-describedby="paste-help"></textarea><p id="paste-help">Then paste inside the desktop.<br>Terminals use Ctrl+Shift+V.</p><form method="dialog"><button>Cancel</button></form></dialog><script type="module" src="/client.js"></script></body></html>`
const desktopCSS = `:root{font-family:system-ui,sans-serif;color-scheme:dark;color:#eceaf0;background:#15141b}*{box-sizing:border-box}body{margin:0;height:100dvh;display:flex;flex-direction:column}header{display:flex;align-items:center;gap:1rem;padding:.65rem 1rem;background:#1c1a24;flex-wrap:wrap;font-size:.85rem}header p{flex:1;margin:0}a{color:#c5a3ef;font-weight:600}button{border:1px solid #454051;border-radius:.5rem;padding:.4rem .7rem;background:#25222f;color:inherit;cursor:pointer}a:focus-visible,button:focus-visible{outline:3px solid #c5a3ef;outline-offset:3px}main{flex:1;min-height:0;overflow:hidden;position:relative}#dcv-display{width:100%;height:100%;transform-origin:top left}main:fullscreen{width:100vw;height:100vh} @media(max-width:480px){header{gap:.5rem;font-size:.75rem}header p{flex-basis:45%}button:disabled{opacity:.5;cursor:default}}`
const desktopJS = `import dcv from '/dcvjs/dcv.js';
const status=document.getElementById('status');
let credentials,connection;
const copy=document.getElementById('clipboard');
const paste=document.getElementById('paste');
const pasteDialog=document.getElementById('paste-dialog');
const pasteText=document.getElementById('paste-text');
paste.onclick=()=>{if(connection){pasteText.value='';pasteDialog.showModal();connection.captureClipboardEvents(true);pasteText.focus()}};
pasteDialog.addEventListener('close',()=>{pasteText.value=''});
pasteDialog.addEventListener('keydown',event=>event.stopPropagation());
pasteDialog.addEventListener('keyup',event=>event.stopPropagation());
copy.onclick=()=>{if(connection&&!connection.syncClipboards()){copy.title='Copy text inside the desktop, then try again.'}};
const display=document.getElementById('dcv-display');
const viewport=document.getElementById('desktop-viewport');
document.getElementById('fullscreen').onclick=()=>viewport.requestFullscreen?.();
const fit=()=>{const ratio=Math.min(1,viewport.clientWidth/display.offsetWidth,viewport.clientHeight/display.offsetHeight);if(Number.isFinite(ratio)&&ratio>0)display.style.transform='scale('+ratio+')'};
const resize=()=>{fit();if(connection){connection.requestResolution(Math.max(320,viewport.clientWidth),Math.max(240,viewport.clientHeight))?.catch(()=>{})}};
new ResizeObserver(fit).observe(display);
new ResizeObserver(resize).observe(viewport);
if(document.body.dataset.practiceRun)import('/lesson-client.js').catch(()=>{});
let polling=false;const ended=async message=>{status.textContent=message;if(polling)return;polling=true;for(let i=0;i<24;i++){try{const r=await fetch('/status',{cache:'no-store'});if(!r.ok)break;const state=await r.json();if(state.takenOverBy){status.textContent=state.takenOverBy+' took over this desktop. Return to your lab to take it back.';break}if(state.code==='available')break}catch{break}await new Promise(done=>setTimeout(done,5000))}polling=false};
dcv.setLogLevel(dcv.LogLevel.ERROR);
try {
 const response=await fetch('/credentials',{cache:'no-store'});
 if(!response.ok)throw new Error('Access ended');credentials=await response.json();
 dcv.authenticate(location.origin,{promptCredentials:(auth)=>auth.sendCredentials({username:credentials.username,password:credentials.password}),error:()=>{credentials=null;ended('Connection ended. Return to your lab to reconnect.')},success:async(_,sessions)=>{
 const session=sessions.find(s=>s.sessionId===credentials.sessionID);credentials=null;
 if(!session){status.textContent='Your desktop is unavailable. Return to your lab to reconnect.';return}
 try{connection=await dcv.connect({url:location.origin,sessionId:session.sessionId,authToken:session.authToken,divId:'dcv-display',clipboardAutoSync:true,callbacks:{clipboardEvent:(client,event)=>{if(event.name==='established'){client.captureClipboardEvents(true);copy.disabled=false;paste.disabled=false}else if(event.name==='paste'&&pasteDialog.open){pasteDialog.close();status.textContent='Connected · clipboard ready; paste inside the desktop.'}},firstFrame:()=>{resize();status.textContent='Connected'},disconnect:()=>{copy.disabled=true;paste.disabled=true;pasteDialog.close();pasteText.value='';connection=null;ended('Disconnected. Return to your lab to reconnect.')}}});resize()}catch{status.textContent='Connection failed. Return to your lab to reconnect.'}
 }});
}catch{ended('Access ended. Return to your lab to reconnect.')}`

// Desktop policy is pinned to the template. Ownership/membership is checked by
// the caller and the library verifies the instance's machine and desktop access.
func (s *Service) desktopMachine(ctx context.Context, template string) (string, error) {
	b, err := s.Store.instructorPolicy(ctx, template)
	if err != nil {
		return "", err
	}
	if b.Practice == nil || b.Practice.DesktopMachine == "" || lc.Contains(b.Visibility.HiddenMachines, b.Practice.DesktopMachine) {
		return "", fmt.Errorf("Pinned desktop machine unavailable.")
	}
	return b.Practice.DesktopMachine, nil
}
