# Contributing

Thanks for taking an interest. This repository is a working delivery platform,
so the contribution rules exist to keep the pipeline itself trustworthy — not
as ceremony.

---

## Ground rules

1. **`main` is always deployable.** It is protected; nothing lands without a
   green CI run and a review.
2. **Every change to `main` triggers a deployment to `dev`.** CI rewrites the
   image tag in `gitops/environments/dev/values.yaml` and Argo CD reconciles.
   If you are not comfortable with your change reaching a running cluster
   minutes after merge, it is not ready to merge.
3. **Promotion to staging and production is a pull request**, raised by the
   `promote` workflow. Nobody clicks a deploy button; the audit trail is git
   history.
4. **Never hand-edit an `image.tag` in `gitops/environments/*/values.yaml`.**
   That line is machine-owned. Use the promote workflow.

---

## Branch strategy

Trunk-based development with short-lived branches. Long-lived release branches
are deliberately avoided — they encourage divergence and make the "what is
actually deployed?" question hard to answer, which is the exact question this
repository exists to answer.

```
main ──●────●────●────●────●──────────────────▶  always deployable, auto-deploys to dev
        \       /      \    /
         ●──●──●        ●──●                      feat/… and fix/… branches, < 3 days old
```

### Branch naming

| Prefix      | Use for                                        | Example                        |
| ----------- | ---------------------------------------------- | ------------------------------ |
| `feat/`     | New capability                                 | `feat/add-ratelimit-middleware` |
| `fix/`      | Bug fix                                        | `fix/readiness-race-on-sigterm` |
| `chore/`    | Tooling, deps, housekeeping                    | `chore/bump-trivy-action`       |
| `docs/`     | Documentation only                             | `docs/clarify-promotion-flow`   |
| `ci/`       | Pipeline changes                               | `ci/add-kubeconform-strict`     |
| `refactor/` | Behaviour-preserving restructuring             | `refactor/extract-config`       |

Delete the branch after merge. Squash-merge so `main` reads as one commit per
logical change — this is what makes the generated changelog useful.

---

## Conventional commits

Commit messages and **pull request titles** follow
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/). This is
enforced by `commitlint` in `.github/workflows/pr-validation.yml`, and it is
what drives automatic changelog generation in `release.yml`.

```
<type>(<optional scope>): <description>

[optional body]

[optional footer(s)]
```

### Types

| Type       | Meaning                                | Version bump |
| ---------- | -------------------------------------- | ------------ |
| `feat`     | A new feature                          | minor        |
| `fix`      | A bug fix                              | patch        |
| `perf`     | Performance improvement                | patch        |
| `refactor` | Neither fixes a bug nor adds a feature | patch        |
| `docs`     | Documentation only                     | none         |
| `test`     | Adding or correcting tests             | none         |
| `build`    | Build system or dependencies           | none         |
| `ci`       | CI configuration                       | none         |
| `chore`    | Anything else                          | none         |
| `revert`   | Reverts a previous commit              | patch        |

### Scopes used in this repo

`app`, `chart`, `gitops`, `ci`, `docs`, `scripts`, `security`

### Breaking changes

Append `!` after the type/scope **and** add a `BREAKING CHANGE:` footer:

```
feat(chart)!: require Kubernetes 1.27+ for the new PDB fields

BREAKING CHANGE: `podDisruptionBudget.unhealthyPodEvictionPolicy` requires
the PDBUnhealthyPodEvictionPolicy feature gate, GA in Kubernetes 1.27.
```

### Good vs bad

```
✅ feat(app): expose build commit on the root endpoint
✅ fix(chart): mount an emptyDir at /tmp so readOnlyRootFilesystem works
✅ ci(security): upload Trivy SARIF to the GitHub Security tab
✅ chore(deps): bump prometheus/client_golang to v1.20.5

❌ updated stuff
❌ WIP
❌ Fix bug
❌ feat: changes            <- describe the change, not that there is one
```

---

## Pull request flow

1. **Branch** from an up-to-date `main`.
2. **Commit** using conventional commits. Keep commits logically atomic.
3. **Run locally before pushing**:

   ```powershell
   make test
   make lint
   make chart-lint
   make chart-template   # then eyeball ./rendered/*.yaml
   ```

4. **Open the PR.** Fill in the template — in particular the *risk* and
   *rollback* sections. A PR that cannot describe its own rollback is a PR
   that has not been thought through.
5. **CI must be green.** `pr-validation.yml` runs yamllint, `helm lint`,
   `helm template | kubeconform --strict`, commitlint on the PR title, and
   posts a rendered-manifest diff as a comment. Read that diff — it is the
   closest thing to a `kubectl diff` you get before merge.
6. **Review.** At least one approval from a CODEOWNER. Reviewers are expected
   to look at the rendered manifest diff, not just the Helm template source.
7. **Squash-merge.** The squash title becomes the changelog entry, so it must
   be a valid conventional commit.

### What reviewers look for

- Does this change the rendered Kubernetes output? If so, is that intentional?
- Are resource requests/limits still sane?
- Does it widen the attack surface (new capability, new network path, new
  mounted secret)?
- Is there a rollback path that does not require a rebuild?

---

## Changing the Helm chart

The chart is a public contract. If you add a value:

1. Add it to `charts/app/values.yaml` **with a comment explaining it**.
2. Add it to `charts/app/values.schema.json` with a type and, where sensible,
   constraints. The schema is what stops a typo like `replicaCount: "3"`
   (string) from silently rendering an invalid Deployment.
3. Bump `version:` in `charts/app/Chart.yaml` (SemVer — the chart version is
   independent of the app version).
4. If the default behaviour changes, say so in the PR body under *risk*.

---

## Changing a workflow

- Pin new actions to a **full 40-character commit SHA**, with the human
  readable tag in a trailing comment:
  `uses: actions/checkout@08c6903cd8c0fde910a37f88322edcfb5dd907a8 # v5.0.0`
- Declare the minimum `permissions:` the job needs. The repo default is
  `contents: read`; escalate per-job, never per-workflow unless every job
  needs it.
- Add a `concurrency:` group if the workflow can be triggered more than once
  for the same ref.

Dependabot (`.github/dependabot.yml`) updates these SHAs weekly, so a pinned
action is a maintained action, not a frozen one.

---

## Reporting a security issue

Do **not** open a public issue for a vulnerability. Email the maintainer
directly. See the *Security* section of the README.

---

## Code style

| Language   | Tool                  | Enforced by                        |
| ---------- | --------------------- | ---------------------------------- |
| Go         | `gofmt`, `goimports`  | `.golangci.yml`, CI `lint` job     |
| YAML       | `yamllint`            | `.yamllint.yml`, PR validation     |
| Helm       | `helm lint`           | PR validation                      |
| Manifests  | `kubeconform -strict` | PR validation                      |
| Shell      | `set -euo pipefail`   | review                             |
| PowerShell | `$ErrorActionPreference = 'Stop'` | review                 |

Whitespace and line endings are handled by `.editorconfig`.
