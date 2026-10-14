package hosted

import (
	"context"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type reconnectEngine struct {
	Engine
	info    lc.AccessInfo
	err     error
	opens   int
	machine string
}

func (e *reconnectEngine) OpenDesktopWithOptions(_ context.Context, _ string, machine string, _ lc.AccessOptions) (lc.AccessInfo, error) {
	e.opens++
	e.machine = machine
	return e.info, e.err
}
func TestReconnectRechecksNativeSessionAndReplacesOnlyChangedRelay(t *testing.T) {
	s, v, _ := serviceFor(t)
	ctx := context.Background()
	teams := setupMeeting(t, s.Store)
	_ = s.Store.Put(ctx, "template", "", Template{})
	_ = s.Store.Put(ctx, "template-instructor", "", InstructorBundle{Practice: &instructor.Practice{DesktopMachine: "workstation"}})
	if err := s.Store.BeginSwitch(ctx, v.User.ID, teams[0].ID, "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishSwitch(ctx, v.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(`UPDATE teams SET status='ready',instance_id='instance'`); err != nil {
		t.Fatal(err)
	}
	id := digest("instance\x00workstation\x00" + v.User.ID)[:24]
	dc, cancel := context.WithCancel(ctx)
	defer cancel()
	original := &desktopSession{record: accessRecord{id, v.User.ID, v.ID, teams[0].ID, "instance", v.Expires}, info: lc.AccessInfo{ID: id, URL: "http://127.0.0.1:1/launch?token=old", Expires: v.Expires}, ctx: dc, cancel: cancel}
	s.desktops[id] = original
	engine := &reconnectEngine{info: original.info}
	s.Engine = engine
	launch := func() int {
		w := httptest.NewRecorder()
		s.launch(w, httptest.NewRequest("POST", "/api/access", nil), v)
		return w.Code
	}
	if status := launch(); status != 200 || engine.opens != 1 || s.desktops[id] != original {
		t.Fatalf("cached reconnect: %d opens=%d", status, engine.opens)
	}
	engine.err = errors.New("unknown native status")
	if status := launch(); status != 503 || original.ctx.Err() != nil || s.desktops[id] != original {
		t.Fatal("unknown outcome replaced or revoked existing relay")
	}
	engine.err = nil
	redeemed := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redeemed++
		http.SetCookie(w, &http.Cookie{Name: "relay", Value: "test"})
		w.WriteHeader(303)
	}))
	defer relay.Close()
	engine.info = lc.AccessInfo{ID: id, URL: relay.URL + "/launch?token=new", Expires: time.Now().Add(time.Hour)}
	if status := launch(); status != 200 || s.desktops[id] == original || original.ctx.Err() == nil || redeemed != 1 {
		t.Fatalf("replacement failed status=%d redeemed=%d", status, redeemed)
	}
	defer s.desktops[id].cancel()
	if status := launch(); status != 200 || redeemed != 1 {
		t.Fatal("reconnect replayed consumed provider ticket")
	}
}
