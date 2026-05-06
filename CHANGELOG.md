# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-04-30

Production hardening release. Closes the four gaps surfaced after v0.2.x reached production-grade quality and exited the post-review polish wave (v0.2.7).

### Added

- **CEL admission validation** on `CronJobMonitorSpec` via `+kubebuilder:validation:XValidation` markers. Apiserver-side rejects `spec.timeZone` typos (`est`, `usa/new_york`), shapes `spec.schedule` (must be 5-token whitespace-separated, `@descriptor`, or `CRON_TZ=`/`TZ=` prefix), and enforces cross-field invariants (`gracePeriodSeconds < 86400`, `alertAfterMissedRuns ≤ historyLimit`, `maxConsecutiveFailures ≤ historyLimit`). No webhook server, no cert-manager dependency.
- **Burn-rate SLO alerts** on `cronguard_missed_runs_total` (new counter; existing `cronguard_missed_runs` gauge stays). Two alerts following the SRE Workbook two-window pattern: `CronGuardMissedRunsBurnFast` (5m+1h, ~14× burn, warning) and `CronGuardMissedRunsBurnSlow` (1h+6h, ~2× burn, info). Chart values for thresholds; new runbook at `docs/runbooks/burn-rate-missed-runs.md`. ConsecutiveFailures burn-rate intentionally deferred to v0.4 — different ratio semantics.
- **HA metrics deduplication.** New `internal/leader` package + `cmd/main.go` integration that flips `cronguard.io/role` label on the local pod between `leader` and `standby` based on `mgr.Elected()`. Chart's `ServiceMonitor` adds a `relabelings: keep regex: leader` rule when `replicaCount > 1`. Result: with HA install, Prometheus scrapes only the active replica; previously both pods served `/metrics` with identical labels and every gauge was doubled.

### Fixed

- **Drift annotation re-stamping.** `internal/history.Merge` now carries `ExpectedStartTime` and `DriftSeconds` from a replaced record when the incoming record has them nil. Fixes the case where a Job transitioning Running → Succeeded/Failed lost its drift annotation in `RecentExecutions[]`. The aggregate `cronguard_schedule_drift_seconds` gauge was unaffected.
- **CEL `timeZone` regex relaxed** to accept all valid IANA shapes (single-segment names like `GMT`, `MST`, `Universal`; offsets like `Etc/GMT+3`, `Etc/GMT-7`). The original regex over-rejected these even though `time.LoadLocation` accepts them.
- **`cronguard_missed_runs_total` counter map thread-safety.** Added `sync.Mutex` around the `lastMissed` tracking map; previously safe only because `MaxConcurrentReconciles` defaults to 1, but now correct under any concurrency setting.

### CI

- New required RBAC marker: `pods/{get,patch}` on the operator's own namespace (chart: namespaced Role + RoleBinding; kustomize: `config/rbac/self_role.yaml` + `config/rbac/self_role_binding.yaml`). Used by the new HA Labeler in `cmd/main.go`. Limited to the operator's own namespace via Role+RoleBinding (chart) or namespaced Role (kustomize).

## [0.2.7] - 2026-05-05

Code-review follow-up. No user-facing breaks; mostly correctness fixes
in the reconciler's error paths plus polish across the chart, image,
and CI surfaces.

### Fixed (correctness)

- Reconciler early-return paths (CronJob not found / suspended / invalid
  schedule / invalid timezone) now reset `MissedRuns` and
  `ScheduleDriftSeconds` to 0 so the matching metrics no longer emit
  pre-error values (suspend exempted per Phase 1 spec §5.6 — the
  missed-run counter is intentionally frozen across suspend).
- `LastScheduleTime` / `LastSuccessTime` / `LastFailureTime` are now
  monotonically non-decreasing. Previously, when the only successful
  record rotated out of the bounded `RecentExecutions` ring buffer,
  `LastSuccessTime` dropped to nil and `cronguard_last_success_timestamp_seconds`
  reported 0 — confusing step-down that didn't match reality.
- Spec §5.6 missing warning event implemented: when `CronJobMonitor.spec.schedule`
  differs from `CronJob.spec.schedule`, the controller emits a `ScheduleMismatch`
  warning, gated on `observedGeneration` so it fires once per spec change
  rather than every reconcile.
- Spec §5.6 missing 30s requeue implemented: all four early-return paths
  now `RequeueAfter: 30s` rather than relying on RateLimited backoff.
- Per-reconcile event spam on stuck states fixed. Early-return event
  emission gated via transition detection (`shouldEmitReconciledEvent`);
  events fire only when prior state was nil or different reason. CRL
  with a CronJob that's been deleted no longer produces ~60 etcd writes
  per hour.

