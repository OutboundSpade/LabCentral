//go:build faultinject

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func TestAWSBuilderSIGKILLMatrix(t *testing.T) {
	if action := os.Getenv("LC_BUILD_CHILD_ACTION"); action != "" {
		endpoint := os.Getenv("LC_BUILD_ENDPOINT")
		api := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider("example", "example", ""), RetryMaxAttempts: 1})
		g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: os.Getenv("LC_BUILD_ROOT")}}, api: api, c: lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "server"}}}
		body := []byte("Action=" + action + "&ClientToken=example-token&Name=labcentral-owner")
		ctx := faultinject.WithOperation(context.Background(), "aws", "image.build")
		_, _ = g.do(ctx, &http.Request{}, body, func(ctx context.Context) (nativeproxy.Reply, error) {
			return faultinject.Call(ctx, "packer."+action, func(ctx context.Context) (nativeproxy.Reply, error) {
				req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return nativeproxy.Reply{}, err
				}
				defer res.Body.Close()
				b, err := io.ReadAll(res.Body)
				if err != nil {
					return nativeproxy.Reply{}, err
				}
				reply := nativeproxy.Reply{Status: res.StatusCode, Header: res.Header, Body: b}
				if err = g.after(ctx, nil, body, reply); err != nil {
					return nativeproxy.Reply{}, err
				}
				return reply, nil
			})
		})
		os.Exit(3)
	}
	for _, action := range []string{"RunInstances", "CreateImage"} {
		for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				inventory, api := cleanupMatrixFixture(t)
				root := t.TempDir()
				marker := filepath.Join(root, "hit")
				id := "i-test"
				if action == "CreateImage" {
					id = "ami-test"
				}
				inventory.mu.Lock()
				inventory.exists[id] = false
				inventory.mu.Unlock()
				selector, _ := json.Marshal(map[string]any{"operation": "aws/image.build", "step": "packer." + action, "fault": fault, "marker": marker})
				cmd := exec.Command(os.Args[0], "-test.run=^TestAWSBuilderSIGKILLMatrix$")
				cmd.Env = append(os.Environ(), "LC_BUILD_CHILD_ACTION="+action, "LC_BUILD_ENDPOINT="+aw.ToString(api.Options().BaseEndpoint), "LC_BUILD_ROOT="+root, "LABCENTRAL_FAULT="+string(selector))
				out, err := cmd.CombinedOutput()
				var status *exec.ExitError
				if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatal("not selected SIGKILL", err, string(out))
				}
				if _, err = os.Stat(marker); err != nil {
					t.Fatal(err)
				}
				g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "server"}}}
				body := []byte("Action=" + action + "&ClientToken=example-token&Name=labcentral-owner")
				reply, err := g.do(context.Background(), &http.Request{}, body, func(context.Context) (nativeproxy.Reply, error) {
					t.Fatal("interrupted native create replayed")
					return nativeproxy.Reply{}, nil
				})
				inventory.mu.Lock()
				exists := inventory.exists[id]
				inventory.mu.Unlock()
				if !exists {
					if err == nil || !strings.Contains(err.Error(), "recovery_required") {
						t.Fatal("unknown absence lost intent", reply, err)
					}
					return
				}
				if err != nil || reply.Status != 409 {
					t.Fatal(reply, err)
				}
				c := cleanupMatrixCommand("machine")
				if action == "CreateImage" {
					c = cleanupMatrixCommand("image")
				}
				if result, e := g.p.destroy(context.Background(), api, c); e != nil || !result.Absent {
					t.Fatal(result, e)
				}
				inventory.mu.Lock()
				defer inventory.mu.Unlock()
				if action == "CreateImage" && inventory.exists[id] {
					t.Fatal("captured image leaked")
				}
				if action == "RunInstances" && !inventory.terminated {
					t.Fatal("builder leaked")
				}
			})
		}
	}
}
