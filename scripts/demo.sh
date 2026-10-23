#!/usr/bin/env bash
# Scripted walkthrough for a LinkedIn demo recording or a live interview
# screenshare. Does nothing you couldn't do by hand — exists purely to make
# the demo repeatable: consistent pacing, consistent talking points.
set -euo pipefail

show_step() {
    local title="$1" narration="$2" command="$3"
    echo ""
    echo -e "\033[35m=== ${title} ===\033[0m"
    echo -e "${narration}"
    if [ -n "$command" ]; then
        echo ""
        echo -e "\033[90m  \$ ${command}\033[0m"
    fi
    echo ""
    read -r -p $'\033[90m-- press Enter to continue --\033[0m'
}

echo -e "\033[35m===========================================================\033[0m"
echo -e "\033[35m gitops-cicd-platform — demo walkthrough\033[0m"
echo -e "\033[35m (run this alongside a screen recording / screenshare)\033[0m"
echo -e "\033[35m===========================================================\033[0m"

show_step "1. Argo CD app tree" \
    "Open the Argo CD UI and show the 'root-app' Application tree: root-app -> app-dev/app-staging/app-prod -> their Kubernetes resources. Point out Synced/Healthy status and the app-prod card showing a pending manual sync, if there is one." \
    "open https://localhost:8080"

show_step "2. The running app, proving the chain end-to-end" \
    "curl the app's root endpoint and show the commit SHA in the JSON response — proof that git push -> CI -> GHCR -> GitOps -> cluster all actually worked." \
    "curl -s http://localhost:8081/ | jq ."

show_step "3. Make a change" \
    "Edit app/main.go (something trivially visible), commit, and push to main. Narrate: 'this is the only step a human does — everything after this is automatic.'" \
    "git commit -am 'demo: visible change for the recording' && git push"

show_step "4. GitHub Actions run graph" \
    "Switch to the Actions tab and show the CI job graph: lint -> test -> build -> security -> sign -> gitops-update, all green." \
    "open https://github.com/fakhfakhahmed/gitops-cicd-platform/actions"

show_step "5. GitHub Security tab" \
    "Show the Code scanning alerts populated from the Trivy SARIF upload." \
    "open https://github.com/fakhfakhahmed/gitops-cicd-platform/security/code-scanning"

show_step "6. The automatic GitOps commit" \
    "Show the github-actions[bot] commit that bumped gitops/environments/dev/values.yaml's image.tag." \
    "git log --oneline -5 -- gitops/environments/dev/values.yaml"

show_step "7. Argo CD picks it up" \
    "Back in the Argo CD UI, show app-dev reconciling: a new ReplicaSet rolling out within a minute or two, with zero manual action." \
    ""

show_step "8. The app again, now with the new SHA" \
    "curl the root endpoint again and show the commit field now matches the commit from step 3. Full loop closed, on camera." \
    "curl -s http://localhost:8081/ | jq ."

echo ""
echo -e "\033[36mDemo complete. See docs/screenshots/README.md for the exact shot list if you're assembling a LinkedIn post instead of a video.\033[0m"
