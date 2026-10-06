package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type browserLoginIdentity struct {
	Identity
	authorizationURL string
}

func (i browserLoginIdentity) LoginURL(string, string, string) string { return i.authorizationURL }

// Exercises the shipped UI and hosted response headers across distinct origins.
// It does not substitute for a live DCV connection.
func TestHostedBrowserLaunch(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, session, _ := serviceFor(t)
	teams := setupMeeting(t, s.Store)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := s.Store.BeginSwitch(ctx, session.User.ID, teams[0].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, session.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(`UPDATE teams SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	if _, err := os.Stat(filepath.Join(s.Config.WebRoot, "lab", "index.html")); err != nil {
		t.Fatal("build static UI first: ", err)
	}
	s.Config.SDKRoot = t.TempDir()
	sdk, err := os.ReadFile("testdata/dcv.js")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Config.SDKRoot, "dcv.js"), sdk, 0600); err != nil {
		t.Fatal(err)
	}
	desktop := &desktopSession{record: accessRecord{ID: "access", UserID: session.User.ID, SessionID: session.ID, TeamID: teams[0].ID, InstanceID: teams[0].InstanceID, Expires: time.Now().Add(time.Hour)}, ctx: ctx, cancel: cancel, cookie: "browser-desktop-cookie"}
	s.desktops["access"] = desktop
	s.tickets[digest("browser-ticket")] = launchTicket{"access", session.ID, time.Now().Add(time.Minute)}
	// Both origins use the real HTTP policy and launch endpoint.
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	display := httptest.NewUnstartedServer(nil)
	defer display.Close()
	s.Config.Origin, s.Config.DesktopOrigin = "https://"+app.Listener.Addr().String(), "https://"+display.Listener.Addr().String()
	// Bind distinct ports before starting either handler.
	app.Config.Handler, display.Config.Handler = s.Handler(), s.Handler()
	app.StartTLS()
	display.StartTLS()
	cmd := exec.CommandContext(ctx, "node", "testdata/launch.mjs", app.URL, display.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestHostedExerciseBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, teams := exerciseFixture(t)
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	s.exerciseRunner = func(_ context.Context, team Team, run ExerciseRun, _ lc.Source) (string, error) {
		if team.ID == teams[1].ID {
			return "", fmt.Errorf("Web SSH connection unavailable; retry or reset")
		}
		return "Baseline and symptoms verified", nil
	}
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	cmd := exec.CommandContext(ctx, "node", "testdata/exercises.mjs", app.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestHostedCapacityBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, session, _ := serviceFor(t)
	var redirected atomic.Bool
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/authorize" {
			http.NotFound(w, r)
			return
		}
		redirected.Store(true)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<title>OIDC redirect fixture</title>"))
	}))
	defer idp.Close()
	s.Identity = browserLoginIdentity{Identity: s.Identity, authorizationURL: idp.URL + "/authorize"}
	session.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(context.Background(), "template", "built", Template{ID: "built", Name: "Example Branch", Commit: "abcdef123", Built: true}); err != nil {
		t.Fatal(err)
	}
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/capacity.mjs", app.URL, idp.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	if !redirected.Load() {
		t.Fatal("browser did not follow the server-owned identity redirect")
	}
}

func TestHostedTemplateTimelineBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, _ := templateFixture(t)
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	cmd := exec.CommandContext(ctx, "node", "testdata/templates.mjs", app.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestHostedScoreboardBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, _ := checkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var template Template
	if err := s.Store.Get(ctx, "template", "", &template); err != nil {
		t.Fatal(err)
	}
	template.Commit = "abcdef123456"
	template.CommitTitle = "Pinned private training checks"
	template.Built = true
	if err := s.Store.Put(ctx, "template", "", template); err != nil {
		t.Fatal(err)
	}
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	cmd := exec.CommandContext(ctx, "node", "testdata/scoreboard.mjs", app.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestHostedActivityRecoveryBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, teams := checkFixture(t)
	ctx := context.Background()
	failedCheck(t, s, teams[0])
	cleanup, err := s.Store.Enqueue(ctx, "access-cleanup", "startup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE jobs SET status='failed',error='Guest cleanup unconfirmed' WHERE id=?`, cleanup.ID); err != nil {
		t.Fatal(err)
	}
	old := failedCheck(t, s, teams[1])
	var run CheckRun
	if err := s.Store.Get(ctx, "check", old.Target, &run); err != nil {
		t.Fatal(err)
	}
	run.MeetingID = "ended-meeting"
	if err := s.Store.Put(ctx, "check", run.ID, run); err != nil {
		t.Fatal(err)
	}
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	checkCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	cmd := exec.CommandContext(checkCtx, "node", "testdata/activity.mjs", app.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
