// Package pscheck parses platform-owned programs with an explicitly configured
// local PowerShell executable. It performs no guest or cloud operations.
package pscheck

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func Syntax(t *testing.T, program string) {
	t.Helper()
	shell := os.Getenv("LABCENTRAL_TEST_POWERSHELL")
	if shell == "" {
		t.Skip("set LABCENTRAL_TEST_POWERSHELL for local PowerShell syntax checks")
	}
	path := filepath.Join(t.TempDir(), "program.ps1")
	if err := os.WriteFile(path, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$tokens=$null; $errors=$null; $ast=[Management.Automation.Language.Parser]::ParseFile($env:LABCENTRAL_SYNTAX_FILE,[ref]$tokens,[ref]$errors); if ($errors.Count) {$errors | ForEach-Object {Write-Error $_}; exit 1}; foreach ($command in $ast.FindAll({param($n) $n -is [Management.Automation.Language.CommandAst] -and $n.GetCommandName() -eq 'Add-Type'},$true)) {for ($i=0; $i -lt $command.CommandElements.Count-1; $i++) {if ($command.CommandElements[$i] -is [Management.Automation.Language.CommandParameterAst] -and $command.CommandElements[$i].ParameterName -eq 'TypeDefinition') {$source=$command.CommandElements[$i+1]; if ($source -isnot [Management.Automation.Language.StringConstantExpressionAst]) {throw 'C# definition must be literal'}; Add-Type -TypeDefinition $source.Value -ErrorAction Stop}}}`)
	cmd.Env = append(os.Environ(), "LABCENTRAL_SYNTAX_FILE="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell parse: %s: %v", output, err)
	}
}

func EncodedSyntax(t *testing.T, command string) string {
	t.Helper()
	parts := strings.Split(command, " -EncodedCommand ")
	if len(parts) != 2 {
		t.Fatal("expected encoded PowerShell command")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(parts[1], "; exit $LASTEXITCODE"))
	if err != nil || len(b)%2 != 0 {
		t.Fatal("invalid encoded PowerShell", err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	program := string(utf16.Decode(u))
	Syntax(t, program)
	return program
}
