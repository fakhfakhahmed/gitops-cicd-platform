#!/usr/bin/env bash
# Installs Argo CD into the cluster, bootstraps the AppProject and root
# Application, and prints everything needed to log in. Idempotent.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="argocd"
ARGOCD_VERSION="v2.13.2"
MANIFEST_URL="https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

step() { printf '\033[36m==> %s\033[0m\n' "$1"; }
ok()   { printf '\033[32m    %s\033[0m\n' "$1"; }

if ! command -v kubectl >/dev/null 2>&1; then
    printf '\033[31mkubectl is not installed or not on PATH.\033[0m\n' >&2
    exit 1
fi

step "Applying namespaces (gitops/bootstrap/namespace.yaml)"
kubectl apply -f "${REPO_ROOT}/gitops/bootstrap/namespace.yaml"

if kubectl get deployment argocd-server -n "$NAMESPACE" >/dev/null 2>&1; then
    ok "Argo CD is already installed in namespace '${NAMESPACE}' — skipping install, will still wait for rollout."
else
    step "Installing Argo CD ${ARGOCD_VERSION}"
    kubectl apply -n "$NAMESPACE" -f "$MANIFEST_URL"
fi

step "Waiting for argocd-server rollout (this can take a few minutes on first install)"
kubectl rollout status deployment/argocd-server -n "$NAMESPACE" --timeout=300s

step "Applying AppProject and root Application"
kubectl apply -f "${REPO_ROOT}/gitops/applications/appproject.yaml"
kubectl apply -f "${REPO_ROOT}/gitops/bootstrap/root-app.yaml"

step "Retrieving initial admin password"
if kubectl get secret argocd-initial-admin-secret -n "$NAMESPACE" >/dev/null 2>&1; then
    PASSWORD=$(kubectl get secret argocd-initial-admin-secret -n "$NAMESPACE" -o jsonpath='{.data.password}' | base64 -d)
    ok "Admin password: ${PASSWORD}"
    ok "(This secret is deleted automatically the first time you change the password.)"
else
    ok "argocd-initial-admin-secret not found — the admin password may already have been changed."
fi

echo ""
echo "Argo CD is installed. Next steps:"
echo "  1. Port-forward the UI:"
echo "       scripts/port-forward.sh"
echo "  2. Open:   https://localhost:8080  (user: admin)"
echo "  3. Watch the 'root-app' Application sync its children (app-dev/staging/prod)."
