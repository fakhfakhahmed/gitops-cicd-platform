# LEARN.md — the concepts behind this repo

This is the companion document to [`README.md`](README.md): where the
README shows *what* this platform does, this explains *why* it's built this
way, down to concepts worth understanding on their own. Read it top to
bottom as a primer, or jump to the "Interview questions" section if you
already know the territory and want to pressure-test your answers.

## CI vs CD vs GitOps

These three terms get used interchangeably and shouldn't be:

- **CI (Continuous Integration)**: automatically building and testing every
  change, on every push. In this repo, that's `lint` + `test` in `ci.yml`.
  CI's job is to answer "is this change safe to merge," nothing about
  deployment.
- **CD (Continuous Delivery/Deployment)**: automatically getting a change
  into a deployable artifact (Delivery) or all the way into production
  (Deployment) after CI passes. The `build` → `security` → `sign` chain in
  `ci.yml` is Continuous Delivery — it produces a signed, scanned artifact
  ready to deploy, but does not itself deploy to prod.
- **GitOps**: a *specific implementation strategy* for the "get it running"
  part of CD, where the desired state lives declaratively in git and a
  controller *inside* the target environment (Argo CD) pulls and reconciles
  it, rather than a pipeline pushing changes into the environment. Not every
  CD pipeline is GitOps — most aren't. This repo's `gitops-update` job is
  the boundary: everything before it is CD, everything after it (Argo CD's
  reconciliation) is GitOps.

## Push vs. pull deployment

**Push**: CI (or a human) holds credentials for the target environment and
actively calls it — `kubectl apply`, `helm upgrade --install`, an API call to
a deploy service. The deploying system reaches out.

**Pull**: something running inside the target environment watches a source
of truth (git, here) and reaches *out* to it on its own schedule, applying
whatever it finds. The target environment reaches in.

The security argument for pull: in a push model, CI needs standing
credentials to every environment it can deploy to, which means a compromised
CI pipeline can compromise every environment those credentials reach. In a
pull model, CI never holds a cluster credential at all — compromising CI at
worst lets an attacker make a bad git commit, which is visible in the commit
history, revertible, and (for `app-prod` specifically) gated behind a manual
sync that a compromised pipeline still cannot trigger by itself.

## Why the image tag lives in git

Because "what is deployed right now" needs exactly one unambiguous answer,
and it needs to survive the pipeline that produced it being gone. If that
answer lived in a CI run's logs, or a human's memory of "we deployed
Tuesday's build," reconstructing a lost cluster — or just auditing what
changed and when — would require archaeology. `gitops/environments/<env>/
values.yaml` is that one answer, versioned, diffable, and blamable with
`git blame`.

## Declarative vs. imperative

**Imperative**: a sequence of commands describing *how* to get to a state —
`kubectl scale deployment app --replicas=5`, run once, now. Nothing records
*why* 5, and running the same command twice from different starting points
can produce different results.

**Declarative**: a description of the *desired end state* — `replicas: 5` in
a values file — that a reconciler is responsible for achieving, however many
times you apply it. `kubectl apply` (and everything Helm/Argo CD do) is
declarative: applying the same manifest 100 times in a row is a no-op after
the first. This repo is declarative end to end; the only imperative action
anywhere in the normal flow is a human running `argocd app sync app-prod`.

## Reconciliation loops

A reconciliation loop is: observe actual state → compare to desired state →
if different, act to close the gap → repeat forever. Argo CD's
`application-controller` runs one of these per `Application`, roughly every
few minutes (plus immediately on a detected git change, if a webhook is
configured). This is also exactly what a Kubernetes controller (like the
Deployment controller reconciling ReplicaSets, or the HPA reconciling
replica count against observed CPU) does — Argo CD is, structurally, one
more controller, just one whose "desired state" source happens to be a git
repository instead of a Kubernetes object someone `kubectl apply`'d.

## Immutable tags vs. digests

