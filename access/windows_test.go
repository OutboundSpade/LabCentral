package access

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/pscheck"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestWindowsDesktopOwnershipAndCleanupPrograms(t *testing.T) {
	c := lc.Command{Owner: "instance", Operation: "access", Machine: lc.PlannedMachine{OS: "windows", Name: "server"}}
	program := pscheck.EncodedSyntax(t, windowsDesktopProgram(c, "password ' $(Get-Secret)"))
	if strings.Contains(program, "Get-Secret") || !strings.Contains(program, "console account SID mismatch") {
		t.Fatal("credential or account ownership escaped payload")
	}
	if !strings.Contains(program, "if (!(Test-Path -LiteralPath $registry))") {
		t.Fatal("existing desktop ownership registry can be replaced")
	}
	marker, enable := strings.Index(program, "-Name Owner -PropertyType"), strings.Index(program, "Enable-LocalUser")
	if marker < 0 || enable <= marker {
		t.Fatal("account enabled before ownership intent")
	}
	var commands atomic.Int32
	server := sshtest.NewWithResult(t, nil, func(command, user string, _ []byte) ([]byte, uint32) {
		commands.Add(1)
		program := pscheck.EncodedSyntax(t, command)
		if strings.Contains(program, "list-sessions") {
			return []byte("[]"), 0
		}
		if !strings.Contains(program, "console account SID mismatch") || !strings.Contains(program, "GetOwnerSid") || !strings.Contains(program, "WTSLogoffSession") {
			t.Error("cleanup lacks owned logoff")
		}
		return nil, 0
	})
	key, _ := sshtest.Key(t)
	client, err := remote.Dial(context.Background(), server.Address, "Administrator", key, server.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = closeWindowsDCV(context.Background(), c, client); err != nil {
		t.Fatal(err)
	}
	if commands.Load() != 3 {
		t.Fatal("cleanup did not confirm console absence", commands.Load())
	}
}

func TestWindowsDesktopCredentialMeetsComplexityWithoutReducingEntropy(t *testing.T) {
	password := desktopPassword("windows")
	if len(password) < 52 || !strings.ContainsAny(password, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(password, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(password, "0123456789") || !strings.ContainsAny(password, "!@#$%") {
		t.Fatal("Windows credential does not meet default Server complexity")
	}
	linux := desktopPassword("linux")
	if len(linux) != 48 || strings.Trim(linux, "0123456789abcdef") != "" {
		t.Fatal("Linux credential generation changed")
	}
}

func TestDesktopBudgetsLeaveLinuxUnchanged(t *testing.T) {
	if desktopOpenBudget("linux") != 2*time.Minute || desktopCloseBudget("linux") != 20*time.Second {
		t.Fatal("Linux budgets changed")
	}
	if desktopOpenBudget("windows") != 10*time.Minute || desktopCloseBudget("windows") != 2*time.Minute {
		t.Fatal("Windows budgets truncate cold logon/logoff")
	}
}
