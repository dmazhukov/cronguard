# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
