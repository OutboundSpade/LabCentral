package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"html"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type accessRecord struct {
	ID, UserID, SessionID, TeamID, InstanceID string
	Expires                                   time.Time
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
	unlock := s.lockUser(v.User.ID)
	defer unlock()
	team, meeting, e := s.readyTeam(r.Context(), v.User.ID)
	if e != nil {
		writeError(w, 409, e.Error())
		return
	}
	id := digest(team.InstanceID + "\x00workstation\x00" + v.User.ID)[:24]
	s.mu.Lock()
	d := s.desktops[id]
	s.mu.Unlock()
	if d != nil && (!d.info.Expires.After(time.Now()) || d.record.SessionID != v.ID) {
		if e = s.revokeUser(r.Context(), v.User.ID); e != nil {
			writeError(w, 503, "Your previous desktop is closing. Please retry.")
			return
		}
		d = nil
	}
	if d == nil {
		expiry := meeting.Expires
		if v.Expires.Before(expiry) {
			expiry = v.Expires
		}
		record := accessRecord{id, v.User.ID, v.ID, team.ID, team.InstanceID, expiry}
		if e = s.Store.Put(r.Context(), "desktop", id, record); e != nil {
			writeError(w, 503, "Could not reserve your desktop.")
			return
		}
		openCtx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		info, e := s.Engine.OpenDesktopWithOptions(openCtx, team.InstanceID, "workstation", lc.AccessOptions{Principal: v.User.ID, Expires: expiry.Format(time.RFC3339), RetainWorkspace: true})
		if e != nil {
			writeError(w, 503, "Your desktop could not start. Retry shortly; your files are retained.")
			return
		}
		if info.ID != id {
			writeError(w, 503, "Desktop identity mismatch.")
			return
		}
		d, e = newDesktop(record, info)
		if e != nil {
			_ = s.Engine.CloseAccess(context.Background(), id)
			writeError(w, 503, "Desktop connection could not be established. Retry shortly.")
			return
		}
		s.mu.Lock()
		s.desktops[id] = d
		s.mu.Unlock()
	}
	ticket := randomID(32)
	s.mu.Lock()
	s.tickets[digest(ticket)] = launchTicket{id, v.ID, time.Now().Add(time.Minute)}
	s.mu.Unlock()
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
	s.mu.Lock()
	var d *desktopSession
	for _, candidate := range s.desktops {
		if candidate.cookie == cookie.Value {
			d = candidate
			break
		}
	}
	s.mu.Unlock()
	if d == nil {
		http.Error(w, "Desktop session ended. Return to your lab to reconnect.", 401)
		return
	}
	if !s.desktopAuthorized(r.Context(), d) {
		d.cancel()
		http.Error(w, "Desktop access has ended.", 403)
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(desktopHTML, "{{origin}}", html.EscapeString(s.Config.Origin))))
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
		}{d.info.Username, d.info.Password, d.info.Username})
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
	s.mu.Lock()
	key := digest(r.PostForm.Get("ticket"))
	t, ok := s.tickets[key]
	delete(s.tickets, key)
	d := s.desktops[t.AccessID]
	s.mu.Unlock()
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
	team, _, e := s.readyTeam(ctx, d.record.UserID)
	return e == nil && team.ID == d.record.TeamID && team.InstanceID == d.record.InstanceID
}

