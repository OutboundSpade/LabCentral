package guest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

// WindowsUserData is platform-owned EC2Launch v2 user data; it never includes
// author programs. EC2Launch's 3010 contract resumes after one controlled reboot.
func WindowsUserData(m lc.PlannedMachine, management []string) (string, error) {
	if m.PublicKey == "" || m.HostPrivateKey == "" || m.HostPublicKey == "" {
		return "", fmt.Errorf("instance keys are required")
	}
	return windowsUserData(m, management, false)
}

// WindowsBuildUserData uses the per-build keypair prepared by the native builder.
func WindowsBuildUserData(publicKey string, management []string) (string, error) {
	return windowsUserData(lc.PlannedMachine{Name: "lc-build", PublicKey: publicKey}, management, true)
}
func windowsUserData(m lc.PlannedMachine, management []string, build bool) (string, error) {
	if len(management) == 0 {
		return "", fmt.Errorf("Windows bootstrap requires explicit management ranges")
	}
	for _, cidr := range management {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return "", fmt.Errorf("invalid Windows management range")
		}
	}
	if len(m.Name) > 15 {
		return "", fmt.Errorf("Windows computer names must be at most 15 characters")
	}
	allowed := []string{}
	for _, nic := range m.NICs {
		allowed = append(allowed, nic.CIDR)
		for _, r := range nic.Routes {
			allowed = append(allowed, r.CIDR)
		}
	}
	payload, _ := json.Marshal(struct {
		Name, PublicKey, HostPrivateKey, HostPublicKey, Hosts string
		Management, Networks                                  []string
		Build, Desktop                                        bool
		NICs                                                  []lc.PlannedNIC
	}{m.Name, strings.TrimSpace(m.PublicKey), m.HostPrivateKey, m.HostPublicKey, Hosts(m), management, allowed, build, lc.Contains(m.Spec.Access, "desktop"), m.NICs})
	program := `$ErrorActionPreference='Stop'
$p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + base64.StdEncoding.EncodeToString(payload) + `')) | ConvertFrom-Json
$root='C:\ProgramData\LabCentral'
New-Item -ItemType Directory -Force -Path $root | Out-Null
# Restrict platform payloads and step staging to SYSTEM and local administrators.
& icacls.exe $root /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) {throw 'staging ACL failed'}
Remove-Item (Join-Path $root 'ready') -ErrorAction SilentlyContinue
# Complete the controlled hostname reboot before SSH can admit Packer.
if ($env:COMPUTERNAME -ine $p.Name) {
 if (Test-Path (Join-Path $root 'renamed')) {throw 'hostname reboot did not converge'}
 Rename-Computer -NewName $p.Name -Force
 New-Item -ItemType File -Force -Path (Join-Path $root 'renamed') | Out-Null
 exit 3010
}
$cap=Get-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
if ($cap.State -ne 'Installed') {Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 | Out-Null}
if (!(Test-Path -LiteralPath 'HKLM:\SOFTWARE\OpenSSH')) {New-Item -Path 'HKLM:\SOFTWARE\OpenSSH' -Force | Out-Null}
New-ItemProperty -Path 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell -Value "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -PropertyType String -Force | Out-Null
$ssh='C:\ProgramData\ssh'
New-Item -ItemType Directory -Force -Path $ssh | Out-Null
Stop-Service sshd -ErrorAction SilentlyContinue
` + windows.KeyProtection + `[IO.File]::WriteAllText((Join-Path $ssh 'administrators_authorized_keys'),$p.PublicKey + "` + "`n" + `")
& icacls.exe (Join-Path $ssh 'administrators_authorized_keys') /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
if ($LASTEXITCODE -ne 0) {throw 'authorized key ACL failed'}
Protect-LabCentralKey (Join-Path $ssh 'administrators_authorized_keys')
if (!$p.Build) {
 Remove-Item "$ssh\ssh_host_*" -Force -ErrorAction SilentlyContinue
 [IO.File]::WriteAllText((Join-Path $ssh 'ssh_host_ed25519_key'),$p.HostPrivateKey)
 [IO.File]::WriteAllText((Join-Path $ssh 'ssh_host_ed25519_key.pub'),$p.HostPublicKey)
 & icacls.exe (Join-Path $ssh 'ssh_host_ed25519_key') /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
 if ($LASTEXITCODE -ne 0) {throw 'host key ACL failed'}
} else {& "$env:SystemRoot\System32\OpenSSH\ssh-keygen.exe" -A; if ($LASTEXITCODE -ne 0) {throw 'host key generation failed'}}
# ssh-keygen assigns the individual Administrator as owner; sshd runs as SYSTEM.
# Apply the same restricted ownership to generated and supplied host keys.
Protect-LabCentralKey (Join-Path $ssh 'ssh_host_ed25519_key')
@'
PubkeyAuthentication yes
PasswordAuthentication no
AllowUsers administrator
HostKey __PROGRAMDATA__/ssh/ssh_host_ed25519_key
Subsystem sftp sftp-server.exe
Match Group administrators
 AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys
'@ | Set-Content -Encoding ascii (Join-Path $ssh 'sshd_config')
Set-NetFirewallProfile -Profile Domain,Public,Private -Enabled True
Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP','LabCentral-*' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -Name LabCentral-SSH -DisplayName 'LabCentral management SSH' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 22 -RemoteAddress $p.Management | Out-Null
foreach ($cidr in $p.Networks) {
 New-NetFirewallRule -Name ('LabCentral-Network-' + $cidr.Replace('/','-')) -DisplayName 'LabCentral lab network' -Direction Inbound -Action Allow -RemoteAddress $cidr | Out-Null
}
if ($p.Desktop) {
 # The fixed console owner remains disabled until an authorized desktop launch.
 if (!(Get-LocalUser -Name lab -ErrorAction SilentlyContinue)) {New-LocalUser -Name lab -Password (ConvertTo-SecureString ([Guid]::NewGuid().ToString('N') + 'aA1!') -AsPlainText -Force) -Disabled -Description 'LabCentral console' | Out-Null}
 $console=Get-LocalUser -Name lab
 if ($console.Description -ne 'LabCentral console') {throw 'console account ownership mismatch'}
 $registry='HKLM:\SOFTWARE\LabCentral\Desktop'
 if (!(Test-Path -LiteralPath $registry)) {New-Item -Force -Path $registry | Out-Null}
 $sid=(Get-ItemProperty -Path $registry -Name SID -ErrorAction SilentlyContinue).SID
 if ($sid -and $sid -ne $console.SID.Value) {throw 'console SID changed'}
 New-ItemProperty -Force -Path $registry -Name SID -PropertyType String -Value $console.SID.Value | Out-Null
 if (!(Get-LocalGroupMember -SID 'S-1-5-32-544' | Where-Object Name -eq ($env:COMPUTERNAME + '\lab'))) {Add-LocalGroupMember -SID 'S-1-5-32-544' -Member lab}
 New-NetFirewallRule -Name LabCentral-DCV -DisplayName 'LabCentral DCV gateway' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8443 -RemoteAddress $p.Management | Out-Null
}
if (!$p.Build -and $p.NICs.Count -gt 0) {
 $address=Get-NetIPAddress -AddressFamily IPv4 | Where-Object IPAddress -eq $p.NICs[0].address | Select-Object -First 1
 if (!$address) {throw 'management NIC is unavailable'}
 foreach ($route in $p.NICs[0].routes) {
  $existing=Get-NetRoute -InterfaceIndex $address.InterfaceIndex -DestinationPrefix $route.cidr -ErrorAction SilentlyContinue
  if ($existing) {if (@($existing | Where-Object NextHop -ne $route.via).Count -gt 0) {throw 'route ownership mismatch'}} else {
   New-NetRoute -InterfaceIndex $address.InterfaceIndex -DestinationPrefix $route.cidr -NextHop $route.via -PolicyStore PersistentStore | Out-Null
  }
 }
}
if (!$p.Build) {[IO.File]::WriteAllText("$env:SystemRoot\System32\drivers\etc\hosts",$p.Hosts)}
Set-Service sshd -StartupType Automatic
Start-Service sshd
if ((Get-Service sshd).Status -ne 'Running') {throw 'sshd is not ready'}
if ($p.Desktop -and (Get-Service dcvserver).Status -ne 'Running') {Start-Service dcvserver}
New-Item -ItemType File -Force -Path (Join-Path $root 'ready') | Out-Null
`
	// The outer user-data file contains only a fixed platform script and JSON. persist
	// lets EC2Launch resume after the rename reboot; the marker makes reboot bounded.
	ud := "<powershell>\n" + program + "\n</powershell>\n<persist>true</persist>\n"
	if len(ud) > 16384 {
		return "", fmt.Errorf("Windows EC2 user data exceeds 16 KiB")
	}
	return ud, nil
}
