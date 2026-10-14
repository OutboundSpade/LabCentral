package remote

import (
	"os/exec"
	"testing"
)

// The readiness command accepts cloud-init's "done with recoverable errors" status (2)
// and rejects a hard error (1).
func TestReadinessCommandAcceptsDegradedCloudInit(t *testing.T) {
	for code, ready := range map[string]bool{"0": true, "2": true, "1": false} {
		dir := t.TempDir()
		script := "cloud-init() { return " + code + "; }; test() { return 0; }; " + readinessCommand
		err := exec.Command("bash", "-c", script).Run()
		if (err == nil) != ready {
			t.Fatalf("cloud-init exit %s: ready=%v, err=%v (%s)", code, ready, err, dir)
		}
	}
}