// Caller holds the user lock. Durable records are removed only after provider confirmation.
func (s *Service) revokeUser(ctx context.Context, user string) error {
	records, e := s.Store.List(ctx, "desktop")
	if e != nil {
		return e
	}
	for _, b := range records {
		var record accessRecord
		if e = json.Unmarshal(b, &record); e != nil {
			return e
		}
		if record.UserID != user {
			continue
		}
		s.mu.Lock()
		if d := s.desktops[record.ID]; d != nil {
			d.cancel()
			delete(s.desktops, record.ID)
		}
		s.mu.Unlock()
		if e = s.Engine.CloseAccess(ctx, record.ID); e != nil {
			return e
		}
		if e = s.Store.Delete(ctx, "desktop", record.ID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) closeDesktop(w http.ResponseWriter, r *http.Request, v Session) {
	unlock := s.lockUser(v.User.ID)
	e := s.revokeUser(r.Context(), v.User.ID)
	unlock()
	if e != nil {
		writeError(w, 503, "Desktop disconnected; guest cleanup is pending.")
		return
	}
	writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true})
}
func (s *Service) revalidateDesktops(ctx context.Context) {
	s.mu.Lock()
	list := make([]*desktopSession, 0, len(s.desktops))
	for _, d := range s.desktops {
		list = append(list, d)
	}
	s.mu.Unlock()
	for _, d := range list {
		if !s.desktopAuthorized(ctx, d) {
			unlock := s.lockUser(d.record.UserID)
			if !s.desktopAuthorized(ctx, d) {
				_ = s.revokeUser(ctx, d.record.UserID)
			}
			unlock()
		}
	}
	s.mu.Lock()
	records, _ := s.Store.List(ctx, "desktop")
	for _, b := range records {
		var a accessRecord
		if json.Unmarshal(b, &a) == nil && s.desktops[a.ID] == nil {
			_, _ = s.Store.Enqueue(ctx, "access-cleanup", "pending")
			break
		}
	}
	for k, t := range s.tickets {
		if !t.Expires.After(time.Now()) {
			delete(s.tickets, k)
		}
	}
	s.mu.Unlock()
}

const desktopHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/desktop.css"><title>Your lab desktop · LabCentral</title></head><body><header><a href="{{origin}}/lab/">← Your lab</a><p id="status" role="status">Connecting to your workspace…</p><button id="fullscreen" type="button">Full screen</button></header><main id="dcv-display" aria-label="Remote desktop"></main><script type="module" src="/client.js"></script></body></html>`
const desktopCSS = `:root{font-family:system-ui,sans-serif;color:#262232;background:#111018}*{box-sizing:border-box}body{margin:0;height:100dvh;display:flex;flex-direction:column}header{display:flex;align-items:center;gap:1rem;padding:.65rem 1rem;background:#fff;font-size:.85rem}header p{flex:1;margin:0}a{color:#6032a0;font-weight:600}button{border:1px solid #dad6e2;border-radius:.5rem;padding:.4rem .7rem;background:white;color:inherit;cursor:pointer}a:focus-visible,button:focus-visible{outline:3px solid #7344ae;outline-offset:3px}main{flex:1;min-height:0;overflow:hidden}main:fullscreen{width:100vw;height:100vh} @media(max-width:480px){header{gap:.5rem;font-size:.75rem}}`
const desktopJS = `import dcv from '/dcvjs/dcv.js';
const status=document.getElementById('status');
let credentials,connection;
const display=document.getElementById('dcv-display');
document.getElementById('fullscreen').onclick=()=>display.requestFullscreen?.();
const resize=()=>{if(connection){connection.requestResolution(Math.max(640,display.clientWidth),Math.max(480,display.clientHeight))?.catch(()=>{})}};
new ResizeObserver(resize).observe(display);
dcv.setLogLevel(dcv.LogLevel.ERROR);
try {
 const response=await fetch('/credentials',{cache:'no-store'});
 if(!response.ok)throw new Error('Access ended');credentials=await response.json();
 dcv.authenticate(location.origin,{promptCredentials:(auth)=>auth.sendCredentials({username:credentials.username,password:credentials.password}),error:()=>{credentials=null;status.textContent='Connection ended. Return to your lab to reconnect.'},success:async(_,sessions)=>{
 const session=sessions.find(s=>s.sessionId===credentials.sessionID);credentials=null;
 if(!session){status.textContent='Your desktop is unavailable. Return to your lab to reconnect.';return}
 try{connection=await dcv.connect({url:location.origin,sessionId:session.sessionId,authToken:session.authToken,divId:'dcv-display',callbacks:{disconnect:()=>{status.textContent='Disconnected. Return to your lab to reconnect.'}}});resize();status.textContent='Connected · Files are retained until the meeting ends.'}catch{status.textContent='Connection failed. Return to your lab to reconnect.'}
 }});
}catch{status.textContent='Access ended. Return to your lab to reconnect.'}`
