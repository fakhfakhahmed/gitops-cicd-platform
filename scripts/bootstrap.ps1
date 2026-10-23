#Requires -Version 5.1
<#
.SYNOPSIS
    One command, empty machine to running GitOps platform: cluster-up,
    install-argocd, done.

.DESCRIPTION
    This is the script a recruiter or interviewer would actually run. It
    chains the two other setup scripts and prints exactly what to do next —
    nothing here is new logic, it is purely orchestration so the README's
    Quickstart really is "one command."
#>

$ErrorActionPreference = 'Stop'
$ScriptDir = $PSScriptRoot

Write-Host "===========================================================" -ForegroundColor Magenta
Write-Host " gitops-cicd-platform — full local bootstrap" -ForegroundColor Magenta
Write-Host "===========================================================" -ForegroundColor Magenta

& "$ScriptDir/cluster-up.ps1"
if ($LASTEXITCODE -ne 0) { exit 1 }

& "$ScriptDir/install-argocd.ps1"
if ($LASTEXITCODE -ne 0) { exit 1 }

Write-Host ""
Write-Host "===========================================================" -ForegroundColor Magenta
Write-Host " Bootstrap complete." -ForegroundColor Magenta
Write-Host "===========================================================" -ForegroundColor Magenta
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Cyan
Write-Host "  scripts/port-forward.ps1    # expose Argo CD UI + the app"
Write-Host "  scripts/demo.ps1            # scripted walkthrough for a demo/recording"
Write-Host ""
Write-Host "The 'root-app' Application will reconcile app-dev/staging/prod" -ForegroundColor White
Write-Host "automatically. app-dev deploys the 'main' image tag out of the box;" -ForegroundColor White
Write-Host "push a commit through CI to see the real end-to-end flow." -ForegroundColor White
