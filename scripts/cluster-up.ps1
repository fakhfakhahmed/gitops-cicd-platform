#Requires -Version 5.1
<#
.SYNOPSIS
    Starts (or reuses) a local minikube cluster with the addons this repo
    depends on: ingress (nginx) and metrics-server (required for the HPA to
    report anything other than <unknown>).

.DESCRIPTION
    Idempotent: if minikube is already running this profile, the script
    detects that and skips straight to addon enablement instead of failing
    or restarting the cluster underneath whatever is already deployed to it.
#>

$ErrorActionPreference = 'Stop'

$Profile = 'gitops-platform'

function Write-Step { param([string]$Message) Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "    $Message" -ForegroundColor Green }
function Write-Warn2 { param([string]$Message) Write-Host "    $Message" -ForegroundColor Yellow }

if (-not (Get-Command minikube -ErrorAction SilentlyContinue)) {
    Write-Host "minikube is not installed or not on PATH. Install it first:" -ForegroundColor Red
    Write-Host "  https://minikube.sigs.k8s.io/docs/start/" -ForegroundColor Red
    exit 1
}

Write-Step "Checking for an existing '$Profile' cluster"
$statusJson = minikube status -p $Profile -o json 2>$null
$alreadyRunning = $false
if ($LASTEXITCODE -eq 0 -and $statusJson) {
    try {
        $status = $statusJson | ConvertFrom-Json
        if ($status.Host -eq 'Running' -and $status.Kubelet -eq 'Running') {
            $alreadyRunning = $true
        }
    } catch {
        # Non-JSON or unexpected output means "not running" — fall through
        # to a fresh start rather than guessing.
    }
}

if ($alreadyRunning) {
    Write-Ok "Cluster '$Profile' is already running — reusing it."
} else {
    Write-Step "Starting minikube cluster '$Profile'"
    # cpus/memory are deliberately modest: this repo's whole point is to
    # demonstrate the delivery pipeline, not to stress-test a workload, and a
    # laptop-friendly footprint means `scripts/bootstrap` works on a machine
    # that is also running a browser, an IDE and everything else at once.
    minikube start `
        --profile $Profile `
        --cpus 4 `
        --memory 4096 `
        --driver docker `
        --kubernetes-version stable
    if ($LASTEXITCODE -ne 0) {
        Write-Host "minikube start failed." -ForegroundColor Red
        exit 1
    }
    Write-Ok "Cluster started."
}

Write-Step "Enabling required addons (ingress, metrics-server)"
foreach ($addon in @('ingress', 'metrics-server')) {
    $enabled = (minikube addons list -p $Profile -o json 2>$null |
        ConvertFrom-Json).$addon.Status -eq 'enabled'
    if ($enabled) {
        Write-Ok "Addon '$addon' already enabled."
    } else {
        minikube addons enable $addon -p $Profile
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Failed to enable addon '$addon'." -ForegroundColor Red
            exit 1
        }
        Write-Ok "Addon '$addon' enabled."
    }
}

Write-Step "Pointing kubectl at '$Profile'"
kubectl config use-context $Profile | Out-Null

Write-Ok "Cluster is up. Run scripts/install-argocd.ps1 next (or scripts/bootstrap.ps1 for both steps)."
