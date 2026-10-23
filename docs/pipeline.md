# Pipeline walkthrough: `ci.yml`

A stage-by-stage read of [`.github/workflows/ci.yml`](../.github/workflows/ci.yml),
in the order jobs actually run.

## `changes` — path filtering

Uses `dorny/paths-filter` to look at which files the push/PR actually
touched. Everything downstream is gated on its output: a change confined to
`docs/` runs no lint, test, build, scan, or sign job at all. This is not a
cosmetic optimization — it is what keeps feedback fast for documentation
PRs and keeps GitHub Actions minutes spent proportional to what actually
needs verifying.

## `lint` — `golangci-lint`

Runs against `app/`, using the exact config in `.golangci.yml` at the repo
root — the same config a developer runs locally via `make lint`. Catches
style and correctness issues (unchecked errors, shadowed variables, unused
code) before a single test executes. Fails fast and cheap.

## `test` — `go test -race -coverprofile`

The race detector is not optional here: `server.ready` and
`server.shuttingDown` in `app/main.go` are `atomic.Bool` values written by
one goroutine (the signal handler / warm-up goroutine) and read by every
request handler concurrently. A plain `go test` run would pass even with a
genuine data race, because races only manifest probabilistically depending
on goroutine scheduling — exactly the kind of bug that is invisible in CI
until it is a 3am page. `-race` makes the scheduler adversarial enough to
catch it reliably.

Coverage is uploaded as a build artifact (14-day retention) rather than
pushed to a third-party coverage service, to keep the demo self-contained —
wiring up Codecov/Coveralls is a one-line swap if this became a real
project.

## `build` — multi-arch image, GHCR push

1. **QEMU + Buildx**: lets a single `ubuntu-latest` (amd64) runner produce a
   genuine `linux/arm64` image via emulation. Without this, an arm64 user
   (Apple Silicon, AWS Graviton, a Raspberry Pi cluster) would either get no
   image or would get amd64 silently emulated at the *runtime* layer, which
   is far slower than a native build.
2. **`docker/metadata-action`**: computes every tag from git context alone —
   `main`, `sha-<short>`, `pr-<number>` — so there is no hand-written
   `if: github.ref == 'refs/heads/main'` tag logic anywhere to get subtly
   wrong.
3. **GHA layer cache** (`cache-from/to: type=gha`): a build with an
   unchanged `go.sum` reuses the dependency-download layer instead of
   re-fetching every module, which is most of a cold build's wall-clock
   time.
4. **`push:` is conditional** on this being a real push to `main` — a PR
   build still exercises the full multi-arch build (so a broken Dockerfile
   is caught in review) but never publishes an image for a branch that
   might never merge.
5. **`sbom: true` / `provenance: true`**: buildx's own embedded
   attestations, in addition to (not instead of) the standalone SPDX SBOM
   generated in the next job — this is what `docker buildx imagetools
   inspect` can show without any extra tooling.

**What breaks without this stage**: nothing downstream has an artifact to
scan, sign, or deploy. This is the pipeline's actual output.

## `security` — Trivy + SBOM

- **Trivy image scan**: scans the exact digest the `build` job produced (not
  the mutable `main` tag) for known CVEs in OS packages and the Go module
  graph. Results go to SARIF, uploaded to the repo's **Security tab** via
  `github/codeql-action/upload-sarif` — vulnerabilities become first-class,
  trackable findings instead of text buried in a build log.
- **Trivy filesystem scan**: a second pass over the *repository*, catching
  Dockerfile misconfigurations and IaC issues that an image-only scan would
  miss.
- **SBOM** (`anchore/sbom-action`, SPDX format): a complete, machine-readable
  manifest of every package and module in the image. This is the artifact
  that turns "are we affected by the next Log4Shell" into a `grep` across
  stored SBOMs instead of a scramble to re-scan every running image in the
  fleet.

**What breaks without this stage**: vulnerabilities ship silently. Nobody
finds out until a scanner *outside* this pipeline (or an attacker) does.

## `sign` — cosign keyless signing

Runs only on a real push to `main`, after both `build` and `security`
succeed. `permissions: id-token: write` is the entire mechanism: it lets
this job mint a short-lived OIDC token from GitHub's own identity provider,
which cosign exchanges with Sigstore's Fulcio for a signing certificate that
is valid for minutes and cryptographically bound to this exact workflow
run. There is no long-lived private key anywhere in this repo or its
secrets for an attacker to exfiltrate.

Signs **by digest**, not by tag — a signature over a mutable tag would stop
meaning anything the moment the tag moved. It also attests the SBOM as a
predicate, and finishes with a self-check `cosign verify` so a broken
signing step fails the pipeline immediately rather than silently producing
an unverifiable image.

**What breaks without this stage**: there is no way to prove, later, that an
image actually came from this pipeline and hasn't been tampered with or
swapped in the registry.

## `gitops-update` — the push-to-pull handoff

The **only** job in the whole pipeline with `contents: write`, and even
there it is scoped to one commit touching one file:
`gitops/environments/dev/values.yaml`. It installs `yq`, rewrites
`image.tag` to `sha-<short-sha>`, and commits as `github-actions[bot]` with
`[skip ci]` in the message (without that flag, the bot's own commit would
trigger a new CI run, which would produce another commit, forever).

This is the exact moment the pipeline's control model flips from **push**
(CI decides what happens next) to **pull** (Argo CD decides what happens
next, on its own schedule, by comparing git to the cluster). See
[`gitops.md`](gitops.md) for why that boundary is the whole idea.
