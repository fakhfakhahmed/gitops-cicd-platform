# Screenshot list (for the LinkedIn post / README)

Capture these exact four, in this order, after running `scripts/demo.ps1`
(or `.sh`) against a working local bootstrap. Save them into this directory
as `01-argocd-tree.png`, `02-actions-run.png`, `03-security-tab.png`,
`04-app-response.png`, and reference them from `README.md`'s Screenshots
section.

1. **Argo CD application tree — Synced / Healthy.**
   The `root-app` tree view with `app-dev`, `app-staging`, `app-prod`
   expanded far enough to show their child resources (Deployment, Service,
   Ingress, HPA, PDB). Every node green. This is the single image that best
   communicates "this is a real, working GitOps setup" at a glance.

2. **GitHub Actions run graph — all jobs green.**
   A completed `ci.yml` run showing the full job graph: `changes` → `lint` +
   `test` → `build` → `security` → `sign` → `gitops-update`. The graph view
   (not the log view) is what reads instantly on a LinkedIn feed.

3. **GitHub Security tab — Trivy findings.**
   The Code scanning alerts page showing SARIF results imported from the
   Trivy scan, with the tool name ("Trivy") and severity visible. This is
   the single frame that proves "supply-chain security" is more than a
   README claim.

4. **The app's `/` endpoint — commit SHA on screen.**
   A terminal (or browser) showing `curl http://localhost:8081/` with the
   JSON response visible, specifically the `commit` field. Crop it next to
   a `git log -1` or the GitHub commit page showing the *same* SHA — that
   pairing is the actual proof-of-delivery screenshot.

## Tips for a clean capture

- Use a light terminal theme or increase font size before capturing —
  LinkedIn's image compression is not kind to small monospace text.
- Redact nothing that doesn't need redacting; a public demo repo gains
  credibility from being fully inspectable. Do blur an actual email address
  or token value if one is ever visible in a screenshot by accident.
- Keep the browser chrome out of the Argo CD and GitHub screenshots —
  crop to just the content area.
