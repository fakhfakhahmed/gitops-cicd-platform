#!/usr/bin/env bash
# Starts (or reuses) a local minikube cluster with the ingress and
# metrics-server addons this repo depends on. Idempotent — safe to re-run.
set -euo pipefail

PROFILE="gitops-platform"

step() { printf '\033[36m==> %s\033[0m\n' "$1"; }
ok()   { printf '\033[32m    %s\033[0m\n' "$1"; }

if ! command -v minikube >/dev/null 2>&1; then
    printf '\033[31mminikube is not installed or not on PATH. Install it first:\n  https://minikube.sigs.k8s.io/docs/start/\033[0m\n' >&2
    exit 1
fi

step "Checking for an existing '${PROFILE}' cluster"
already_running=false
if status_json=$(minikube status -p "$PROFILE" -o json 2>/dev/null); then
    host_status=$(echo "$status_json" | grep -o '"Host":"[^"]*"' | cut -d'"' -f4 || true)
    kubelet_status=$(echo "$status_json" | grep -o '"Kubelet":"[^"]*"' | cut -d'"' -f4 || true)
    if [ "$host_status" = "Running" ] && [ "$kubelet_status" = "Running" ]; then
        already_running=true
    fi
fi

if [ "$already_running" = true ]; then
    ok "Cluster '${PROFILE}' is already running — reusing it."
else
    step "Starting minikube cluster '${PROFILE}'"
    minikube start \
        --profile "$PROFILE" \
        --cpus 4 \
        --memory 4096 \
        --driver docker \
        --kubernetes-version stable
    ok "Cluster started."
fi

step "Enabling required addons (ingress, metrics-server)"
for addon in ingress metrics-server; do
    status=$(minikube addons list -p "$PROFILE" -o json 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('$addon',{}).get('Status',''))" 2>/dev/null || echo "")
    if [ "$status" = "enabled" ]; then
        ok "Addon '${addon}' already enabled."
    else
        minikube addons enable "$addon" -p "$PROFILE"
        ok "Addon '${addon}' enabled."
    fi
done

step "Pointing kubectl at '${PROFILE}'"
kubectl config use-context "$PROFILE" >/dev/null

ok "Cluster is up. Run scripts/install-argocd.sh next (or scripts/bootstrap.sh for both steps)."
