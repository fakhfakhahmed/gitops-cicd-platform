#Requires -Version 5.1
<#
.SYNOPSIS
    Tears down the local minikube cluster used by this repo.

.DESCRIPTION
    Idempotent: if the cluster does not exist, this is a no-op rather than an
    error — "make sure it's torn down" should be safe to run twice.
#>

$ErrorActionPreference = 'Stop'

$Profile = 'gitops-platform'

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "    $Message" -ForegroundColor Green }

if (-not (Get-Command minikube -ErrorAction SilentlyContinue)) {
    Write-Ok "minikube is not installed — nothing to tear down."
    exit 0
}

$profiles = minikube profile list -o json 2>$null | ConvertFrom-Json
$exists = $profiles.valid | Where-Object { $_.Name -eq $Profile }

if (-not $exists) {
    Write-Ok "Cluster '$Profile' does not exist — nothing to do."
    exit 0
}

Write-Step "Deleting minikube cluster '$Profile'"
minikube delete -p $Profile
if ($LASTEXITCODE -ne 0) {
    Write-Host "minikube delete failed." -ForegroundColor Red
    exit 1
}

Write-Ok "Cluster '$Profile' deleted."
