package hosted

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real hosted HTTP policy and static UI, with guest desktop/SSH transport fixtures.
func TestHostedEightMachineBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for browser checks")
	}
	s, _, _, engine := rangeAccessFixture(t)
	addRangeAppliances(t, s, engine)
	s.Config.WebRoot = filepath.Join("..", "web", "build")
	s.Config.SDKRoot = t.TempDir()
	sdk, err := os.ReadFile("testdata/dcv.js")
	if err != nil {
		t.Fatal(err)
	}
	sdk = []byte(strings.ReplaceAll(string(sdk), "sessionId:''", "sessionId:'console'"))
	sdk = []byte(strings.ReplaceAll(string(sdk), "authenticate(url,callbacks){", "authenticate(url,callbacks){window.fixtureDcvRoute=Object.fromEntries(callbacks.httpExtraSearchParamsCallback());"))
	sdk = []byte(strings.ReplaceAll(string(sdk), "window.fixtureClipboardAuto=", "window.fixtureDcvChannelRoute=Object.fromEntries(options.callbacks.httpExtraSearchParamsCallback());window.fixtureClipboardAuto="))
	if err = os.WriteFile(filepath.Join(s.Config.SDKRoot, "dcv.js"), sdk, 0600); err != nil {
		t.Fatal(err)
	}
	app, display := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	defer app.Close()
	defer display.Close()
	s.Config.Origin, s.Config.DesktopOrigin = "https://"+app.Listener.Addr().String(), "https://"+display.Listener.Addr().String()
	app.Config.Handler, display.Config.Handler = s.Handler(), s.Handler()
	app.StartTLS()
	display.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/range-access.mjs", app.URL, display.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
