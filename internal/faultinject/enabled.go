//go:build faultinject

package faultinject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptrace"
	"os"
	"sync"
	"syscall"
	"time"
)

type operationKey struct{}
type sentKey struct{}

func Sent(ctx context.Context) {
	if notify, ok := ctx.Value(sentKey{}).(func()); ok {
		notify()
	}
}

func WithOperation(ctx context.Context, platform, operation string) context.Context {
	return context.WithValue(ctx, operationKey{}, platform+"/"+operation)
}

type selection struct {
	Operation string `json:"operation"`
	Step      string `json:"step"`
	Fault     string `json:"fault"`
	Nth       int    `json:"nth"`
	// Marker is a private, exclusive receipt shared with an external kill harness.
	Marker string `json:"marker"`
}

var selections struct {
	sync.Mutex
	counts map[string]int
}

type rejection struct{ fault string }

func (e *rejection) Error() string { return "faultinject: " + e.fault + " rejected before mutation" }
func Status(err error) int {
	var e *rejection
	if !errors.As(err, &e) {
		return 0
	}
	return map[string]int{"reject-authorization": 403, "reject-validation": 400, "throttle": 429, "server-error": 500}[e.fault]
}
func Rejected(err error) bool {
	var e *rejection
	return errors.As(err, &e) && e.fault != "server-error"
}

// Call selects exactly one occurrence per process, or across processes when Marker
// is supplied. It never records request bodies, credentials or native identifiers.
// Response loss executes the real request before withholding its result. A during
// kill uses net/http's WroteRequest hook, after the request has left the client;
// its outcome is deliberately unknown. For SSH, use pause-before/pause-after and
// externally kill the controller while a guest step is in progress.
func Call[T any](ctx context.Context, step string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	raw := os.Getenv("LABCENTRAL_FAULT")
	if raw == "" {
		return call(ctx)
	}
	var s selection
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return zero, fmt.Errorf("faultinject: invalid selector: %w", err)
	}
	operation, _ := ctx.Value(operationKey{}).(string)
	if s.Operation != operation || s.Step != step {
		return call(ctx)
	}
	selections.Lock()
	if selections.counts == nil {
		selections.counts = map[string]int{}
	}
	selections.counts[raw]++
	n := selections.counts[raw]
	selections.Unlock()
	if s.Nth == 0 {
		s.Nth = 1
	}
	if n != s.Nth {
		return call(ctx)
	}
	signal := func() error {
		if s.Marker == "" {
			return nil
		}
		f, err := os.OpenFile(s.Marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(operation + " " + step + " " + s.Fault + "\n")
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err == nil {
			err = ce
		}
		return err
	}
	// A pre-existing receipt means another selected process already injected it.
	if s.Marker != "" {
		if _, err := os.Stat(s.Marker); err == nil {
			return call(ctx)
		} else if !os.IsNotExist(err) {
			return zero, err
		}
	}
	kill := func() { _ = syscall.Kill(os.Getpid(), syscall.SIGKILL); select {} }
	pause := func() error {
		if err := signal(); err != nil {
			return err
		}
		timer := time.NewTicker(20 * time.Millisecond)
		defer timer.Stop()
		for {
			if s.Marker != "" {
				if _, err := os.Stat(s.Marker + ".continue"); err == nil {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	switch s.Fault {
	case "kill-before":
		if err := signal(); err != nil {
			return zero, err
		}
		kill()
	case "pause-before":
		if err := pause(); err != nil {
			return zero, err
		}
	case "reject-authorization", "reject-validation", "throttle", "server-error":
		if err := signal(); err != nil {
			return zero, err
		}
		return zero, &rejection{s.Fault}
	case "kill-during":
		var once sync.Once
		notify := func() {
			once.Do(func() {
				if signal() == nil {
					kill()
				}
			})
		}
		ctx = context.WithValue(ctx, sentKey{}, notify)
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				notify()
			}
		}})
	case "response-loss", "kill-after", "pause-after":
	default:
		return zero, fmt.Errorf("faultinject: unsupported fault %q", s.Fault)
	}
	value, err := call(ctx)
	// WroteRequest runs on net/http's writer goroutine. Do not let a very fast
	// response return while its selected kill is still persisting the receipt.
	if s.Fault == "kill-during" && err == nil {
		Sent(ctx)
	}
	if err != nil {
		return value, err
	}
	switch s.Fault {
	case "response-loss":
		if err := signal(); err != nil {
			return zero, err
		}
		return zero, fmt.Errorf("faultinject: response lost after %s: %w", step, context.DeadlineExceeded)
	case "kill-after":
		if err := signal(); err != nil {
			return zero, err
		}
		kill()
	case "pause-after":
		if err := pause(); err != nil {
			return zero, err
		}
		return value, nil
	}
	return value, nil
}

// Trace records only normalized step names and status codes, never bodies or credentials.
func Trace(ctx context.Context, step string, status int) {
	if path := os.Getenv("LABCENTRAL_FAULT_TRACE"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			fmt.Fprintf(f, "%s %d\n", step, status)
			f.Close()
		}
	}
}

func TraceError(ctx context.Context, step string, err error) {
	if path := os.Getenv("LABCENTRAL_FAULT_TRACE"); path != "" {
		if f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
			fmt.Fprintf(f, "%s error %T: %v\n", step, err, err)
			f.Close()
		}
	}
}
