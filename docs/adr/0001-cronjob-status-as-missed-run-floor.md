# ADR 0001 — Use `CronJob.status.lastScheduleTime` as a floor for missed-run counting

- **Status:** accepted
- **Date:** 2026-09-05
- **Supersedes:** nothing
- **Defect:** M5

## Context

`ScheduleHealthy` answers one question: did the scheduler fire the expected
slot? Until now the reconciler answered it with a proxy — is there a Job object
for it? — because `status.lastScheduleTime` on the monitor only ever advanced
from Jobs the operator personally observed.

Kubernetes deletes finished Jobs on purpose (`ttlSecondsAfterFinished`,
`successfulJobsHistoryLimit`), so that proxy decays. Nothing observes the Jobs
while the operator is down, garbage collection erases them, and the floor stays
frozen at the last run the operator saw. Every slot between that frozen floor
and now is then charged as missed, whether or not it ran.

The numbers are not marginal. A `*/5 * * * *` CronJob with
`ttlSecondsAfterFinished: 60`, an operator restart after two hours of downtime,
and 23 successful runs in between yields 23 phantom missed runs, a
`ScheduleHealthy=False`, a `Ready=False`, and — because the burn counter takes
the positive delta — a single +23 step that trips `CronGuardMissedRunsBurnFast`.
A page, for a CronJob that ran perfectly 23 times.

`batchv1.CronJob.status.lastScheduleTime` records the same fact without the
decay: it lives on an object nothing garbage-collects. We already fetch that
object. We were ignoring its status.

The project's correctness-campaign notes call this option "a smaller stance
change" and gate it on an ADR. This is that ADR.

## Decision

Take the missed-run floor to be the later of the monitor's own
`status.lastScheduleTime` and the CronJob's, admitting the latter only when we
are monitoring the CronJob's own slot grid.

```
floor := creationTimestamp                 // dead man's switch, unchanged
if monitor.status.lastScheduleTime != nil:
    floor = monitor.status.lastScheduleTime
if not scheduleOverridden and monitor.spec.timeZone == "" and
   cronjob.status.lastScheduleTime > floor:
    floor = cronjob.status.lastScheduleTime
```

The floor is computed per reconcile and is never written back into
`status.lastScheduleTime`, which remains the actual start of the most recent
observed run — the value drift is computed against and
`cronguard_last_schedule_timestamp_seconds` publishes.

### What we rely on, and how sure we are of it

kube-controller-manager writes `status.lastScheduleTime` only after the Job POST
succeeds. It does not advance it on a create failure, on a
`startingDeadlineSeconds` miss, or on a `concurrencyPolicy: Forbid` skip. That
one-sidedness is the whole basis of the decision: every case a user would call a
real miss leaves the witness un-advanced, so the slot stays countable.

Be clear about the evidentiary status. The upstream type comment says only
"Information when was the last time the job was successfully scheduled"; it does
not state the three non-advancement cases. Those are **observed** behaviour of
kube-controller-manager, which is not a dependency of this repository and cannot
be verified from this checkout. If a future Kubernetes release advanced the
field on a skipped slot, CronGuard would quietly start masking real misses. The
tripwires are the `blackout` and `catchup` specs in
`internal/controller/correctness_test.go`.

### What we deliberately excluded

`status.lastSuccessfulTime` is a **completion** time, not a slot time. Take
`*/10 * * * *` with `concurrencyPolicy: Forbid`, a Job for the 12:00 slot that
runs forty minutes and completes at 12:40, suppressing 12:10, 12:20 and 12:30.
`lastSuccessfulTime` is 12:40; using it as the floor at 12:45 reports zero
missed runs where the truth is three. It would convert an over-count into an
under-count.

An overridden `spec.schedule` or an explicit `spec.timeZone` withholds the
CronJob witness entirely. The two slot grids are then computed independently —
robfig here, kube-controller-manager there — and a witness about a different
grid is not evidence about ours. This is deliberately conservative: a
`spec.timeZone` identical to the CronJob's is also excluded, even though the
grids happen to match, because deciding equality on a string comparison would
reopen the masking path around a DST transition.

## Consequences

The change is one-sided. `MissedRunsSince` counts slots in `(floor, now-grace]`
and is monotonically non-increasing in `floor`; the new rule only ever takes a
maximum. So it can remove alerts and never add one.

**Missed-run counts will drop on upgrade** for clusters using
`ttlSecondsAfterFinished` or tight history limits, and a monitor that was
chronically alerting may go quiet. Someone tuning thresholds against the
inflated numbers should expect a step change.

### The under-count we accept

`CronJob.status.lastScheduleTime` records a point, not a gap. If
kube-controller-manager itself goes down and then recovers *before* the operator
does, it creates a single catch-up Job and stamps the field with it — erasing
every slot it skipped. The floor jumps past all of them and those misses are
never counted.

We accept this. The over-count it replaces fires on every TTL-GC cluster after
any operator restart; this under-count requires the control plane itself to fail
and recover inside the operator's own downtime, a window in which cluster-level
alerting is already firing. The behaviour is pinned by the `catchup` spec so it
is a documented trade rather than a discovery.

Separately and unchanged by this decision: `status.missedRuns` is a *current
streak*, and the burn counter accrues its positive delta, so a miss streak that
both begins and ends while the operator is not running is invisible. Neither
`lastScheduleTime` nor `lastSuccessfulTime` records skipped slots, so this is
not recoverable from the Kubernetes API at all.

## Alternatives considered

**Document the limitation and change nothing.** This was the previous stance. It
leaves a routine, high-magnitude false positive on the project's flagship
burn-rate alert.

**Assume every unobserved slot was missed.** That is what the code already did,
and it is what produced the defect.

**Read run history from Prometheus** (the `--prometheus-url` idea). It would
close the streak-inside-downtime gap too, but it breaks the read-only,
no-outbound-IO stance and needs its own ADR.

## Compliance

No CRD, CEL, RBAC, metric-name, label or cardinality change. The collector stays
list-driven over status with no scrape-time API calls. We read more of an object
already fetched under an existing `get;list;watch` grant, and write nothing to
it.
