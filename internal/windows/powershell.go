// Package windows contains platform-owned guest programs. Author programs are
// payload data and are executed only by PowerShell inside a verified guest.
package windows

import (
	"encoding/base64"
	"encoding/binary"
	"unicode/utf16"
)

// Command avoids both SSH's default shell parsing and interpolation of payload data.
func Command(program string) string {
	return ProcessCommand(program) + "; exit $LASTEXITCODE"
}

// ProcessCommand invokes PowerShell directly; Command also propagates its status
// through the PowerShell default shell used by Windows OpenSSH.
func ProcessCommand(program string) string {
	u := utf16.Encode([]rune("$ProgressPreference='SilentlyContinue'\n" + program))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

func PayloadCommand(program string, payload []byte) string {
	return Command("$ErrorActionPreference='Stop'; $p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(payload) + "')) | ConvertFrom-Json\n" + program)
}

const Ready = `$ErrorActionPreference='Stop'
if (!(Test-Path 'C:\ProgramData\LabCentral\ready')) {exit 1}
if ((Get-Service sshd).Status -ne 'Running') {exit 1}
`
const DesktopReady = `if ((Get-Service dcvserver).Status -ne 'Running') {exit 1}
`

// The exclusive file handle is released by the OS on process termination. A job
// object kills the complete author process tree if the launcher dies, including
// when its SSH channel is cancelled. Output goes to bounded, drained pipes.
const StepLauncher = `
$root = 'C:\ProgramData\LabCentral'
New-Item -ItemType Directory -Force -Path $root | Out-Null
$lock = $null; $dir = $null; $gate = $null; $job = [IntPtr]::Zero; $proc = $null
try {
 $until = [DateTime]::UtcNow.AddSeconds(5)
 do {
  try {$lock = [IO.File]::Open((Join-Path $root 'exercise.lock'), 'OpenOrCreate', 'ReadWrite', 'None')} catch [IO.IOException] {Start-Sleep -Milliseconds 100}
 } until ($lock -or [DateTime]::UtcNow -ge $until)
 if (!$lock) {exit 43}
 $dir = Join-Path $root ('step-' + [Guid]::NewGuid().ToString('N'))
 New-Item -ItemType Directory -Path $dir | Out-Null
 foreach ($f in $p.files) {
  $target = [IO.Path]::GetFullPath((Join-Path $dir $f.path))
  if (!$target.StartsWith($dir + '\', [StringComparison]::OrdinalIgnoreCase)) {throw 'unsafe payload path'}
  New-Item -ItemType Directory -Force -Path (Split-Path $target) | Out-Null
  [IO.File]::WriteAllBytes($target, [Convert]::FromBase64String($f.content))
 }
 $program = [IO.Path]::GetFullPath((Join-Path $dir $p.script))
 if (!$program.StartsWith($dir + '\', [StringComparison]::OrdinalIgnoreCase)) {throw 'unsafe script path'}
 Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Threading.Tasks;
using System.Runtime.InteropServices;
public static class LabCentralJob {
 [DllImport("kernel32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr CreateJobObject(IntPtr a, string n);
 [DllImport("kernel32.dll")] public static extern bool SetInformationJobObject(IntPtr j, int c, IntPtr p, uint l);
 [DllImport("kernel32.dll")] public static extern bool AssignProcessToJobObject(IntPtr j, IntPtr p);
 [DllImport("kernel32.dll")] public static extern bool CloseHandle(IntPtr h);
 public static IntPtr Create() {
  IntPtr j=CreateJobObject(IntPtr.Zero,null);
  // JOBOBJECT_EXTENDED_LIMIT_INFORMATION: BasicLimitInformation.LimitFlags
  // is at offset 16 on both Windows architectures. All other limits are zero.
  int size=IntPtr.Size==8 ? 144 : 112;
  IntPtr p=Marshal.AllocHGlobal(size);
  try {Marshal.Copy(new byte[size],0,p,size); Marshal.WriteInt32(p,16,0x2000);
   if(j==IntPtr.Zero || !SetInformationJobObject(j,9,p,(uint)size)) throw new Exception("job setup failed");
  } catch {if(j!=IntPtr.Zero) CloseHandle(j); throw;} finally {Marshal.FreeHGlobal(p);}
  return j;
 }
 public static async Task<long> Drain(Stream r) {
  var b=new byte[4096]; long size=0; int n;
  while((n=await r.ReadAsync(b,0,b.Length))>0) {size+=n; if(size>1048576) throw new Exception("guest output exceeds bound");}
  return size;
 }
}
'@
 $job = [LabCentralJob]::Create()
 $gate = Join-Path $root ('gate-' + [Guid]::NewGuid().ToString('N'))
 $start = New-Object Diagnostics.ProcessStartInfo
 $start.FileName = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
 # Wait on a platform gate before evaluating author code, so the job owns the
 # child before it can spawn descendants.
 $child = '$ErrorActionPreference="Stop"; while (!(Test-Path -LiteralPath $env:LABCENTRAL_GATE)) {Start-Sleep -Milliseconds 20}; try { & $env:LABCENTRAL_PROGRAM; if (!$?) {exit 1}; exit 0 } catch {exit 1}'
 $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($child))
 $start.Arguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ' + $encoded
 $start.WorkingDirectory = $dir; $start.UseShellExecute = $false
 $start.RedirectStandardOutput = $true; $start.RedirectStandardError = $true
 $start.EnvironmentVariables['LABCENTRAL_STEP_DIR'] = $dir
 $start.EnvironmentVariables['LABCENTRAL_GATE'] = $gate
 $start.EnvironmentVariables['LABCENTRAL_PROGRAM'] = $program
 $proc = New-Object Diagnostics.Process
 $proc.StartInfo = $start
 if (!$proc.Start()) {throw 'start failed'}
 if (![LabCentralJob]::AssignProcessToJobObject($job,$proc.Handle)) {throw 'job assignment failed'}
 $stdout = [LabCentralJob]::Drain($proc.StandardOutput.BaseStream)
 $stderr = [LabCentralJob]::Drain($proc.StandardError.BaseStream)
 [IO.File]::WriteAllText($gate,'go')
 $deadline = [DateTime]::UtcNow.AddSeconds([int]$p.timeoutSeconds)
 while (!$proc.WaitForExit(50)) {
  if ($stdout.IsFaulted -or $stderr.IsFaulted -or [DateTime]::UtcNow -ge $deadline) {throw 'guest timeout or output bound'}
 }
 if (!$stdout.Wait(5000) -or !$stderr.Wait(5000) -or ($stdout.Result + $stderr.Result) -gt 1048576) {throw 'guest output bound'}
 if ($proc.ExitCode -ne 0) {exit 42}
 exit 0
} catch {exit 43} finally {
 if ($job -ne [IntPtr]::Zero) {[LabCentralJob]::CloseHandle($job) | Out-Null}
 if ($proc) {if (!$proc.HasExited) {$proc.Kill()}; $proc.Dispose()}
 if ($gate) {Remove-Item -Force -LiteralPath $gate -ErrorAction SilentlyContinue}
 if ($dir) {Remove-Item -Recurse -Force -LiteralPath $dir -ErrorAction SilentlyContinue}
 if ($lock) {$lock.Dispose()}
}
`
