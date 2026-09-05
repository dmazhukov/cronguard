# CronGuard runbooks

Runbooks linked from the default `PrometheusRule` annotations. Each runbook covers symptom, likely cause, and remediation steps.

## Index

- [schedule-missed](schedule-missed.md) — `CronGuardScheduleMissed`
- [consecutive-failures](consecutive-failures.md) — `CronGuardConsecutiveFailures`
- [duration-exceeded](duration-exceeded.md) — `CronGuardDurationExceeded`
- [not-ready](not-ready.md) — `CronGuardNotReady`
- [operator-down](operator-down.md) — `CronGuardOperatorDown`
- [burn-rate-missed-runs](burn-rate-missed-runs.md) — `CronGuardMissedRunsBurnFast` / `CronGuardMissedRunsBurnSlow`

Each runbook has the same shape: Symptom, Quick triage, Common causes, Remediation, Appendix.

Background on how the numbers behind these alerts are defined: [ADR 0001](../adr/0001-cronjob-status-as-missed-run-floor.md) explains what counts as a missed run across operator downtime, and the `internal/schedule` package doc defines behaviour across daylight-saving transitions.
