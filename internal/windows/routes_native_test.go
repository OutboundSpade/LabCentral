package windows

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestNativeWindowsRoutePersistenceIdempotenceAndConflict(t *testing.T) {
	shell := os.Getenv("LABCENTRAL_TEST_WINDOWS_POWERSHELL")
	if runtime.GOOS != "windows" || shell == "" {
		t.Skip("requires an explicitly configured disposable Windows guest")
	}
	program := `$ErrorActionPreference='Stop'
$nic=Get-NetRoute -DestinationPrefix '0.0.0.0/0' | Select-Object -First 1
$ip=Get-NetIPAddress -InterfaceIndex $nic.InterfaceIndex -AddressFamily IPv4 | Select-Object -First 1
$prefix='198.51.100.' + (Get-Random -Minimum 20 -Maximum 240) + '/32'
if (Get-NetRoute -DestinationPrefix $prefix -ErrorAction SilentlyContinue) {throw 'test route already exists'}
$p=[pscustomobject]@{Build=$false; NICs=@([pscustomobject]@{address=$ip.IPAddress;routes=@([pscustomobject]@{cidr=$prefix;via=$nic.NextHop})})}
$apply={
` + Routes + `}
try {
 & $apply
 foreach ($store in 'ActiveStore','PersistentStore') {
  $routes=@(Get-NetRoute -InterfaceIndex $nic.InterfaceIndex -DestinationPrefix $prefix -PolicyStore $store)
  if ($routes.Count -ne 1 -or $routes[0].NextHop -ne $nic.NextHop) {throw 'route absent or duplicated in store'}
 }
 & $apply
 $p.NICs[0].routes[0].via='192.0.2.1'
 $rejected=$false
 try {& $apply} catch {if ($_.Exception.Message -ne 'route ownership mismatch') {throw}; $rejected=$true}
 if (!$rejected) {throw 'conflicting route accepted'}
} finally {
 $routes=Get-NetRoute -InterfaceIndex $nic.InterfaceIndex -DestinationPrefix $prefix -ErrorAction SilentlyContinue
 if (@($routes | Where-Object NextHop -ne $nic.NextHop).Count -gt 0) {throw 'test route ownership changed'}
 $routes | Remove-NetRoute -Confirm:$false
}
`
	args := strings.Fields(ProcessCommand(program))
	if out, err := exec.Command(shell, args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("native route persistence: %s: %v", out, err)
	}
}
