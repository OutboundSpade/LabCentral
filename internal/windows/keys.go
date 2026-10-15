package windows

// KeyProtection restricts Windows host keys to SYSTEM and the Administrators group.
const KeyProtection = `function Protect-LabCentralKey([string]$path) {
 $acl=New-Object Security.AccessControl.FileSecurity
 $acl.SetAccessRuleProtection($true,$false)
 $acl.SetOwner((New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))
 foreach ($sid in @('S-1-5-18','S-1-5-32-544')) {
  $identity=New-Object Security.Principal.SecurityIdentifier($sid)
  $rule=New-Object Security.AccessControl.FileSystemAccessRule($identity,'FullControl','Allow')
  $acl.AddAccessRule($rule)
 }
 Set-Acl -LiteralPath $path -AclObject $acl
}
`
