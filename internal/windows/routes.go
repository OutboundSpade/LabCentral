package windows

// Routes installs missing guest routes in both default stores and rejects conflicts.
const Routes = `if (!$p.Build -and $p.NICs.Count -gt 0) {
 $address=Get-NetIPAddress -AddressFamily IPv4 | Where-Object IPAddress -eq $p.NICs[0].address | Select-Object -First 1
 if (!$address) {throw 'management NIC is unavailable'}
 foreach ($route in $p.NICs[0].routes) {
  $existing=Get-NetRoute -InterfaceIndex $address.InterfaceIndex -DestinationPrefix $route.cidr -ErrorAction SilentlyContinue
  if ($existing) {if (@($existing | Where-Object NextHop -ne $route.via).Count -gt 0) {throw 'route ownership mismatch'}} else {
   New-NetRoute -InterfaceIndex $address.InterfaceIndex -DestinationPrefix $route.cidr -NextHop $route.via | Out-Null
  }
 }
}
`
