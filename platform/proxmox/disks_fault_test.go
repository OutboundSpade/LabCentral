//go:build faultinject

package proxmox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func TestProxmoxDiskCleanupSIGKILLMatrix(t *testing.T) {
	if raw := os.Getenv("LC_DISK_COMMAND"); raw != "" {
		b, _ := base64.StdEncoding.DecodeString(raw)
		var cmd lc.Command
		if err := json.Unmarshal(b, &cmd); err != nil {
			t.Fatal(err)
		}
		c, err := NewClient(cmd.Environment)
		if err != nil {
			t.Fatal(err)
		}
		p := &Provider{Journal: journal.Journal{Root: os.Getenv("LC_DISK_ROOT")}}
		_, _ = p.destroy(faultinject.WithOperation(context.Background(), "proxmox", "resource.destroy"), c, cmd)
		os.Exit(3)
	}
	for _, step := range []string{"POST/qemu/{id}/status/stop", "DELETE/qemu/{id}", "DELETE/storage/{storage}/content/{volume}"} {
		for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
			t.Run(step+"/"+fault, func(t *testing.T) {
				f, c, cmd := diskFixture(t)
				root := t.TempDir()
				marker := filepath.Join(root, "hit")
				b, _ := json.Marshal(cmd)
				selected, _ := json.Marshal(map[string]any{"operation": "proxmox/resource.destroy", "step": step, "fault": fault, "marker": marker})
				child := exec.Command(os.Args[0], "-test.run=^TestProxmoxDiskCleanupSIGKILLMatrix$")
				child.Env = append(os.Environ(), "LC_DISK_COMMAND="+base64.StdEncoding.EncodeToString(b), "LC_DISK_ROOT="+root, "LABCENTRAL_FAULT="+string(selected))
				output, err := child.CombinedOutput()
				var status *exec.ExitError
				if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatal("not selected SIGKILL", err, string(output))
				}
				if _, err = os.Stat(marker); err != nil {
					t.Fatal(err)
				}
				p := &Provider{Journal: journal.Journal{Root: root}}
				result, err := p.destroy(context.Background(), c, cmd)
				if err != nil || !result.Absent {
					t.Fatal(result, err)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.vm || !f.foreignVM || len(f.volumes) != 1 || f.volumes[foreignTestDisk] != 2050 {
					t.Fatal("disk leak or foreign deletion", f.volumes)
				}
			})
		}
	}
}
