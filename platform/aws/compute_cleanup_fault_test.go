//go:build faultinject

package aws

import (
	"context"
	"encoding/base64"
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

func awsMutationChild(t *testing.T, endpoint, root, mode string, c lc.Command, step boundary, fault string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "hit")
	selector, _ := json.Marshal(map[string]any{"operation": "aws/" + c.Action, "step": step.action, "nth": step.nth, "fault": fault, "marker": marker})
	data, _ := json.Marshal(c)
	child := exec.Command(os.Args[0], "-test.run=^TestAWSComputeCleanupSIGKILLMatrix$")
	child.Env = append(os.Environ(), "LC_AWS_MUTATION_CHILD="+mode, "LC_AWS_MUTATION_ENDPOINT="+endpoint, "LC_AWS_MUTATION_ROOT="+root, "LC_AWS_MUTATION_COMMAND="+base64.StdEncoding.EncodeToString(data), "LABCENTRAL_FAULT="+string(selector))
	output, err := child.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v %s", err, output)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("boundary not reached", err)
	}
}
func TestAWSComputeCleanupSIGKILLMatrix(t *testing.T) {
	if mode := os.Getenv("LC_AWS_MUTATION_CHILD"); mode != "" {
		data, err := base64.StdEncoding.DecodeString(os.Getenv("LC_AWS_MUTATION_COMMAND"))
		if err != nil {
			t.Fatal(err)
		}
		var c lc.Command
		if err = json.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}
		api := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(os.Getenv("LC_AWS_MUTATION_ENDPOINT")), Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1}, faultOptions)
		p := &Provider{Journal: journal.Journal{Root: os.Getenv("LC_AWS_MUTATION_ROOT")}}
		ctx := faultinject.WithOperation(context.Background(), "aws", c.Action)
		switch mode {
		case "machine", "gateway":
			_, _ = computeMatrixRun(ctx, p, api, c, mode == "gateway")
		case "key":
			_ = p.cleanupBuild(ctx, api, c)
		default:
			_, _ = p.destroy(ctx, api, c)
		}
		os.Exit(3)
	}
	for _, gateway := range []bool{false, true} {
		for _, step := range computeBoundaries(t, gateway) {
			for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
				t.Run(fmt.Sprintf("compute/%t/%s/%d/%s", gateway, step.action, step.nth, fault), func(t *testing.T) {
					f, api, p, c := computeMatrixFixture(t, gateway)
					var mu sync.Mutex
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						res, err := f.do(r)
						if err != nil {
							w.WriteHeader(500)
							return
						}
						defer res.Body.Close()
						for key, values := range res.Header {
							w.Header()[key] = values
						}
						w.WriteHeader(res.StatusCode)
						io.Copy(w, res.Body)
					}))
					defer server.Close()
					mode := "machine"
					if gateway {
						mode = "gateway"
					}
					awsMutationChild(t, server.URL, p.Journal.Root, mode, c, step, fault)
					mu.Lock()
					defer mu.Unlock()
					p = &Provider{Journal: journal.Journal{Root: p.Journal.Root}}
					c.Recovery = true
					result, err := computeMatrixRun(context.Background(), p, api, c, gateway)
					if err != nil {
						if step.action != "RunInstances" || !strings.Contains(err.Error(), "recovery_required") {
							t.Fatal(err)
						}
						return
					}
					c.Prior = result.Resource
					if result, err = p.destroy(context.Background(), api, c); err != nil || !result.Absent {
						t.Fatal(result, err)
					}
					c.Prior = c.Network
					if result, err = p.destroyNetwork(context.Background(), api, c); err != nil || !result.Absent || len(f.objects) != 0 {
						t.Fatal("native cleanup", result, err, f.objects)
					}
				})
			}
		}
	}
	for _, step := range []string{"TerminateInstances", "DeleteVolume", "DeregisterImage", "DeleteSnapshot", "DeleteKeyPair"} {
		for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
			t.Run("cleanup/"+step+"/"+fault, func(t *testing.T) {
				f, api := cleanupMatrixFixture(t)
				c := cleanupMatrixCommand("machine")
				c.Action = "resource.destroy"
				mode := "cleanup"
				if step == "DeregisterImage" || step == "DeleteSnapshot" {
					c = cleanupMatrixCommand("image")
					c.Action = "resource.destroy"
				}
				if step == "DeleteKeyPair" {
					c = lc.Command{Action: "image.build", Owner: "owner", Image: lc.PlannedImage{Name: "server"}}
					mode = "key"
				}
				endpoint := aw.ToString(api.Options().BaseEndpoint)
				root := t.TempDir()
				awsMutationChild(t, endpoint, root, mode, c, boundary{step, 1}, fault)
				p := &Provider{Journal: journal.Journal{Root: root}}
				if mode == "key" {
					if err := p.cleanupBuild(context.Background(), api, c); err != nil {
						t.Fatal(err)
					}
				} else {
					if result, err := p.destroy(context.Background(), api, c); err != nil || !result.Absent {
						t.Fatal(result, err)
					}
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if c.Prior.Kind == "image" {
					if f.exists["ami-test"] || f.exists["snap-test"] {
						t.Fatal("native image leak")
					}
				} else if !f.terminated || f.exists["vol-test"] {
					t.Fatal("native instance/volume leak")
				}
				if mode == "key" && f.exists["key-test"] {
					t.Fatal("native key leak")
				}
			})
		}
	}
}
