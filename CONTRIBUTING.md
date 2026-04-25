# Contributing to CronGuard

Thanks for your interest in improving CronGuard.

## Getting started

```bash
git clone https://github.com/dmazhukov/cronguard.git
cd cronguard
make test        # runs unit + envtest
```

`make test` is the single command used in CI. If it passes locally, your PR will pass the primary CI job.

## Workflow

1. Open an issue for significant changes before you start coding.
2. Fork, branch, implement.
3. Add or update tests.
4. Run `make lint test`.
5. Submit a PR with a clear description.

## Commit style

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(controller): handle CronJob suspend
fix(metrics): emit 0 for nil LastSuccessTime
docs(readme): add PromQL examples
```

## Code style

- `gofmt` + `goimports` via `make fmt`.
- Linters: `make lint` (golangci-lint).
- Keep files focused on one responsibility.
- Keep comments minimal — only explain why, not what.

## Testing philosophy

- Pure logic (schedule, history) uses stdlib `testing` with table-driven cases.
- Controller behaviour uses Ginkgo + Gomega with `envtest`.
- No mocked apiserver — only the real one embedded via `envtest`.

## Reporting security issues

See [SECURITY.md](SECURITY.md) — do not open public issues for vulnerabilities.
