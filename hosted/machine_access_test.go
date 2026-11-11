package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/coder/websocket"
)

// Local hosted/relay fixtures only: these tests make no cloud or DCV claim.
type rangeAccessEngine struct {
	Engine
	src            lc.Source
	relay          string
	mu             sync.Mutex
	opened, closed map[string]string
	terminal       chan string
	closeError     error
}

func (e *rangeAccessEngine) Inspect(_ context.Context, id string) (lc.Instance, error) {
	p := lc.Plan{Source: e.src}
	for _, name := range lc.SortedMachineNames(e.src.Lab) {
		spec := e.src.Lab.Machines[name]
		p.Machines = append(p.Machines, lc.PlannedMachine{Name: name, Spec: spec, OS: lc.OSFamily(e.src.Lab.Images[spec.Image].OS)})
		p.Machines[len(p.Machines)-1].Appliance = e.src.Lab.Images[spec.Image].ApplianceKind()
		if p.Machines[len(p.Machines)-1].Appliance == "panos" {
			p.Machines[len(p.Machines)-1].Address = "172.30.0.200"
		}
	}
	return lc.Instance{ID: id, Status: "ready", Plan: p}, nil
}

func addRangeAppliances(t *testing.T, s *Service, e *rangeAccessEngine) {
	t.Helper()
	for _, kind := range []string{"vyos", "panos"} {
		e.src.Lab.Images[kind] = lc.Image{Base: kind, Appliance: &lc.ApplianceImage{Kind: kind}}
		e.src.Lab.Machines[kind] = lc.Machine{Image: kind, Access: []string{"ssh"}}
	}
	if err := s.Store.Put(context.Background(), "source", "pinned", e.src); err != nil {
		t.Fatal(err)
	}
}

func TestApplianceInventoryExposesTerminalAndPrivateManagementHint(t *testing.T) {
	s, _, _, e := rangeAccessFixture(t)
	addRangeAppliances(t, s, e)
	inventory := decodeLearning[CoachMachines](t, request(s, "GET", "/api/machines", "", "", ""), 200)
	found := 0
	for _, m := range inventory.Machines {
		if m.Appliance != "" {
			found++
			if !m.SSH || m.Desktop {
				t.Fatal("appliance offered unsupported access", m)
			}
			if m.Appliance == "panos" && m.ManagementURL != "https://172.30.0.200/" {
				t.Fatal("private management hint missing", m)
			}
		}
	}
	if found != 2 {
		t.Fatal("appliances absent", inventory)
	}
}
func (e *rangeAccessEngine) OpenDesktopWithOptions(_ context.Context, instance, machine string, o lc.AccessOptions) (lc.AccessInfo, error) {
	id := digest(instance + "\x00" + machine + "\x00" + o.Principal)[:24]
	e.mu.Lock()
	e.opened[id] = machine
	e.mu.Unlock()
	return lc.AccessInfo{ID: id, URL: e.relay + "/launch", Username: machine, Password: "fixture-" + machine, Expires: time.Now().Add(time.Hour)}, nil
}
func (e *rangeAccessEngine) CloseAccess(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeError != nil {
		return e.closeError
	}
	e.closed[id] = e.opened[id]
	delete(e.opened, id)
	return nil
}
func (e *rangeAccessEngine) WithSSHSession(ctx context.Context, _, machine string, _ func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	e.terminal <- machine
	<-ctx.Done()
	return ctx.Err()
}
func rangeAccessFixture(t *testing.T) (*Service, Session, []Team, *rangeAccessEngine) {
	t.Helper()
	s, v, teams, original := windowsFixture(t)
	src := original.src
	src.Lab.Machines = map[string]lc.Machine{}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("box-%d", i+1)
		if i == 0 {
			name = "server"
		}
		src.Lab.Machines[name] = lc.Machine{Image: "iis", CPU: 2, Memory: "4Gi", Disk: "30Gi", Access: []string{"ssh", "desktop"}}
	}
	if err := s.Store.Put(context.Background(), "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	e := &rangeAccessEngine{Engine: s.Engine, src: src, relay: original.relay, opened: map[string]string{}, closed: map[string]string{}, terminal: make(chan string, 8)}
	s.Engine = e
	return s, v, teams, e
}

