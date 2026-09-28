package access

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRelayRequiresSingleUseLaunchAndLocalOrigin(t *testing.T) {
	r, e := NewRelay(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) }))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://" + r.host
	res, e := client.Get(base + "/")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("unauthenticated access")
	}
	res, e = client.Get(r.URL)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 303 {
		t.Fatal("launch failed")
	}
	cookies := res.Cookies()
	res, e = client.Get(r.URL)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("replayed launch accepted")
	}
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("Origin", "https://evil.invalid")
	res, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin accepted")
	}
	req.Header.Del("Origin")
	res, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("authorized request failed")
	}
	u, _ := url.Parse(r.URL)
	if !strings.HasPrefix(u.Host, "127.0.0.1:") {
		t.Fatal("non-loopback bind")
	}
}
