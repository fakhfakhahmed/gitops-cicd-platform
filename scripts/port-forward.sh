#!/usr/bin/env bash
# Port-forwards the Argo CD UI and the app's dev Service in parallel.
# Ctrl+C stops both — the EXIT trap kills whichever background jobs are
# still running either way.
set -euo pipefail

step() { printf '\033[36m==> %s\033[0m\n' "$1"; }
ok()   { printf '\033[32m    %s\033[0m\n' "$1"; }

PIDS=()

cleanup() {
    step "Stopping port-forwards"
    for pid in "${PIDS[@]:-}"; do
        kill "$pid" >/dev/null 2>&1 || true
    done
}
trap cleanup EXIT INT TERM

step "Starting port-forward: argocd-server -> https://localhost:8080"
kubectl port-forward svc/argocd-server -n argocd 8080:443 >/tmp/pf-argocd.log 2>&1 &
PIDS+=($!)

step "Starting port-forward: app-dev -> http://localhost:8081"
kubectl port-forward svc/app-dev -n app-dev 8081:80 >/tmp/pf-app.log 2>&1 &
PIDS+=($!)

sleep 2

echo ""
ok "Argo CD UI:   https://localhost:8080  (user: admin)"
ok "App:          http://localhost:8081/"
ok "App health:   http://localhost:8081/healthz"
ok "App metrics:  http://localhost:8081/metrics"
echo ""
echo -e "\033[33mPress Ctrl+C to stop both port-forwards.\033[0m"

wait
