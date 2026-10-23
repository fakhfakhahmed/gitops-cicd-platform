# Runbook

Operational procedures for this platform. Written for the on-call version of
you at 2am, not for a leisurely read.

## Rolling back a GitOps deployment

**Do not** use `kubectl rollout undo`. It changes the cluster directly,
which means the cluster now disagrees with git — and for `app-dev` /
`app-staging` (both `selfHeal: true`), Argo CD will notice that disagreement
and revert your rollback within one reconcile loop, usually under a minute.
You would appear to roll back, then silently roll forward again, with no
error anywhere.

The correct rollback is a **git revert**:

```bash
# Find the commit that bumped the tag you want to undo.
git log --oneline -- gitops/environments/<env>/values.yaml

# Revert it (creates a new commit, doesn't rewrite history).
git revert <bad-commit-sha>
git push origin main
```

For `app-dev`/`app-staging`, Argo CD's automated sync picks up the revert
commit and redeploys the previous tag automatically. For `app-prod`, follow
the revert with:

```bash
argocd app sync app-prod
```

Argo CD also keeps its own revision history (`argocd app history app-dev`),
which lets you `argocd app rollback app-dev <id>` as a faster *tactical*
fix during an active incident — but always follow it up with the matching
git revert, or the next sync (automated or manual) will undo your rollback
by re-applying whatever git still says.

## Handling a failed / stuck sync

```bash
argocd app get app-staging          # see the current sync/health status
argocd app diff app-staging         # see exactly what differs from git
```

Common causes, in order of likelihood:

1. **A CRD is missing** (`no matches for kind "ServiceMonitor"`) — the
   target cluster doesn't have the Prometheus Operator installed but
   `serviceMonitor.enabled: true` is set for that environment. Fix: install
   the CRD, or set `serviceMonitor.enabled: false` for that environment.
2. **A resource is stuck `Progressing` forever** — check
   `kubectl describe deployment <name> -n <ns>` for the actual event
   (ImagePullBackOff, CrashLoopBackOff — see below — or a failed readiness
   probe). Argo CD reports "Progressing" as long as the Deployment hasn't
   hit `progressDeadlineSeconds` (600s here); it will flip to "Degraded"
   after that, which is itself a useful signal that something is actually
   wrong rather than just slow.
3. **A sync hook or resource hook is failing** — `argocd app get <name>`
   shows per-resource health; find the specific resource and
   `kubectl describe` it.

If the Application is `OutOfSync` and automated sync should have handled it
but hasn't: check `argocd app get <name> -o json | jq .status.operationState`
for a stuck or errored sync operation, and `argocd app sync <name> --force`
as a last resort (this does a fresh apply rather than relying on the
existing operation state).

## `ImagePullBackOff` from a private GHCR package

GHCR packages inherit the visibility of the repository that published them.
If `fakhfakhahmed/gitops-cicd-platform` (or its packages) is ever made
private, every pod referencing `ghcr.io/fakhfakhahmed/gitops-cicd-platform/app`
starts failing with `ImagePullBackOff` / `401 Unauthorized`.

Fix: create an `imagePullSecret` and reference it:

```bash
kubectl create secret docker-registry ghcr-pull-secret \
  --docker-server=ghcr.io \
  --docker-username=<github-username> \
  --docker-password=<a token with read:packages scope> \
  --namespace=<app-namespace>
```

Then set, per environment in `gitops/environments/<env>/values.yaml`:

```yaml
imagePullSecrets:
  - name: ghcr-pull-secret
```

## `CrashLoopBackOff` from the read-only root filesystem

`securityContext.readOnlyRootFilesystem: true` means the ONLY writable path
in the container is the `tmpDir` emptyDir mounted by the chart (default
`/tmp`, see `charts/app/templates/deployment.yaml`). Any code path — the
app's own, or a debugging tool someone execs in — that tries to write
anywhere else fails immediately, and if that write happens during startup
the container crash-loops.

Diagnose: `kubectl logs <pod> --previous` right after the crash — a
`permission denied` or `read-only file system` error on a specific path
confirms this. Fix is either to make that write path go through the
existing `tmpDir` mount, or add another `emptyDir` volume + mount for it in
`charts/app/values.yaml`'s `volumes` / `volumeMounts` — never to disable
`readOnlyRootFilesystem` as the fix.

## HPA not scaling ("unknown" targets)

```bash
kubectl get hpa <name> -n <ns>
# TARGETS column shows <unknown>/70% instead of a real percentage
```

The HPA (`autoscaling/v2`) reads CPU/memory utilization from the
**metrics-server** aggregated API — if it isn't installed (or isn't ready
yet) the HPA has nothing to read and reports `<unknown>` forever, never
scaling in either direction. On minikube:

```bash
minikube addons enable metrics-server
kubectl top pods -n <ns>   # should return numbers once it's ready
```

(`scripts/cluster-up` already enables this addon — this is what to check if
the HPA still looks broken in a hand-built cluster.)

## Rotating the GHCR token used by CI

CI authenticates to GHCR with `secrets.GITHUB_TOKEN` — the automatically
generated, run-scoped token GitHub issues for every workflow run, not a
long-lived personal access token. There is nothing to rotate in normal
operation: a new token is minted per run and expires when the run ends.

If a long-lived PAT is ever introduced instead (for example, to let a
script running *outside* Actions push to GHCR), rotate it by:

1. Generate a new fine-grained PAT scoped to exactly `write:packages` on
   this repository, with the shortest acceptable expiry.
2. Update the consuming secret (`gh secret set GHCR_PAT`).
3. Revoke the old PAT in GitHub Settings → Developer settings → Personal
   access tokens.
4. Confirm the next workflow run using it succeeds before considering the
   rotation complete.
