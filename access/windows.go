package access

import (
	"context"
	"encoding/json"
	"fmt"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/dcv"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"golang.org/x/crypto/ssh"
)

// Windows Server's default policy requires multiple character classes. Keep the
// existing 192 random bits and satisfy that policy without changing Linux secrets.
func desktopPassword(os string) string {
	password := random()
	if os == "windows" {
		password += "Aa1!"
	}
	return password
}

func windowsDesktopProgram(c lc.Command, password string) string {
	payload, _ := json.Marshal(struct{ Marker, Password string }{dcvMarker(c), password})
	return windows.PayloadCommand(`
$registry='HKLM:\SOFTWARE\LabCentral\Desktop'
if (!(Test-Path -LiteralPath $registry)) {New-Item -Force -Path $registry | Out-Null}
$old=(Get-ItemProperty -Path $registry -Name Owner -ErrorAction SilentlyContinue).Owner
$user=Get-LocalUser -Name lab -ErrorAction Stop
if ((Get-ItemProperty -Path $registry -Name SID -ErrorAction Stop).SID -ne $user.SID.Value) {throw 'console account SID mismatch'}
if ($old -and $old -ne $p.Marker -and $user.Enabled) {throw 'console is owned by another access'}
if (!$old -and $user.Description -ne 'LabCentral console') {throw 'console user ownership mismatch'}
New-ItemProperty -Force -Path $registry -Name Owner -PropertyType String -Value $p.Marker | Out-Null
Set-LocalUser -Name lab -Password (ConvertTo-SecureString $p.Password -AsPlainText -Force) -PasswordNeverExpires $true
Enable-LocalUser -Name lab
`, payload)
}
func closeWindowsDCV(ctx context.Context, c lc.Command, client *ssh.Client) error {
	exists, _, err := dcvWindowsState(ctx, client)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		Marker         string
		Retain, Exists bool
	}{dcvMarker(c), c.Access.RetainWorkspace, exists})
	_, err = remote.Run(ctx, client, windows.PayloadCommand(`
$registry='HKLM:\SOFTWARE\LabCentral\Desktop'
$owner=(Get-ItemProperty -Path $registry -Name Owner -ErrorAction SilentlyContinue).Owner
if (!$owner) {exit 0}
if ($owner -ne $p.Marker) {throw 'desktop ownership mismatch'}
$sid=(Get-ItemProperty -Path $registry -Name SID -ErrorAction Stop).SID
if ($sid -ne (Get-LocalUser lab -ErrorAction Stop).SID.Value) {throw 'console account SID mismatch'}
Disable-LocalUser -Name lab
if ($p.Exists) {
 & 'C:\Program Files\NICE\DCV\Server\bin\dcv.exe' close-session console
 if ($LASTEXITCODE -ne 0) {throw 'console close unconfirmed'}
}
# Log off the owned local console account before removing or retaining its profile.
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class LabCentralLogoff {
 [StructLayout(LayoutKind.Sequential)] struct Session { public int Id; public IntPtr Name; public int State; }
 [DllImport("wtsapi32.dll",EntryPoint="WTSEnumerateSessionsW")] static extern bool Enumerate(IntPtr s,int r,int v,out IntPtr p,out int count);
 [DllImport("wtsapi32.dll",EntryPoint="WTSQuerySessionInformationW")] static extern bool Query(IntPtr s,int id,int kind,out IntPtr p,out int size);
 [DllImport("wtsapi32.dll")] static extern bool WTSLogoffSession(IntPtr s,int id,bool wait);
 [DllImport("wtsapi32.dll")] static extern void WTSFreeMemory(IntPtr p);
 static string Read(int id,int kind) {IntPtr p; int size;if(!Query(IntPtr.Zero,id,kind,out p,out size)) throw new Exception("session identity unavailable");try{return Marshal.PtrToStringUni(p);}finally{WTSFreeMemory(p);}}
 public static void Run() {
  IntPtr p; int count;if(!Enumerate(IntPtr.Zero,0,1,out p,out count)) throw new Exception("session inventory unavailable");
  try {for(int i=0;i<count;i++) {
   Session s=(Session)Marshal.PtrToStructure(IntPtr.Add(p,i*Marshal.SizeOf(typeof(Session))),typeof(Session));
   if(String.Equals(Read(s.Id,5),"lab",StringComparison.OrdinalIgnoreCase) && String.Equals(Read(s.Id,7),Environment.MachineName,StringComparison.OrdinalIgnoreCase)) {
    if(!WTSLogoffSession(IntPtr.Zero,s.Id,true)) throw new Exception("logoff unconfirmed");
   }
  }} finally {WTSFreeMemory(p);}
 }
}
'@
[LabCentralLogoff]::Run()
# End remaining processes, preserving the profile only on request.
Get-CimInstance Win32_Process | ForEach-Object {
 $processOwner=Invoke-CimMethod -InputObject $_ -MethodName GetOwnerSid -ErrorAction SilentlyContinue
 if ($processOwner.Sid -eq $sid) {Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop}
}
if (!$p.Retain) {
 Get-CimInstance Win32_UserProfile | Where-Object SID -eq $sid | Remove-CimInstance
}
`, payload))
	if err != nil {
		return err
	}
	exists, _, err = dcv.WindowsState(ctx, func(ctx context.Context, command string) ([]byte, error) { return remote.Run(ctx, client, command) })
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("Windows DCV console removal unconfirmed")
	}
	return nil
}

func dcvWindowsState(ctx context.Context, client *ssh.Client) (bool, bool, error) {
	return dcv.WindowsState(ctx, func(ctx context.Context, command string) ([]byte, error) { return remote.Run(ctx, client, command) })
}
