//go:build faultinject

package proxmox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
)

func TestProxmoxBuilderSIGKILLMatrix(t *testing.T) {
	if action := os.Getenv("LC_BUILD_CHILD_ACTION"); action != "" {
		b, _ := base64.StdEncoding.DecodeString(os.Getenv("LC_BUILD_COMMAND"))
		var c lc.Command
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		api, err := NewClient(c.Environment)
		if err != nil {
			t.Fatal(err)
		}
		g := &proxmoxBuildGuard{p: &Provider{Journal: journal.Journal{Root: os.Getenv("LC_BUILD_ROOT")}}, api: api, c: c}
		path := "/api2/json/nodes/node/qemu/2000/template"
		body := []byte(`{}`)
		if action == "clone" {
			path = "/api2/json/nodes/node/qemu/2100/clone"
			body, _ = json.Marshal(map[string]any{"newid": 2000, "description": marker(c.Owner, c.Image.Name)})
		}
		req := &http.Request{Method: "POST", URL: &url.URL{Path: path}}
		ctx := faultinject.WithOperation(context.Background(), "proxmox", "image.build")
		_, _ = g.do(ctx, req, body, func(ctx context.Context) (nativeproxy.Reply, error) {
			return faultinject.Call(ctx, "packer."+action, func(ctx context.Context) (nativeproxy.Reply, error) {
				r, _ := http.NewRequestWithContext(ctx, "POST", api.URL+strings.TrimPrefix(path, "/api2/json"), strings.NewReader(string(body)))
				res, err := api.HTTP.Do(r)
				if err != nil {
					return nativeproxy.Reply{}, err
				}
				defer res.Body.Close()
				b, err := io.ReadAll(res.Body)
				return nativeproxy.Reply{Status: res.StatusCode, Header: res.Header, Body: b}, err
			})
		})
		os.Exit(3)
	}
	for _, action := range []string{"clone", "template"} {
		for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				inventory, api, c := pveMatrixFixture(t, false)
				c.Image.Name = "image"
				root := t.TempDir()
				markerPath := filepath.Join(root, "hit")
				inventory.mu.Lock()
				if action == "template" {
					inventory.vms[2000] = &matrixVM{config: map[string]any{"description": marker(c.Owner, c.Image.Name)}}
				}
				inventory.mu.Unlock()
				selector, _ := json.Marshal(map[string]any{"operation": "proxmox/image.build", "step": "packer." + action, "fault": fault, "marker": markerPath})
				b, _ := json.Marshal(c)
				child := exec.Command(os.Args[0], "-test.run=^TestProxmoxBuilderSIGKILLMatrix$")
				child.Env = append(os.Environ(), "LC_BUILD_CHILD_ACTION="+action, "LC_BUILD_COMMAND="+base64.StdEncoding.EncodeToString(b), "LC_BUILD_ROOT="+root, "LABCENTRAL_FAULT="+string(selector))
				out, err := child.CombinedOutput()
				var status *exec.ExitError
				if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatal("not selected SIGKILL", err, string(out))
				}
				if _, err = os.Stat(markerPath); err != nil {
					t.Fatal(err)
				}
				g := &proxmoxBuildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: c}
				path := "/api2/json/nodes/node/qemu/2000/template"
				body := []byte(`{}`)
				if action == "clone" {
					path = "/api2/json/nodes/node/qemu/2100/clone"
					body, _ = json.Marshal(map[string]any{"newid": 2000, "description": marker(c.Owner, c.Image.Name)})
				}
				reply, err := g.do(context.Background(), &http.Request{Method: "POST", URL: &url.URL{Path: path}}, body, func(context.Context) (nativeproxy.Reply, error) {
					t.Fatal("interrupted native create replayed")
					return nativeproxy.Reply{}, nil
				})
				inventory.mu.Lock()
				vm := inventory.vms[2000]
				done := vm != nil && (action == "clone" || vm.config["template"] == 1)
				inventory.mu.Unlock()
				if !done {
					if err == nil || !strings.Contains(err.Error(), "recovery_required") {
						t.Fatal("unknown outcome discarded", reply, err)
					}
				} else if err != nil || reply.Status != 409 {
					t.Fatal(reply, err)
				}
				// The known owned VM may always be cleaned directly by its exact receipt.
				if vm != nil {
					c.Prior = lc.Resource{Kind: "machine", Name: c.Image.Name, ID: "2000", Owner: c.Owner}
					if result, e := g.p.destroy(context.Background(), api, c); e != nil || !result.Absent {
						t.Fatal(result, e)
					}
				}
				inventory.mu.Lock()
				defer inventory.mu.Unlock()
				if inventory.vms[2000] != nil || inventory.vms[2050] == nil || inventory.vms[2100] == nil {
					t.Fatal("leak or foreign deletion")
				}
			})
		}
	}
}
