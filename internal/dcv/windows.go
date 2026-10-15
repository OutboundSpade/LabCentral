package dcv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

const WindowsCLI = `& 'C:\Program Files\NICE\DCV\Server\bin\dcv.exe' `
const WindowsCertificate = `C:\Windows\System32\config\systemprofile\AppData\Local\NICE\dcv\dcv.pem`

func WindowsState(ctx context.Context, run Run) (bool, bool, error) {
	output, err := run(ctx, windows.Command(`$ErrorActionPreference='Stop'
$output=`+WindowsCLI+`list-sessions -j
if ($LASTEXITCODE -ne 0) {exit $LASTEXITCODE}
if (![string]::IsNullOrWhiteSpace(($output -join [Environment]::NewLine))) {
 $sessions=@(($output -join [Environment]::NewLine) | ConvertFrom-Json)
 foreach ($session in $sessions) {
  $account=New-Object Security.Principal.NTAccount([string]$session.owner)
  if ($account.Translate([Security.Principal.SecurityIdentifier]).Value -eq (Get-LocalUser lab).SID.Value) {$session.owner='lab'}
 }
 ConvertTo-Json -InputObject @($sessions) -Depth 8 -Compress
}`))
	if err != nil {
		return false, false, err
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return false, false, errEmptyStatus
	}
	var sessions []struct{ ID, Owner, Type, Status string }
	if err = json.Unmarshal(output, &sessions); err != nil {
		return false, false, fmt.Errorf("invalid Windows DCV status: %w", err)
	}
	for _, s := range sessions {
		if s.ID == "console" {
			if !strings.EqualFold(s.Owner, "lab") || s.Type != "console" {
				return true, false, fmt.Errorf("Windows DCV console ownership mismatch")
			}
			return true, s.Status == "running", nil
		}
		// Windows permits only one console. Never close an unrecognized session.
		return true, false, fmt.Errorf("unrecognized Windows DCV console")
	}
	return false, false, nil
}
func WindowsReady(ctx context.Context, run Run) (bool, error) {
	_, ready, err := WindowsState(ctx, run)
	return ready, err
}
func StartWindows(ctx context.Context, run Run) error {
	exists, ready, err := WindowsState(ctx, run)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if !exists {
		if _, err = run(ctx, windows.Command(WindowsCLI+`create-session --type console --owner lab console; if ($LASTEXITCODE -ne 0) {exit $LASTEXITCODE}`)); err != nil {
			return err
		}
	}
	wait, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		_, ready, err = WindowsState(wait, run)
		if err != nil && err != errEmptyStatus {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("Windows console readiness unconfirmed: %w", wait.Err())
		case <-tick.C:
		}
	}
}