A **tag** (`app:main`, `app:v1.2.3`) is a mutable pointer — whoever has push
rights to the registry can repoint it at different bytes at any time,
including after you've already pulled and tested the image it used to
point to. A **digest** (`app@sha256:abcd...`) is the cryptographic hash of
the image's content — it cannot be repointed; a different image has a
different digest, full stop.

This repo signs images **by digest**, and `charts/app/values.yaml`'s
`app.image` helper prefers a digest over a tag whenever one is set. A
signature over a tag is a signature over "whatever this currently points
to," which is a meaningless guarantee the moment the tag moves. Tags stay
useful as a human-readable label (`gitops/environments/dev/values.yaml`
still shows `sha-a1b2c3d` for readability); the digest is what the machinery
actually trusts.

## CI pipeline stages — what and why

Full per-stage detail lives in [`docs/pipeline.md`](docs/pipeline.md).
Quick reference:

| Stage | What it does | What breaks without it |
|---|---|---|
| `lint` | `golangci-lint` against `app/` | Style/correctness bugs reach review; slower, noisier PRs |
| `test` | `go test -race -coverprofile` | Data races and regressions ship silently |
| `build` | Multi-arch buildx, GHCR push by digest | No artifact exists for anything downstream |
| `security` | Trivy scan (image + fs) → SARIF, SPDX SBOM | Vulnerabilities ship with zero visibility |
| `sign` | cosign keyless sign + SBOM attestation, by digest | No way to later prove an image is untampered and genuinely from this pipeline |
| `gitops-update` | Bump `dev` `image.tag`, commit `[skip ci]` | Nothing ever actually reaches a cluster |

## Helm concepts

- **Templates**: Go `text/template` files under `charts/app/templates/`
  rendered against `.Values`, `.Release`, `.Chart`. `helm template` renders
  without installing — what CI uses to validate output.
- **Values precedence** (lowest to highest): the chart's own
  `values.yaml` → a parent chart's values (N/A here) → `-f`/`--values` files
  in the order given, later wins → `--set`/`--set-string`/`--set-json`.
  This repo uses only the first two: `charts/app/values.yaml` as the base,
  one environment overlay (`gitops/environments/<env>/values.yaml`) on top,
  via Argo CD's multi-source `$values` reference. Nothing uses `--set`,
  deliberately — an imperative command-line override is invisible to git
  and therefore not reproducible.
- **Helpers** (`_helpers.tpl`): named template partials (`app.name`,
  `app.fullname`, `app.labels`, `app.image`...) that compute a value exactly
  once so every template that needs it can't compute it differently. The
  single most important one here is `app.selectorLabels`: it is deliberately
  a *minimal* subset of `app.labels` because a Deployment's
  `spec.selector.matchLabels` is immutable after creation — if the selector
  ever included a field that changes between releases (like a version
  label), every upgrade would fail with "field is immutable."
- **`values.schema.json`**: a JSON Schema validated automatically by `helm
  template`/`helm install`/`helm lint`. Turns `replicaCount: "3"` (a string
  where an integer is required) or `autoscaling.minReplicas: 0` into a
  render-time failure — caught in CI, never reaches a cluster.

## Argo CD concepts

- **`Application`**: the core CRD — one per deployable unit per environment
  here (`app-dev`, `app-staging`, `app-prod`). Declares a source (repo +
  path + Helm values) and a destination (cluster + namespace).
- **`AppProject`**: the multi-tenancy boundary. `gitops/applications/
  appproject.yaml` restricts which source repos, destination namespaces, and
  resource kinds every `Application` in this project may use — an
  `Application` naming an unlisted repo or an unlisted resource kind is
  rejected outright, independent of any user's RBAC permissions.
- **Sync waves**: an annotation (`argocd.argoproj.io/sync-wave`) that orders
  resource application within a sync. This repo uses it at the
  `Application` level — `app-dev` is wave 0, `app-staging` wave 1, `app-prod`
  wave 2 — so a shared change (like a chart template fix) rolls through
  environments in order rather than hitting all three simultaneously.
