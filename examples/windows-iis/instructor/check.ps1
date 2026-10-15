$ErrorActionPreference = 'Stop'
try {
    $response = Invoke-WebRequest -UseBasicParsing -Uri http://localhost/index.html -TimeoutSec 10
    if ($response.StatusCode -ne 200 -or $response.Content -notmatch 'LabCentral IIS is ready') { exit 1 }
    exit 0
} catch { exit 1 }
