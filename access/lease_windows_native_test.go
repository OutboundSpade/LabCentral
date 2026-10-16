//go:build windows

package access

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

// Run only on an explicitly selected disposable Windows lab with no active
// desktop. This tests the real registry/mutex wrapper, not a PowerShell mock.
func TestWindowsDesktopLeaseNative(t *testing.T) {
	if os.Getenv("LABCENTRAL_TEST_WINDOWS_LEASE") != "1" {
		t.Skip("requires an explicitly selected disposable Windows guest")
	}
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
	c.Access.Generation = strings.Repeat("c", 24)
	if out, err := run("revoke", "Write-Output 'never-started generation revoked'"); err != nil {
		t.Fatal("retired predecessor blocked recovery before grant submission", err, out)
	}
	if out, err := run("begin", "Write-Output 'late never-started grant'"); err == nil || !strings.Contains(out, "revoked") {
		t.Fatal("never-started generation could grant after recovery", err, out)
	}
}

// Select only on a fresh disposable Windows desktop guest. This exercises exactly
// the grant/revoke composition used by openDCV and closeWindowsDCV, including the
// payload, account mutation, lease mutex and SSH's PowerShell command grammar.
func TestWindowsDesktopLeaseActualGrantAndRevoke(t *testing.T) {
	if os.Getenv("LABCENTRAL_TEST_WINDOWS_LEASE") != "1" {
		t.Skip("requires an explicitly selected disposable Windows guest")
	}
	c := leaseFixtureCommand()
	c.Machine.OS = "windows"
	c.Access.RetainWorkspace = true
	run := func(command string) string {
		t.Helper()
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
		if err != nil {
			t.Fatalf("native platform command: %v: %s", err, out)
		}
		return string(out)
	}
	run(windows.Command(`$ErrorActionPreference='Stop'; if ((Get-LocalUser lab).Enabled) {throw 'test requires an inactive console account'}; if (Test-Path 'HKLM:\SOFTWARE\LabCentral\DesktopLease') {throw 'test requires no existing lease'}; if ((Get-ItemProperty 'HKLM:\SOFTWARE\LabCentral\Desktop' -Name Owner -ErrorAction SilentlyContinue).Owner) {throw 'test requires no existing desktop owner'}`))
	defer func() {
		// Restore only the exact fixture's own registry records after disabling it.
		_, _ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", leaseCommand(c, "revoke", windowsDesktopRevokeProgram(c, false))).CombinedOutput()
		_, _ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windows.Command(`$marker='`+dcvMarker(c)+`'; $desktop='HKLM:\SOFTWARE\LabCentral\Desktop'; if ((Get-ItemProperty $desktop -Name Owner -ErrorAction SilentlyContinue).Owner -eq $marker) {Remove-ItemProperty $desktop -Name Owner}; $lease='HKLM:\SOFTWARE\LabCentral\DesktopLease'; if ((Get-ItemProperty $lease -Name Owner -ErrorAction SilentlyContinue).Owner -eq $marker) {Remove-Item -Recurse -Force $lease}`)).CombinedOutput()
	}()
	run(leaseCommand(c, "begin", windowsDesktopProgram(c, desktopPassword("windows"))))
	run(windows.Command(`if (!(Get-LocalUser lab).Enabled) {throw 'grant did not enable account'}`))
	run(leaseCommand(c, "revoke", windowsDesktopRevokeProgram(c, false)))
	run(windows.Command(`if ((Get-LocalUser lab).Enabled) {throw 'revoke left account enabled'}`))
}
