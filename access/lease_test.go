package access

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/pscheck"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

func linuxLeaseFixture(t *testing.T, c lc.Command, mode, program string) (*exec.Cmd, string) {
	t.Helper()
	dir := t.TempDir()
	return linuxLeaseIn(t, dir, c, mode, program), dir
}
func linuxLeaseIn(t *testing.T, dir string, c lc.Command, mode, program string) *exec.Cmd {
	t.Helper()
	script := linuxLease(c, mode, program)
	script = strings.ReplaceAll(script, "/var/lib/labcentral/desktop-leases", filepath.Join(dir, "leases"))
	script = strings.ReplaceAll(script, "/run/lock/labcentral-desktop-", filepath.Join(dir, "lock-"))
	// Only the platform lease wrapper runs locally. The payloads below modify
	// fixture files, never local accounts, credentials or desktop services.
	return exec.Command("bash", "-c", `sudo(){ "$@"; }; `+script)
}
func leaseFixtureCommand() lc.Command {
	return lc.Command{Owner: "example-owner", Operation: "example-workspace", Access: lc.AccessOptions{Generation: strings.Repeat("a", 24)}}
}
func TestDesktopLeaseRejectsLateGrantAfterConfirmedAbsence(t *testing.T) {
	c := leaseFixtureCommand()
	dir := t.TempDir()
	account := filepath.Join(dir, "account")
	if out, err := linuxLeaseIn(t, dir, c, "revoke", "test ! -e "+shellQuote(account)).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// Reconstruct the command after the original controller/SSH connection is gone.
	data, _ := json.Marshal(c)
	var delayed lc.Command
	if err := json.Unmarshal(data, &delayed); err != nil {
		t.Fatal(err)
	}
	if out, err := linuxLeaseIn(t, dir, delayed, "begin", "touch "+shellQuote(account)).CombinedOutput(); err == nil || !strings.Contains(string(out), "revoked") {
		t.Fatal("late guest mutation accepted", err, string(out))
	}
	if _, err := os.Stat(account); !os.IsNotExist(err) {
		t.Fatal("late account leak", err)
	}
	newer := c
	newer.Access.Generation = strings.Repeat("b", 24)
	if out, err := linuxLeaseIn(t, dir, newer, "begin", "touch "+shellQuote(account)).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := linuxLeaseIn(t, dir, c, "revoke", "rm "+shellQuote(account)).CombinedOutput(); err == nil || !strings.Contains(string(out), "generation differs") {
		t.Fatal("stale revocation crossed generations", err, string(out))
	}
	if _, err := os.Stat(account); err != nil {
		t.Fatal("new generation removed", err)
	}
	if out, err := linuxLeaseIn(t, dir, newer, "revoke", "rm "+shellQuote(account)).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
}
func TestDesktopLeaseSerializesGuestEffectsAfterControllerLoss(t *testing.T) {
	c := leaseFixtureCommand()
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	account := filepath.Join(dir, "account")
	grant := linuxLeaseIn(t, dir, c, "begin", "touch "+shellQuote(started)+"; while test ! -e "+shellQuote(release)+"; do sleep 0.01; done; touch "+shellQuote(account))
	if err := grant.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { grant.Process.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("guest did not start")
		}
		time.Sleep(time.Millisecond)
	}
	revoke := linuxLeaseIn(t, dir, c, "revoke", "rm -f "+shellQuote(account))
	done := make(chan error, 1)
	go func() { done <- revoke.Run() }()
	select {
	case err := <-done:
		t.Fatal("revoke crossed unfinished guest grant", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := grant.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(account); !os.IsNotExist(err) {
		t.Fatal("account survived confirmed cleanup", err)
	}
}
func TestDesktopLeaseRefusesForeignMarkerBeforeGuestMutation(t *testing.T) {
	c := leaseFixtureCommand()
	dir := t.TempDir()
	if out, err := linuxLeaseIn(t, dir, c, "begin", "true").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	foreign := c
	foreign.Owner = "another-owner"
	changed := filepath.Join(dir, "foreign-mutated")
	if out, err := linuxLeaseIn(t, dir, foreign, "revoke", "touch "+shellQuote(changed)).CombinedOutput(); err == nil || !strings.Contains(string(out), "ownership_mismatch") {
		t.Fatal(err, string(out))
	}
	if _, err := os.Stat(changed); !os.IsNotExist(err) {
		t.Fatal("foreign guest state mutated", err)
	}
}

func TestWindowsLeaseComposesActualGrantAndRevokeCommands(t *testing.T) {
	c := leaseFixtureCommand()
	c.Machine.OS = "windows"
	for _, item := range []struct{ mode, command, mutation string }{
		{"begin", windowsDesktopProgram(c, "example-password"), "Enable-LocalUser"},
		{"revoke", windowsDesktopRevokeProgram(c, true), "Disable-LocalUser"},
	} {
		t.Run(item.mode, func(t *testing.T) {
			program, err := windows.CommandProgram(leaseCommand(c, item.mode, item.command))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(program, "ComSpec") || strings.Contains(program, "-EncodedCommand") || !strings.Contains(program, item.mutation) || !strings.Contains(program, "$leaseMutex") {
				t.Fatal("mutation was not composed directly inside its lease")
			}
			if os.Getenv("LABCENTRAL_TEST_POWERSHELL") != "" {
				pscheck.Syntax(t, program)
			}
		})
	}
}
