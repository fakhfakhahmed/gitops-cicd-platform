# GitOps: push vs. pull, drift, and why prod is different

## Push vs. pull

A **push** deployment model means CI has the credentials and makes the
final call: at the end of a pipeline, a job runs `kubectl apply` or `helm
upgrade` directly against the cluster. The pipeline is the one artifact in
your whole system that can change production, which means its credentials
(a kubeconfig, a service account token) have to live somewhere CI can reach
— typically a repository or organization secret.

A **pull** deployment model — GitOps — inverts that. CI's job ends at a git
commit. Something running *inside the cluster* (Argo CD, here) continuously
compares the desired state in git against the cluster's actual state and
reconciles any difference, on its own loop, using credentials that never
leave the cluster. CI never holds a cluster credential at all; the worst a
compromised CI run can do is make a bad git commit, which is visible,
revertible, and — for prod — still gated behind a human-run sync (see
below).

This repo's `ci.yml` stops at the `gitops-update` job: it writes one line in
`gitops/environments/dev/values.yaml` and pushes. Argo CD, watching that
repository from inside the cluster, does everything after that.

## Why the image tag lives in git

`gitops/environments/<env>/values.yaml` is the only place "what is
currently deployed" is recorded. Not a deployment script's local state, not
a value someone typed into a CI variable, not tribal knowledge about "we
deployed Tuesday's build." If the cluster disappeared entirely, rebuilding
it from this repository would reproduce the exact same state — same image
digest, same replica counts, same resource limits — because every one of
those facts is a line in a file under version control.

## Drift and self-heal

"Drift" is any difference between what git says should be running and what
is actually running: someone ran `kubectl scale deployment app --replicas=5`
by hand, or `kubectl edit` to patch an env var during an incident, or a
resource was deleted outright.

With `syncPolicy.automated.selfHeal: true` (set on `app-dev` and
`app-staging` in `gitops/applications/`), Argo CD treats any such drift as
an error state and reverts it on its next reconcile loop — usually within
seconds. This sounds aggressive until you consider the alternative: without
self-heal, a hand-edit sits there, unaudited, until *someone notices it
doesn't match git*, which in practice means it sits there until the next
`helm upgrade` or Argo CD sync silently reverts it anyway, except now it's a
surprise instead of a policy.

## Why `app-prod` is manual-sync

`gitops/applications/app-prod.yaml` sets no `syncPolicy.automated` block at
all — Argo CD's way of expressing "watch and report status, but never act
without a human telling you to." This is the one deliberate inconsistency
in an otherwise fully-automated pipeline, and it exists for a specific
reason:

Automated sync + selfHeal means "whatever git says becomes the cluster
state, unattended, the moment Argo CD notices." For dev and staging that is
exactly the point — fast feedback, no human in the loop for low-risk
environments. For production, "unattended" is the problem: a promotion PR
merging while nobody is watching, or a selfHeal reconcile firing in the
middle of an active incident and reverting the very `kubectl` mitigation an
engineer just applied, are both real failure modes with real precedent in
production outages.

With manual sync, Argo CD still continuously computes and displays
Synced/OutOfSync/Degraded for `app-prod` in real time — nothing about
observability is lost. The only thing manual sync removes is Argo CD's
ability to *act* on that observation without a person running
`argocd app sync app-prod` (or clicking Sync in the UI) first. That single
click is the last of two independent gates on anything reaching prod — the
promotion PR's review being the first (see
[`architecture.md`](architecture.md) for the full promotion diagram).

## App of apps

`gitops/bootstrap/root-app.yaml` is the only Argo CD `Application` object a
human ever applies directly (via `scripts/install-argocd`). It points at
`gitops/applications/` and, with `automated.prune + selfHeal`, treats every
file in that directory as a child `Application` to create, update, or
delete. Adding environment #4 is a `git add` of one YAML file; removing one
is deleting that file. No `kubectl apply` run by a human against a live
cluster is ever part of changing what environments exist.

`gitops/applicationset/environments.yaml` shows the pattern you'd reach for
once "one file per environment" stops scaling — ten ephemeral preview
environments, twenty regional clusters — by generating `Application`
objects from a git-directory generator instead of hand-writing each one. It
intentionally is not wired into `root-app.yaml`, so it doesn't fight the
hand-written Applications over ownership of `dev`/`staging`/`prod`.

## Values precedence in this setup

Each environment's Argo CD `Application` combines **two sources**: the
chart itself (`charts/app`) and that environment's values file
(`gitops/environments/<env>/values.yaml`), via Argo CD's multi-source
`$values` reference. This is Helm's own `-f` layering, expressed
declaratively: `charts/app/values.yaml` is the base, the environment file
overrides exactly the fields that differ. One chart, three thin overlays,
zero duplicated templates.
