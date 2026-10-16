//go:build faultinject

package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestSSHGuestResponseLossNeverReplaysCommand(t *testing.T) {
	var effects atomic.Int32
	server := sshtest.New(t, nil, func(string) string { effects.Add(1); return "mutated" })
	private, _ := sshtest.Key(t)
	client, err := Dial(context.Background(), server.Address, "lab", private, server.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	selector, _ := json.Marshal(map[string]any{"operation": "aws/access.open", "step": "ssh.exec", "fault": "response-loss", "marker": filepath.Join(t.TempDir(), "hit")})
	t.Setenv("LABCENTRAL_FAULT", string(selector))
	result, err := Run(faultinject.WithOperation(context.Background(), "aws", "access.open"), client, "fixture mutation")
	if result != nil || !errors.Is(err, context.DeadlineExceeded) || effects.Load() != 1 {
		t.Fatal("guest command replay or false result", result, err, effects.Load())
	}
}
func TestSSHGuestSIGKILLBoundaries(t *testing.T) {
	if address := os.Getenv("LC_SSH_CHILD_ADDRESS"); address != "" {
		private, err := os.ReadFile(os.Getenv("LC_SSH_CHILD_KEY"))
		if err != nil {
			t.Fatal(err)
		}
		ctx := faultinject.WithOperation(context.Background(), "proxmox", "access.open")
		client, err := Dial(ctx, address, "lab", private, os.Getenv("LC_SSH_CHILD_HOST"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = Run(ctx, client, "fixture mutation")
		os.Exit(3)
	}
	for _, fault := range []string{"kill-before", "kill-during", "kill-after"} {
		t.Run(fault, func(t *testing.T) {
			var effects atomic.Int32
			server := sshtest.New(t, nil, func(string) string { effects.Add(1); return "mutated" })
			private, _ := sshtest.Key(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "key")
			if err := os.WriteFile(path, private, 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "hit")
			selector, _ := json.Marshal(map[string]any{"operation": "proxmox/access.open", "step": "ssh.exec", "fault": fault, "marker": marker})
			child := exec.Command(os.Args[0], "-test.run=^TestSSHGuestSIGKILLBoundaries$")
			child.Env = append(os.Environ(), "LC_SSH_CHILD_ADDRESS="+server.Address, "LC_SSH_CHILD_KEY="+path, "LC_SSH_CHILD_HOST="+server.HostKey, "LABCENTRAL_FAULT="+string(selector))
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("not SIGKILL: %v %s", err, output)
			}
			if _, err = os.Stat(marker); err != nil {
				t.Fatal(err)
			}
			if fault == "kill-before" && effects.Load() != 0 {
				t.Fatal("pre-call effect")
			}
			if fault == "kill-after" && effects.Load() != 1 {
				t.Fatal("not killed after real effect")
			}
		})
	}
}
