#Requires -Version 5.1
<#
.SYNOPSIS
    Port-forwards the Argo CD UI and the app's dev Service in parallel, in
    separate PowerShell jobs, and prints the URLs to hit.

.DESCRIPTION
    Runs both port-forwards as background jobs so this one script gives you
    both endpoints from a single terminal. Press Ctrl+C (or close the
    terminal) to stop; the EXIT trap cleans the jobs up either way.
#>

$ErrorActionPreference = 'Stop'

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "    $Message" -ForegroundColor Green }

$jobs = @()

function Stop-Jobs {
    foreach ($j in $script:jobs) {
        if ($j -and (Get-Job -Id $j.Id -ErrorAction SilentlyContinue)) {
            Stop-Job -Id $j.Id -ErrorAction SilentlyContinue
            Remove-Job -Id $j.Id -ErrorAction SilentlyContinue
        }
    }
}

try {
    Write-Step "Starting port-forward: argocd-server -> https://localhost:8080"
    $jobs += Start-Job -ScriptBlock {
        kubectl port-forward svc/argocd-server -n argocd 8080:443
    }

    Write-Step "Starting port-forward: app-dev -> http://localhost:8081"
    $jobs += Start-Job -ScriptBlock {
        kubectl port-forward svc/app-dev -n app-dev 8081:80
    }

    Start-Sleep -Seconds 2

    Write-Host ""
    Write-Ok "Argo CD UI:   https://localhost:8080  (user: admin)"
    Write-Ok "App:          http://localhost:8081/"
    Write-Ok "App health:   http://localhost:8081/healthz"
    Write-Ok "App metrics:  http://localhost:8081/metrics"
    Write-Host ""
    Write-Host "Press Ctrl+C to stop both port-forwards." -ForegroundColor Yellow

    # Block here, surfacing job output, until the user interrupts.
    while ($true) {
        Receive-Job -Job $jobs -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
    }
} finally {
    Write-Step "Stopping port-forwards"
    Stop-Jobs
}
