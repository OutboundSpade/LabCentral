//go:build faultinject

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func TestAWSNativeSIGKILLMatrix(t *testing.T) {
	if endpoint := os.Getenv("LC_NATIVE_CHILD_ENDPOINT"); endpoint != "" {
		api := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1}, faultOptions)
		p := &Provider{Journal: journal.Journal{Root: os.Getenv("LC_NATIVE_CHILD_JOURNAL")}}
		ctx := faultinject.WithOperation(context.Background(), "aws", "network.create")
		c := twoNetworks()
		if prior := os.Getenv("LC_NATIVE_CHILD_PRIOR"); prior != "" {
			b, err := os.ReadFile(prior)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &c.Prior); err != nil {
				t.Fatal(err)
			}
			ctx = faultinject.WithOperation(ctx, "aws", "resource.destroy")
			_, _ = p.destroyNetwork(ctx, api, c)
		} else {
			_, _ = p.network(ctx, api, c)
		}
		os.Exit(3)
	}
	for _, cleanup := range []bool{false, true} {
		for _, step := range networkBoundaries(t, cleanup) {
			for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
				operation := "network.create"
				if cleanup {
					operation = "resource.destroy"
				}
				t.Run(operation+"/"+step.action+"/"+fmt.Sprint(step.nth)+"/"+fault, func(t *testing.T) {
					f, api := newNetworkEC2(t)
					root := t.TempDir()
					prior := ""
					var resource lc.Resource
					if cleanup {
						p := &Provider{Journal: journal.Journal{Root: root}}
						r, err := p.network(context.Background(), api, twoNetworks())
						if err != nil {
							t.Fatal(err)
						}
						resource = r.Resource
						prior = filepath.Join(t.TempDir(), "prior")
						b, _ := json.Marshal(resource)
						if err = os.WriteFile(prior, b, 0600); err != nil {
							t.Fatal(err)
						}
					}
					var mu sync.Mutex
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						response, err := f.do(r)
						if err != nil {
							w.WriteHeader(500)
							return
						}
						defer response.Body.Close()
						for key, values := range response.Header {
							w.Header()[key] = values
						}
						w.WriteHeader(response.StatusCode)
						_, _ = io.Copy(w, response.Body)
					}))
					defer server.Close()
					marker := filepath.Join(t.TempDir(), "hit")
					selector, _ := json.Marshal(map[string]any{"operation": "aws/" + operation, "step": step.action, "nth": step.nth, "fault": fault, "marker": marker})
					child := exec.Command(os.Args[0], "-test.run=^TestAWSNativeSIGKILLMatrix$")
					child.Env = append(os.Environ(), "LC_NATIVE_CHILD_ENDPOINT="+server.URL, "LC_NATIVE_CHILD_PRIOR="+prior, "LC_NATIVE_CHILD_JOURNAL="+root, "LABCENTRAL_FAULT="+string(selector))
					output, err := child.CombinedOutput()
					var status *exec.ExitError
					if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
						t.Fatalf("not SIGKILL: %v %s", err, output)
					}
					if _, err = os.Stat(marker); err != nil {
						t.Fatal("selected boundary not reached", err)
					}
					mu.Lock()
					defer mu.Unlock()
					p := &Provider{Journal: journal.Journal{Root: root}}
					var result lc.Result
					if cleanup {
						c := twoNetworks()
						c.Prior = resource
						result, err = p.destroyNetwork(context.Background(), api, c)
					} else {
						result, err = p.network(context.Background(), api, twoNetworks())
					}
					if err != nil {
						if cleanup {
							t.Fatal(err)
						}
						if !strings.Contains(err.Error(), "recovery_required") {
							t.Fatal(err)
						}
						// Uncertain pre-create intent remains durable and cannot be silently retried.
						count := len(f.objects)
						if _, retry := p.network(context.Background(), api, twoNetworks()); retry == nil || len(f.objects) != count {
							t.Fatal("unknown create replayed", retry)
						}
						return
					}
					c := twoNetworks()
					if !cleanup {
						c.Prior = result.Resource
						result, err = p.destroyNetwork(context.Background(), api, c)
					}
					if err != nil || !result.Absent || len(f.objects) != 0 {
						t.Fatal("native cleanup", result, err, f.objects)
					}
				})
			}
		}
	}
}
