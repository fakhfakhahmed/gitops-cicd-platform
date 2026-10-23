#Requires -Version 5.1
<#
.SYNOPSIS
    Installs Argo CD into the cluster, bootstraps the AppProject and root
    Application, and prints everything needed to log in.

.DESCRIPTION
    Idempotent: re-running this script against a cluster that already has
    Argo CD installed just waits for the rollout and re-applies the bootstrap
    manifests (which are themselves safe to re-apply — `kubectl apply` is a
    no-op against unchanged objects).
#>

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$Namespace = 'argocd'
# Pinned version, not "latest" — this is the installation manifest itself, so
# pinning it is the same supply-chain argument made throughout this repo for
# GitHub Actions: a reproducible install beats a moving target, and an
# upgrade is then a deliberate version bump reviewed in a PR, not whatever
# the upstream project happened to ship today.
$ArgoCdVersion = 'v2.13.2'
$ManifestUrl = "https://raw.githubusercontent.com/argoproj/argo-cd/$ArgoCdVersion/manifests/install.yaml"

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "    $Message" -ForegroundColor Green }

foreach ($cmd in @('kubectl')) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "$cmd is not installed or not on PATH." -ForegroundColor Red
        exit 1
    }
}

Write-Step "Applying namespaces (gitops/bootstrap/namespace.yaml)"
kubectl apply -f "$RepoRoot/gitops/bootstrap/namespace.yaml"
if ($LASTEXITCODE -ne 0) { exit 1 }

$existing = kubectl get deployment argocd-server -n $Namespace --ignore-not-found -o name 2>$null
if ($existing) {
    Write-Ok "Argo CD is already installed in namespace '$Namespace' — skipping install, will still wait for rollout."
} else {
    Write-Step "Installing Argo CD $ArgoCdVersion"
    kubectl apply -n $Namespace -f $ManifestUrl
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Argo CD install failed." -ForegroundColor Red
        exit 1
    }
}

Write-Step "Waiting for argocd-server rollout (this can take a few minutes on first install)"
kubectl rollout status deployment/argocd-server -n $Namespace --timeout=300s
if ($LASTEXITCODE -ne 0) {
    Write-Host "argocd-server did not become ready in time. Check: kubectl get pods -n $Namespace" -ForegroundColor Red
    exit 1
}

Write-Step "Applying AppProject and root Application"
kubectl apply -f "$RepoRoot/gitops/applications/appproject.yaml"
kubectl apply -f "$RepoRoot/gitops/bootstrap/root-app.yaml"
if ($LASTEXITCODE -ne 0) { exit 1 }

Write-Step "Retrieving initial admin password"
$secretExists = kubectl get secret argocd-initial-admin-secret -n $Namespace --ignore-not-found -o name 2>$null
if ($secretExists) {
    $encoded = kubectl get secret argocd-initial-admin-secret -n $Namespace -o jsonpath='{.data.password}'
    $password = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))
    Write-Ok "Admin password: $password"
    Write-Ok "(This secret is deleted automatically the first time you change the password.)"
} else {
    Write-Ok "argocd-initial-admin-secret not found — the admin password may already have been changed."
}

Write-Host ""
Write-Host "Argo CD is installed. Next steps:" -ForegroundColor Cyan
Write-Host "  1. Port-forward the UI:" -ForegroundColor White
Write-Host "       scripts/port-forward.ps1" -ForegroundColor White
Write-Host "  2. Open:   https://localhost:8080  (user: admin)" -ForegroundColor White
Write-Host "  3. Watch the 'root-app' Application sync its children (app-dev/staging/prod)." -ForegroundColor White
