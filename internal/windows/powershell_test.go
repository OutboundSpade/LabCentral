package windows

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/internal/pscheck"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeCommand(t *testing.T, command string) string {
	t.Helper()
	parts := strings.Split(command, " -EncodedCommand ")
	if len(parts) != 2 {
		t.Fatal("unencoded command")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(parts[1], "; exit $LASTEXITCODE"))
	if err != nil || len(b)%2 != 0 {
		t.Fatal("invalid UTF16", err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}
func TestPowerShellPayloadIsData(t *testing.T) {
	payload := []byte(`{"script":"escape'; $(Get-Secret); 💻.ps1"}`)
	command := PayloadCommand("Write-Output $p.script", payload)
	program := decodeCommand(t, command)
	encoded := base64.StdEncoding.EncodeToString(payload)
	if !strings.Contains(program, encoded) || strings.Contains(program, "Get-Secret") {
		t.Fatal("payload interpolated into program")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != string(payload) {
		t.Fatal("payload did not round trip")
	}
	if len(Command(StepLauncher)) >= 32767 {
		t.Fatal("fixed launcher exceeds Windows command-line limit")
	}
}
func TestLauncherLockTimeoutAndProcessOwnership(t *testing.T) {
	// The guest-native harness (enabled by LABCENTRAL_TEST_WINDOWS_POWERSHELL)
	// exercises these operations on Windows; these checks also prevent removing
	// its critical ownership and fail-closed branches on a Linux development host.
	for _, part := range []string{
		"[IO.File]::Open", "'OpenOrCreate', 'ReadWrite', 'None'", "AddSeconds(5)", "if (!$lock) {exit 43}",
		"AssignProcessToJobObject($job,$proc.Handle)", "CloseHandle($job)", "Marshal.WriteInt32(p,16,0x2000)",
		"LABCENTRAL_GATE", "LABCENTRAL_STEP_DIR", "1048576", "$stdout.IsFaulted", "AddSeconds([int]$p.timeoutSeconds)",
		"$proc.ExitCode -ne 0", "exit 42", "exit 43", "Remove-Item -Recurse -Force -LiteralPath $dir", "$lock.Dispose()",
	} {
		if !strings.Contains(StepLauncher, part) {
			t.Fatal("missing launcher guarantee", part)
		}
	}
	assign, release := strings.Index(StepLauncher, "AssignProcessToJobObject($job,$proc.Handle)"), strings.Index(StepLauncher, "[IO.File]::WriteAllText($gate")
	if assign < 0 || release <= assign {
		t.Fatal("author code can run before process ownership")
	}
}

func TestLauncherPowerShellSyntax(t *testing.T) { pscheck.Syntax(t, StepLauncher) }

func TestSSHCommandPropagatesPowerShellExitStatus(t *testing.T) {
	if !strings.HasSuffix(Command("exit 42"), "; exit $LASTEXITCODE") {
		t.Fatal("SSH default shell would collapse failure classification")
	}
}
func TestNativeWindowsDefaultShellExitStatus(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires explicitly configured Windows guest")
	}
	for _, code := range []int{0, 42, 43} {
		cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", Command(fmt.Sprintf("exit %d", code)))
		err := cmd.Run()
		got := 0
		if err != nil {
			if ex, ok := err.(*exec.ExitError); ok {
				got = ex.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if got != code {
			t.Fatalf("default shell status %d, want %d", got, code)
		}
	}
}
