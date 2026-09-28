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
	"sync"
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
	if d != nil && (d.ctx.Err() != nil || !d.info.Expires.After(time.Now()) || d.record.SessionID != v.ID) {
		if e = s.revokeUser(r.Context(), v.User.ID); e != nil {
			writeError(w, 503, "Your previous desktop is closing. Please retry.")
			return
		}
		d = nil
	}
	expiry := meeting.Expires
	if v.Expires.Before(expiry) {
		expiry = v.Expires
	}
	record := accessRecord{id, v.User.ID, v.ID, team.ID, team.InstanceID, expiry}
	if e = s.Store.Put(r.Context(), "desktop", id, record); e != nil {
		writeError(w, 503, "Could not reserve your desktop.")
		return
	}
	// Recheck the native session even when a local relay is cached. A guest
	// session can exit independently while its retained workspace remains.
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
	if d == nil || d.info.URL != info.URL {
		next, err := newDesktop(record, info)
		if err != nil {
			_ = s.Engine.CloseAccess(context.Background(), id)
			writeError(w, 503, "Desktop connection could not be established. Retry shortly.")
			return
		}
		s.mu.Lock()
		s.desktops[id] = next
		s.mu.Unlock()
		if d != nil {
			d.cancel()
		}
		d = next
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
	s.mu.Lock()
	records, _ := s.Store.List(ctx, "desktop")
	for _, b := range records {
		var a accessRecord
		if json.Unmarshal(b, &a) == nil && (s.desktops[a.ID] == nil || s.desktops[a.ID].ctx.Err() != nil) {
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

const desktopHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/desktop.css"><title>Your lab desktop · LabCentral</title></head><body><header><a href="{{origin}}/lab/">← Your lab</a><p id="status" role="status">Connecting…</p><button id="clipboard" type="button" disabled title="Copy from the desktop. Paste with Ctrl+V or ⌘V.">Copy from desktop</button><button id="fullscreen" type="button">Full screen</button></header><main id="desktop-viewport" aria-label="Remote desktop"><div id="dcv-display"></div></main><script type="module" src="/client.js"></script></body></html>`
const desktopCSS = `:root{font-family:system-ui,sans-serif;color-scheme:dark;color:#eceaf0;background:#15141b}*{box-sizing:border-box}body{margin:0;height:100dvh;display:flex;flex-direction:column}header{display:flex;align-items:center;gap:1rem;padding:.65rem 1rem;background:#1c1a24;flex-wrap:wrap;font-size:.85rem}header p{flex:1;margin:0}a{color:#c5a3ef;font-weight:600}button{border:1px solid #454051;border-radius:.5rem;padding:.4rem .7rem;background:#25222f;color:inherit;cursor:pointer}a:focus-visible,button:focus-visible{outline:3px solid #c5a3ef;outline-offset:3px}main{flex:1;min-height:0;overflow:hidden;position:relative}#dcv-display{width:100%;height:100%;transform-origin:top left}main:fullscreen{width:100vw;height:100vh} @media(max-width:480px){header{gap:.5rem;font-size:.75rem}header p{flex-basis:45%}button:disabled{opacity:.5;cursor:default}}`
const desktopJS = `import dcv from '/dcvjs/dcv.js';
const status=document.getElementById('status');
let credentials,connection;
const copy=document.getElementById('clipboard');
copy.onclick=()=>{if(connection&&!connection.syncClipboards()){copy.title='Copy text inside the desktop, then try again.'}};
const display=document.getElementById('dcv-display');
const viewport=document.getElementById('desktop-viewport');
document.getElementById('fullscreen').onclick=()=>viewport.requestFullscreen?.();
const fit=()=>{const ratio=Math.min(1,viewport.clientWidth/display.offsetWidth,viewport.clientHeight/display.offsetHeight);if(Number.isFinite(ratio)&&ratio>0)display.style.transform='scale('+ratio+')'};
const resize=()=>{fit();if(connection){connection.requestResolution(Math.max(320,viewport.clientWidth),Math.max(240,viewport.clientHeight))?.catch(()=>{})}};
new ResizeObserver(fit).observe(display);
new ResizeObserver(resize).observe(viewport);
dcv.setLogLevel(dcv.LogLevel.ERROR);
try {
 const response=await fetch('/credentials',{cache:'no-store'});
 if(!response.ok)throw new Error('Access ended');credentials=await response.json();
 dcv.authenticate(location.origin,{promptCredentials:(auth)=>auth.sendCredentials({username:credentials.username,password:credentials.password}),error:()=>{credentials=null;status.textContent='Connection ended. Return to your lab to reconnect.'},success:async(_,sessions)=>{
 const session=sessions.find(s=>s.sessionId===credentials.sessionID);credentials=null;
 if(!session){status.textContent='Your desktop is unavailable. Return to your lab to reconnect.';return}
 try{connection=await dcv.connect({url:location.origin,sessionId:session.sessionId,authToken:session.authToken,divId:'dcv-display',clipboardAutoSync:true,callbacks:{firstFrame:()=>{resize();status.textContent='Connected'},disconnect:()=>{copy.disabled=true;connection=null;status.textContent='Disconnected. Return to your lab to reconnect.'}}});connection.captureClipboardEvents(true);copy.disabled=false;resize()}catch{status.textContent='Connection failed. Return to your lab to reconnect.'}
 }});
}catch{status.textContent='Access ended. Return to your lab to reconnect.'}`
