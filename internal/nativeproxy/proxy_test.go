package nativeproxy

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCapabilityAndOwnershipGuardPreventUpstreamMutation(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, "accepted") }))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	endpoint, stopProxy, err := Start(context.Background(), Config{Upstream: target, Protocol: "aws", Step: func(*http.Request, []byte) string { return "mutation" }, Before: func(context.Context, *http.Request, []byte) ([]byte, error) {
		return nil, errors.New("ownership_mismatch")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopProxy()
	u, _ := url.Parse(endpoint)
	for _, path := range []string{"/", u.Path + "/native"} {
		res, err := http.Post(u.Scheme+"://"+u.Host+path, "text/plain", strings.NewReader("secret"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := 404
		if path != "/" {
			want = 403
		}
		if res.StatusCode != want {
			t.Fatal(res.StatusCode, want)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unverified mutation reached upstream")
	}
}

func TestProxyCloseCancelsAndWaitsForNativeHandler(t *testing.T) {
	started, settled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(settled) }))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	endpoint, stopProxy, err := Start(context.Background(), Config{Upstream: target, Step: func(*http.Request, []byte) string { return "mutation" }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		res, _ := http.Get(endpoint + "/native")
		if res != nil {
			res.Body.Close()
		}
		close(done)
	}()
	<-started
	stopProxy()
	<-settled
	<-done
}

func TestCompressedNativeReplyDecodedBeforeReceipt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		io.WriteString(z, "<DescribeImagesResponse/>")
		z.Close()
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	var observed atomic.Bool
	// Disable automatic transport decompression to exercise an explicit native gzip
	// response, as returned when the builder supplied Accept-Encoding itself.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	endpoint, stop, err := Start(context.Background(), Config{Upstream: target, Transport: transport, Step: func(*http.Request, []byte) string { return "read" }, After: func(_ context.Context, _ *http.Request, _ []byte, res Reply) error {
		if string(res.Body) != "<DescribeImagesResponse/>" || res.Header.Get("Content-Encoding") != "" {
			t.Error("receipt saw compressed bytes", res.Header)
		}
		observed.Store(true)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	req, _ := http.NewRequest("POST", endpoint+"/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || string(body) != "<DescribeImagesResponse/>" || !observed.Load() {
		t.Fatal(string(body), observed.Load(), err)
	}
}

func TestFinalGuardDenialIsDefinitelyNotSubmitted(t *testing.T) {
	var calls atomic.Int32
	var classified atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	endpoint, stop, err := Start(context.Background(), Config{Upstream: target, Protocol: "aws", Step: func(*http.Request, []byte) string { return "create" }, Verify: func(context.Context, *http.Request, []byte) error { return errors.New("ownership_mismatch") }, Do: func(ctx context.Context, _ *http.Request, _ []byte, call func(context.Context) (Reply, error)) (Reply, error) {
		result, err := call(ctx)
		classified.Store(IsBeforeSubmission(err))
		return result, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	res, err := http.Post(endpoint+"/", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || calls.Load() != 0 || !classified.Load() {
		t.Fatal("guard denial confused with lost response", res.StatusCode, calls.Load(), classified.Load())
	}
}
