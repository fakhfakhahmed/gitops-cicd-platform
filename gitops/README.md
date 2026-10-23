# GitOps configuration

This directory is the **source of truth** for what runs in every environment.
Nobody — not a developer, not an on-call engineer, not CI itself — runs
`kubectl apply` or `helm upgrade` against a real cluster by hand. Argo CD
continuously reconciles whatever is in this directory (on the `main` branch)
onto the cluster, and the only way to change what is running is to change a
file here and get it merged.

If a value is wrong in a running cluster, the fix is a pull request against
this directory, never a `kubectl edit`. See [`../docs/gitops.md`](../docs/gitops.md)
for why, and [`../docs/runbook.md`](../docs/runbook.md) for how drift gets
corrected automatically when someone tries anyway.

## Layout

```
gitops/
├── bootstrap/            one-time cluster setup — namespaces, the Argo CD
│                          Helm values, and the single root Application a
│                          human applies by hand (everything downstream of
│                          that is managed by Argo CD itself)
├── applications/          one Argo CD Application per environment, plus the
│                          AppProject that scopes what they're allowed to do
├── applicationset/        the git-generator ApplicationSet pattern, shown as
│                          the alternative to hand-written Application files
│                          once you have more than a handful of environments
└── environments/
    ├── dev/values.yaml      rewritten automatically by CI on every merge
    ├── staging/values.yaml  rewritten only by a promotion PR (dev → staging)
    └── prod/values.yaml     rewritten only by a promotion PR (staging → prod),
                              and even then requires a manual Argo CD sync
```

## The promotion flow

```
   merge to main
        │
        ▼
 ci.yml builds, scans, signs the image, then the gitops-update job
 rewrites gitops/environments/dev/values.yaml and commits it
        │
        ▼
   Argo CD (automated sync) deploys to app-dev within ~minutes
        │
        │  a human decides dev looks good, runs:
        │  gh workflow run promote.yml -f from_env=dev -f to_env=staging
        ▼
 promote.yml reads the tag out of dev's values file, writes it into
 staging's, and opens a PULL REQUEST — a human reviews and merges it
        │
        ▼
   Argo CD (automated sync) deploys to app-staging
        │
        │  same promote.yml, from_env=staging to_env=prod
        ▼
 another promotion PR, reviewed and merged
        │
        ▼
   Argo CD app-prod is now OutOfSync (by design — manual sync, see
   gitops/applications/app-prod.yaml) until a human runs:
   argocd app sync app-prod
```

Two independent approvals stand between "an image was built" and "production
is running it": the promotion PR's review, and the manual sync. Dev gets
neither gate, on purpose — it is meant to be disposable and fast.

## Why a `values.yaml` per environment instead of per-environment charts

One chart (`charts/app`), three thin values overlays. The alternative —
`charts/app-dev`, `charts/app-staging`, `charts/app-prod` — means every
template change has to be made (and reviewed, and tested) three times and
will eventually drift. Here, a template bug fix is one PR that every
environment picks up the next time it syncs.
