# CronGuardNotReady

## Symptom

Alert fires when `cronguard_condition{type="Ready"} == 0` for at least 10 minutes. `Ready` is the aggregate condition: it is True only when all four axis conditions — `Reconciled`, `ScheduleHealthy`, `ExecutionHealthy`, `DurationHealthy` — are True. If any one of them is False, `Ready` is False.

User-visible impact: at least one SLO axis is broken on this monitor. Because `Ready` rolls up four signals, this page is a "something is wrong" indicator; the specific failure is named in the `reason` field of the False sub-condition.

> Resource names below assume the default Helm install (`helm install cronguard ...`) into namespace `cronguard-system`. For the `kubectl apply -f install.yaml` install path, the Deployment is `cronguard-controller-manager` and the Service is `cronguard-controller-manager-metrics-service` — substitute accordingly.

## Why this matters

Ready aggregates four sub-conditions. This page exists to catch combinations the narrower alerts miss — for example a `Reconciled=False` (no narrower alert exists for the controller half), or two axes flapping such that one is always False. Triage means routing to the right specific runbook.

## Quick triage (~2 min)

1. Find which axis is False — `Ready` aggregates four:
   ```bash
   kubectl describe cronjobmonitor <name> -n <ns> | sed -n '/Conditions:/,/Events:/p'
   ```
   Or, more concisely:
   ```bash
   kubectl get cronjobmonitor <name> -n <ns> -o json \
     | jq '.status.conditions[] | {type, status, reason, message, lastTransitionTime}'
   ```
2. The `reason` on the False condition tells you the next runbook:
   - `Reconciled=False` → see "Reconciler failures" below.
   - `ScheduleHealthy=False` → [schedule-missed.md](schedule-missed.md).
   - `ExecutionHealthy=False` → [consecutive-failures.md](consecutive-failures.md).
   - `DurationHealthy=False` → [duration-exceeded.md](duration-exceeded.md).
3. Cross-check with Prometheus — sometimes multiple axes are False at once:
   ```promql
   cronguard_condition{namespace="<ns>",name="<name>"} == 0
   ```
4. Pull the controller's recent log lines for this CJM:
   ```bash
   kubectl -n cronguard-system logs deploy/cronguard --tail=500 \
     | grep -iE "<cjm-name>|<ref-name>"
   ```
5. Check controller-side reconcile errors (if Reconciled is the offender):
   ```promql
   sum by (namespace, name) (rate(cronguard_reconcile_total{result="error"}[5m]))
   ```

## Common causes

- One of the specific axis alerts is firing or about to fire — follow that runbook instead.
- Reconciler is stuck on this object: the referenced CronJob no longer exists, the schedule expression no longer parses, or status updates are losing repeated optimistic-lock fights.
- Operator restarted recently and has not caught up to all objects yet (transient — should clear in seconds).
- Status update conflicts: high resource-version churn on the CronJobMonitor (a controller higher up the chain is stomping it).

## Remediation

This page is meta — its job is to direct triage to the right place. Once you know the offending axis, follow that runbook.

### Reconciler failures (`Reconciled=False`)

Read the `message` on the False condition; common values:

- `cronJobNotFound` — the referenced CronJob was deleted or the `cronJobRef.name` is wrong.
  ```bash
  kubectl get cronjob <ref-name> -n <ns>
  kubectl get cronjobmonitor <name> -n <ns> -o jsonpath='{.spec.cronJobRef}'
  ```
  Either restore the CronJob or fix `cronJobRef.name` on the monitor.
- `parseScheduleError` — schedule expression no longer accepted by the parser. Check the spec:
  ```bash
  kubectl get cronjob <ref-name> -n <ns> -o jsonpath='{.spec.schedule}'
  ```
  Fix the expression (CronGuard accepts standard 5-field cron with the robfig/cron v3 grammar).
- `statusUpdateConflict` (rare, transient) — usually self-heals; if persistent, inspect operator logs for retry storms:
  ```bash
  kubectl -n cronguard-system logs deploy/cronguard --tail=500 | grep -i conflict
  ```

### Operator just restarted

Confirm and wait one reconcile interval (~30s):

```bash
kubectl -n cronguard-system get pods -o jsonpath='{.items[*].status.startTime}'
```

If the alert does not clear within 2–3 minutes after restart, escalate to [operator-down.md](operator-down.md) — something deeper is wrong.

### Cannot localize the cause

If the False condition's `reason` and `message` are insufficient, trace the full reconcile path for one object:

```bash
kubectl annotate cronjobmonitor <name> -n <ns> cronguard.io/debug=$(date +%s) --overwrite
kubectl -n cronguard-system logs deploy/cronguard --tail=200 -f | grep <name>
```

This will trigger a fresh reconcile and let you watch the controller's decision in real time.

## Related signals

- Aggregate condition: `Ready`.
- Sub-conditions: `Reconciled`, `ScheduleHealthy`, `ExecutionHealthy`, `DurationHealthy`.
- Metrics: `cronguard_condition{type="..."}`, `cronguard_reconcile_total{result="..."}`.
- Adjacent runbooks: [schedule-missed.md](schedule-missed.md), [consecutive-failures.md](consecutive-failures.md), [duration-exceeded.md](duration-exceeded.md), [operator-down.md](operator-down.md).

## Appendix

Which axis is most often False across the fleet:

```promql
sum by (type) (cronguard_condition == 0)
```

CJMs that are currently NotReady:

```promql
cronguard_condition{type="Ready"} == 0
```

Walk every False condition for one CJM:

```bash
kubectl get cronjobmonitor <name> -n <ns> -o json \
  | jq '.status.conditions[] | select(.status == "False")'
```
