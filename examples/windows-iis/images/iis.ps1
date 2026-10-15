$ErrorActionPreference = 'Stop'
Install-WindowsFeature Web-Server -IncludeManagementTools | Out-Null
Set-Content -Encoding utf8 -Path C:\inetpub\wwwroot\index.html -Value '<h1>LabCentral IIS is ready</h1>'
Start-Service W3SVC
