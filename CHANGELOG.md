# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.2] - 2026-04-25

### Added

- Artifact Hub ownership claim — `artifacthub-repo.yml` now contains the real `repositoryID` UUID issued by Artifact Hub. The cronguard Helm chart is now discoverable at https://artifacthub.io/packages/helm/cronguard/cronguard with verified-owner status.

## [0.2.1] - 2026-04-25

### Fixed (critical)

- **`cronguard_*` metrics now actually exposed at `/metrics`.** v0.1.0–v0.2.0 registered the custom Prometheus collector, the reconcile counters, and `cronguard_build_info` on `prometheus.DefaultRegisterer`, but controller-runtime's `metricsserver` only serves `controller-runtime/pkg/metrics.Registry`. The `cronguard_*` namespace described in the README, alert rules, and Grafana dashboard was effectively absent in production. `cmd/main.go` now registers on `crmetrics.Registry`.
- **e2e workflow no longer masks failures.** `test/e2e/run-e2e.sh` had a layered EXIT trap (`kill $PF_PID 2>/dev/null || true; cleanup`) where `|| true` reset `$?` to 0 before `cleanup` captured it. Result: the metric-assertion step printed "MISSING metric" and `exit 1`, but the workflow still reported green — which is how the metrics defect above shipped through three releases. Trap now captures `$?` first and passes it explicitly to `cleanup $1`.

### Added

- Real, production-grade content in `docs/runbooks/` for all five default alerts (was placeholders in v0.2.0). Each runbook has Symptom / Why this matters / Quick triage / Common causes / Remediation / Related signals / Appendix sections with copy-paste `kubectl` and PromQL.
- Dependabot weekly schedule (Mon 07:00 MSK) for Go modules (grouped: kubernetes, controller-runtime, observability), GitHub Actions, and the operator Dockerfile.
- pre-commit hook framework (`.pre-commit-config.yaml`) running gofmt, govet, go-mod-tidy, golangci-lint, helm lint, promtool. Setup via `pip install pre-commit && pre-commit install`. Documented in new `docs/development.md`.
- Real asciicast at `docs/cast/install.cast` recorded against an actual `kind` cluster, plus `docs/cast/install.gif` rendered via `agg` for inline GitHub README embed (replaces the synthetic cast from v0.2.0).
- New `docs/development.md` with contributor guidance: pre-commit setup, Make targets, branching strategy, commit style, release process.

## [0.2.0] - 2026-04-25

### Added — Distribution & observability

- **Helm chart** at `charts/cronguard/` with full configurability (image, replicas, namespace scope, leader election, resources, security context, ServiceMonitor, PrometheusRule). CRD ships in `crds/` (Helm 3 native).
- **Grafana dashboard** at `config/grafana/cronguard-dashboard.json` — six panels (table, time-since-last-success stat, drift heatmap, last-duration timeseries, conditions state-timeline, reconcile rate). Vanilla JSON for Grafana OSS or Cloud.
- **PrometheusRule** at `config/prometheus/rules.yaml` — five default alerts (`CronGuardScheduleMissed`, `CronGuardConsecutiveFailures`, `CronGuardDurationExceeded`, `CronGuardNotReady`, `CronGuardOperatorDown`). Also available as opt-in chart template via `prometheusRule.enabled=true`.
- **Runbook stubs** at `docs/runbooks/` — placeholders linked from each alert's `runbook_url`. Concrete remediation lands in a follow-up patch.
- **Kind-based e2e workflow** `.github/workflows/e2e.yml` — spins up a real cluster, builds and loads the image, installs via Helm, applies sample manifests, scrapes `/metrics`, asserts required metric families. Runs on every push/PR plus nightly. `make e2e` for local runs.
- **Helm chart publishing** in `release.yml` — packages the chart and publishes to both `oci://ghcr.io/dmazhukov/charts/cronguard` (Helm 3.8+ OCI) and `https://dmazhukov.github.io/cronguard/` (GitHub Pages Helm repo). `peaceiris/actions-gh-pages@v4` creates the orphan branch on first run.
- **Artifact Hub publication** prep — `artifacthub-repo.yml` published alongside the chart; UUID populated post-registration.
- **README walkthrough** — collapsible install walkthrough in the top-level README plus a synthetic asciicast at `docs/cast/install.cast` and recording instructions for replacing it with a real session.
- **Distribution docs** at `docs/distribution.md` — three install paths documented end-to-end.

### CI

- New `prometheus-rules` CI job runs `promtool check rules` on the standalone manifest.
- New `e2e` workflow with concurrency group `e2e-${ref}`.
- Release workflow gains a `chart` job (depends on `release`) with `gh-pages` concurrency group.

## [0.1.2] - 2026-04-25

### Fixed
- `CachedLister.List` errors are now logged at V(1) instead of being silently swallowed.

### Removed
- Unused CertManager helpers from `test/e2e` and `test/utils` (Phase 1 has no webhooks).

### Tests
- Added envtest for ResourceVersion-conflict reconcile retry.
- Added unit tests for `cronguard_last_duration_seconds`, `cronguard_last_failure_timestamp_seconds`, `cronguard_next_expected_timestamp_seconds`, and `cronguard_running_jobs`.

### Refactor
- Extracted the Job→CronJobMonitor mapper from `SetupWithManager` into a named method.

## [0.1.1] - 2026-04-25

### Fixed
- `LastFailureTime` now populates from `StartTime` when Job `CompletionTime` is unset (K8s 1.35+ leaves Failed Jobs with nil `CompletionTime`); `cronguard_last_failure_timestamp_seconds` updates on every observed failure.
- CI workflow uses `golangci-lint-action@v7` to support golangci-lint v2.
- Release workflow pins kustomize install to a stable release tarball.
- `Result.Requeue` (deprecated) replaced with `RequeueAfter` to satisfy controller-runtime staticcheck.

### Added
- `Clock` field on the reconciler with a `clock.PassiveClock` seam for deterministic tests.
- Operational events on SLO transition crossings: Warning on `ScheduleHealthy`/`ExecutionHealthy`/`DurationHealthy` flipping to False, Normal on `Reconciled` recovering to True.
- envtest specs for missed-runs detection (using fake clock) and suspended CronJob behaviour.

### Hygiene
- Removed unused `configmaps` rules from leader-election RBAC (controller-runtime defaults to Lease).
- `runAsUser: 65532` set explicitly in manager Deployment securityContext.
- Stripped CertManager bootstrap from the e2e suite (Phase 1 has no webhooks).

## [0.1.0] - 2026-04-25

### Added
- Initial operator scaffold.
- `CronJobMonitor` CRD (`monitoring.cronguard.io/v1alpha1`).
- Controller reconciling schedule, duration, and execution SLO.
- Prometheus custom collector with `cronguard_*` metrics.
- Distroless multi-arch container image.
- `envtest`-backed controller tests.
