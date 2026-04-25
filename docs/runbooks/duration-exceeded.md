# CronGuardDurationExceeded

## Symptom

Alert fires when `cronguard_condition{type="DurationHealthy"} == 0` for at least 1 minute. The most recently completed Job ran longer than `maxDurationSeconds` configured on the `CronJobMonitor`.

User-visible impact: the Job is finishing — output is being produced — but it is finishing late. Downstream consumers see fresher data later than expected; back-to-back schedules may overlap or get squeezed.

## Why this matters

Duration is a budget, not a hard limit. The alert is `info` severity by design: it fires before things break, while the team still has slack to investigate before a true SLO violation. Ignoring it long enough turns it into a `CronGuardScheduleMissed` (overlap with `Forbid`) or a `CronGuardConsecutiveFailures` (Job killed by `activeDeadlineSeconds`).

## Quick triage (~2 min)

1. Inspect the recent execution history — how does the latest run compare to its peers?
   ```bash
   kubectl get cjmon <name> -n <ns> -o jsonpath='{.status.recentExecutions}' | jq '.[] | {start: .startTime, end: .completionTime, duration: .durationSeconds, success: .succeeded}'
   ```
2. Pull the latest Job's pod resource shape and current usage:
   ```bash
   kubectl get pod -n <ns> -l job-name=<latest-job> \
     -o jsonpath='{range .items[*]}{.spec.containers[*].resources}{"\n"}{end}' | jq
   kubectl top pod -n <ns> -l job-name=<latest-job> --containers   # requires metrics-server
   ```
3. Check the CronJob's defaults vs. what is actually happening:
   ```bash
   kubectl get cronjob <ref-name> -n <ns> -o yaml \
     | yq '.spec | {schedule, concurrencyPolicy, jobTemplate.spec.activeDeadlineSeconds}'
   kubectl get cronjobmonitor <name> -n <ns> -o jsonpath='{.spec.slo.maxDurationSeconds}'
   ```
4. Look for CPU throttling on the most recent pod:
   ```bash
   kubectl logs -n <ns> -l job-name=<latest-job> --tail=200 | grep -iE "throttl|slow|timeout"
   ```
5. If the pod restarted mid-run (eviction, OOM, node lost), the wall-clock measurement spans multiple attempts:
   ```bash
   kubectl get pod -n <ns> -l job-name=<latest-job> \
     -o jsonpath='{.items[*].status.containerStatuses[*].restartCount}'
   kubectl describe pod -n <ns> -l job-name=<latest-job> | grep -E "Last State|Reason|Message" | head
   ```
6. Quick Prometheus comparison — current vs. prior runs:
   ```promql
   cronguard_last_duration_seconds{namespace="<ns>",name="<name>"}
   ```

## Common causes

- Workload growth: more rows / files / events to process than the budget anticipated.
- Performance regression introduced by a recent code change (N+1 query, accidental sleep, unbounded retry).
- Slower downstream dependency (DB, API, S3) inflating per-call latency.
- CPU throttling: container is hitting its CPU limit and being descheduled by cgroups.
- Memory pressure causing GC churn or paging that extends wall-clock time.
- Pod evicted and restarted mid-run, so total time is spread across multiple chunks.
- Network throughput cap on data transfer (cross-AZ, NAT gateway saturation).

## Remediation

### Confirm whether it is genuine growth or a regression

Plot the last 30 days:

```promql
max_over_time(cronguard_last_duration_seconds{namespace="<ns>",name="<name>"}[30d])
```

If the curve is monotonically rising for weeks, it is growth — adjust the budget. If it stepped up after a deploy, it is a regression — find the offending change.

### Profile the workload

For Go workloads, ship a `pprof` capture from the next live run:

```bash
kubectl exec -n <ns> -ti <pod-of-latest-job> -- \
  curl -s http://localhost:6060/debug/pprof/profile?seconds=30 -o /tmp/cpu.pprof
kubectl cp <ns>/<pod>:/tmp/cpu.pprof ./cpu.pprof
go tool pprof -http=:0 ./cpu.pprof
```

For other runtimes, use the equivalent (py-spy, async-profiler, etc.).

### Raise the budget when growth is real

If the workload genuinely needs more time, lift `maxDurationSeconds` and document why in the manifest commit:

```bash
kubectl patch cronjobmonitor <name> -n <ns> --type=merge \
  -p '{"spec":{"slo":{"maxDurationSeconds":<new-budget>}}}'
```

If the new budget is close to the schedule interval, also bump `activeDeadlineSeconds` on the underlying CronJob and verify `concurrencyPolicy` will tolerate the longer runs.

### Lift CPU throttling

Inspect throttling directly via cgroup metrics in Prometheus:

```promql
sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{namespace="<ns>",pod=~"<job-prefix>.*"}[5m]))
  / sum by (pod) (rate(container_cpu_cfs_periods_total{namespace="<ns>",pod=~"<job-prefix>.*"}[5m]))
```

A throttled-fraction above ~0.1 is meaningful. Raise the CPU limit:

```bash
kubectl patch cronjob <ref-name> -n <ns> --type=json -p='[
  {"op":"replace","path":"/spec/jobTemplate/spec/template/spec/containers/0/resources/limits/cpu","value":"<new-limit>"}
]'
```

Consider removing the CPU limit entirely for batch workloads if your platform tolerates it; CPU requests still ensure scheduling, and removing the cap eliminates throttling.

### Slow downstream dependency

Confirm with the dependency's own metrics or a probe Job. If owner is on call, hand off; meanwhile widen the duration budget so this alert does not page repeatedly.

### Pod eviction mid-run

If `restartCount > 0`, the wall-clock includes a restart. Pin the pod to stable nodes (taints / `priorityClassName: system-cluster-critical` or a workload-specific high priority) and ensure resource requests are honest enough that the scheduler does not co-locate the Job with eviction-prone workloads.

## Related signals

- Metrics: `cronguard_last_duration_seconds`, `cronguard_last_schedule_timestamp_seconds`.
- Status condition: `DurationHealthy=False, reason=DurationExceeded`.
- Spec field: `spec.slo.maxDurationSeconds`.
- Adjacent runbooks: [schedule-missed.md](schedule-missed.md) (slow runs eventually block the next slot under `Forbid`), [consecutive-failures.md](consecutive-failures.md) (slow runs eventually trip `activeDeadlineSeconds` and become failures).

## Appendix

Top-10 worst-offender CronJobs in the cluster:

```promql
topk(10, cronguard_last_duration_seconds)
```

Ratio of last duration to its budget — anything above 1.0 is over-budget:

```promql
cronguard_last_duration_seconds
  / on(namespace, name) group_left() cronguard_max_duration_seconds
```

One-shot historical median for capacity planning:

```promql
quantile_over_time(0.5, cronguard_last_duration_seconds{namespace="<ns>",name="<name>"}[30d])
```
