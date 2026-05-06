# CronGuardMissedRunsBurn{Fast,Slow}

## Symptom

`CronGuardMissedRunsBurnFast` (severity warning) or `CronGuardMissedRunsBurnSlow` (severity info) is firing.

These two alerts use the [Google SRE Workbook](https://sre.google/workbook/alerting-on-slos/) two-window burn-rate pattern keyed on `cronguard_missed_runs_total`. They fire when the SLO error budget for "schedule fires on time" is being consumed faster than the budget allows.

| Alert | Fast window | Slow window | Burn-rate threshold (vs normal) |
|---|---|---|---|
| Fast | 5 min | 1 h | ≈ 14× (consumes 1 month budget in ~2h) |
| Slow | 1 h | 6 h | ≈ 2-3× (consumes 1 month budget in ~5d) |

User-visible impact: the underlying CronJob is missing scheduled fires — either kube-controller-manager isn't firing the slot, or `concurrencyPolicy: Forbid` is blocking it because the previous run is still alive.

## Quick triage (~3 min)

1. Identify the affected CronJobMonitor:
   ```bash
   kubectl get cronjobmonitor --all-namespaces \
     -o json | jq '.items[] | select(.status.missedRuns > 0) | {namespace: .metadata.namespace, name: .metadata.name, missed: .status.missedRuns, schedule: .status.resolvedSchedule}'
   ```

2. Look at its recent executions:
   ```bash
   kubectl describe cronjobmonitor <name> -n <ns>
   ```

3. Check the underlying CronJob for blocking conditions:
   ```bash
   kubectl get cronjob <ref-name> -n <ns> -o jsonpath='{.spec.concurrencyPolicy}{"\n"}'
   kubectl get cronjob <ref-name> -n <ns> -o jsonpath='{.status.active[*].name}{"\n"}'
   ```
   If `concurrencyPolicy: Forbid` and there are active Jobs, the cron is blocking new runs deliberately. Either the workload is hung (page on-call for the workload), or the schedule is too tight for the workload.

4. Check kube-controller-manager health:
   ```bash
   kubectl -n kube-system logs -l component=kube-controller-manager --tail=100 | grep -iE "cronjob|miss"
   ```

## Common causes

- **Long-running workload + `concurrencyPolicy: Forbid`** — the previous Job hasn't completed, so the next slot is skipped. Fix the workload runtime or relax the concurrency policy.
- **kube-controller-manager unhealthy** — affects every CronJob in the cluster, not just one. Coordinate with cluster-admins.
- **Tight schedule + long pod-startup** — the slot fires but the Job's pod takes longer to start than the slot interval; `gracePeriodSeconds` may need to increase.
- **CronJobMonitor has `spec.timeZone` mismatched with `CronJob.spec.timeZone`** — operator computes missed runs against `spec.timeZone`, but kube-controller-manager fires against `CronJob.spec.timeZone`. Check both:
  ```bash
  kubectl get cronjobmonitor <name> -n <ns> -o jsonpath='{.status.resolvedTimeZone}{"\n"}'
  kubectl get cronjob <ref-name> -n <ns> -o jsonpath='{.spec.timeZone}{"\n"}'
  ```

## Remediation

- Fast burn often resolves itself once the blocking Job completes — check for that first before paging the workload owner.
- For slow burn, the system isn't failing; the schedule is just chronically late. Consider relaxing `gracePeriodSeconds` or raising `alertAfterMissedRuns` if the cadence is acceptable.

## Why two windows

The pair (fast, slow) reduces both false positives (transient latency in scheduling) and false negatives (a slowly-deteriorating system). Either alone produces noise. See the SRE Workbook chapter linked above.

## Related signals

- `cronguard_missed_runs` — gauge of consecutive missed runs since last success. Companion to the `_total` counter; useful for "how bad is it right now."
- `cronguard_condition{type="ScheduleHealthy"}` — flips to 0 once `MissedRuns >= alertAfterMissedRuns`. Different threshold, complementary signal.
- `CronGuardScheduleMissed` alert (existing) — fires on the binary `ScheduleHealthy=0` condition, no rate sensitivity.
