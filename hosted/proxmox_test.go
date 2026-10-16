package hosted

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestProxmoxConsoleHolderMatrix(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, string)
	}{
		{"conflict", testConsoleConflictTakeoverAndDisplacedCookie},
		{"authorization", testConsoleTakeoverAuthorization},
		{"unknown-revoke-restart", testConsoleUnknownRevokePersistsAcrossRestart},
		{"concurrent-takeovers", testConsoleConcurrentTakeoversNeverOverlap},
		{"unknown-open", testConsoleUnknownOpenRequiresConfirmedCleanup},
		{"confirmed-destruction", testConsoleEndClearsHolderOnlyAfterConfirmedDestruction},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, "proxmox") })
	}
}
func TestConsoleKeyPlatforms(t *testing.T) {
	for _, tc := range []struct {
		platform, os string
		console      bool
	}{
		{"aws", "linux", false}, {"aws", "windows", true}, {"proxmox", "linux", true},
	} {
		a := accessRecord{Platform: tc.platform, OS: tc.os, InstanceID: "instance", Machine: "desktop"}
		if (a.consoleKey() != "") != tc.console {
			t.Fatal(tc)
		}
		a.TeamID = "practice:run"
		if a.consoleKey() != "" {
			t.Fatal("personal console entered team holder namespace")
		}
	}
}
func TestProxmoxDesktopOriginPolicy(t *testing.T) {
	s, v, teams, _ := consoleFixture(t, "proxmox")
	ctx := context.Background()
	launch := decodeLearning[Launch](t, consoleRequest(s, v, "/api/access"), 200)
	d := s.desktops[consoleRecord(v, teams[0]).ID]
	r := httptest.NewRequest("POST", launch.URL, strings.NewReader("ticket="+launch.Ticket))
	r.Header.Set("Origin", s.Config.Origin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", launch.URL, strings.NewReader("ticket="+launch.Ticket))
	r.Header.Set("Origin", s.Config.Origin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("ticket replay accepted")
	}
	for _, tc := range []struct {
		path, origin, host string
		want               int
	}{
		{"/", "", "", 200}, {"/client.js", "", "", 200}, {"/credentials", "", "", 200},
		{"/dcvjs/dcv.js", "", "", 404}, {"/websocket", "", "", 403}, {"/credentials", "https://foreign.example.invalid", "", 403},
		{"/credentials", "", "foreign.example.invalid", 403},
	} {
		r = httptest.NewRequest("GET", s.Config.DesktopOrigin+tc.path, nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", tc.origin)
		if tc.host != "" {
			r.Host = tc.host
		}
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		for _, secret := range []string{"PVEAPIToken", "vncticket=", "vncproxy", "TokenSecret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("native secret in response", secret)
			}
		}
		if tc.path == "/client.js" && tc.want == 200 && !strings.Contains(w.Body.String(), "/vendor/core/rfb.js") {
			t.Fatal("not noVNC")
		}
	}
	// Twenty-second policy loop invokes this same authorization/cancellation path.
	if err := s.Store.Delete(ctx, "session", v.ID); err != nil {
		t.Fatal(err)
	}
	s.revalidateDesktops(ctx)
	if d.ctx.Err() == nil {
		t.Fatal("revoked transport remained active")
	}
	r = httptest.NewRequest("GET", s.Config.DesktopOrigin+"/credentials", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func proxmoxEnvironment() lc.Environment {
	return lc.Environment{Version: 2, Name: "test", Platform: "proxmox", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]lc.Base{"ubuntu-24.04": {ID: "2100", Identity: strings.Repeat("a", 40), User: "ubuntu", DiskGiB: 20}}, Proxmox: lc.ProxmoxConfig{URL: "https://pve.example.invalid:8006", Node: "node", Storage: "local", Pool: "labs", TokenID: "labcentral@pve!labs", TokenEnv: "LABCENTRAL_TEST_TOKEN", VMIDStart: 2000, VMIDEnd: 2099, VLANStart: 100, VLANEnd: 163, CIDR: "10.80.0.0/16", ManagementBridge: "vmbr0", LabBridge: "vmbr3"}}
}
func TestHostedProxmoxEnvironment(t *testing.T) {
	env := proxmoxEnvironment()
	if err := ValidateHostedEnvironment(env); err != nil {
		t.Fatal(err)
	}
	env.Platform = "unknown"
	if err := ValidateHostedEnvironment(env); err == nil {
		t.Fatal("unknown accepted")
	}
	env = proxmoxEnvironment()
	env.Proxmox.LabBridge = ""
	if err := ValidateHostedEnvironment(env); err == nil {
		t.Fatal("invalid Proxmox accepted")
	}
}

func TestRevokedLaunchTicketCannotAttachToReopenedConsole(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		t.Run(platform, func(t *testing.T) {
			s, v, _, _ := consoleFixture(t, platform)
			old := decodeLearning[Launch](t, consoleRequest(s, v, "/api/access"), 200)
			decodeLearning[struct{ OK bool }](t, consoleRequest(s, v, "/api/access/close"), 200)
			next := decodeLearning[Launch](t, consoleRequest(s, v, "/api/access"), 200)
			redeem := func(ticket string) int {
				r := httptest.NewRequest("POST", s.Config.DesktopOrigin+"/launch", strings.NewReader("ticket="+ticket))
				r.Header.Set("Origin", s.Config.Origin)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w.Code
			}
			if got := redeem(old.Ticket); got != 403 {
				t.Fatal("revoked ticket attached to new grant", got)
			}
			if got := redeem(next.Ticket); got != 303 {
				t.Fatal("fresh ticket rejected", got)
			}
		})
	}
}
