package windows

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run only in an explicitly configured disposable Windows guest, as Administrator.
// This harness is intentionally skipped on Linux; syntax/fakes are not execution evidence.
func TestNativeWindowsStepFailureTimeoutLockAndCleanup(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires an explicitly configured disposable Windows guest")
	}
	run := func(script string, timeout int) *exec.Cmd {
		payload, _ := json.Marshal(struct {
			Files []struct {
				Path    string `json:"path"`
				Content []byte `json:"content"`
			} `json:"files"`
			Script  string `json:"script"`
			Timeout int    `json:"timeoutSeconds"`
		}{[]struct {
			Path    string `json:"path"`
			Content []byte `json:"content"`
		}{{"check.ps1", []byte(script)}}, "check.ps1", timeout})
		program := "$p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd())) | ConvertFrom-Json\n" + StepLauncher
		args := strings.Fields(ProcessCommand(program))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, shell, args[1:]...)
		cmd.Stdin = bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(payload)))
		return cmd
	}
	exit := func(cmd *exec.Cmd) int {
		t.Helper()
		err := cmd.Run()
		if err == nil {
			return 0
		}
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		t.Fatal(err)
		return -1
	}
	if got := exit(run("exit 0", 5)); got != 0 {
		t.Fatal("success", got)
	}
	if got := exit(run("exit 7", 5)); got != 42 {
		t.Fatal("failure classification", got)
	}
	observation := filepath.Join(t.TempDir(), "step.txt")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	childPID := filepath.Join(t.TempDir(), "child-pid.txt")
	script := fmt.Sprintf("[IO.File]::WriteAllText(%s,$env:LABCENTRAL_STEP_DIR); $child=Start-Process powershell.exe -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 60') -PassThru; [IO.File]::WriteAllText(%s,[string]$child.Id); Start-Sleep -Seconds 60", quote(observation), quote(childPID))
	started := time.Now()
	if got := exit(run(script, 1)); got != 43 || time.Since(started) > 10*time.Second {
		t.Fatal("timeout did not terminate", got)
	}
	dir, err := os.ReadFile(observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(string(dir)); !os.IsNotExist(err) {
		t.Fatal("temporary directory retained", err)
	}
	pidRaw, err := os.ReadFile(childPID)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	check := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", "if (Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction SilentlyContinue) {exit 1}")
	if err = check.Run(); err != nil {
		t.Fatal("author descendant survived timeout", err)
	}
	if got := exit(run("[Console]::Write(('x' * 2097152)); Start-Sleep -Seconds 60", 5)); got != 43 {
		t.Fatal("output bound", got)
	}
	// Hold the actual exclusive file handle in one launcher. A second launcher
	// must fail before author execution, then the first times out and releases it.
	_ = os.Remove(observation)
	first := run(script, 10)
	if err = first.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(observation); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first launcher did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := exit(run("exit 0", 5)); got != 43 {
		t.Fatal("overlapping launcher entered guest", got)
	}
	if err = first.Wait(); err == nil {
		t.Fatal("timeout unexpectedly passed")
	}
	if got := exit(run("exit 0", 5)); got != 0 {
		t.Fatal("lock was not released", got)
	}
}
