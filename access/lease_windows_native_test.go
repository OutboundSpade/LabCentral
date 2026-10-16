//go:build windows

package access

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

// Run only on an explicitly selected disposable Windows lab with no active
// desktop. This tests the real registry/mutex wrapper, not a PowerShell mock.
func TestWindowsDesktopLeaseNative(t *testing.T) {
	c := leaseFixtureCommand()
	c.Machine.OS = "windows"
	run := func(mode, script string) (string, error) {
		command := leaseCommand(c, mode, windows.Command(script))
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
		return string(out), err
	}
	marker := dcvMarker(c)
	defer func() {
		script := `$path='HKLM:\SOFTWARE\LabCentral\DesktopLease'; if ((Get-ItemProperty $path -Name Owner -ErrorAction SilentlyContinue).Owner -eq '` + marker + `') {Remove-Item -Recurse -Force $path}`
		_, _ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windows.Command(script)).CombinedOutput()
	}()
	if out, err := run("revoke", "Write-Output 'absence recorded'"); err != nil {
		t.Fatal(err, out)
	}
	if out, err := run("begin", "Write-Output 'late mutation'"); err == nil || !strings.Contains(out, "revoked") {
		t.Fatal("late grant accepted", err, out)
	}
	c.Access.Generation = strings.Repeat("b", 24)
	if out, err := run("begin", "Write-Output 'new generation'"); err != nil || !strings.Contains(out, "new generation") {
		t.Fatal(err, out)
	}
	c.Access.Generation = strings.Repeat("a", 24)
	if out, err := run("revoke", "Write-Output 'foreign mutation'"); err == nil || !strings.Contains(out, "generation differs") {
		t.Fatal("stale revoke accepted", err, out)
	}
	c.Access.Generation = strings.Repeat("b", 24)
	if out, err := run("revoke", "Write-Output 'new generation revoked'"); err != nil {
		t.Fatal(err, out)
	}
}
