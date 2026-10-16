package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDCVProxyPinsCertificateAndKeepsRelayCookieLocal(t *testing.T) {
	requests := make(chan *http.Request, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	for _, valid := range []bool{true, false} {
		pin := sha256.Sum256(upstream.Certificate().Raw)
		if !valid {
			pin[0] ^= 1
		}
		proxy, transport := dcvProxy(target, pin)
		local := httptest.NewServer(proxy)
		request, _ := http.NewRequest("GET", local.URL+"/auth", nil)
		request.AddCookie(&http.Cookie{Name: "labcentral_access", Value: "local-only"})
		request.AddCookie(&http.Cookie{Name: "dcv-auth", Value: "guest-session"})
		response, err := local.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		local.Close()
		transport.CloseIdleConnections()
		if !valid {
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("untrusted upstream accepted: %d", response.StatusCode)
			}
			select {
			case <-requests:
				t.Fatal("request reached untrusted upstream")
			default:
			}
			continue
		}
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("trusted upstream rejected: %d", response.StatusCode)
		}
		received := <-requests
		if _, err := received.Cookie("labcentral_access"); err != http.ErrNoCookie {
			t.Fatal("relay cookie disclosed upstream")
		}
		if c, err := received.Cookie("dcv-auth"); err != nil || c.Value != "guest-session" {
			t.Fatal("upstream authentication cookie lost")
		}
		if received.Host != target.Host || received.Header.Get("Origin") != target.String() {
			t.Fatal("wrong upstream authority")
		}
	}
}

func TestCachedDesktopReadinessPreservesUnknownOutcomes(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		m := NewManager()
		closed := false
		checked := false
		d := &desktop{info: Info{ID: "operation", Expires: time.Now().Add(time.Hour)}, close: func() error { closed = true; return nil }, ready: func(context.Context) (bool, error) {
			checked = true
			if unknown {
				return false, errors.New("unreachable")
			}
			return true, nil
		}}
		m.sessions["operation"] = d
		c := lc.Command{Action: "access.open", Operation: "operation", Owner: "owner", Prior: lc.Resource{Owner: "owner", Name: "workstation"}, Machine: lc.PlannedMachine{Name: "workstation"}}
		_, err := m.Execute(context.Background(), c)
		if (err != nil) != unknown || !checked || closed || m.sessions["operation"] != d {
			t.Fatalf("unknown=%v err=%v checked=%v closed=%v", unknown, err, checked, closed)
		}
	}
}

func TestRetiredCachedDesktopCannotReuseItsRevokedGeneration(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			manager := NewManager()
			closed := 0
			expiry := time.Now().Add(time.Hour)
			if expired {
				expiry = time.Now().Add(-time.Second)
			}
			old := &desktop{info: Info{Expires: expiry}, ready: func(context.Context) (bool, error) { return false, nil }, close: func() error { closed++; return nil }}
			manager.sessions["operation"] = old
			c := lc.Command{Action: "access.open", Owner: "owner", Operation: "operation", Prior: lc.Resource{Owner: "owner", Name: "desktop"}, Machine: lc.PlannedMachine{Name: "desktop"}}
			_, err := manager.Execute(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), "previous desktop generation retired") || closed != 1 || manager.sessions["operation"] != nil {
				t.Fatal("retired generation reopened", closed, err)
			}
		})
	}
}
