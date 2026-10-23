# Security: the supply-chain story

This document walks the full chain from "a developer pushes a commit" to
"a verifiably-untampered binary is running in a hardened pod," and what each
link in that chain is actually defending against.

## 1. Pinned GitHub Actions

Every `uses:` reference in every workflow under `.github/workflows/` is
pinned to a 40-character commit SHA, with a `# vX.Y.Z` comment for
readability:

```yaml
- uses: actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11 # v4.1.1
```

A floating tag (`@v4`) is a mutable pointer controlled by a third party. If
that third party's account or CI is compromised, they can move `v4` to
point at a malicious commit and every workflow across the internet that
trusts `@v4` runs it on its next trigger — with whatever `permissions:` that
workflow granted. This happened in the wild (`tj-actions/changed-files`,
March 2025). A SHA pin cannot be moved; `Dependabot` (configured in
`.github/dependabot.yml`) is what keeps these pins from going stale, by
opening a PR with the new SHA every time an action cuts a release — so the
pin is reviewed on every bump instead of trusted blindly forever.

## 2. Least-privilege tokens

The default `permissions:` block at the top of `ci.yml` is `contents: read`
— every job inherits read-only access to the repository unless it
explicitly declares more. Only two jobs ever get write access, and each is
scoped to exactly what it does:

- `build` / `sign`: `packages: write`, to push to GHCR — nothing else.
- `sign`: `id-token: write`, to request the OIDC token cosign needs — see
  below.
- `gitops-update`: `contents: write`, and even there it is one commit to one
  file.

If any one job's dependency chain were ever compromised, the blast radius is
bounded by that job's own declared permissions, not by whatever the whole
workflow could theoretically do.

## 3. Multi-arch builds

`docker buildx` with QEMU emulation produces genuine `linux/amd64` and
`linux/arm64` images from a single x86 runner. This is adjacent to security
rather than central to it, but it matters for supply-chain trust in one
specific way: without it, an arm64 consumer either gets no image, or
silently falls back to amd64-under-emulation at the *runtime* layer — a
performance and provenance surprise that undermines "the image we scanned
is the image you're running."

## 4. Trivy: vulnerability + misconfiguration scanning

Two scans run per build: an **image** scan (OS packages, Go module CVEs, by
digest) and a **filesystem** scan (Dockerfile misconfigurations, IaC
issues in the repo itself). Both upload SARIF to GitHub's Security tab via
`github/codeql-action/upload-sarif`, so findings are tracked as structured,
dismissable, trackable alerts rather than text in a build log nobody
revisits after the job goes green. A separate weekly run
(`security-scan.yml`) re-scans on a schedule, independent of any code
change, because a CVE disclosed against an *already-shipped* dependency is
exactly the case a per-push scan cannot catch.

## 5. SBOM (Software Bill of Materials)

`anchore/sbom-action` produces a complete SPDX-format manifest of every
package and Go module in the image. The value shows up the day a
widely-used dependency has an incident: instead of re-scanning every image
running in a fleet (which can take hours, assuming the tooling even still
exists for an old build), you `grep` the stored SBOMs for the affected
package name and get an answer in seconds. The SBOM is also what gets
cosign-attested in the next step, so its authenticity is provable, not just
its existence.

## 6. Cosign keyless signing

Every image pushed from `main` is signed with `cosign sign --yes`, using
**keyless** signing: `permissions: id-token: write` lets the job mint a
short-lived OIDC token from GitHub's identity provider, which cosign
exchanges with Sigstore's **Fulcio** for a signing certificate valid for
minutes, bound to the exact repository, workflow file, and run that
requested it. The signature (and the SBOM attestation built the same way)
is recorded in Sigstore's **Rekor**, a public, append-only transparency log.

The practical upshot: there is no private key in this repository, in GitHub
Secrets, or on anyone's laptop for an attacker to steal. Compromising the
signing step requires compromising the GitHub Actions run itself — and that
compromise would be visible as an anomalous entry in Rekor's public log,
forever.

Signing happens **by digest**, never by tag. A signature over `app:main`
stops meaning anything the instant `main` is repointed at different bytes —
the digest is what cannot move, so it's the only thing worth signing.

## 7. Distroless, non-root, read-only root filesystem

`app/Dockerfile`'s runtime stage is a distroless base image: no shell, no
package manager, no coreutils. An attacker who achieves RCE in the running
process has no `/bin/sh` to pivot to — there is nothing there to run.
`charts/app/values.yaml` pairs this with `runAsNonRoot: true` (UID 65532,
enforced by the kubelet, not just requested), `readOnlyRootFilesystem:
true` (nowhere to drop a webshell or a modified binary), `allowPrivilegeEscalation:
false`, every Linux capability dropped, and `seccompProfile:
RuntimeDefault` (blocks roughly 44 syscalls that container escapes
typically rely on). Each of these narrows what a successful compromise can
actually *do*, independently of whether the compromise itself was
prevented.

## 8. NetworkPolicy

Kubernetes' default pod network is flat: any pod can reach any other pod's
port, across namespaces, with no authentication. `charts/app/templates/networkpolicy.yaml`
flips the app's pods to default-deny and explicitly allows only ingress
from the ingress controller and Prometheus, plus the egress needed for DNS.
A compromised pod anywhere else in the cluster can no longer reach this
service directly — it has to go through the ingress controller, where
whatever auth and rate-limiting lives there actually applies. (Enforcement
depends on the CNI — see the long comment in `values.yaml`; minikube's
default CNI accepts but does not enforce these objects, which is itself a
useful fact to know before assuming a policy is protecting you.)

## What's missing today: admission-time verification

Everything above proves an image *was* signed and scanned. Nothing in this
repo today **enforces**, at the cluster's admission boundary, that only
signed images can run. A policy engine like **Kyverno** (or Gatekeeper)
with an `ImageVerify` policy would reject any pod spec referencing an
unsigned image before it is ever scheduled — moving the guarantee from "we
have evidence this was done" to "this cannot run unless it was done." This
is the first item in the README's "what I'd do next in production" list,
and it is the natural next step once this pipeline has a cluster that isn't
a disposable local minikube.
