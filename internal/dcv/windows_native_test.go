package dcv

import (
	"encoding/json"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestNativeWindowsSessionJSONEmptyAndOwnedConsole(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires explicitly configured disposable Windows guest with lab account")
	}
	for _, fixture := range []string{`[]`, `[{"id":"console","owner":"lab","type":"console","status":"running"}]`} {
		program := "$ErrorActionPreference='Stop'; $output='" + fixture + "'\n" + windowsSessionJSON
		args := strings.Fields(windows.ProcessCommand(program))
		out, err := exec.Command(shell, args[1:]...).Output()
		if err != nil {
			t.Fatal(err)
		}
		var got, want []map[string]any
		if err = json.Unmarshal(out, &got); err != nil {
			t.Fatalf("status JSON: %s: %v", out, err)
		}
		if err = json.Unmarshal([]byte(fixture), &want); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("array changed: %s", out)
		}
		if len(got) > 0 && got[0]["owner"] != "lab" {
			t.Fatalf("owner changed: %s", out)
		}
	}
}

func TestNativeWindowsCertificateLocation(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires explicitly configured Windows DCV guest")
	}
	args := strings.Fields(windows.ProcessCommand("$ErrorActionPreference='Stop'; $pem=[IO.File]::ReadAllText('" + WindowsCertificate + "'); if (!$pem.Contains('BEGIN CERTIFICATE')) {throw 'missing PEM certificate'}"))
	if out, err := exec.Command(shell, args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("certificate: %s: %v", out, err)
	}
}
