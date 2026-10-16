//go:build faultinject

package proxmox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func TestProxmoxNativeSIGKILLMatrix(t *testing.T) {
	if raw := os.Getenv("LC_PVE_CHILD_COMMAND"); raw != "" {
		var cmd lc.Command
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &cmd); err != nil {
			t.Fatal(err)
		}
		client, err := NewClient(cmd.Environment)
		if err != nil {
			t.Fatal(err)
		}
		p := &Provider{Journal: journal.Journal{Root: os.Getenv("LC_PVE_CHILD_JOURNAL")}}
		ctx := faultinject.WithOperation(context.Background(), "proxmox", cmd.Action)
		if cmd.Action == "resource.destroy" {
			_, _ = p.destroy(ctx, client, cmd)
		} else {
			_, _ = pveRun(ctx, p, client, cmd, cmd.Action == "gateway.create")
		}
		os.Exit(3)
	}
	for _, gateway := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			operation := "machine.create"
			if gateway {
				operation = "gateway.create"
			}
			if cleanup {
				operation = "resource.destroy"
			}
			for _, step := range pveBoundaries(t, gateway, cleanup) {
				for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
					t.Run(fmt.Sprintf("%s/%t/%s/%d/%s", operation, gateway, step.step, step.nth, fault), func(t *testing.T) {
						f, client, cmd := pveMatrixFixture(t, gateway)
						root := t.TempDir()
						p := &Provider{Journal: journal.Journal{Root: root}}
						if cleanup {
							_, err := pveRun(context.Background(), p, client, cmd, gateway)
							if (err != nil) != gateway {
								t.Fatal(err)
							}
							kind := "machine"
							if gateway {
								kind = "gateway"
							}
							cmd.Action = "resource.destroy"
							cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
						}
						marker := filepath.Join(t.TempDir(), "hit")
						selector, _ := json.Marshal(map[string]any{"operation": "proxmox/" + operation, "step": step.step, "nth": step.nth, "fault": fault, "marker": marker})
						body, _ := json.Marshal(cmd)
						child := exec.Command(os.Args[0], "-test.run=^TestProxmoxNativeSIGKILLMatrix$")
						child.Env = append(os.Environ(), "LC_PVE_CHILD_COMMAND="+base64.StdEncoding.EncodeToString(body), "LC_PVE_CHILD_JOURNAL="+root, "LABCENTRAL_FAULT="+string(selector))
						output, err := child.CombinedOutput()
						var status *exec.ExitError
						if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
							t.Fatalf("not SIGKILL: %v %s", err, output)
						}
						if _, err = os.Stat(marker); err != nil {
							t.Fatal("boundary not reached", err)
						}
						p = &Provider{Journal: journal.Journal{Root: root}}
						if !cleanup {
							_, err = pveRun(context.Background(), p, client, cmd, gateway)
							if err != nil && !gateway && !strings.HasSuffix(step.step, "/clone") {
								t.Fatal("recovery failed", err)
							}
						}
						kind := "machine"
						if gateway {
							kind = "gateway"
						}
						cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
						result, err := p.destroy(context.Background(), client, cmd)
						f.mu.Lock()
						defer f.mu.Unlock()
						if err != nil || !result.Absent || len(f.vms) != 2 || f.vms[2050] == nil || f.vms[2100] == nil {
							t.Fatal("inventory not clean or foreign resource lost", result, err, f.vms)
						}
					})
				}
			}
		}
	}
}