- **Self-heal**: `syncPolicy.automated.selfHeal: true` makes Argo CD revert
  any drift (a hand `kubectl edit`, a manual scale) back to what git says,
  on its next reconcile loop. On by design for `dev`/`staging`; deliberately
  absent for `prod` — see the interview Q&A below.
- **Prune**: `syncPolicy.automated.prune: true` deletes cluster resources
  that were removed from git, instead of leaving them as orphans nobody
  remembers creating.
- **Sync windows**: a schedule (`gitops/applications/appproject.yaml`'s
  `syncWindows`) restricting *when* syncs — automated or manual — are even
  allowed to run, independent of the per-`Application` sync policy. A second,
  belt-and-braces control on top of `app-prod`'s manual-sync setting.

## Kubernetes concepts used here

- **Readiness vs. liveness, and the preStop sleep**: liveness answers "is
  this process wedged beyond recovery" (failure → restart); readiness
  answers "should this pod receive traffic right now" (failure → removed
  from Service endpoints, nothing restarted). `app/main.go`'s
  `handleHealthz` never checks dependencies — if it did, a dependency outage
  would restart every pod in the fleet simultaneously. The `preStop` sleep
  (`lifecycle.preStop` in `charts/app/values.yaml`, paired with the app's own
  `PRESTOP_DELAY`) exists because SIGTERM and "this pod's IP is removed from
  the Service" happen concurrently with no ordering guarantee — the sleep
  buys time for kube-proxy / the ingress controller's routing tables to
  catch up before the listener actually closes, which is what makes a
  rolling update genuinely zero-downtime instead of dropping a handful of
  requests on every deploy.
- **Rolling updates**: `maxSurge: 1, maxUnavailable: 0` means a new pod is
  created and must become Ready *before* an old one is removed — capacity
  never dips below the declared replica count during a deploy, at the cost
  of one extra pod's resources for the rollout's duration.
- **PDB vs. HPA interaction**: a `PodDisruptionBudget` only constrains
  *voluntary* disruptions (node drains, cluster-autoscaler scale-down) — it
  has no say over involuntary ones (a node crashing, an OOMKill). The HPA
  changes `spec.replicas`, which Argo CD must be told to `ignoreDifferences`
  on (see `gitops/applications/*.yaml`) or the two fight over that field
  forever, with Argo CD reporting permanent drift. `minAvailable:
  replicaCount` on the PDB is a classic self-inflicted outage: it permits
  zero evictions ever, which means `kubectl drain` — and therefore any node
  upgrade — blocks forever.
