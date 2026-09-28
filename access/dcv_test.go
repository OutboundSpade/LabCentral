package access

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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
