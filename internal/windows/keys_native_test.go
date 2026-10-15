package windows

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This executes Windows ACL APIs in an explicitly configured disposable guest.
func TestNativeWindowsBuildHostKeyACL(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires an explicitly configured disposable Windows guest")
	}
	path := filepath.Join(t.TempDir(), "host-key")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	program := `$ErrorActionPreference='Stop'
` + KeyProtection + `
$path=` + quote(path) + `
[IO.File]::WriteAllText($path,'test key contents')
# Reproduce the generated key's individual owner and add a forbidden reader.
$acl=Get-Acl $path
$acl.SetOwner((New-Object Security.Principal.SecurityIdentifier((Get-LocalUser Administrator).SID.Value)))
$acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule((New-Object Security.Principal.SecurityIdentifier('S-1-5-32-545')),'Read','Allow')))
Set-Acl $path $acl
Protect-LabCentralKey $path
$acl=Get-Acl $path
if (!$acl.AreAccessRulesProtected -or $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-32-544') {throw 'host key owner or inheritance is unsafe'}
$rules=@($acl.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))
if ($rules.Count -ne 2) {throw 'extra host key access rule'}
foreach ($rule in $rules) {
 if ($rule.IdentityReference.Value -notin 'S-1-5-18','S-1-5-32-544' -or $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') {throw 'unsafe host key access rule'}
}
`
	args := strings.Fields(ProcessCommand(program))
	if out, err := exec.Command(shell, args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("native host key ACL: %s: %v", out, err)
	}
}