func TestEightMachineDesktopsRemainIndependent(t *testing.T) {
	s, v, teams, e := rangeAccessFixture(t)
	inventory := decodeLearning[CoachMachines](t, request(s, "GET", "/api/machines", "", "", ""), 200)
	if len(inventory.Machines) != 8 || inventory.InstanceID != teams[0].InstanceID {
		t.Fatal(inventory)
	}
	var allCookies []*http.Cookie
	for _, machine := range inventory.Machines {
		if !machine.SSH || !machine.Desktop {
			t.Fatal("access omitted", machine)
		}
		launch := decodeLearning[Launch](t, consoleRequest(s, v, "/api/machines/"+machine.Name+"/desktop"), 200)
		r := httptest.NewRequest("POST", launch.URL, strings.NewReader("ticket="+launch.Ticket))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", s.Config.Origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		id := digest(teams[0].InstanceID + "\x00" + machine.Name + "\x00" + v.User.ID)[:24]
		if w.Code != 303 || w.Header().Get("Location") != desktopPath(id)+"/" {
			t.Fatal(w.Code, w.Header())
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != desktopCookieName(id) || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" {
			t.Fatal(cookies)
		}
		allCookies = append(allCookies, cookies[0])
	}
	// A browser sends all host cookies. The URL must still select only its guest.
	for _, machine := range inventory.Machines {
		id := digest(teams[0].InstanceID + "\x00" + machine.Name + "\x00" + v.User.ID)[:24]
		r := httptest.NewRequest("GET", s.Config.DesktopOrigin+desktopPath(id)+"/credentials", nil)
		for _, cookie := range allCookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"`+machine.Name+`"`) {
			t.Fatal("desktop changed machines", machine.Name, w.Code, w.Body.String())
		}
	}
	decodeLearning[struct{ OK bool }](t, consoleRequest(s, v, "/api/machines/server/desktop/close"), 200)
	e.mu.Lock()
	openCount, closedCount := len(e.opened), len(e.closed)
	e.mu.Unlock()
	if openCount != 7 || closedCount != 1 {
		t.Fatal("closing one desktop affected siblings", openCount, closedCount)
	}
	// A different cookie copied under another machine's name cannot authorize it.
	id := digest(teams[0].InstanceID + "\x00box-2\x00" + v.User.ID)[:24]
	r := httptest.NewRequest("GET", s.Config.DesktopOrigin+desktopPath(id)+"/credentials", nil)
	r.AddCookie(&http.Cookie{Name: desktopCookieName(id), Value: allCookies[len(allCookies)-1].Value})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("cross-machine cookie accepted")
	}
	decodeLearning[struct{ OK bool }](t, consoleRequest(s, v, "/api/access/close"), 200)
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opened) != 0 || len(e.closed) != 8 {
		t.Fatal("close-all left sessions", e.opened, e.closed)
	}
}

func TestTeamMachineVisibilityAndAuthorization(t *testing.T) {
	s, v, teams, _ := privateFixture(t)
	visible := decodeLearning[CoachMachines](t, request(s, "GET", "/api/machines", "", "", ""), 200)
	if len(visible.Machines) != 1 || visible.Machines[0].Name != "workstation" {
		t.Fatal("hidden machine disclosed", visible)
	}
	for _, path := range []string{"/api/machines/support/desktop", "/api/machines/missing/desktop", "/api/admin/teams/" + teams[1].ID + "/machines/workstation/desktop"} {
		w := consoleRequest(s, v, path)
		if w.Code != 403 && w.Code != 409 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/api/machines/support/terminal", "/api/machines/missing/terminal"} {
		w := request(s, "GET", path, "", s.Config.Origin, v.CSRF)
		if w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	if w := request(s, "POST", "/api/machines/workstation/desktop", "{}", s.Config.Origin, "wrong"); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	if err := s.Store.BeginSwitch(context.Background(), v.User.ID, teams[1].ID, "incident"); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", "/api/machines", "", "", ""); w.Code != 403 {
		t.Fatal("switching user admitted", w.Code)
	}
}

func TestEightTeamTerminalsRevokeOnTeamSwitch(t *testing.T) {
	s, v, teams, e := rangeAccessFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	server := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + server.Listener.Addr().String()
	server.Config.Handler = s.Handler()
	server.StartTLS()
	defer server.Close()
	var connections []*websocket.Conn
	for _, name := range lc.SortedMachineNames(e.src.Lab) {
		conn, _, err := websocket.Dial(ctx, server.URL+"/api/machines/"+name+"/terminal", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Origin": []string{s.Config.Origin}, "Cookie": []string{"__Host-labcentral=cookie"}}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		connections = append(connections, conn)
		hello, _ := json.Marshal(coachTerminalHello{CSRF: v.CSRF, MeetingID: "meeting", Template: "pinned", InstanceID: teams[0].InstanceID})
		if err = conn.Write(ctx, websocket.MessageText, hello); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-e.terminal:
			if got != name {
				t.Fatal("wrong machine", got)
			}
		case <-ctx.Done():
			t.Fatal("student terminal not admitted", ctx.Err())
		}
	}
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[1].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	for _, conn := range connections {
		_, _, err := conn.Read(ctx)
		if websocket.CloseStatus(err) != coachAccessRevoked {
			t.Fatal("terminal survived team switch", err)
		}
	}
}

func TestMachineCleanupIntentPersistsIndependently(t *testing.T) {
	s, v, teams, e := rangeAccessFixture(t)
	for _, name := range []string{"server", "box-2"} {
		decodeLearning[Launch](t, consoleRequest(s, v, "/api/machines/"+name+"/desktop"), 200)
	}
	e.mu.Lock()
	e.closeError = errors.New("fixture cleanup outcome unknown")
	e.mu.Unlock()
	if w := consoleRequest(s, v, "/api/machines/server/desktop/close"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	id := digest(teams[0].InstanceID + "\x00server\x00" + v.User.ID)[:24]
	reopened, err := OpenStore(s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	var record accessRecord
	var operation desktopOperation
	err = errors.Join(reopened.Get(context.Background(), "desktop", id, &record), reopened.Get(context.Background(), "desktop-operation", id, &operation))
	if closeErr := reopened.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || record.Machine != "server" || !record.MachineScoped || operation.Status != "closing" {
		t.Fatal("cleanup intent not retained", record, operation, err)
	}
	e.mu.Lock()
	e.closeError = nil
	e.mu.Unlock()
	decodeLearning[struct{ OK bool }](t, consoleRequest(s, v, "/api/machines/server/desktop/close"), 200)
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opened) != 1 {
		t.Fatal("recovered cleanup affected sibling", e.opened)
	}
}

func TestDesktopProtocolSelectorIsAuthenticatedAndStripped(t *testing.T) {
	s, v, teams, e := rangeAccessFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/launch" {
			http.SetCookie(w, &http.Cookie{Name: "relay", Value: "fixture"})
			w.WriteHeader(303)
			return
		}
		if r.URL.Path != "/protocol" || r.URL.Query().Get("native") != "kept" || r.URL.Query().Has("labcentralDesktop") {
			t.Error("provider request was not preserved and stripped", r.URL)
		}
		w.Write([]byte("fixture protocol reply"))
	}))
	defer upstream.Close()
	e.relay = upstream.URL
	decodeLearning[Launch](t, consoleRequest(s, v, "/api/machines/server/desktop"), 200)
	id := digest(teams[0].InstanceID + "\x00server\x00" + v.User.ID)[:24]
	d := s.desktops[id]
	for _, tc := range []struct {
		path   string
		cookie bool
		status int
	}{
		{"/protocol?native=kept&labcentralDesktop=" + id, true, 200},
		{"/protocol?native=kept&labcentralDesktop=" + id, false, 401},
		{"/protocol?labcentralDesktop=" + id + "&labcentralDesktop=" + id, true, 404},
		{desktopPath(id) + "/protocol?native=kept&labcentralDesktop=" + id, true, 200},
		{desktopPath(id) + "/protocol?labcentralDesktop=" + strings.Repeat("b", 24), true, 404},
	} {
		r := httptest.NewRequest("GET", s.Config.DesktopOrigin+tc.path, nil)
		if tc.cookie {
			r.AddCookie(&http.Cookie{Name: desktopCookieName(id), Value: d.cookie})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestMeetingRangeDoesNotRequireDefaultDesktop(t *testing.T) {
	s, v, _, _ := rangeAccessFixture(t)
	ctx := context.Background()
	var b InstructorBundle
	if err := s.Store.Get(ctx, "template-instructor", "pinned", &b); err != nil {
		t.Fatal(err)
	}
	b.Practice, b.Stages, b.Scenarios = nil, nil, nil
	if err := s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
		t.Fatal(err)
	}
	view := decodeLearning[Overview](t, request(s, "GET", "/api/me", "", "", ""), 200)
	if len(view.Lab.Machines) != 8 || view.Lab.DefaultDesktopMachine != "" {
		t.Fatal(view.Lab)
	}
	decodeLearning[Launch](t, consoleRequest(s, v, "/api/machines/server/desktop"), 200)
}

func TestScopedDesktopRechecksMachinePermission(t *testing.T) {
	s, v, teams, _ := rangeAccessFixture(t)
	ctx := context.Background()
	for _, name := range []string{"server", "box-2"} {
		decodeLearning[Launch](t, consoleRequest(s, v, "/api/machines/"+name+"/desktop"), 200)
	}
	var src lc.Source
	if err := s.Store.Get(ctx, "source", "pinned", &src); err != nil {
		t.Fatal(err)
	}
	spec := src.Lab.Machines["box-2"]
	spec.Access = []string{"ssh"}
	src.Lab.Machines["box-2"] = spec
	if err := s.Store.Put(ctx, "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	s.revalidateDesktops(ctx)
	for _, name := range []string{"server", "box-2"} {
		id := digest(teams[0].InstanceID + "\x00" + name + "\x00" + v.User.ID)[:24]
		if revoked := s.desktops[id].ctx.Err() != nil; revoked != (name == "box-2") {
			t.Fatal("permission change affected wrong machine", name, revoked)
		}
	}
}
