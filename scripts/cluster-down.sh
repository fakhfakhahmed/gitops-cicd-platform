#!/usr/bin/env bash
# Tears down the local minikube cluster used by this repo. Idempotent.
set -euo pipefail

PROFILE="gitops-platform"

step() { printf '\033[36m==> %s\033[0m\n' "$1"; }
ok()   { printf '\033[32m    %s\033[0m\n' "$1"; }

if ! command -v minikube >/dev/null 2>&1; then
    ok "minikube is not installed — nothing to tear down."
    exit 0
fi

if ! minikube profile list -o json 2>/dev/null | grep -q "\"Name\":\"${PROFILE}\""; then
    ok "Cluster '${PROFILE}' does not exist — nothing to do."
    exit 0
fi

step "Deleting minikube cluster '${PROFILE}'"
minikube delete -p "$PROFILE"

ok "Cluster '${PROFILE}' deleted."