### Fixed (other)

- Standalone `config/prometheus/rules.yaml` `CronGuardOperatorDown` alert
  switched from `up{job=~"...controller-manager-metrics.*"} == 0` (which
  silently never-fired for non-default scrape configs) to
  `absent(cronguard_build_info) == 1`. Independent of the user's
  Prometheus job-naming.
- Removed `config/prometheus/monitor.yaml` and `monitor_tls_patch.yaml`
  scaffold ServiceMonitor — its `scheme: https` did not match the
  HTTP-only metrics server in `cmd/main.go`. Nothing referenced it.
- Runbook `docs/runbooks/not-ready.md` corrected: the `Reconciled=False`
  reason names listed (`cronJobNotFound`, `parseScheduleError`,
  `statusUpdateConflict`) didn't match the controller's actual reason
  constants (`CronJobNotFound`, `InvalidSchedule`, `InvalidTimeZone`,
  `CronJobSuspended`). Rewrote with correct values and added
  ResourceVersion-conflict triage in terms of metric rate, not a
  fictional reason.

### Added

- Reconciler structured logging via logr at three levels:
  - V(0): error paths
  - V(1): per-reconcile entry, early-return reasons (set
    `--zap-log-level=1` to enable)
  - V(2): schedule resolution detail, requeue choice, post-reconcile
    summary (set `--zap-log-level=2`)
- Helm chart: `extraArgs`, `extraEnv`, `topologySpreadConstraints`,
  opt-in `podDisruptionBudget` (refuses single-replica install with a
  templating-time `fail`).
- Chart `templates/NOTES.txt` enhanced with runbook pointer, CRD-upgrade
  `kubectl apply` URL, and replicaCount > 1 metric-doubling caveat.
- Image `org.opencontainers.image.*` labels (source, description,
  licenses, version, revision, created). `image.source` is what GHCR
  uses to link the image to the source repo; supply-chain tools
  consume the rest.
- CI `trivy` image-scan step (HIGH/CRITICAL severity, fixed CVEs only).
- CI `manifests-clean` job — fails the PR if `make manifests generate`
  produces a diff against checked-in CRD/RBAC/deepcopy outputs.
- CI `chart-lint` job — `helm lint` plus `kubeconform -strict` against
  default and full-opt-in chart renders. Phase 2 spec §6 acceptance
  criterion was previously not wired up.
- API `+listType=map +listMapKey=type` markers on `Conditions` and
  `+listType=atomic` on `RecentExecutions`. Server-side apply now
  merges per-condition rather than overwriting the whole list.

### Changed

- Helm chart selector labels hardcoded to `app.kubernetes.io/name: cronguard`
  (literal). Previously templated by `nameOverride` — a re-render with a
  different override would have produced an immutable-selector `helm upgrade`
  failure. Cosmetic label customization stays available via the broader
  `cronguard.labels` helper.
- Helm chart bumped to v0.2.7. AppVersion to 0.2.7.
- Quickstart pins in `README.md`, `charts/cronguard/README.md`, and
  `docs/distribution.md` bumped from `0.2.2` to `0.2.6`. The pins had
  been stuck at `0.2.2` despite four releases since. README Roadmap
  updated to mention shipped timezone-aware schedules (`spec.timeZone`)
  under v0.2.x.

### CI

- `release.yml` gained a `bump-docs` job that runs after `chart` publishes
  successfully and opens a PR via `peter-evans/create-pull-request@v8`
  with the version pins bumped to `${tag#v}` across README, chart README,
  and `docs/distribution.md`. Idempotent (no-op if pins are already
  current). Closes the v0.2.2 → v0.2.6 doc-drift class of bug.

### Removed

- Dead kubebuilder Go-e2e suite (`test/e2e/e2e_test.go`,
  `e2e_suite_test.go`, the entire `test/utils/` package). Was never
  invoked by CI; would not have worked against the actual chart install
  in any case (kustomize-style names, HTTPS port that the binary doesn't
  open). Real e2e via `test/e2e/run-e2e.sh` is unchanged.
- `make test-e2e`, `setup-test-e2e`, `cleanup-test-e2e` Makefile targets,
  and `KIND` / `KIND_CLUSTER` vars (only used by the removed targets).
- `dependabot.yml` controller-runtime `< 0.24.0` ignore rule. v0.24.0
  shipped upstream (PR #53), so the kubernetes group can bump it via
  the normal weekly cycle.

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
