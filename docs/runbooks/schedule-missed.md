# CronGuardScheduleMissed

## Symptom

Alert fires when `cronguard_condition{type="ScheduleHealthy"} == 0` for at least 5 minutes — a CronJob's expected runs have not started within the configured grace period.

## Likely cause

- `concurrencyPolicy: Forbid` blocked a new run while the previous one is still active
- Kubernetes scheduler delay or control-plane outage
- The referenced `CronJob` was paused (`suspend: true`) but the monitor is still tracking it
- Workload nodes saturated; pod could not schedule

## Remediation

TODO — fill in steps for triage and recovery (kubectl get cronjob/-o yaml, check Job history, inspect cluster events, scale up control plane if needed).

## Related

- Metric: `cronguard_missed_runs`, `cronguard_schedule_drift_seconds`
- Status condition: `ScheduleHealthy=False, reason=ScheduleMissed`
