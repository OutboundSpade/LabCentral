package access

import (
	"fmt"
	"regexp"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

var generationPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Every guest mutation participates in the same lease fence. Revocation saves a
// tombstone even when the account does not yet exist, so a delayed SSH exec cannot
// create it after the library has confirmed cleanup and retired its intent.
func linuxLease(c lc.Command, mode, program string) string {
	if c.Access.Generation == "" {
		return program
	} // Retained records keep their original format.
	if !generationPattern.MatchString(c.Access.Generation) {
		return "exit 1"
	}
	name := dcvName(c)
	marker := dcvMarker(c)
	generation := c.Access.Generation
	script := `set -eu
umask 077
root=/var/lib/labcentral/desktop-leases
mkdir -p "$root"; chmod 700 "$root"
lease="$root/` + name + `"
mkdir -p "$lease/revoked"; chmod 700 "$lease" "$lease/revoked"
if test -f "$lease/owner"; then test "$(cat "$lease/owner")" = '` + marker + `' || { echo 'ownership_mismatch: desktop lease owner' >&2; exit 1; }; else printf '%s' '` + marker + `' > "$lease/owner"; fi
generation='` + generation + `'
current=''; if test -f "$lease/current"; then current=$(cat "$lease/current"); fi
`
	switch mode {
	case "begin":
		script += `test ! -f "$lease/revoked/$generation" || { echo 'access generation was revoked; recover before reopening' >&2; exit 1; }
if test -n "$current" && test "$current" != "$generation"; then
  : > "$lease/revoked/$current"; sync -f "$lease/revoked/$current"
fi
printf '%s' "$generation" > "$lease/current.new"; sync -f "$lease/current.new"; mv "$lease/current.new" "$lease/current"; sync -f "$lease"
`
	case "revoke":
		script += `: > "$lease/revoked/$generation"; sync -f "$lease/revoked/$generation"; sync -f "$lease/revoked"
if test -n "$current"; then test "$current" = "$generation" || { echo 'ownership_mismatch: desktop access generation differs' >&2; exit 1; }; fi
`
	default:
		script += `test "$current" = "$generation" && test ! -f "$lease/revoked/$generation" || { echo 'ownership_mismatch: desktop access generation differs or was revoked' >&2; exit 1; }
`
	}
	return "sudo flock -w 60 /run/lock/labcentral-desktop-" + name + ".lock sh -ec " + shellQuote(script+program)
}
func psQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func windowsLease(c lc.Command, mode, program string) string {
	if c.Access.Generation == "" {
		return program
	}
	if !generationPattern.MatchString(c.Access.Generation) {
		return "throw 'invalid desktop generation'"
	}
	generation := psQuote(c.Access.Generation)
	marker := psQuote(dcvMarker(c))
	script := `$ErrorActionPreference='Stop'
$leaseMutex=New-Object Threading.Mutex($false,'Global\LabCentralDesktopLease')
$leaseLocked=$false
try {
 try {$leaseLocked=$leaseMutex.WaitOne(60000)} catch [Threading.AbandonedMutexException] {$leaseLocked=$true}
 if (!$leaseLocked) {throw 'desktop lease busy; retry recovery'}
 $lease='HKLM:\SOFTWARE\LabCentral\DesktopLease'
 $revoked=$lease+'\Revoked'
 if (!(Test-Path -LiteralPath $revoked)) {New-Item -Force -Path $revoked | Out-Null}
 $leaseGeneration=` + generation + `
 $leaseMarker=` + marker + `
 $leaseOwner=(Get-ItemProperty -LiteralPath $lease -Name Owner -ErrorAction SilentlyContinue).Owner
 $leaseCurrent=(Get-ItemProperty -LiteralPath $lease -Name Generation -ErrorAction SilentlyContinue).Generation
`
	switch mode {
	case "begin":
		script += ` if ((Get-ItemProperty -LiteralPath $revoked -Name $leaseGeneration -ErrorAction SilentlyContinue).$leaseGeneration) {throw 'access generation was revoked; recover before reopening'}
 if ($leaseOwner -and $leaseOwner -ne $leaseMarker) {
  $console=Get-LocalUser -Name lab -ErrorAction Stop
  $nativeOwner=(Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\LabCentral\Desktop' -Name Owner -ErrorAction SilentlyContinue).Owner
  if ($console.Enabled -or $nativeOwner -ne $leaseOwner) {throw 'ownership_mismatch: desktop lease owner'}
 }
 if ($leaseCurrent -and $leaseCurrent -ne $leaseGeneration) {New-ItemProperty -Force -LiteralPath $revoked -Name $leaseCurrent -PropertyType DWord -Value 1 | Out-Null}
 New-ItemProperty -Force -LiteralPath $lease -Name Owner -PropertyType String -Value $leaseMarker | Out-Null
 New-ItemProperty -Force -LiteralPath $lease -Name Generation -PropertyType String -Value $leaseGeneration | Out-Null
`
	case "revoke":
		script += ` New-ItemProperty -Force -LiteralPath $revoked -Name $leaseGeneration -PropertyType DWord -Value 1 | Out-Null
 if (($leaseOwner -and $leaseOwner -ne $leaseMarker) -or ($leaseCurrent -and $leaseCurrent -ne $leaseGeneration)) {throw 'ownership_mismatch: desktop access generation differs'}
`
	default:
		script += ` if ($leaseOwner -ne $leaseMarker -or $leaseCurrent -ne $leaseGeneration -or (Get-ItemProperty -LiteralPath $revoked -Name $leaseGeneration -ErrorAction SilentlyContinue).$leaseGeneration) {throw 'ownership_mismatch: desktop access generation differs or was revoked'}
`
	}
	// Flush the revocation/generation before executing any guest effect. A late
	// grant sees it even if its original SSH controller was killed.
	script += ` $leaseKey=Get-Item -LiteralPath $lease; $leaseKey.Flush(); $leaseKey.Close()
 $revokeKey=Get-Item -LiteralPath $revoked; $revokeKey.Flush(); $revokeKey.Close()
` + program + `
} finally {if ($leaseLocked) {$leaseMutex.ReleaseMutex()}; $leaseMutex.Dispose()}
`
	return script
}
func leaseCommand(c lc.Command, mode, command string) string {
	if c.Access.Generation == "" {
		return command
	}
	if c.Machine.OS == "windows" {
		script := fmt.Sprintf("$leaseOutput = & $env:ComSpec /d /s /c %s\n$leaseExit=$LASTEXITCODE\n$leaseOutput\nif ($leaseExit -ne 0) {exit $leaseExit}", psQuote(command))
		return windows.Command(windowsLease(c, mode, script))
	}
	return linuxLease(c, mode, command)
}
