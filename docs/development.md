# Development guide

## Pre-commit hooks

We use [pre-commit](https://pre-commit.com/) to catch lint, format, and basic
sanity issues before they hit CI.

### Setup

```bash
pip install pre-commit       # or `brew install pre-commit`
pre-commit install           # installs the git hook
pre-commit run --all-files   # one-time bulk pass to verify everything is clean
```

After `pre-commit install`, the configured hooks run automatically on each
`git commit`. To bypass (rarely): `git commit --no-verify`.

### What runs

- **trailing-whitespace, end-of-file-fixer, check-yaml, check-added-large-files,
  check-merge-conflict, check-case-conflict, mixed-line-ending** — generic
  hygiene from `pre-commit/pre-commit-hooks`.
- **go-fmt, go-vet, go-mod-tidy** — Go formatting and module hygiene.
- **golangci-lint-full** — same v2.8.0 config that runs in CI.
- **helm lint charts/cronguard** — chart sanity, only when `charts/` files change.
- **promtool check rules** — Prometheus rule validation, only when
  `config/prometheus/rules.yaml` changes.

The Helm and promtool hooks need `helm`, `yq`, and `promtool` on `$PATH`.
On macOS: `brew install helm yq prometheus`.

## Dependency updates

Dependabot opens weekly PRs against `main` every Monday at 07:00 MSK for:

- Go modules (grouped: `kubernetes`, `controller-runtime`, `observability`,
  ungrouped for the long tail) — label `dependencies`, `go`
- GitHub Actions — label `dependencies`, `github-actions`
- Docker base image (`Dockerfile`) — label `dependencies`, `docker`

Auto-merge can be enabled per-PR via `gh pr merge --rebase --auto` after CI
passes. Branch protection on `main` enforces the same five required checks
(`lint`, `test`, `build`, `vuln`, `analyze (go)`), so a Dependabot PR cannot
merge if it breaks anything.

## Local make targets

```bash
make manifests           # regen CRDs/RBAC from kubebuilder markers
make generate            # regen DeepCopy
make fmt vet
make lint                # golangci-lint
make test                # unit + envtest, writes cover.out
make build               # bin/manager
make docker-build IMG=cronguard:dev
make run                 # run against current kube-context
make e2e                 # kind-based end-to-end test
make install             # kubectl apply CRD
make deploy IMG=...      # apply manager Deployment
make undeploy
```

## Branching strategy

Trunk-based: `main` is the single long-lived branch. Feature branches are
short-lived (`feat/*`, `fix/*`, `chore/*`, `ci/*`, `docs/*`). All merges go
through pull requests with rebase-only merging — no squash, no merge commits.
Branch protection enforces linear history.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/). Scope is
encouraged for multi-package work:

```
feat(controller): emit events on SLO transitions
fix(metrics): handle nil EndTime for failed Jobs
docs(runbooks): real content for ScheduleMissed
chore(ci): bump golangci-lint-action to v7
```

## Releases

Tags follow [SemVer](https://semver.org/). Pre-1.0 (`0.x.y`) tolerates breaking
CRD changes at minor versions; the CHANGELOG calls them out.

To cut a release:

1. Update `CHANGELOG.md` (move `Unreleased` to a versioned block).
2. Bump `charts/cronguard/Chart.yaml` `version` and `appVersion`.
3. PR with the changelog/version bump.
4. After merge, tag locally and push:
   ```bash
   git tag -a v0.X.Y -m "..."
   git push origin v0.X.Y
   ```
5. The release workflow handles the rest: GitHub Release with assets, multi-arch
   image push to ghcr.io, Helm chart push to OCI + GitHub Pages.
