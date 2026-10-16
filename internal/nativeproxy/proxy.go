// Package nativeproxy mediates the pinned builder's native API requests. Provider
// hooks verify ownership before mutation and retain native identities afterwards.
package nativeproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
)

type Reply struct {
	Status int
	Header http.Header
	Body   []byte
}

type beforeSubmission struct{ cause error }

func (e *beforeSubmission) Error() string { return e.cause.Error() }
func (e *beforeSubmission) Unwrap() error { return e.cause }

// BeforeSubmission records a local guard failure before RoundTrip was invoked.
// It must never wrap an HTTP transport error or a failure after a native response.
func BeforeSubmission(err error) error { return &beforeSubmission{err} }
func IsBeforeSubmission(err error) bool {
	var before *beforeSubmission
	return errors.As(err, &before)
}

type Config struct {
	Verify    func(context.Context, *http.Request, []byte) error
	Upstream  *url.URL
	Transport http.RoundTripper
	Protocol  string
	Step      func(*http.Request, []byte) string
	Before    func(context.Context, *http.Request, []byte) ([]byte, error)
	Prepare   func(*http.Request, []byte) error
	After     func(context.Context, *http.Request, []byte, Reply) error
	Do        func(context.Context, *http.Request, []byte, func(context.Context) (Reply, error)) (Reply, error)
}

// Start exposes a private loopback capability, never an unauthenticated signing
// proxy. The endpoint lives only in the protected temporary Packer configuration.
func Start(ctx context.Context, c Config) (string, func(), error) {
	if c.Upstream == nil || c.Step == nil {
		return "", nil, fmt.Errorf("native proxy requires an upstream and step classifier")
	}
	baseCtx, cancel := context.WithCancel(ctx)
	var gate sync.Mutex
	var handlers sync.WaitGroup
	closed := false
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return "", nil, err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		cancel()
		listener.Close()
		return "", nil, err
	}
	prefix := "/" + hex.EncodeToString(token[:])
	if c.Transport == nil {
		c.Transport = http.DefaultTransport
	}
	reject := func(w http.ResponseWriter, status int, message string) {
		if c.Protocol == "aws" {
			code := map[int]string{403: "UnauthorizedOperation", 400: "InvalidParameterValue", 429: "RequestLimitExceeded", 500: "InternalError"}[status]
			if code == "" {
				code = "UnauthorizedOperation"
			}
			w.Header().Set("Content-Type", "text/xml")
			w.WriteHeader(status)
			// Messages are fixed categories; never interpolate upstream bodies or secrets.
			fmt.Fprintf(w, "<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors></Response>", code, message)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"data":null,"message":"native ownership check rejected or unavailable"}`)
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		gate.Lock()
		if closed {
			gate.Unlock()
			w.WriteHeader(503)
			return
		}
		handlers.Add(1)
		gate.Unlock()
		defer handlers.Done()
		if in.URL.Path != prefix && !strings.HasPrefix(in.URL.Path, prefix+"/") {
			http.NotFound(w, in)
			return
		}
		r := in.Clone(baseCtx)
		r.URL.Path = strings.TrimPrefix(in.URL.Path, prefix)
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		r.URL.RawPath = ""
		body, err := io.ReadAll(io.LimitReader(in.Body, (8<<20)+1))
		if err != nil || len(body) > 8<<20 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		label := c.Step(r, body)
		// Guards run before the fault boundary: a during-send fault must not kill an
		// ownership read instead of the selected mutation.
		if c.Before != nil {
			body, err = c.Before(baseCtx, r, body)
			if err != nil {
				reject(w, 403, "ownership check rejected or unavailable; resource retained")
				return
			}
		}
		r.RequestURI = ""
		r.URL.Path = strings.TrimRight(c.Upstream.Path, "/") + r.URL.Path
		r.URL.Scheme = c.Upstream.Scheme
		r.URL.Host = c.Upstream.Host
		r.Host = c.Upstream.Host
		for _, header := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length"} {
			r.Header.Del(header)
		}
		r.Header.Del("Accept-Encoding")
		r.TransferEncoding = nil
		r.ContentLength = int64(len(body))
		r.Body = http.NoBody
		if len(body) > 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if c.Prepare != nil {
			if err = c.Prepare(r, body); err != nil {
				reject(w, 403, "native request could not be authorized before submission")
				return
			}
		}
		perform := func(callCtx context.Context) (Reply, error) {
			if c.Verify != nil {
				if err := c.Verify(baseCtx, r, body); err != nil {
					return Reply{}, BeforeSubmission(err)
				}
			}
			req := r.Clone(callCtx)
			res, err := c.Transport.RoundTrip(req)
			if err != nil {
				return Reply{}, err
			}
			defer res.Body.Close()
			reader := io.Reader(res.Body)
			header := res.Header.Clone()
			if strings.EqualFold(header.Get("Content-Encoding"), "gzip") {
				zipped, err := gzip.NewReader(res.Body)
				if err != nil {
					return Reply{}, err
				}
				defer zipped.Close()
				reader = zipped
				header.Del("Content-Encoding")
				header.Del("Content-Length")
			}
			b, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
			if err != nil {
				return Reply{}, err
			}
			if len(b) > 8<<20 {
				return Reply{}, fmt.Errorf("native response exceeds bound")
			}
			value, err := faultinject.Call(callCtx, label+".received", func(context.Context) (Reply, error) { return Reply{res.StatusCode, header, b}, nil })
			if err != nil {
				return Reply{}, err
			}
			faultinject.Trace(baseCtx, label, res.StatusCode)
			if c.After != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
				if err = c.After(baseCtx, r, body, value); err != nil {
					return Reply{}, err
				}
			}
			return value, nil
		}
		nativeCall := func(callCtx context.Context) (Reply, error) { return faultinject.Call(callCtx, label, perform) }
		var result Reply
		if c.Do != nil {
			result, err = c.Do(baseCtx, r, body, nativeCall)
		} else {
			result, err = nativeCall(baseCtx)
		}
		if err != nil {
			faultinject.TraceError(baseCtx, label, err)
			if IsBeforeSubmission(err) {
				reject(w, 403, "ownership check rejected before submission; resource retained")
				return
			}
			if status := faultinject.Status(err); status != 0 {
				reject(w, status, "injected rejection before mutation")
				return
			}
			// The request may have completed. A reset preserves uncertainty; a synthetic
			// authorization response here would falsely claim rejection before mutation.
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		for name, values := range result.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(result.Status)
		_, _ = w.Write(result.Body)
	})
	server := &http.Server{Handler: handler, ErrorLog: log.New(io.Discard, "", 0), ReadHeaderTimeout: 30 * time.Second}
	go server.Serve(listener)
	var once sync.Once
	close := func() {
		once.Do(func() {
			gate.Lock()
			closed = true
			gate.Unlock()
			cancel()
			_ = server.Close()
			handlers.Wait()
		})
	}
	stop := context.AfterFunc(ctx, close)
	return "http://" + listener.Addr().String() + prefix, func() { stop(); close() }, nil
}
