package hosted

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHostedLiveScoringBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, v, _, _, mode := liveScoreFixture(t)
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	production := s.Handler()
	// Fixture controls alter only local test data; they are not service routes.
	app.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_fixture/stale":
			mode.Store(1)
			expireScoreCache(t, s)
			w.WriteHeader(204)
		case "/_fixture/ordinary":
			m, _ := s.Store.Meeting(r.Context())
			s.Store.Delete(r.Context(), "live-red-blue", m.ID)
			w.WriteHeader(204)
		case "/_fixture/coach":
			v.User.Admin = true
			v.Checked = time.Now()
			s.Store.Put(r.Context(), "session", v.ID, v)
			w.WriteHeader(204)
		default:
			production.ServeHTTP(w, r)
		}
	})
	app.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/live-scoring.mjs", app.URL)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
