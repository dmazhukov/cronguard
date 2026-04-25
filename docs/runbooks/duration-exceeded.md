# CronGuardDurationExceeded

## Symptom

`cronguard_condition{type="DurationHealthy"} == 0` for 1+ minute — the most recently completed Job ran longer than `maxDurationSeconds`.

## Likely cause

- Workload growth: more data than the budget anticipated
- Performance regression in the Job code
- Slower dependencies (DB, API) inflating runtime
- Insufficient resources (CPU throttling) extending wall-clock time

## Remediation

TODO — profile the Job, compare current `cronguard_last_duration_seconds` to historical baseline, decide whether to raise the SLO or fix the regression.

## Related

- Metric: `cronguard_last_duration_seconds`
- Status condition: `DurationHealthy=False, reason=DurationExceeded`
- Spec field: `maxDurationSeconds`
