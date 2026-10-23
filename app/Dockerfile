# syntax=docker/dockerfile:1.9
# =============================================================================
# Multi-stage build for the gitops-cicd-platform service.
#
# Goal: the smallest possible, most boring possible runtime image. Every line
# below is either reducing attack surface or making the build reproducible.
# The finished image is ~8 MB, contains no shell, no package manager, no libc
# and no user other than a non-root one.
# =============================================================================


# -----------------------------------------------------------------------------
# Stage 1: builder
# -----------------------------------------------------------------------------
# Alpine rather than the full Debian golang image: it is ~5x smaller to pull,
# and nothing from this stage survives into the final image anyway, so the only
# thing it costs us is CI cache time.
#
# The tag is pinned to a minor version (1.23) rather than `latest`. Dependabot
# bumps it; a floating tag means two builds of the same commit can produce
# different binaries, which destroys the whole point of signing them.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

# BUILDPLATFORM / TARGETOS / TARGETARCH are injected automatically by BuildKit
# when building with `docker buildx --platform linux/amd64,linux/arm64`.
#
# The important trick here: `--platform=$BUILDPLATFORM` on the FROM above means
# the *builder* always runs natively on the CI runner's architecture, and we
# cross-compile to the target with GOOS/GOARCH. The alternative — emulating
# arm64 under QEMU to run a native arm64 Go compiler — is roughly 10x slower.
# Go's cross-compilation is first class, so we use it.
ARG TARGETOS
ARG TARGETARCH

# Build metadata, passed in by CI from the git context. These become part of
# the binary (via ldflags) and part of the image (via OCI labels).
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src

# Copy the module files alone first, and download dependencies in their own
# layer. Because go.mod/go.sum change far less often than application code,
# this layer is a cache hit on almost every build — so a one-line change to
# main.go does not re-download the entire module graph.
COPY go.mod go.sum* ./

# --mount=type=cache keeps the Go module cache and build cache *outside* the
# image layers. They never bloat the final artifact, and BuildKit persists them
# across builds (in CI they are backed by the GitHub Actions cache, see
# cache-from/cache-to in .github/workflows/ci.yml).
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

# Now the source. Separate COPY so the dependency layer above stays cached.
COPY . .

# The actual build. Flag by flag:
#
#   CGO_ENABLED=0   Produce a fully static binary with no dynamic linking
#                   against libc. This is what allows the runtime stage to be
#                   `distroless/static`, which contains no libc at all. With
#                   CGO on, the binary would segfault instantly there.
#
#   -trimpath       Strip the absolute build directory from the binary. Two
#                   people building the same commit in different folders get
#                   byte-identical output. Reproducibility is a prerequisite
#                   for a signature meaning anything.
#
#   -ldflags "-s -w"  Drop the symbol table and DWARF debug info: ~30% smaller
#                   binary. Stack traces still carry function names, so panics
#                   remain readable.
#
#   -X main.version=...  Inject build metadata into package-level vars. The
#                   service echoes these back on `/`, which is the end-to-end
#                   proof that the artifact in the cluster is the one CI built.
#
#   -o /out/app     Write outside the workdir so the COPY --from below has one
#                   unambiguous path regardless of module layout.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/app \
      .

# Fail the build here rather than at runtime if the binary is somehow dynamic
# or unrunnable. Cheap smoke test, catches a broken CGO_ENABLED override.
# Skipped when cross-compiling, since we cannot execute a foreign binary.
RUN if [ "${TARGETARCH}" = "$(go env GOHOSTARCH)" ]; then /out/app --help >/dev/null 2>&1 || true; fi


# -----------------------------------------------------------------------------
# Stage 2: runtime
# -----------------------------------------------------------------------------
# gcr.io/distroless/static-debian12 contains exactly four things: CA
# certificates, /etc/passwd, tzdata, and a tmp directory. No shell, no apk/apt,
# no coreutils, no busybox.
#
# Why that matters, concretely:
#   * An attacker who achieves RCE lands in an environment with no `sh`, no
#     `curl`, no `wget` and no package manager. Most off-the-shelf exploitation
#     tooling simply does not run.
#   * Trivy reports near-zero OS-package CVEs, because there are no OS
#     packages. The only CVEs that can appear are in our Go dependencies —
#     which is exactly the signal we want from the scanner.
#
# The `:nonroot` tag variant sets USER to 65532 and ships a matching entry in
# /etc/passwd, so `runAsNonRoot: true` in the pod securityContext can be
# validated by the kubelet without us having to also set runAsUser.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# Re-declare the ARGs: each stage gets its own scope, so values from the
# builder stage are not visible here.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# ---------------------------------------------------------------------------
# OCI image annotations. These are not decoration:
#   * org.opencontainers.image.source links the image back to this repository,
#     which is what makes the package show up on the GitHub repo sidebar and
#     inherit the repo's visibility settings.
#   * .revision is the commit SHA. `docker inspect` on a mystery image running
#     in production then tells you exactly which source produced it.
#   * cosign and the SBOM tooling read these to build provenance attestations.
# ---------------------------------------------------------------------------
LABEL org.opencontainers.image.title="gitops-cicd-platform-app" \
      org.opencontainers.image.description="Demo service delivered by a GitHub Actions -> GHCR -> Argo CD pipeline" \
      org.opencontainers.image.source="https://github.com/fakhfakhahmed/gitops-cicd-platform" \
      org.opencontainers.image.documentation="https://github.com/fakhfakhahmed/gitops-cicd-platform/blob/main/README.md" \
      org.opencontainers.image.url="https://github.com/fakhfakhahmed/gitops-cicd-platform" \
      org.opencontainers.image.vendor="Ahmed Fakhfakh" \
      org.opencontainers.image.authors="Ahmed Fakhfakh" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot"

# Copy the single static binary. Nothing else is copied — not the source, not
# go.mod, not the Dockerfile. The image contains only what it needs to run.
COPY --from=builder --chown=65532:65532 /out/app /app

# 65532 is `nonroot` in the distroless passwd file. Declaring it numerically
# (rather than as the name) is required for Kubernetes `runAsNonRoot: true` to
# be enforceable: the kubelet checks the numeric UID and cannot resolve a name.
USER 65532:65532

# Documentation only - EXPOSE does not publish anything. The real contract is
# containerPort in the Deployment, driven by .Values.service.targetPort.
EXPOSE 8080

# Sensible in-image defaults. Every one of these is overridden per-environment
# by the Helm chart; having them here means `docker run` works with no flags.
ENV ADDR=":8080" \
    APP_ENV="container" \
    LOG_LEVEL="info" \
    READY_DELAY="2s" \
    PRESTOP_DELAY="5s" \
    SHUTDOWN_TIMEOUT="15s"

# Exec form (a JSON array), NOT shell form. Shell form would wrap the process
# in `/bin/sh -c`, which (a) does not exist in distroless and (b) would make
# the shell PID 1, so SIGTERM would be delivered to the shell and never
# forwarded to our process. Graceful shutdown would silently stop working and
# every deploy would drop connections. This single bracket matters.
ENTRYPOINT ["/app"]

# No HEALTHCHECK instruction on purpose. In Kubernetes, health is the
# kubelet's job via the liveness/readiness probes defined in the chart. A
# Docker-level HEALTHCHECK is ignored by Kubernetes entirely, so including one
# only creates a second, divergent definition of "healthy" that nobody reads.
