# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.6] - 2026-04-27

### Fixed (critical)

- Container image now publishes under both `ghcr.io/dmazhukov/cronguard:vX.Y.Z` (matches the git tag) and `ghcr.io/dmazhukov/cronguard:X.Y.Z` (matches `Chart.AppVersion`). Previously only the `v`-prefixed tag was pushed, which meant `helm install` with default values rendered `image: ghcr.io/dmazhukov/cronguard:0.2.5` and got `ImagePullBackOff` — that tag didn't exist, only `:v0.2.5` did. Bug shipped silently from v0.2.0 because the e2e workflow overrides `image.tag` explicitly with the kind-loaded build, so the default-values install path was never tested against the published registry.

## [0.2.5] - 2026-04-27

### Fixed

- `artifacthub.io/category` corrected from `monitoring` to `monitoring-logging`. The previous value was silently rejected by Artifact Hub's category-enum validator since v0.2.0; the chart was indexed but absent from the `monitoring-logging` category filter on artifacthub.io.

## [0.2.4] - 2026-04-27

### Added

- `CronJobMonitor.spec.timeZone` (IANA name, e.g. `America/New_York`) — schedule evaluation now honors the timezone explicitly. Resolution order: `spec.timeZone` → referenced `CronJob.spec.timeZone` → UTC. Status surfaces the resolved zone in `status.resolvedTimeZone`. Closes phantom missed-run reports for CronJobs running outside UTC.
- `internal/schedule.ParseInLocation(expr, loc)` for callers that need explicit timezone binding. `Parse(expr)` now defaults to UTC instead of inheriting `time.Local`, eliminating the silent drift when the operator container's `TZ` env is unset.
- `time/tzdata` embedded in the binary so `time.LoadLocation` succeeds inside `distroless/static` (which omits `/usr/share/zoneinfo`). Image grows ~450 KB; schedule evaluation is now base-image-independent.

### Fixed

- `ReasonInvalidTimeZone` surfaces malformed `spec.timeZone` as `Reconciled=False` + `ScheduleHealthy=Unknown`, instead of reverting to UTC silently. Mirrors the existing `InvalidSchedule` failure path.

### CI

- `release.yml` now waits for `ci.yml` to finish on the tagged commit and aborts unless `conclusion=success`. Previously `release.yml` ran in isolation on tag push — a tag on a red-CI commit would still ship to GHCR/GH Pages/Artifact Hub. Polls every 30s for up to 30 minutes; missing CI run aborts after the first minute.

## [0.2.3] - 2026-04-27

### Fixed (correctness)

- `ScheduleHealthy` now flips to `Unknown` when `spec.schedule` is unparseable. Before, the axis stayed at the last successful value (typically `True/OnSchedule`), masking the configuration error.
- Reconcile honors the injected `Clock` everywhere. Two leftover `time.Now()` / `time.Until()` calls bypassed the seam; missed-runs envtest was non-deterministic by accident.
- `CachedLister.List` now uses a 5-second timeout context. Was `context.Background()` — uncancellable on manager shutdown.
- ResourceVersion-conflict envtest now asserts `apierrors.IsConflict(err)` instead of an incidentally-passing `Reconciled=True` check.
- `CronGuardOperatorDown` PrometheusRule alert no longer hardcodes `job=~"cronguard.*"`. The matcher now keys on `namespace` + `service` so it works for any release name.
- `make docker-buildx` recipe rewritten to use `buildx build` directly. The previous version's `sed`-on-Dockerfile produced a duplicate `FROM --platform` line.
- `release.yml` uses `helm package --version --app-version` flags instead of an in-place `sed` on `Chart.yaml`.

### Added

