#!/usr/bin/env bash
# One command, empty machine to running GitOps platform: cluster-up,
# install-argocd, done.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo -e "\033[35m===========================================================\033[0m"
echo -e "\033[35m gitops-cicd-platform — full local bootstrap\033[0m"
echo -e "\033[35m===========================================================\033[0m"

"${SCRIPT_DIR}/cluster-up.sh"
"${SCRIPT_DIR}/install-argocd.sh"

echo ""
echo -e "\033[35m===========================================================\033[0m"
echo -e "\033[35m Bootstrap complete.\033[0m"
echo -e "\033[35m===========================================================\033[0m"
echo ""
echo "Next steps:"
echo "  scripts/port-forward.sh    # expose Argo CD UI + the app"
echo "  scripts/demo.sh            # scripted walkthrough for a demo/recording"
echo ""
echo "The 'root-app' Application will reconcile app-dev/staging/prod"
echo "automatically. app-dev deploys the 'main' image tag out of the box;"
echo "push a commit through CI to see the real end-to-end flow."