- **Requests vs. limits**: a request is what the scheduler reserves and
  guarantees under contention; a limit is a hard ceiling. This chart sets
  memory request == limit (memory is incompressible — exceeding it means
  OOMKill, there's no "slow down" option) but gives CPU a request with **no
  limit** (CPU is compressible via the CFS quota, and a CPU limit causes
  throttling stalls even when average utilization is low — see the extended
  comment in `charts/app/values.yaml`).
- **`securityContext` fields**: `runAsNonRoot` (kubelet-enforced, not just
  requested), `readOnlyRootFilesystem` (paired with an explicit `emptyDir`
  for the one place that needs to write), `allowPrivilegeEscalation: false`,
  `capabilities.drop: [ALL]`, and `seccompProfile.type: RuntimeDefault` —
  each narrows what a successful compromise of the running process can
  actually do, independent of whatever prevented the compromise.

## Supply chain

- **SBOM** (Software Bill of Materials): a machine-readable manifest of
  every package/module in an artifact. Generated here in SPDX JSON via
  `anchore/sbom-action`. Its value is realized the day a dependency has an
  incident: `grep` stored SBOMs for the affected package instead of
  re-scanning every running image in a fleet.
- **cosign keyless signing**: see [`docs/security.md`](docs/security.md)
  section 6 for the full mechanism — short-lived OIDC-derived certificates
  from Sigstore's Fulcio, recorded in the public Rekor transparency log, with
  no long-lived private key anywhere to steal.
- **SARIF**: Static Analysis Results Interchange Format — the schema Trivy's
  (and CodeQL's, and most SAST tools') findings are expressed in, which is
  what lets `github/codeql-action/upload-sarif` turn scan output into
  tracked, dismissable alerts on GitHub's Security tab instead of text in a
  log.

## Interview questions you can now answer

**1. How do you roll back a GitOps deployment?**
`git revert` the commit that changed the environment's `values.yaml`, then
push. For `dev`/`staging` (automated sync), Argo CD picks up the revert on
its own. For `prod`, follow it with `argocd app sync app-prod`. Never
`kubectl rollout undo` directly — with self-heal on, Argo CD reverts your
rollback right back to what git (incorrectly) still says.

**2. Why is your production Application manual-sync?**
Automated sync + selfHeal means "whatever git says becomes the cluster
state, unattended, the instant Argo CD notices." For prod specifically, that
removes a human from a decision that benefits from one — e.g., a promotion
PR merging while nobody's watching, or selfHeal reverting an emergency fix
mid-incident. Argo CD still continuously computes and shows
Synced/OutOfSync/Degraded either way; manual sync only removes its ability
to *act* on that without `argocd app sync` from a human.

**3. Difference between liveness and readiness probes?**
Liveness: "is this unrecoverably wedged — restart it?" Must never check
dependencies, or a dependency outage restarts the whole fleet at once.
Readiness: "should this pod receive traffic right now?" Failure removes it
from Service endpoints without restarting anything — the correct place to
express "warming up" or "draining."

**4. How do you prevent a compromised CI run from deploying to prod?**
Several independent layers: CI never holds cluster credentials at all (pull
model) — the worst it can do is a bad git commit. That commit only affects
`dev` automatically; reaching `prod` requires a reviewed promotion PR *and*
a manual `argocd app sync`. Each CI job also has least-privilege
`permissions:` (most are `contents: read`; only `gitops-update` gets
`contents: write`, scoped to one file). And every third-party Action is
SHA-pinned, so a compromised upstream Action can't retroactively change what
already-merged workflow runs.

**5. Why pin GitHub Actions to SHAs instead of version tags?**
A tag like `@v4` is a mutable pointer a third party controls; if their
account or CI is compromised, they can repoint `v4` at malicious code and
every consumer trusting that tag runs it on their next trigger — no
consumer-side change needed, no review, nothing. A SHA cannot be moved.
Dependabot (`.github/dependabot.yml`) is what keeps SHA pins from going
stale, by opening a reviewable PR with the new SHA on every release.

**6. What's the difference between CI, CD, and GitOps?**
See the section above — CI is "is this change safe," CD is "get a safe
change into a deployable/deployed artifact," GitOps is one specific pull-
based strategy for the deployment half of CD, where a controller inside the
target environment reconciles from a git-declared desired state rather than
a pipeline pushing into it.

**7. Why does the Helm chart omit a CPU limit?**
A CPU limit is enforced via the Linux CFS quota — the cgroup gets N
microseconds of CPU per 100ms period and freezes every thread once it's
used, even if the node has idle capacity sitting right there. For a
latency-sensitive service that produces p99 throttling spikes from routine
GC pauses or request bursts, even at low average utilization. The CPU
*request* already guarantees a fair share under real contention; the limit
only adds a way to be needlessly slow. (Trade-off: this moves the pod from
Guaranteed to Burstable QoS — still favorable for memory-pressure eviction
as long as memory request == limit, which it does here.)

**8. Why is the image signed by digest and not by tag?**
A tag is a mutable pointer; the moment it's repointed, a signature "over
that tag" no longer describes the bytes actually running. A digest is the
content hash — it cannot move, so it's the only thing worth cryptographically
signing. `gitops/environments/*/values.yaml` still carries a human-readable
tag; the deployment template prefers a digest when one is set.

**9. How does your pipeline achieve zero-downtime deploys?**
Three cooperating mechanisms: (a) `maxSurge:1, maxUnavailable:0` so a new
pod must be Ready before an old one is removed; (b) `minReadySeconds: 5` so
a pod has to stay healthy briefly before counting as available, catching a
pod that passes its first probe and then immediately dies; (c) the
readiness-flip-then-sleep shutdown sequence in `app/main.go` (flip
`ready=false`, keep serving for `PRESTOP_DELAY`, only then stop accepting
connections), which absorbs the propagation delay between "pod is being
terminated" and "every kube-proxy/ingress controller has actually stopped
routing to it."

**10. What does `values.schema.json` buy you that `helm lint` doesn't?**
`helm lint` mostly checks template syntax and a handful of best-practice
heuristics. The JSON Schema enforces *types and constraints on every value*
— `replicaCount` must be an integer, `autoscaling.minReplicas` must be
`>= 1`, etc. — failing `helm template` itself on a typo'd value, which means
it fails in `pr-validation.yml`, which means a malformed value never reaches
`helm install` against a real cluster.

**11. What's the purpose of the AppProject beyond organizing Applications?**
It's an authorization boundary independent of Argo CD RBAC: `sourceRepos`
rejects an `Application` pointing at a repo outside the allowlist (so a
compromised or careless actor with CR-creation rights can't point Argo CD
at an arbitrary repo and have it sync); `destinations` restricts which
namespaces/clusters; `clusterResourceWhitelist`/`namespaceResourceWhitelist`
restrict which Kubernetes *kinds* can even be created — a chart that starts
emitting a `ClusterRoleBinding` fails the sync loudly instead of silently
granting cluster-admin to something.

**12. What happens if the `gitops-update` job's commit triggers another CI
run, which triggers another commit, forever?**
It doesn't, because the commit message includes `[skip ci]`, which GitHub
Actions (and most CI systems) recognize and skip triggering a new run for.
This is explicitly called out in `ci.yml`'s `gitops-update` job as the thing
that prevents exactly that infinite loop.

**13. Why run the race detector in CI test, not just locally?**
Because `go test -race` only catches a race if the scheduler happens to
interleave goroutines in a way that exposes it, and that's probabilistic —
a race can pass locally ten times and still exist. Running it in CI on
every PR maximizes the chances of catching it before merge rather than
relying on any one developer's local run to get unlucky in the right way.
`app/main.go`'s `ready`/`shuttingDown` atomics are exactly the kind of
concurrent state a plain (non-race) test run would never flag.

**14. Why have both a per-push security scan (in `ci.yml`) and a separate
weekly scheduled one (`security-scan.yml`)?**
The per-push scan answers "is the artifact I just built safe, right now."
The weekly scan answers a different question: "has a vulnerability been
*disclosed since* in something I already shipped and am still running" — a
per-push scan structurally cannot catch that, because nothing changed to
trigger it.

**15. Why does the chart set `automount: false` on the ServiceAccount even
though the app never calls the Kubernetes API?**
Because the default behavior mounts a ServiceAccount token into every
container regardless of whether it's used, and that token is a live
Kubernetes API credential. If the app is ever compromised via RCE, that
unused, unnecessary token is a free credential for an attacker to pivot
with. Turning off automount costs the app nothing (it never needed it) and
removes a real escalation path.

## Common failure modes

See [`docs/runbook.md`](docs/runbook.md) for full procedures. Quick index:

- **`ImagePullBackOff`** — usually GHCR package visibility changed to
  private; needs an `imagePullSecrets` entry.
- **Argo CD `OutOfSync` loop that never resolves** — almost always the HPA
  and Argo CD fighting over `spec.replicas`; needs `ignoreDifferences` on
  that JSON pointer (already set in this repo's `Application` manifests —
  this is the failure mode that setting prevents).
- **`CrashLoopBackOff` from the read-only root filesystem** — some code path
  (app or an execed debug tool) tried to write outside the one mounted
  `emptyDir`; check `kubectl logs --previous` for "read-only file system."
- **HPA stuck reporting `<unknown>` targets, never scaling** — metrics-server
  isn't installed or isn't ready; `kubectl top pods` returning nothing
  confirms it.
