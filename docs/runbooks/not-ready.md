# CronGuardNotReady

## Symptom

`cronguard_condition{type="Ready"} == 0` for 10+ minutes — the aggregate `Ready` condition is False, meaning at least one of the four axis conditions (Reconciled, ScheduleHealthy, ExecutionHealthy, DurationHealthy) is False.

## Likely cause

- One or more of the more specific alerts (ScheduleMissed, ConsecutiveFailures, DurationExceeded) has fired
- The referenced CronJob no longer exists
- The schedule expression no longer parses (controller-side regression)

## Remediation

TODO — `kubectl describe cjmon <name>` to see which axis condition is False; follow the runbook for that specific condition.

## Related

- Aggregate condition: `Ready`
- Reason in status condition reveals which axis broke
