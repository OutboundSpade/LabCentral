package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
