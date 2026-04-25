# CronGuardScheduleMissed

## Symptom

Alert fires when `cronguard_condition{type="ScheduleHealthy"} == 0` for at least 5 minutes. CronGuard has observed that one or more expected runs of a CronJob did not start within the configured grace period.

User-visible impact: scheduled work is not happening — reports are not being delivered, batches are not running, retention sweeps are stalled.

## Why this matters

`ScheduleHealthy` is the SLO that the CronJob is starting on time. A missed run does not necessarily mean the previous run failed; it means the controller never created a Job for the slot at all, so any downstream system that depends on its output will silently fall behind. For pipelines with daily cadence, every missed slot is roughly a 24-hour delay until the next attempt.

## Quick triage (~2 min)

1. Inspect the monitor's status block — it tells you what CronGuard saw:
   ```bash
   kubectl get cronjobmonitor <name> -n <ns> -o yaml \
     | yq '.status | {missedRuns, lastScheduleTime, nextExpectedTime, conditions}'
   ```
   `missedRuns` is the count since the last successful start; `nextExpectedTime` is the slot CronGuard is waiting on.
2. Check the underlying CronJob spec — the most common gating settings live here:
   ```bash
   kubectl get cronjob <ref-name> -n <ns> -o yaml \
     | yq '.spec | {schedule, suspend, concurrencyPolicy, startingDeadlineSeconds}'
   ```
   Look for `suspend: true`, `concurrencyPolicy: Forbid`, or a tight `startingDeadlineSeconds`.
3. List recent Jobs owned by the CronJob — is one still running and blocking new starts?
   ```bash
   kubectl get jobs -n <ns> --sort-by=.metadata.creationTimestamp \
     -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
     -o custom-columns=NAME:.metadata.name,COMPLETIONS:.status.succeeded,ACTIVE:.status.active,AGE:.metadata.creationTimestamp
   ```
4. Read the CronJob's events for `JobAlreadyActive`, `MissingJob`, or `FailedNeedsStart`:
   ```bash
   kubectl describe cronjob <ref-name> -n <ns> | sed -n '/Events:/,$p'
   ```
5. Cluster events around the missed slot — scheduling failures show up here:
   ```bash
   kubectl get events -n <ns> --sort-by='.lastTimestamp' | tail -40
   ```
6. If pods exist but never started, check why:
   ```bash
   kubectl get pods -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
     -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,REASON:.status.reason,NODE:.spec.nodeName
   ```

## Common causes

- `concurrencyPolicy: Forbid` is in effect and the previous Job is still running, so the controller skips the new slot.
- `concurrencyPolicy: Replace` killed the previous Job mid-flight; the start happened but was masked.
- `suspend: true` was flipped on the underlying CronJob and CronGuard has not reconciled the transition yet.
- Kubernetes scheduler / control-plane latency (apiserver under load, controller-manager backlog).
- Node resource exhaustion: no node has enough CPU/memory for the pod, so scheduling fails.
- Cluster Autoscaler is provisioning a node, exceeding `startingDeadlineSeconds`.
- `ImagePullBackOff` on the Job's pod (private registry credentials expired, tag deleted).
- Missing ServiceAccount or RBAC the Job pod needs to start.

## Remediation

### Concurrency policy blocking starts

Do not lift `Forbid` reactively — it exists to protect the workload from overlap. Find why the previous Job is hanging:

```bash
kubectl get jobs -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
  --field-selector=status.successful=0 -o yaml | yq '.items[] | {name: .metadata.name, active: .status.active, startTime: .status.startTime}'
kubectl logs -n <ns> -l job-name=<stuck-job-name> --tail=200
```

If the previous Job is genuinely wedged (e.g., waiting on a deadlock), terminate it explicitly so the next slot can start:

```bash
kubectl delete job <stuck-job-name> -n <ns>
```

Consider raising the Job's `activeDeadlineSeconds` so future runs cap themselves rather than blocking the schedule indefinitely.

### CronJob suspended

If the suspension is intentional, also pause the monitor by removing/scaling down the CronJobMonitor or accept that the alert will silence itself once the next reconcile runs. To resume:

```bash
kubectl patch cronjob <ref-name> -n <ns> --type=merge -p '{"spec":{"suspend":false}}'
```

### Scheduler / control-plane delay

Check apiserver and controller-manager health on the cluster. If kube-controller-manager is missing scheduling cycles, the immediate fix is operator-level (cluster admin restarts the leader); for CronGuard, lengthen `gracePeriodSeconds` on the monitor so transient delays do not page:

```bash
kubectl patch cronjobmonitor <name> -n <ns> --type=merge \
  -p '{"spec":{"slo":{"gracePeriodSeconds":120}}}'
```

### Node resource exhaustion

Find which resource is short:

```bash
kubectl describe pod -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
  | grep -A2 "FailedScheduling"
kubectl top nodes
```

If the cluster is genuinely full, scale a node group or trim Job resource requests in the pod template.

### Cluster Autoscaler too slow

Increase `startingDeadlineSeconds` on the CronJob so a pending pod has time to land on a freshly provisioned node:

```bash
kubectl patch cronjob <ref-name> -n <ns> --type=merge \
  -p '{"spec":{"startingDeadlineSeconds":600}}'
```

### ImagePullBackOff

```bash
kubectl describe pod -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
  | grep -A4 "ImagePull"
```

Rotate the registry imagePullSecret if credentials expired, or republish the deleted tag.

### Missing RBAC / ServiceAccount

```bash
kubectl get sa -n <ns> $(kubectl get cronjob <ref-name> -n <ns> -o jsonpath='{.spec.jobTemplate.spec.template.spec.serviceAccountName}')
```

If the SA is missing, restore it from your manifest source of truth.

## Related signals

- Metrics: `cronguard_missed_runs`, `cronguard_schedule_drift_seconds`, `cronguard_last_schedule_timestamp_seconds`.
- Status condition: `ScheduleHealthy=False, reason=ScheduleMissed`.
- Adjacent runbook: [not-ready.md](not-ready.md) — `ScheduleHealthy=False` will also flip the aggregate `Ready` condition once the 10-minute window passes.

## Appendix

Per-monitor missed-runs panel in Grafana / Prometheus:

```promql
max by (namespace, name) (cronguard_missed_runs)
```

Schedule drift across the fleet (positive = late):

```promql
topk(10, cronguard_schedule_drift_seconds)
```

Operator log lines for one CJM (replace `<cjm-name>`):

```bash
kubectl -n cronguard-system logs deploy/cronguard --tail=500 \
  | grep -i "<cjm-name>"
```
