$ErrorActionPreference = 'Stop'
Import-Module WebAdministration
Stop-WebSite -Name 'Default Web Site'