- Helm chart guard: `replicaCount > 1 && !leaderElection.enabled` now fails template render (was silent split-brain hazard).
- Helm chart `serviceAccount.create / name / annotations` block — standard pattern for IRSA / Workload Identity.
- Helm chart Deployment got `revisionHistoryLimit: 5` and explicit `RollingUpdate` strategy.
- `values.schema.json` got `additionalProperties: false` plus 14 missing key definitions; typos in user values now reject at install time.
- `ci.yml` and `codeql.yml` got concurrency blocks to cancel superseded PR runs.
- README metrics table: 2 rows that were missing (`cronguard_last_schedule_timestamp_seconds`, `cronguard_running_jobs`).
- All runbooks: install-path note explaining Helm vs raw-`install.yaml` resource naming differences.

### Changed

- Sample CronJob rewritten with full security context (compatible with restricted Pod Security Standards) and `*/2 * * * *` schedule (was `0 2 * * *` — useless for demos).
- e2e workflow uses `kubectl wait --for=condition=Reconciled` (was 30-iter polling) and waits for port-forward readiness via `curl` retries (was `sleep 3`).
- All Quickstart install commands across README, distribution.md, and chart README pin `--version 0.2.3`. Users landing on prior v0.2.0–v0.2.2 docs hit the metrics-not-exposed regression in v0.2.0.
- README Roadmap rewritten — the previous "Phase 2 (planned)" claim was wrong; v0.2.x had already shipped the entire Phase 2 (Helm chart, Grafana dashboard, PrometheusRule, ServiceMonitor, kind e2e, Artifact Hub).
- SECURITY.md SLA changed to realistic 1-week / 4-week best-effort (was 72h / 2 weeks for a solo maintainer).
- 5 runbooks: corrected `kubectl patch` payloads (the CRD has no `spec.slo` sub-object — patches were silently no-op'ing); removed references to two metrics that don't exist (`cronguard_max_duration_seconds`, `cronguard_condition_last_transition_timestamp_seconds`); replaced unsupported `--field-selector=status.successful=N` queries with `jq` filters; corrected CronJob controller event names (`MissingJob` / `FailedCreate` / `UnexpectedJob`).
- `docs/cast/README.md` now correctly documents `agg` as GIF-only (it was claimed to render SVG).
- RBAC tightened: dropped unused `create`/`delete` verbs on `cronjobmonitors` and `delete` on `leases`.
- Removed ~290 lines of kubebuilder-scaffolded commented-out webhook/cert-manager/prometheus/metrics-auth blocks across `config/default/kustomization.yaml` and `config/manager/manager.yaml`.

## [0.2.2] - 2026-04-25

### Added

- Artifact Hub ownership claim — `artifacthub-repo.yml` now contains the real `repositoryID` UUID issued by Artifact Hub. The cronguard Helm chart is now discoverable at https://artifacthub.io/packages/helm/cronguard/cronguard with verified-owner status.

## [0.2.1] - 2026-04-25

### Fixed (critical)

- **`cronguard_*` metrics now actually exposed at `/metrics`.** v0.1.0–v0.2.0 registered the custom Prometheus collector, the reconcile counters, and `cronguard_build_info` on `prometheus.DefaultRegisterer`, but controller-runtime's `metricsserver` only serves `controller-runtime/pkg/metrics.Registry`. The `cronguard_*` namespace described in the README, alert rules, and Grafana dashboard was effectively absent in production. `cmd/main.go` now registers on `crmetrics.Registry`.
- **e2e workflow no longer masks failures.** `test/e2e/run-e2e.sh` had a layered EXIT trap (`kill $PF_PID 2>/dev/null || true; cleanup`) where `|| true` reset `$?` to 0 before `cleanup` captured it. Result: the metric-assertion step printed "MISSING metric" and `exit 1`, but the workflow still reported green — which is how the metrics defect above shipped through three releases. Trap now captures `$?` first and passes it explicitly to `cleanup $1`.

### Added

- Filled in `docs/runbooks/` content for the five default alerts (placeholders in v0.2.0). Each runbook is structured Symptom / Triage / Causes / Remediation / Appendix, with copy-paste `kubectl` and PromQL.
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
