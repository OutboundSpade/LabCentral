//go:build faultinject

package faultinject

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
)

func selector(t *testing.T, operation, step, fault, marker string, nth int) {
	t.Helper()
	b, _ := json.Marshal(selection{Operation: operation, Step: step, Fault: fault, Marker: marker, Nth: nth})
	t.Setenv("LABCENTRAL_FAULT", string(b))
}
func TestResponseLossPerformsRequestExactlyOnce(t *testing.T) {
	var native atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { native.Add(1); w.Write([]byte("created")) }))
	defer server.Close()
	marker := filepath.Join(t.TempDir(), "receipt")
	selector(t, "aws/network.create", "CreateVpc", "response-loss", marker, 1)
	ctx := WithOperation(context.Background(), "aws", "network.create")
	request := func(ctx context.Context) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		res.Body.Close()
		return "created", nil
	}
	value, err := Call(ctx, "CreateVpc", request)
	if value != "" || !errors.Is(err, context.DeadlineExceeded) || native.Load() != 1 {
		t.Fatal(value, err, native.Load())
	}
	// Same selector/receipt does not keep poisoning recovery or another process.
	value, err = Call(ctx, "CreateVpc", request)
	if value != "created" || err != nil || native.Load() != 2 {
		t.Fatal(value, err, native.Load())
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}
func TestSelectionAndDefiniteRejection(t *testing.T) {
	for _, fault := range []string{"reject-authorization", "reject-validation", "throttle", "server-error"} {
		t.Run(fault, func(t *testing.T) {
			selector(t, "proxmox/machine.create", "POST/qemu/{id}/clone", fault, filepath.Join(t.TempDir(), "receipt"), 2)
			calls := 0
			f := func(context.Context) (bool, error) { calls++; return true, nil }
			ctx := WithOperation(context.Background(), "proxmox", "machine.create")
			if _, err := Call(ctx, "POST/qemu/{id}/clone", f); err != nil {
				t.Fatal(err)
			}
			_, err := Call(ctx, "POST/qemu/{id}/clone", f)
			if err == nil || calls != 1 || Rejected(err) != (fault != "server-error") {
				t.Fatal(err, calls)
			}
			if _, err = Call(ctx, "POST/qemu/{id}/clone", f); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSIGKILLNativeBoundaries(t *testing.T) {
	if target := os.Getenv("LC_FAULT_CHILD_URL"); target != "" {
		ctx := WithOperation(context.Background(), "aws", "network.create")
		_, _ = Call(ctx, "CreateVpc", func(ctx context.Context) (bool, error) {
			req, _ := http.NewRequestWithContext(ctx, "POST", target, nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return false, err
			}
			res.Body.Close()
			return true, nil
		})
		os.Exit(3)
	}
	for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
		t.Run(fault, func(t *testing.T) {
			native := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { native <- struct{}{}; w.Write([]byte("created")) }))
			defer server.Close()
			marker := filepath.Join(t.TempDir(), "receipt")
			selector(t, "aws/network.create", "CreateVpc", fault, marker, 1)
			child := exec.Command(os.Args[0], "-test.run=^TestSIGKILLNativeBoundaries$")
			child.Env = append(os.Environ(), "LC_FAULT_CHILD_URL="+server.URL)
			err := child.Run()
			var status *exec.ExitError
			if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatal("not actually killed", err)
			}
			if _, err = os.Stat(marker); err != nil {
				t.Fatal(err)
			}
			if fault == "kill-after" {
				select {
				case <-native:
				default:
					t.Fatal("kill occurred before native effect")
				}
			}
			if fault == "kill-before" {
				select {
				case <-native:
					t.Fatal("native effect before pre-call kill")
				default:
				}
			}
		})
	}
}
