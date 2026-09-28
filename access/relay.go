// Package access implements local, short-lived browser relays and verified SSH.
package access

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed web
var assets embed.FS

type Relay struct {
	URL                 string
	server              *http.Server
	listener            net.Listener
	token, cookie, host string
	mu                  sync.Mutex
	connections         map[net.Conn]bool
	used                bool
	cancel              context.CancelFunc
	done                chan struct{}
}

func NewRelay(ctx context.Context, handler http.Handler) (*Relay, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	r := &Relay{listener: l, host: l.Addr().String(), token: random(), cookie: random(), cancel: cancel, done: make(chan struct{}), connections: map[net.Conn]bool{}}
	r.URL = "http://" + r.host + "/launch?token=" + r.token
	r.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != r.host {
			http.Error(w, "invalid host", 403)
			return
		}
		origin := req.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.host {
			http.Error(w, "invalid origin", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self' data: ws://"+r.host+"; img-src 'self' data: blob:; font-src 'self' data:; worker-src 'self' blob:; frame-ancestors 'none'")
		if req.URL.Path == "/launch" {
			if req.Method != "GET" {
				http.Error(w, "method", 405)
				return
			}
			r.mu.Lock()
			valid := !r.used && subtle.ConstantTimeCompare([]byte(req.URL.Query().Get("token")), []byte(r.token)) == 1
			if valid {
				r.used = true
			}
			r.mu.Unlock()
			if !valid {
				http.Error(w, "launch token expired or consumed", 403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "labcentral_access", Value: r.cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 1800})
			http.Redirect(w, req, "/", http.StatusSeeOther)
			return
		}
		cookie, e := req.Cookie("labcentral_access")
		if e != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.cookie)) != 1 {
			http.Error(w, "access denied", 403)
			return
		}
		handler.ServeHTTP(w, req)
	})}
	r.server.ConnState = func(c net.Conn, state http.ConnState) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if state == http.StateClosed {
			delete(r.connections, c)
		} else {
			r.connections[c] = true
		}
	}
	go func() { _ = r.server.Serve(l); close(r.done) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-r.done:
			cancel()
		}
	}()
	return r, nil
}
func (r *Relay) Close() error {
	r.cancel()
	err := r.server.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.connections {
		_ = c.Close()
		delete(r.connections, c)
	}
	return err
}
func random() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func staticHandler() http.Handler {
	f, e := fs.Sub(assets, "web")
	if e != nil {
		panic(e)
	}
	return http.FileServer(http.FS(f))
}
func requestPath(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
