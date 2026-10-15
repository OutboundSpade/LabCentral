$ErrorActionPreference = 'Stop'
Import-Module WebAdministration
Start-Service W3SVC
Start-WebSite -Name 'Default Web Site'
