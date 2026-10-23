#Requires -Version 5.1
<#
.SYNOPSIS
    Scripted walkthrough for a LinkedIn demo recording or a live interview
    screenshare: prints exactly what to show and in what order, pausing
    between steps so the narration keeps pace with the screen.

.DESCRIPTION
    This does not do anything you could not do by hand — it exists purely to
    make the demo repeatable and camera-ready: consistent pacing, consistent
    talking points, nothing to remember on the day.
#>

$ErrorActionPreference = 'Stop'

function Show-Step {
    param(
        [string]$Title,
        [string]$Narration,
        [string]$Command
    )
    Write-Host ""
    Write-Host "=== $Title ===" -ForegroundColor Magenta
    Write-Host $Narration -ForegroundColor White
    if ($Command) {
        Write-Host ""
        Write-Host "  $ $Command" -ForegroundColor DarkGray
    }
    Write-Host ""
    Write-Host "-- press Enter to continue --" -ForegroundColor DarkGray -NoNewline
    [void][System.Console]::ReadLine()
}

Write-Host "===========================================================" -ForegroundColor Magenta
Write-Host " gitops-cicd-platform — demo walkthrough" -ForegroundColor Magenta
Write-Host " (run this alongside a screen recording / screenshare)" -ForegroundColor Magenta
Write-Host "===========================================================" -ForegroundColor Magenta

Show-Step -Title "1. Argo CD app tree" `
    -Narration "Open the Argo CD UI and show the 'root-app' Application tree: root-app -> app-dev/app-staging/app-prod -> their Kubernetes resources. Point out Synced/Healthy status and the app-prod card showing 'OutOfSync' or a manual-sync affordance if a promotion is pending." `
    -Command "Start-Process https://localhost:8080"

Show-Step -Title "2. The running app, proving the chain end-to-end" `
    -Narration "curl the app's root endpoint and show the commit SHA in the JSON response. This is the proof that git push -> CI -> GHCR -> GitOps -> cluster all actually worked: the SHA you see here matches the latest commit on GitHub." `
    -Command "curl.exe http://localhost:8081/ | ConvertFrom-Json | Format-List"

Show-Step -Title "3. Make a change" `
    -Narration "Edit app/main.go (something trivially visible, e.g. a string in handleRoot), commit, and push to main. Narrate: 'this is the only step a human does — everything after this is automatic.'" `
    -Command "git commit -am 'demo: visible change for the recording' && git push"

Show-Step -Title "4. GitHub Actions run graph" `
    -Narration "Switch to the Actions tab and show the CI job graph: lint -> test -> build -> security -> sign -> gitops-update, all green. Call out the multi-arch build, the SARIF upload, and the cosign signing step by name." `
    -Command "Start-Process https://github.com/fakhfakhahmed/gitops-cicd-platform/actions"

Show-Step -Title "5. GitHub Security tab" `
    -Narration "Show the Code scanning alerts populated from the Trivy SARIF upload. This is the supply-chain story made visible: vulnerabilities are tracked in the same place as code, not buried in a build log." `
    -Command "Start-Process https://github.com/fakhfakhahmed/gitops-cicd-platform/security/code-scanning"

Show-Step -Title "6. The automatic GitOps commit" `
    -Narration "Show the github-actions[bot] commit that bumped gitops/environments/dev/values.yaml's image.tag — this is the push-to-pull handoff. CI never touched the cluster; it only ever touched this one line in git." `
    -Command "git log --oneline -5 -- gitops/environments/dev/values.yaml"

Show-Step -Title "7. Argo CD picks it up" `
    -Narration "Back in the Argo CD UI, show app-dev reconciling: a new ReplicaSet rolling out within a minute or two of the commit above, with zero manual action." `
    -Command ""

Show-Step -Title "8. The app again, now with the new SHA" `
    -Narration "curl the root endpoint one more time and show the commit field has changed to match the commit from step 3. Full loop closed, on camera." `
    -Command "curl.exe http://localhost:8081/ | ConvertFrom-Json | Format-List"

Write-Host ""
Write-Host "Demo complete. See docs/screenshots/README.md for the exact shot list if you're assembling a LinkedIn post instead of a video." -ForegroundColor Cyan
