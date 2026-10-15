package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

func windowsProvisioners(c lc.Command, dir string, cfg Config) (Config, error) {
	if c.Environment.Platform != "aws" || c.Image.Playbook != "" || c.Image.Script == "" || !strings.HasSuffix(c.Image.Script, ".ps1") {
		return cfg, fmt.Errorf("Windows builds require an AWS guest PowerShell script")
	}
	// Author values never enter Packer's templates or a host command. Packer only
	// copies retained source and JSON and invokes a fixed platform-owned program.
	vars := map[string]any{}
	for _, v := range c.Inputs {
		switch v.Type {
		case "string":
			vars[v.Name] = v.Value
		case "integer":
			n, err := strconv.ParseInt(v.Value, 10, 64)
			if err != nil {
				return cfg, err
			}
			vars[v.Name] = n
		case "boolean":
			b, err := strconv.ParseBool(v.Value)
			if err != nil {
				return cfg, err
			}
			vars[v.Name] = b
		default:
			return cfg, fmt.Errorf("unsupported input type")
		}
	}
	labExtensions, imageExtensions := c.Source.Lab.Extensions, c.Source.Lab.Images[c.Image.Name].Extensions
	if labExtensions == nil {
		labExtensions = lc.Extensions{}
	}
	if imageExtensions == nil {
		imageExtensions = lc.Extensions{}
	}
	vars["labcentral_extensions"] = map[string]lc.Extensions{"lab": labExtensions, "image": imageExtensions}
	inputs, _ := json.Marshal(vars)
	if err := os.WriteFile(filepath.Join(dir, "inputs.json"), inputs, 0600); err != nil {
		return cfg, err
	}
	program, _ := json.Marshal(struct {
		Script string `json:"script"`
	}{c.Image.Script})
	if err := os.WriteFile(filepath.Join(dir, "program.json"), program, 0600); err != nil {
		return cfg, err
	}
	ps := func(inline ...string) {
		cfg.Provisioners = append(cfg.Provisioners, raw(map[string]any{"type": "powershell", "inline": append([]string{"$ErrorActionPreference='Stop'"}, inline...), "execute_command": "powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File {{.Path}}"}))
	}
	ps(`New-Item -ItemType Directory -Force -Path 'C:\ProgramData\LabCentral\build' | Out-Null`)
	for _, f := range []struct{ source, dest string }{{filepath.Join(dir, "source") + string(os.PathSeparator), "C:/ProgramData/LabCentral/build/"}, {filepath.Join(dir, "inputs.json"), "C:/ProgramData/LabCentral/inputs.json"}, {filepath.Join(dir, "program.json"), "C:/ProgramData/LabCentral/program.json"}} {
		cfg.Provisioners = append(cfg.Provisioners, raw(map[string]any{"type": "file", "source": f.source, "destination": f.dest}))
	}
	if c.Image.Desktop {
		pin := lc.WindowsDCVToolchain()
		ps(`[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12`,
			`Invoke-WebRequest -UseBasicParsing -Uri '`+pin.URL+`' -OutFile 'C:\ProgramData\LabCentral\dcv.msi'`,
			`if ((Get-FileHash 'C:\ProgramData\LabCentral\dcv.msi' -Algorithm SHA256).Hash -ine '`+pin.SHA256+`') {throw 'DCV checksum mismatch'}`,
			`$signature=Get-AuthenticodeSignature 'C:\ProgramData\LabCentral\dcv.msi'; if ($signature.Status -ne 'Valid') {throw 'invalid DCV signature'}`,
			`$msi=Start-Process msiexec.exe -ArgumentList '/i C:\ProgramData\LabCentral\dcv.msi /qn /norestart ADDLOCAL=ALL DISABLE_SERVER_AUTOSTART=1 DISABLE_AUTOMATIC_SESSION_CREATION=1' -Wait -PassThru; if ($msi.ExitCode -notin 0,3010) {throw 'DCV install failed'}`,
			`if (!(Test-Path -LiteralPath 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\security')) {New-Item -Force -Path 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\security' | Out-Null}`,
			`New-ItemProperty -Force -Path 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\security' -Name authentication -PropertyType String -Value system | Out-Null`,
			// Disable MSI's automatic console session; access creates the owned lab console.
			`if (!(Test-Path -LiteralPath 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\session-management')) {New-Item -Force -Path 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\session-management' | Out-Null}`,
			`New-ItemProperty -Force -Path 'Registry::HKEY_USERS\S-1-5-18\Software\GSettings\com\nicesoftware\dcv\session-management' -Name create-session -PropertyType DWord -Value 0 | Out-Null`,
			`Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object {$_.DisplayName -match '(DCV|NICE)'} | Disable-NetFirewallRule`,
			`Set-Service dcvserver -StartupType Automatic`, `Start-Service dcvserver`, `Remove-Item -Force 'C:\ProgramData\LabCentral\ready'`)
		// Finish platform driver installation before the author uses Windows
		// servicing. The EC2Launch bootstrap remains idempotent across this reboot.
		cfg.Provisioners = append(cfg.Provisioners, raw(map[string]any{"type": "windows-restart", "restart_timeout": "20m", "restart_check_command": windows.Command(windows.Ready + windows.DesktopReady)}))
	}
	ps(`$env:LABCENTRAL_INPUTS='C:\ProgramData\LabCentral\inputs.json'`,
		`Set-Location 'C:\ProgramData\LabCentral\build'`,
		`$program=Get-Content -Raw 'C:\ProgramData\LabCentral\program.json' | ConvertFrom-Json`,
		`& (Join-Path 'C:\ProgramData\LabCentral\build' $program.script)`,
		`if (!$?) {throw 'author script failed'}; if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) {exit $LASTEXITCODE}`)
	// Final guest provisioner sanitizes, then EC2Launch generalizes and shuts down.
	// skip_create_ami is never used: Packer waits for native stopped state to capture.
	ps(`Remove-Item -Recurse -Force 'C:\ProgramData\LabCentral'`,
		`Get-ChildItem 'C:\ProgramData\ssh\*authorized_keys','C:\ProgramData\ssh\ssh_host_*','C:\Users\*\.ssh\authorized_keys' -ErrorAction SilentlyContinue | Remove-Item -Force`,
		`if (Get-Service dcvserver -ErrorAction SilentlyContinue) {Stop-Service dcvserver; Remove-Item -Force 'C:\Windows\System32\config\systemprofile\AppData\Local\NICE\dcv\dcv.pem','C:\Windows\System32\config\systemprofile\AppData\Local\NICE\dcv\dcv.key' -ErrorAction SilentlyContinue}`,
		`& 'C:\Program Files\Amazon\EC2Launch\EC2Launch.exe' sysprep --shutdown --block`,
		`if ($LASTEXITCODE -ne 0) {throw 'EC2Launch generalize failed'}`)
	// Sysprep closes the SSH connection as the instance shuts down.
	var last map[string]any
	_ = json.Unmarshal(cfg.Provisioners[len(cfg.Provisioners)-1], &last)
	last["skip_clean"] = true
	cfg.Provisioners[len(cfg.Provisioners)-1] = raw(last)
	return cfg, nil
}
