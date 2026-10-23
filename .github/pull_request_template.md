## What

<!-- One or two sentences: what does this change do? -->

## Why

<!-- The problem this solves, or the capability it adds. Link an issue if one exists. -->

## Type of change

- [ ] Application code (`app/`)
- [ ] Helm chart (`charts/`)
- [ ] GitOps config / environment values (`gitops/`)
- [ ] CI/CD workflow (`.github/workflows/`)
- [ ] Documentation only
- [ ] Other (describe above)

## How was this verified?

- [ ] `make test` / `go test ./...` passes locally
- [ ] `helm lint charts/app` passes
- [ ] `helm template charts/app | kubeconform -strict` passes
- [ ] Rendered manifests reviewed manually (for a `gitops/environments/*` or chart template change)
- [ ] N/A (docs-only change)

## Rollout / rollback considerations

<!--
For a gitops/ or charts/ change: which environments does this affect once
merged, and what does rollback look like if it goes wrong? (Usually:
`git revert` this PR — see docs/runbook.md.) For a prod-affecting change,
note explicitly that app-prod requires a manual `argocd app sync` after
merge.
-->

## Checklist

- [ ] I have not hand-edited `gitops/environments/dev/values.yaml`'s `image.tag` (that line is owned by CI)
- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (checked by commitlint on the PR title)
- [ ] Any new GitHub Actions `uses:` references are pinned to a commit SHA, not a floating tag
