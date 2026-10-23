# Architecture

Three diagrams: the end-to-end delivery flow, the environment promotion
flow, and the supply-chain security chain. Each is referenced from
[`README.md`](../README.md); this document is where the detail lives.

## End-to-end delivery flow

```mermaid
flowchart LR
    Dev[Developer] -->|git push / PR| GH[GitHub]
    GH -->|pr-validation.yml| PRCheck[yamllint + helm lint\n+ kubeconform + commitlint]
    GH -->|ci.yml on merge to main| CI[CI Pipeline]

    subgraph CI Pipeline
        Lint[lint] --> Test[test -race]
        Test --> Build[build\nmulti-arch buildx]
        Build --> Security[security\nTrivy + SBOM]
        Security --> Sign[sign\ncosign keyless]
    end

    Build -->|push by digest| GHCR[(GHCR\nghcr.io/.../app)]
    Sign -->|signature + attestation| GHCR
    Sign --> GitOpsUpdate[gitops-update job]
    GitOpsUpdate -->|yq bump image.tag,\ncommit as github-actions bot| Repo[(gitops/environments/dev/values.yaml)]

    Repo -.->|polled / webhook| ArgoCD[Argo CD]
    ArgoCD -->|pull + render + diff| Cluster[(Kubernetes cluster)]
    ArgoCD -->|selfHeal| Cluster

    GHCR -.->|image pull| Cluster
```

The arrow that matters most is the dotted one from the GitOps repo into Argo
CD: CI's job ends at a git commit. Nothing in `ci.yml` holds a kubeconfig,
calls `kubectl`, or calls the Argo CD API. See
[`gitops.md`](gitops.md) for why that split is the entire point of GitOps.

## Environment promotion flow

```mermaid
flowchart TD
    Merge[Merge to main] --> CIBuild[ci.yml builds + signs image]
    CIBuild --> DevBump["gitops-update job bumps\ngitops/environments/dev/values.yaml"]
    DevBump --> DevSync["Argo CD: app-dev\n(automated sync + selfHeal)"]

    DevSync -->|looks good, human decides| PromoteDS["workflow_dispatch: promote.yml\nfrom_env=dev to_env=staging"]
    PromoteDS --> PRStaging["Promotion PR against\ngitops/environments/staging/values.yaml"]
    PRStaging -->|human reviews & merges| StagingSync["Argo CD: app-staging\n(automated sync + selfHeal)"]

    StagingSync -->|looks good, human decides| PromoteSP["workflow_dispatch: promote.yml\nfrom_env=staging to_env=prod"]
    PromoteSP --> PRProd["Promotion PR against\ngitops/environments/prod/values.yaml"]
    PRProd -->|human reviews & merges| ProdOOS["Argo CD: app-prod is now\nOutOfSync (manual sync policy)"]
    ProdOOS -->|human runs\nargocd app sync app-prod| ProdSync[Production updated]
```

Two human checkpoints stand between "an image exists" and "production is
running it": the promotion PR's review, and the manual `argocd app sync`.
Dev gets neither — it is meant to move as fast as CI can push it.

## Supply-chain security chain

```mermaid
flowchart LR
    Source[Source code] --> Build["docker buildx build\nmulti-arch, GHA cache"]
    Build --> Image["Image pushed to GHCR\nby digest"]
    Image --> Scan["Trivy scan\n(image + SBOM)"]
    Scan -->|SARIF| SecurityTab[GitHub Security tab]
    Scan -->|SPDX| SBOM[SBOM artifact]
    Image --> OIDC["GitHub OIDC token\n(id-token: write)"]
    OIDC --> Fulcio["Sigstore Fulcio\nshort-lived cert"]
    Fulcio --> CosignSign["cosign sign --yes\n(by digest)"]
    SBOM --> CosignAttest["cosign attest\n(SBOM predicate)"]
    CosignSign --> Rekor["Sigstore Rekor\n(public transparency log)"]
    CosignAttest --> Rekor
    Rekor --> Verify["cosign verify\n(CI self-check today;\nKyverno admission-time, future work)"]
```

No private key exists anywhere in this chain for an attacker to steal —
every signature is backed by a certificate that is valid for minutes and
bound to the exact workflow run that produced it, which Rekor records
publicly and permanently. See [`security.md`](security.md) for the full
write-up, including what admission-time verification would add on top of
this.
